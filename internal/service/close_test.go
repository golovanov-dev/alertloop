package service

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/golovanov-dev/alertloop/internal/domain"
	"github.com/golovanov-dev/alertloop/internal/storage"
)

// Mute silences recovery however the incident closes. The manual path is
// covered by TestResolvingAMutedIncidentDoesNotNotify; this is the source
// closing a muted, flapping check.
func TestMutedIncidentClosedBySourceSendsNoRecovery(t *testing.T) {
	ingest, events, store := twoChannelService(t, true)
	ctx := context.Background()
	const key = "server-01:noisy:check"

	res, err := ingest.Ingest(ctx, firing(key, "flapping", domain.SeverityWarning))
	if err != nil {
		t.Fatalf("firing: %v", err)
	}
	if _, err := events.Apply(ctx, res.Event.ID, domain.ActionMute); err != nil {
		t.Fatalf("mute: %v", err)
	}
	closed, err := ingest.Ingest(ctx, EventInput{Status: domain.StatusResolved, DedupeKey: key})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if closed.Outcome != OutcomeResolved {
		t.Fatalf("outcome = %q, want %q", closed.Outcome, OutcomeResolved)
	}
	if n := countDeliveries(t, store, domain.KindRecovery); n != 0 {
		t.Fatalf("recovery attempts = %d for a muted incident closed by its source, want 0", n)
	}
}

// Mute cancels the alerts not sent yet and leaves a sent one alone. Unmute
// does not bring the cancelled ones back, and a later resolve tells only the
// channel that was actually told about the problem.
func TestMuteCancelsUnsentAlerts(t *testing.T) {
	ingest, events, store := twoChannelService(t, true)
	ctx := context.Background()
	now := time.Date(2026, 8, 22, 10, 0, 0, 0, time.UTC)

	res, err := ingest.Ingest(ctx, firing("server-01:disk:usage", "disk 95%", domain.SeverityCritical))
	if err != nil {
		t.Fatalf("firing: %v", err)
	}
	// One alert sent, the other failed and waiting for a retry.
	claimed, err := store.ClaimDue(ctx, now, 10)
	if err != nil || len(claimed) != 2 {
		t.Fatalf("claim: %d claimed, err %v", len(claimed), err)
	}
	retry := now.Add(time.Minute)
	sent, failed := claimed[0], claimed[1]
	sent.State, sent.Attempts = domain.DeliverySent, 1
	failed.State, failed.Attempts, failed.NextRetryAt = domain.DeliveryFailed, 1, &retry
	for _, d := range []*domain.DeliveryAttempt{&sent, &failed} {
		if err := store.MarkResult(ctx, d); err != nil {
			t.Fatalf("mark %s: %v", d.ChannelName, err)
		}
	}
	// And one still pending, to a third channel.
	pending := &domain.DeliveryAttempt{
		ID: "pending-alert", EventID: res.Event.ID, Channel: domain.ChannelWebhook, ChannelName: "ops-webhook",
		Kind: domain.KindAlert, State: domain.DeliveryPending, MaxAttempts: 5, CreatedAt: now, UpdatedAt: now,
	}
	if err := store.CreateDeliveryAttempt(ctx, pending); err != nil {
		t.Fatalf("queue pending: %v", err)
	}

	if _, err := events.Apply(ctx, res.Event.ID, domain.ActionMute); err != nil {
		t.Fatalf("mute: %v", err)
	}
	if _, err := events.Apply(ctx, res.Event.ID, domain.ActionUnmute); err != nil {
		t.Fatalf("unmute: %v", err)
	}

	want := map[string]domain.DeliveryState{
		sent.ChannelName:   domain.DeliverySent,
		failed.ChannelName: domain.DeliveryCancelled,
		"ops-webhook":      domain.DeliveryCancelled,
	}
	alerts := attemptsFor(t, store, domain.KindAlert)
	for name, state := range want {
		if alerts[name].State != state {
			t.Fatalf("alert to %s is %q after mute and unmute, want %q", name, alerts[name].State, state)
		}
	}
	if again, err := store.ClaimDue(ctx, now.Add(time.Hour), 10); err != nil || len(again) != 0 {
		t.Fatalf("claim after unmute: %d claimed, err %v; cancelled alerts must stay out of the queue", len(again), err)
	}

	if _, err := events.Apply(ctx, res.Event.ID, domain.ActionResolve); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	recoveries := attemptsFor(t, store, domain.KindRecovery)
	if _, ok := recoveries[sent.ChannelName]; len(recoveries) != 1 || !ok {
		t.Fatalf("recoveries = %v, want exactly one, to %s whose alert was sent", recoveries, sent.ChannelName)
	}
}

// A manual resolve and a source's resolve landing together close the incident
// once and queue one recovery per channel, not two.
func TestConcurrentResolveQueuesOneRecovery(t *testing.T) {
	ingest, events, store := twoChannelService(t, true)
	ctx := context.Background()

	const rounds = 20
	for i := 0; i < rounds; i++ {
		key := fmt.Sprintf("server-01:race:%d", i)
		res, err := ingest.Ingest(ctx, firing(key, "down", domain.SeverityCritical))
		if err != nil {
			t.Fatalf("firing %d: %v", i, err)
		}
		var wg sync.WaitGroup
		var manualErr, ingestErr error
		wg.Add(2)
		go func() {
			defer wg.Done()
			_, manualErr = events.Apply(ctx, res.Event.ID, domain.ActionResolve)
		}()
		go func() {
			defer wg.Done()
			_, ingestErr = ingest.Ingest(ctx, EventInput{Status: domain.StatusResolved, DedupeKey: key})
		}()
		wg.Wait()
		if ingestErr != nil {
			t.Fatalf("round %d: ingest resolve: %v", i, ingestErr)
		}
		if manualErr != nil && !errors.Is(manualErr, domain.ErrInvalidTransition) {
			t.Fatalf("round %d: manual resolve: %v", i, manualErr)
		}
	}
	if n := countDeliveries(t, store, domain.KindRecovery); n != 2*rounds {
		t.Fatalf("recovery attempts = %d after %d raced closes on two channels, want %d", n, rounds, 2*rounds)
	}
}

// An acknowledge that read the incident open, and lands after the source
// closed it, must not reopen it.
func TestAckRacingSourceResolveDoesNotReopen(t *testing.T) {
	ingest, _, base := twoChannelService(t, true)
	ctx := context.Background()
	const key = "server-01:pg:availability"

	res, err := ingest.Ingest(ctx, firing(key, "down", domain.SeverityCritical))
	if err != nil {
		t.Fatalf("firing: %v", err)
	}
	racing := &closeFirstStore{Store: base, close: func() {
		if _, err := ingest.Ingest(ctx, EventInput{Status: domain.StatusResolved, DedupeKey: key}); err != nil {
			t.Errorf("source resolve: %v", err)
		}
	}}
	events := NewEventService(racing, NewRecoveryNotifier(true, 5, quietLogger()), nil)

	if _, err := events.Apply(ctx, res.Event.ID, domain.ActionAck); !errors.Is(err, domain.ErrInvalidTransition) {
		t.Fatalf("ack after the source closed the incident: err = %v, want ErrInvalidTransition", err)
	}
	got, err := base.GetEvent(ctx, res.Event.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.State != domain.StateResolved {
		t.Fatalf("state = %q, want the incident to stay resolved", got.State)
	}
}

// closeFirstStore runs close once, just before the first state change goes
// through, reproducing a request that read the event before someone else
// changed it.
type closeFirstStore struct {
	storage.Store
	close func()
	once  sync.Once
}

func (s *closeFirstStore) TransitionEvent(ctx context.Context, id string, c storage.StateChange) (*domain.Event, error) {
	s.once.Do(s.close)
	return s.Store.TransitionEvent(ctx, id, c)
}

// A client that gives up right after the incident closed must not cost the
// channels their recovery: it is queued with the close, not after it.
func TestRecoverySurvivesRequestCancelledAfterClose(t *testing.T) {
	ingest, _, base := twoChannelService(t, true)
	const key = "server-01:php-fpm:availability"
	if _, err := ingest.Ingest(context.Background(), firing(key, "down", domain.SeverityCritical)); err != nil {
		t.Fatalf("firing: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	svc := NewIngestService(&cancelAfterCloseStore{Store: base, cancel: cancel}, nil, 5,
		NewRecoveryNotifier(true, 5, quietLogger()), nil, quietLogger())
	if _, err := svc.Ingest(ctx, EventInput{Status: domain.StatusResolved, DedupeKey: key}); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if n := countDeliveries(t, base, domain.KindRecovery); n != 2 {
		t.Fatalf("recovery attempts = %d after the request was cancelled post-close, want 2", n)
	}
}

// cancelAfterCloseStore cancels the request context as soon as a state change
// returns, as a client hanging up at that moment would.
type cancelAfterCloseStore struct {
	storage.Store
	cancel context.CancelFunc
}

func (s *cancelAfterCloseStore) TransitionEvent(ctx context.Context, id string, c storage.StateChange) (*domain.Event, error) {
	e, err := s.Store.TransitionEvent(ctx, id, c)
	s.cancel()
	return e, err
}

// Actions apply to incidents only. Resolving a business event used to send
// RESOLVED about "new order #123" to every channel that heard of the order.
func TestActionsApplyToIncidentsOnly(t *testing.T) {
	ingest, events, store := twoChannelService(t, true)
	ctx := context.Background()

	res, err := ingest.Ingest(ctx, EventInput{
		Type: domain.EventBusiness, Severity: domain.SeverityInfo, Source: "shop", Message: "new order #123",
	})
	if err != nil {
		t.Fatalf("ingest: %v", err)
	}
	if _, err := events.Apply(ctx, res.Event.ID, domain.ActionResolve); !errors.Is(err, domain.ErrInvalidTransition) {
		t.Fatalf("resolve business event: err = %v, want ErrInvalidTransition", err)
	}
	got, err := store.GetEvent(ctx, res.Event.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.State != domain.StateNew {
		t.Fatalf("state = %q, want it untouched", got.State)
	}
	if n := countDeliveries(t, store, domain.KindRecovery); n != 0 {
		t.Fatalf("recovery attempts = %d, want 0", n)
	}
}

// Only an incident has a problem that can be over: a business event its
// source closes by dedupe_key tells nobody anything.
func TestBusinessEventClosedBySourceSendsNoRecovery(t *testing.T) {
	ingest, _, store := twoChannelService(t, true)
	ctx := context.Background()
	const key = "shop:order:123"

	if _, err := ingest.Ingest(ctx, EventInput{
		Type: domain.EventBusiness, Severity: domain.SeverityInfo, Source: "shop", Message: "new order #123", DedupeKey: key,
	}); err != nil {
		t.Fatalf("ingest: %v", err)
	}
	res, err := ingest.Ingest(ctx, EventInput{Status: domain.StatusResolved, DedupeKey: key})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if res.Outcome != OutcomeResolved {
		t.Fatalf("outcome = %q, want %q", res.Outcome, OutcomeResolved)
	}
	if n := countDeliveries(t, store, domain.KindRecovery); n != 0 {
		t.Fatalf("recovery attempts = %d for a business event, want 0", n)
	}
}

// Retention can delete the incident between the lookup and the close: the
// recovery then has nothing to close, as for a key never seen, not a 404.
func TestResolveOfIncidentDeletedMidCloseIsNothingToResolve(t *testing.T) {
	ingest, _, base := twoChannelService(t, true)
	const key = "server-01:php-fpm:availability"
	if _, err := ingest.Ingest(context.Background(), firing(key, "down", domain.SeverityCritical)); err != nil {
		t.Fatalf("firing: %v", err)
	}
	deleteAll := func() {
		if _, err := base.DeleteEventsBefore(context.Background(), time.Now().Add(time.Hour)); err != nil {
			t.Errorf("delete: %v", err)
		}
	}
	svc := NewIngestService(&closeFirstStore{Store: base, close: deleteAll}, nil, 5,
		NewRecoveryNotifier(true, 5, quietLogger()), nil, quietLogger())
	res, err := svc.Ingest(context.Background(), EventInput{Status: domain.StatusResolved, DedupeKey: key})
	if err != nil || res.Outcome != OutcomeNothingToResolve {
		t.Fatalf("resolve = %q, %v; want %q and no error", res.Outcome, err, OutcomeNothingToResolve)
	}
}

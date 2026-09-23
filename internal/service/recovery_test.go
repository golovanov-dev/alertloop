package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/golovanov-dev/alertloop/internal/config"
	"github.com/golovanov-dev/alertloop/internal/domain"
	"github.com/golovanov-dev/alertloop/internal/routing"
	"github.com/golovanov-dev/alertloop/internal/storage"
)

func twoChannelService(t *testing.T, notify bool) (*IngestService, *EventService, storage.Store) {
	t.Helper()
	s := newStore(t)
	targets := []domain.ChannelTarget{
		{Type: domain.ChannelTelegram, Name: "ops-telegram"},
		{Type: domain.ChannelEmail, Name: "ops-email"},
	}
	rec := NewRecoveryNotifier(notify, 5, quietLogger())
	clock := clockAt(time.Date(2026, 8, 22, 10, 0, 0, 0, time.UTC))
	ingest := NewIngestService(s, routing.NewAllChannels(targets), 5, rec, clock, quietLogger())
	events := NewEventService(s, rec, clock)
	return ingest, events, s
}

// attemptsFor returns the delivery attempts of one kind, by channel name.
func attemptsFor(t *testing.T, s storage.Store, kind domain.DeliveryKind) map[string]domain.DeliveryAttempt {
	t.Helper()
	page, err := s.ListDeliveryAttempts(context.Background(), storage.DeliveryFilter{}, 200, "")
	if err != nil {
		t.Fatalf("list deliveries: %v", err)
	}
	out := map[string]domain.DeliveryAttempt{}
	for _, d := range page.Items {
		if d.Kind == kind {
			out[d.ChannelName] = d
		}
	}
	return out
}

// The point of the whole feature: whoever was told the database is down is told
// when it comes back.
func TestResolveNotifiesTheChannelsThatWereAlerted(t *testing.T) {
	ingest, _, store := twoChannelService(t, true)
	ctx := context.Background()
	const key = "server-01:postgresql:availability"

	if _, err := ingest.Ingest(ctx, firing(key, "PostgreSQL down", domain.SeverityCritical)); err != nil {
		t.Fatalf("firing: %v", err)
	}
	alerts := attemptsFor(t, store, domain.KindAlert)
	if len(alerts) != 2 {
		t.Fatalf("alert attempts = %d, want one per channel", len(alerts))
	}

	res, err := ingest.Ingest(ctx, EventInput{Status: domain.StatusResolved, DedupeKey: key})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}

	recoveries := attemptsFor(t, store, domain.KindRecovery)
	if len(recoveries) != 2 {
		t.Fatalf("recovery attempts = %d, want one per alerted channel", len(recoveries))
	}
	for _, name := range []string{"ops-telegram", "ops-email"} {
		d, ok := recoveries[name]
		if !ok {
			t.Fatalf("channel %q was alerted but got no recovery notice", name)
		}
		if d.EventID != res.Event.ID {
			t.Fatalf("recovery for %q points at event %q, want the closed incident %q", name, d.EventID, res.Event.ID)
		}
		if d.State != domain.DeliveryPending {
			t.Fatalf("recovery for %q is %q, want it queued", name, d.State)
		}
		if d.Channel != alerts[name].Channel {
			t.Fatalf("recovery for %q targets channel type %q, want %q", name, d.Channel, alerts[name].Channel)
		}
	}
}

// A repeated recovery is idempotent all the way through: the incident stays
// closed AND nobody is told the good news a second time.
func TestRepeatedResolveDoesNotNotifyTwice(t *testing.T) {
	ingest, _, store := twoChannelService(t, true)
	ctx := context.Background()
	const key = "server-01:php-fpm:availability"

	if _, err := ingest.Ingest(ctx, firing(key, "php-fpm down", domain.SeverityCritical)); err != nil {
		t.Fatalf("firing: %v", err)
	}
	for i := 0; i < 3; i++ {
		if _, err := ingest.Ingest(ctx, EventInput{Status: domain.StatusResolved, DedupeKey: key}); err != nil {
			t.Fatalf("resolve %d: %v", i, err)
		}
	}
	if n := countDeliveries(t, store, domain.KindRecovery); n != 2 {
		t.Fatalf("recovery attempts = %d after three resolves, want 2 (one per channel)", n)
	}
}

// A channel whose alert dead-lettered before the incident closed still gets its
// recovery queued. It waits: nothing goes out until the alert is replayed and
// sent, and then the recovery follows it, so the channel never sees "down"
// without "back up", and never "back up" first.
func TestRecoveryForDeadLetteredAlertWaitsForReplay(t *testing.T) {
	ingest, _, store := twoChannelService(t, true)
	ctx := context.Background()
	const key = "server-01:disk:usage"
	later := time.Now().Add(time.Hour)

	if _, err := ingest.Ingest(ctx, firing(key, "disk full", domain.SeverityCritical)); err != nil {
		t.Fatalf("firing: %v", err)
	}
	// Claim before recording an outcome: MarkResult only writes to a row that
	// is still `sending`, which is how the worker reaches it.
	if _, err := store.ClaimDue(ctx, later, 10); err != nil {
		t.Fatalf("claim: %v", err)
	}
	alerts := attemptsFor(t, store, domain.KindAlert)
	mark := func(d domain.DeliveryAttempt, state domain.DeliveryState) {
		t.Helper()
		d.State, d.NextRetryAt = state, nil
		if err := store.MarkResult(ctx, &d); err != nil {
			t.Fatalf("mark %s %s: %v", d.ChannelName, state, err)
		}
	}
	mark(alerts["ops-telegram"], domain.DeliverySent)
	mark(alerts["ops-email"], domain.DeliveryDeadLetter)

	if _, err := ingest.Ingest(ctx, EventInput{Status: domain.StatusResolved, DedupeKey: key}); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	recoveries := attemptsFor(t, store, domain.KindRecovery)
	for _, name := range []string{"ops-telegram", "ops-email"} {
		if d, ok := recoveries[name]; !ok || d.State != domain.DeliveryPending {
			t.Fatalf("recovery for %q = %+v, want it queued", name, d)
		}
	}

	claimIDs := func() []string {
		t.Helper()
		claimed, err := store.ClaimDue(ctx, later, 10)
		if err != nil {
			t.Fatalf("claim: %v", err)
		}
		var ids []string
		for _, c := range claimed {
			ids = append(ids, c.ID)
		}
		return ids
	}
	want := func(got []string, ids ...string) {
		t.Helper()
		if len(got) != len(ids) {
			t.Fatalf("claimed %v, want %v", got, ids)
		}
		for i := range ids {
			if got[i] != ids[i] {
				t.Fatalf("claimed %v, want %v", got, ids)
			}
		}
	}

	// Only the telegram recovery goes: the email one waits behind its alert.
	want(claimIDs(), recoveries["ops-telegram"].ID)
	want(claimIDs())

	// Replayed: the alert goes first, the recovery only once it is sent.
	if _, err := store.Replay(ctx, alerts["ops-email"].ID, later); err != nil {
		t.Fatalf("replay: %v", err)
	}
	want(claimIDs(), alerts["ops-email"].ID)
	replayed, err := store.GetDeliveryAttempt(ctx, alerts["ops-email"].ID)
	if err != nil {
		t.Fatalf("get replayed alert: %v", err)
	}
	mark(*replayed, domain.DeliverySent)
	want(claimIDs(), recoveries["ops-email"].ID)
}

// A recovery goes only to channels an alert was queued to. A configured channel
// the routing did not send the alert to gets no recovery either.
func TestRecoverySkipsChannelsTheAlertWasNotRoutedTo(t *testing.T) {
	s := newStore(t)
	targets := []domain.ChannelTarget{
		{Type: domain.ChannelTelegram, Name: "ops-telegram"},
		{Type: domain.ChannelEmail, Name: "ops-email"},
	}
	router, err := routing.New(config.Routing{Default: []string{"ops-telegram"}}, targets)
	if err != nil {
		t.Fatalf("router: %v", err)
	}
	rec := NewRecoveryNotifier(true, 5, quietLogger())
	clock := clockAt(time.Date(2026, 8, 22, 10, 0, 0, 0, time.UTC))
	ingest := NewIngestService(s, router, 5, rec, clock, quietLogger())
	ctx := context.Background()
	const key = "server-01:cron:backup"

	if _, err := ingest.Ingest(ctx, firing(key, "backup failed", domain.SeverityError)); err != nil {
		t.Fatalf("firing: %v", err)
	}
	if _, err := ingest.Ingest(ctx, EventInput{Status: domain.StatusResolved, DedupeKey: key}); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	recoveries := attemptsFor(t, s, domain.KindRecovery)
	if len(recoveries) != 1 {
		t.Fatalf("recovery attempts = %v, want only ops-telegram", recoveries)
	}
	if _, ok := recoveries["ops-email"]; ok {
		t.Fatal("a channel the alert was never routed to got a recovery notice")
	}
}

// An alert still queued when the incident closes WILL be delivered, so its
// recipient must also get the recovery — otherwise they are left with a problem
// that never ended. This is the flapping case: down and up within seconds.
func TestRecoveryIncludesChannelsWhoseAlertIsStillQueued(t *testing.T) {
	ingest, _, store := twoChannelService(t, true)
	ctx := context.Background()
	const key = "server-01:http:health"

	if _, err := ingest.Ingest(ctx, firing(key, "health endpoint 500", domain.SeverityError)); err != nil {
		t.Fatalf("firing: %v", err)
	}
	// Nothing has been sent: both alerts are still pending.
	if _, err := ingest.Ingest(ctx, EventInput{Status: domain.StatusResolved, DedupeKey: key}); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if n := countDeliveries(t, store, domain.KindRecovery); n != 2 {
		t.Fatalf("recovery attempts = %d, want 2: a queued alert will still be delivered", n)
	}
}

// With no channels configured nobody was told anything, so there is nothing to
// correct. It must not be an error, and it must not queue undeliverable work.
func TestResolveWithNoChannelsQueuesNothing(t *testing.T) {
	s := newStore(t)
	rec := NewRecoveryNotifier(true, 5, quietLogger())
	ingest := NewIngestService(s, routing.NewAllChannels(nil), 5, rec,
		clockAt(time.Date(2026, 8, 22, 10, 0, 0, 0, time.UTC)), quietLogger())
	ctx := context.Background()

	if _, err := ingest.Ingest(ctx, firing("k", "boom", domain.SeverityError)); err != nil {
		t.Fatalf("firing: %v", err)
	}
	if _, err := ingest.Ingest(ctx, EventInput{Status: domain.StatusResolved, DedupeKey: "k"}); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if n := countDeliveries(t, s, domain.KindRecovery); n != 0 {
		t.Fatalf("recovery attempts = %d with no channels configured, want 0", n)
	}
}

func TestNotifyOnResolveCanBeDisabled(t *testing.T) {
	ingest, _, store := twoChannelService(t, false)
	ctx := context.Background()
	const key = "server-01:worker:heartbeat"

	if _, err := ingest.Ingest(ctx, firing(key, "heartbeat stale", domain.SeverityError)); err != nil {
		t.Fatalf("firing: %v", err)
	}
	if _, err := ingest.Ingest(ctx, EventInput{Status: domain.StatusResolved, DedupeKey: key}); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if n := countDeliveries(t, store, domain.KindRecovery); n != 0 {
		t.Fatalf("recovery attempts = %d with notify_on_resolve off, want 0", n)
	}
	// The incident is still closed. The setting controls notification, not the
	// lifecycle.
	page, _ := store.ListEvents(ctx, storage.EventFilter{State: domain.StateResolved}, 10, "")
	if len(page.Items) != 1 {
		t.Fatalf("resolved events = %d, want 1: the setting must not affect the lifecycle", len(page.Items))
	}
}

// Closing an incident from the console notifies the same people as an ingested
// recovery. The operator who clicked Resolve knows; the Telegram group does not.
func TestManualResolveActionNotifies(t *testing.T) {
	ingest, events, store := twoChannelService(t, true)
	ctx := context.Background()

	res, err := ingest.Ingest(ctx, firing("server-01:cron:import", "import failed", domain.SeverityError))
	if err != nil {
		t.Fatalf("firing: %v", err)
	}
	if _, err := events.Apply(ctx, res.Event.ID, domain.ActionResolve); err != nil {
		t.Fatalf("manual resolve: %v", err)
	}
	if n := countDeliveries(t, store, domain.KindRecovery); n != 2 {
		t.Fatalf("recovery attempts = %d after a manual resolve, want 2", n)
	}

	// Resolving an already-resolved event is refused and stays silent.
	if _, err := events.Apply(ctx, res.Event.ID, domain.ActionResolve); !errors.Is(err, domain.ErrInvalidTransition) {
		t.Fatalf("second manual resolve: err = %v, want ErrInvalidTransition", err)
	}
	if n := countDeliveries(t, store, domain.KindRecovery); n != 2 {
		t.Fatalf("recovery attempts = %d after resolving twice, want 2", n)
	}
}

// Acknowledging or muting is not the end of an incident and must not tell
// anyone it is over.
func TestNonResolveActionsDoNotNotify(t *testing.T) {
	ingest, events, store := twoChannelService(t, true)
	ctx := context.Background()

	res, err := ingest.Ingest(ctx, firing("server-01:swap:usage", "swap in use", domain.SeverityWarning))
	if err != nil {
		t.Fatalf("firing: %v", err)
	}
	for _, action := range []domain.EventAction{domain.ActionAck, domain.ActionMute, domain.ActionUnmute, domain.ActionEscalate} {
		if _, err := events.Apply(ctx, res.Event.ID, action); err != nil {
			t.Fatalf("%s: %v", action, err)
		}
	}
	if n := countDeliveries(t, store, domain.KindRecovery); n != 0 {
		t.Fatalf("recovery attempts = %d, want 0: only resolving ends an incident", n)
	}
}

// A recurrence after a recovery must alert again — and the recovery for the
// first outage must not be confused with the alert for the second.
func TestRecurrenceAfterRecoveryAlertsAgain(t *testing.T) {
	ingest, _, store := twoChannelService(t, true)
	ctx := context.Background()
	const key = "server-01:postgresql:availability"

	if _, err := ingest.Ingest(ctx, firing(key, "down", domain.SeverityCritical)); err != nil {
		t.Fatalf("first outage: %v", err)
	}
	if _, err := ingest.Ingest(ctx, EventInput{Status: domain.StatusResolved, DedupeKey: key}); err != nil {
		t.Fatalf("recovery: %v", err)
	}
	if _, err := ingest.Ingest(ctx, firing(key, "down again", domain.SeverityCritical)); err != nil {
		t.Fatalf("second outage: %v", err)
	}

	if n := countDeliveries(t, store, domain.KindAlert); n != 4 {
		t.Fatalf("alert attempts = %d, want 4 (two outages across two channels)", n)
	}
	if n := countDeliveries(t, store, domain.KindRecovery); n != 2 {
		t.Fatalf("recovery attempts = %d, want 2 (one outage recovered so far)", n)
	}
}

// The recovery for the first outage must belong to the first incident, not be
// re-pointed at the new one. Otherwise the message would carry the new outage's
// start time and read as though the current problem had already ended.
func TestRecoveryBelongsToTheIncidentItClosed(t *testing.T) {
	ingest, _, store := twoChannelService(t, true)
	ctx := context.Background()
	const key = "server-01:filesystem-root:usage"

	first, err := ingest.Ingest(ctx, firing(key, "disk 95%", domain.SeverityCritical))
	if err != nil {
		t.Fatalf("first outage: %v", err)
	}
	if _, err := ingest.Ingest(ctx, EventInput{Status: domain.StatusResolved, DedupeKey: key}); err != nil {
		t.Fatalf("recovery: %v", err)
	}
	second, err := ingest.Ingest(ctx, firing(key, "disk 95% again", domain.SeverityCritical))
	if err != nil {
		t.Fatalf("second outage: %v", err)
	}
	if second.Event.ID == first.Event.ID {
		t.Fatal("the recurrence reused the closed incident")
	}

	page, _ := store.ListDeliveryAttempts(ctx, storage.DeliveryFilter{}, 200, "")
	for _, d := range page.Items {
		if d.Kind == domain.KindRecovery && d.EventID != first.Event.ID {
			t.Fatalf("recovery attempt %s points at event %s, want the incident it closed (%s)",
				d.ID, d.EventID, first.Event.ID)
		}
	}
}

// Mute means "stop telling me about this incident". Sending a channel the end
// of a story it was deliberately not told the beginning of is the opposite of
// what was asked for.
func TestResolvingAMutedIncidentDoesNotNotify(t *testing.T) {
	ingest, events, store := twoChannelService(t, true)
	ctx := context.Background()

	res, err := ingest.Ingest(ctx, firing("server-01:noisy:check", "flapping", domain.SeverityWarning))
	if err != nil {
		t.Fatalf("firing: %v", err)
	}
	if _, err := events.Apply(ctx, res.Event.ID, domain.ActionMute); err != nil {
		t.Fatalf("mute: %v", err)
	}
	if _, err := events.Apply(ctx, res.Event.ID, domain.ActionResolve); err != nil {
		t.Fatalf("resolve: %v", err)
	}

	if n := countDeliveries(t, store, domain.KindRecovery); n != 0 {
		t.Fatalf("recovery attempts = %d for a muted incident, want 0", n)
	}
	// Muting affects notification, not the lifecycle: the incident is closed.
	closed, err := store.GetEvent(ctx, res.Event.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if closed.State != domain.StateResolved || closed.ResolvedAt == nil {
		t.Fatalf("state = %q resolved_at = %v; muting must not change the lifecycle",
			closed.State, closed.ResolvedAt)
	}
}

// The narrow race: an incident is closed between the dedupe lookup and the
// refresh. The source is saying "the problem is STILL HAPPENING", so the report
// must open a new incident - not be discarded as a duplicate of something that
// is already over.
func TestFiringRetriesWhenTheIncidentClosesUnderneathIt(t *testing.T) {
	s := newStore(t)
	rec := NewRecoveryNotifier(true, 5, quietLogger())
	targets := []domain.ChannelTarget{{Type: domain.ChannelWebhook, Name: "wh"}}
	svc := NewIngestService(&resolveRacingStore{Store: s}, routing.NewAllChannels(targets), 5, rec,
		clockAt(time.Date(2026, 8, 24, 10, 0, 0, 0, time.UTC)), quietLogger())
	ctx := context.Background()
	const key = "server-01:pg:availability"

	if _, err := svc.Ingest(ctx, firing(key, "down", domain.SeverityCritical)); err != nil {
		t.Fatalf("first firing: %v", err)
	}

	// The wrapper closes the incident just before the refresh lands.
	again, err := svc.Ingest(ctx, firing(key, "still down", domain.SeverityCritical))
	if err != nil {
		t.Fatalf("second firing: %v", err)
	}
	if again.Outcome != OutcomeCreated {
		t.Fatalf("outcome = %q, want %q: a report that the problem continues must not be dropped",
			again.Outcome, OutcomeCreated)
	}
	if again.Event.Message != "still down" {
		t.Fatalf("message = %q, want the newest report", again.Event.Message)
	}
	if n := countDeliveries(t, s, domain.KindAlert); n != 2 {
		t.Fatalf("alert attempts = %d, want 2: the new incident must notify", n)
	}
}

// resolveRacingStore closes the open incident at the exact moment the service
// tries to refresh it, reproducing the race deterministically.
type resolveRacingStore struct {
	storage.Store
	fired bool
}

func (s *resolveRacingStore) RefreshOpenEvent(ctx context.Context, id string, u storage.EventUpdate) (*domain.Event, error) {
	if !s.fired {
		s.fired = true
		e, err := s.GetEvent(ctx, id)
		if err != nil {
			return nil, err
		}
		if _, err := s.TransitionEvent(ctx, id, storage.StateChange{
			From: e.State, To: domain.StateResolved, At: u.LastSeenAt,
		}); err != nil {
			return nil, err
		}
	}
	return s.Store.RefreshOpenEvent(ctx, id, u)
}

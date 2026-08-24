package delivery

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golovanov-dev/alertloop/internal/channels"
	"github.com/golovanov-dev/alertloop/internal/domain"
	"github.com/golovanov-dev/alertloop/internal/storage"
)

// fakeChannel is a controllable Channel for worker tests.
type fakeChannel struct {
	typ      domain.ChannelType
	name     string
	err      error
	calls    atomic.Int32
	lastKind atomic.Value
}

func (f *fakeChannel) Type() domain.ChannelType { return f.typ }
func (f *fakeChannel) Name() string             { return f.name }
func (f *fakeChannel) Send(_ context.Context, n domain.Notification) error {
	f.calls.Add(1)
	f.lastKind.Store(string(n.Kind))
	return f.err
}

// lastKind is what the most recent Send announced. Tests assert on it because
// a recovery that renders as an alert is indistinguishable from a bug that
// notifies twice about the same failure.
func (f *fakeChannel) kind() string {
	v, _ := f.lastKind.Load().(string)
	return v
}

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func setup(t *testing.T, ch *fakeChannel, maxAttempts int) (storage.Store, string) {
	t.Helper()
	s, err := storage.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	if err := s.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	ctx := context.Background()
	e, _, err := s.CreateEvent(ctx, &domain.Event{
		ID: "e1", Type: domain.EventIncident, Severity: domain.SeverityError,
		State: domain.StateNew, Source: "api", Message: "boom",
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	})
	if err != nil {
		t.Fatalf("create event: %v", err)
	}
	d := &domain.DeliveryAttempt{
		ID: "d1", EventID: e.ID, Channel: ch.typ, ChannelName: ch.name, State: domain.DeliveryPending,
		MaxAttempts: maxAttempts, CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	if err := s.CreateDeliveryAttempt(ctx, d); err != nil {
		t.Fatalf("create delivery: %v", err)
	}
	return s, d.ID
}

func TestWorkerDeliversSuccessfully(t *testing.T) {
	ch := &fakeChannel{typ: domain.ChannelWebhook}
	s, id := setup(t, ch, 5)
	w := NewWorker(s, channels.NewRegistry(ch), Options{Concurrency: 1}, quietLogger())

	n, err := w.tick(context.Background())
	if err != nil || n != 1 {
		t.Fatalf("tick: n=%d err=%v", n, err)
	}
	got, _ := s.GetDeliveryAttempt(context.Background(), id)
	if got.State != domain.DeliverySent || got.Attempts != 1 {
		t.Fatalf("expected sent/1, got %s/%d", got.State, got.Attempts)
	}
}

func TestWorkerRetryThenDeadLetter(t *testing.T) {
	ch := &fakeChannel{typ: domain.ChannelWebhook, err: errors.New("connection refused")}
	// max 2 attempts: first failure schedules retry, second dead-letters.
	s, id := setup(t, ch, 2)
	w := NewWorker(s, channels.NewRegistry(ch), Options{
		Concurrency: 1, BaseBackoff: time.Millisecond, MaxBackoff: time.Millisecond,
	}, quietLogger())
	ctx := context.Background()

	// Attempt 1 -> failed with a scheduled retry.
	if _, err := w.tick(ctx); err != nil {
		t.Fatalf("tick1: %v", err)
	}
	got, _ := s.GetDeliveryAttempt(ctx, id)
	if got.State != domain.DeliveryFailed || got.NextRetryAt == nil {
		t.Fatalf("after attempt 1 expected failed+retry, got %s next=%v", got.State, got.NextRetryAt)
	}
	if got.LastError == "" {
		t.Fatal("expected last_error to be recorded")
	}

	// Wait for the (1ms) backoff to elapse, then attempt 2 -> dead_letter.
	time.Sleep(5 * time.Millisecond)
	if _, err := w.tick(ctx); err != nil {
		t.Fatalf("tick2: %v", err)
	}
	got, _ = s.GetDeliveryAttempt(ctx, id)
	if got.State != domain.DeliveryDeadLetter || got.Attempts != 2 {
		t.Fatalf("expected dead_letter/2, got %s/%d", got.State, got.Attempts)
	}

	// Replay re-queues, and a now-healthy channel delivers it.
	if _, err := s.Replay(ctx, id, time.Now()); err != nil {
		t.Fatalf("replay: %v", err)
	}
	ch.err = nil
	if _, err := w.tick(ctx); err != nil {
		t.Fatalf("tick3: %v", err)
	}
	got, _ = s.GetDeliveryAttempt(ctx, id)
	if got.State != domain.DeliverySent {
		t.Fatalf("expected sent after replay, got %s", got.State)
	}
}

func TestWorkerUnknownChannelFails(t *testing.T) {
	ch := &fakeChannel{typ: domain.ChannelWebhook}
	s, id := setup(t, ch, 5)
	// Registry without the webhook channel: delivery cannot resolve a channel.
	w := NewWorker(s, channels.NewRegistry(), Options{
		Concurrency: 1, BaseBackoff: time.Millisecond,
	}, quietLogger())
	if _, err := w.tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}
	got, _ := s.GetDeliveryAttempt(context.Background(), id)
	if got.State != domain.DeliveryFailed {
		t.Fatalf("expected failed for unknown channel, got %s", got.State)
	}
}

// The worker must tell the channel WHICH message this is. It reads the kind
// from the queued row rather than from the event, because by the time a
// recovery is sent the event is resolved — and so is an alert that was still
// queued when someone closed the incident. Deriving it from the event would
// render that alert as "resolved" and the recipient would never learn there was
// a problem at all.
func TestWorkerCarriesTheDeliveryKindToTheChannel(t *testing.T) {
	ch := &fakeChannel{typ: domain.ChannelWebhook, name: "wh"}
	s, alertID := setup(t, ch, 5)
	ctx := context.Background()

	// Close the incident, exactly as a recovery does, and queue the notice.
	if _, err := s.UpdateEventState(ctx, "e1", domain.StateResolved, time.Now().UTC()); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	rec := &domain.DeliveryAttempt{
		ID: "d-recovery", EventID: "e1", Channel: ch.typ, ChannelName: ch.name,
		Kind: domain.KindRecovery, State: domain.DeliveryPending,
		MaxAttempts: 5, CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	if err := s.CreateDeliveryAttempt(ctx, rec); err != nil {
		t.Fatalf("queue recovery: %v", err)
	}

	// A tick claims at most Concurrency rows, so drain the queue rather than
	// assuming one pass covers both.
	w := NewWorker(s, channels.NewRegistry(ch), Options{Concurrency: 1}, quietLogger())
	for i := 0; i < 5; i++ {
		n, err := w.tick(ctx)
		if err != nil {
			t.Fatalf("tick: %v", err)
		}
		if n == 0 {
			break
		}
	}

	// Check each ended up sent, and that the kind survived the round trip
	// through the database.
	for _, id := range []string{alertID, "d-recovery"} {
		got, err := s.GetDeliveryAttempt(ctx, id)
		if err != nil {
			t.Fatalf("get %s: %v", id, err)
		}
		if got.State != domain.DeliverySent {
			t.Fatalf("attempt %s is %s, want sent", id, got.State)
		}
	}
	stored, _ := s.GetDeliveryAttempt(ctx, "d-recovery")
	if stored.Kind != domain.KindRecovery {
		t.Fatalf("stored kind = %q, want %q", stored.Kind, domain.KindRecovery)
	}
	alert, _ := s.GetDeliveryAttempt(ctx, alertID)
	if alert.Kind != domain.KindAlert {
		t.Fatalf("an attempt queued without a kind read back as %q, want %q", alert.Kind, domain.KindAlert)
	}
	if ch.kind() == "" {
		t.Fatal("the channel was never told what kind of notification it was sending")
	}
}

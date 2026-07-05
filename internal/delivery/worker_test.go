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
	typ   domain.ChannelType
	name  string
	err   error
	calls atomic.Int32
}

func (f *fakeChannel) Type() domain.ChannelType { return f.typ }
func (f *fakeChannel) Name() string             { return f.name }
func (f *fakeChannel) Send(_ context.Context, _ *domain.Event) error {
	f.calls.Add(1)
	return f.err
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

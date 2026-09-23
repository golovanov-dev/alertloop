package delivery

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golovanov-dev/alertloop/internal/channels"
	"github.com/golovanov-dev/alertloop/internal/domain"
	"github.com/golovanov-dev/alertloop/internal/storage"
)

// fakeChannel is a controllable Channel for worker tests.
type fakeChannel struct {
	typ     domain.ChannelType
	name    string
	timeout time.Duration
	err     error
	// send, when set, replaces the plain `return err`.
	send func(ctx context.Context) error

	calls atomic.Int32

	mu          sync.Mutex
	kinds       []domain.DeliveryKind
	deadline    time.Time
	inFlight    int
	maxInFlight int
}

func (f *fakeChannel) Type() domain.ChannelType { return f.typ }
func (f *fakeChannel) Name() string             { return f.name }
func (f *fakeChannel) Timeout() time.Duration   { return f.timeout }
func (f *fakeChannel) Send(ctx context.Context, n domain.Notification) error {
	f.mu.Lock()
	f.kinds = append(f.kinds, n.Kind)
	f.deadline, _ = ctx.Deadline()
	f.inFlight++
	f.maxInFlight = max(f.maxInFlight, f.inFlight)
	f.mu.Unlock()
	f.calls.Add(1)
	defer func() {
		f.mu.Lock()
		f.inFlight--
		f.mu.Unlock()
	}()
	if f.send != nil {
		return f.send(ctx)
	}
	return f.err
}

func (f *fakeChannel) sentKinds() []domain.DeliveryKind {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.kinds)
}

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// newStore opens an in-memory store holding one incident, "e1".
func newStore(t *testing.T) storage.Store {
	t.Helper()
	s, err := storage.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	ctx := context.Background()
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if _, _, err := s.CreateEvent(ctx, &domain.Event{
		ID: "e1", Type: domain.EventIncident, Severity: domain.SeverityError,
		State: domain.StateNew, Source: "api", Message: "boom",
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}); err != nil {
		t.Fatalf("create event: %v", err)
	}
	return s
}

// enqueue queues an alert of e1 to ch. createdAt orders the queue.
func enqueue(t *testing.T, s storage.Store, id string, ch *fakeChannel, maxAttempts int, createdAt time.Time) {
	t.Helper()
	d := &domain.DeliveryAttempt{
		ID: id, EventID: "e1", Channel: ch.typ, ChannelName: ch.name, State: domain.DeliveryPending,
		MaxAttempts: maxAttempts, CreatedAt: createdAt, UpdatedAt: createdAt,
	}
	if err := s.CreateDeliveryAttempt(context.Background(), d); err != nil {
		t.Fatalf("create delivery: %v", err)
	}
}

func setup(t *testing.T, ch *fakeChannel, maxAttempts int) (storage.Store, string) {
	t.Helper()
	s := newStore(t)
	enqueue(t, s, "d1", ch, maxAttempts, time.Now())
	return s, "d1"
}

func attempt(t *testing.T, s storage.Store, id string) *domain.DeliveryAttempt {
	t.Helper()
	got, err := s.GetDeliveryAttempt(context.Background(), id)
	if err != nil {
		t.Fatalf("get %s: %v", id, err)
	}
	return got
}

// step fills the free slots once and waits for every send it started.
func step(t *testing.T, w *Worker) int {
	t.Helper()
	ctx := context.Background()
	s := w.newScheduler(ctx)
	defer s.cancelSend()
	n, err := s.fill(ctx)
	if err != nil {
		t.Fatalf("fill: %v", err)
	}
	for s.total > 0 {
		s.finished(<-s.done)
	}
	return n
}

// start runs w in the background. cancel tells it to stop; wait returns once
// Run has.
func start(t *testing.T, w *Worker) (cancel, wait func()) {
	t.Helper()
	ctx, cancelCtx := context.WithCancel(context.Background())
	exited := make(chan struct{})
	go func() {
		w.Run(ctx)
		close(exited)
	}()
	wait = func() {
		cancelCtx()
		select {
		case <-exited:
		case <-time.After(10 * time.Second):
			t.Fatal("Run did not return after cancellation")
		}
	}
	t.Cleanup(wait)
	return cancelCtx, wait
}

// stop cancels a started worker and waits for Run to return.
func stop(cancel, wait func()) {
	cancel()
	wait()
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func TestWorkerDeliversSuccessfully(t *testing.T) {
	ch := &fakeChannel{typ: domain.ChannelWebhook}
	s, id := setup(t, ch, 5)
	w := NewWorker(s, channels.NewRegistry(ch), Options{Concurrency: 1}, quietLogger())

	if n := step(t, w); n != 1 {
		t.Fatalf("started %d sends, want 1", n)
	}
	got := attempt(t, s, id)
	if got.State != domain.DeliverySent || got.Attempts != 1 {
		t.Fatalf("expected sent/1, got %s/%d", got.State, got.Attempts)
	}
}

// Replay starts a new cycle of retries: a failure right after it schedules a
// retry instead of dead-lettering again.
func TestWorkerRetryDeadLetterAndReplayStartsANewCycle(t *testing.T) {
	ch := &fakeChannel{typ: domain.ChannelWebhook, err: errors.New("connection refused")}
	// max 2 attempts: first failure schedules retry, second dead-letters.
	s, id := setup(t, ch, 2)
	w := NewWorker(s, channels.NewRegistry(ch), Options{
		Concurrency: 1, BaseBackoff: time.Second, MaxBackoff: time.Second,
	}, quietLogger())
	clock := time.Now().UTC()
	w.now = func() time.Time { return clock }
	ctx := context.Background()

	step(t, w)
	got := attempt(t, s, id)
	if got.State != domain.DeliveryFailed || got.NextRetryAt == nil || got.LastError == "" {
		t.Fatalf("after attempt 1 expected failed+retry+error, got %s next=%v err=%q", got.State, got.NextRetryAt, got.LastError)
	}

	clock = clock.Add(time.Minute)
	step(t, w)
	got = attempt(t, s, id)
	if got.State != domain.DeliveryDeadLetter || got.Attempts != 2 {
		t.Fatalf("expected dead_letter/2, got %s/%d", got.State, got.Attempts)
	}

	replayed, err := s.Replay(ctx, id, clock)
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if replayed.Attempts != 0 {
		t.Fatalf("replay left attempts at %d, want 0", replayed.Attempts)
	}
	step(t, w)
	got = attempt(t, s, id)
	if got.State != domain.DeliveryFailed || got.Attempts != 1 {
		t.Fatalf("a failure after replay gave %s/%d, want failed/1", got.State, got.Attempts)
	}

	ch.err = nil
	clock = clock.Add(time.Minute)
	step(t, w)
	if got = attempt(t, s, id); got.State != domain.DeliverySent {
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
	step(t, w)
	if got := attempt(t, s, id); got.State != domain.DeliveryFailed {
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
	s, _ := setup(t, ch, 5)
	ctx := context.Background()

	// Close the incident, exactly as a recovery does, and queue the notice.
	if _, err := s.TransitionEvent(ctx, "e1", storage.StateChange{
		From: domain.StateNew, To: domain.StateResolved, At: time.Now().UTC(),
	}); err != nil {
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

	w := NewWorker(s, channels.NewRegistry(ch), Options{Concurrency: 1}, quietLogger())
	for n := 1; n > 0; {
		n = step(t, w)
	}

	// The recovery is held until the alert is sent, so the order is fixed.
	want := []domain.DeliveryKind{domain.KindAlert, domain.KindRecovery}
	if got := ch.sentKinds(); !slices.Equal(got, want) {
		t.Fatalf("the channel was told %v, want %v", got, want)
	}
}

// A channel that hangs holds at most its share of the slots; alerts to other
// channels queued behind its backlog are delivered meanwhile.
func TestWorkerSlowChannelDoesNotHoldUpOthers(t *testing.T) {
	slow := &fakeChannel{typ: domain.ChannelEmail, name: "mail", send: func(ctx context.Context) error {
		<-ctx.Done()
		return ctx.Err()
	}}
	fast := &fakeChannel{typ: domain.ChannelTelegram, name: "tg"}
	s := newStore(t)
	t0 := time.Now().Add(-time.Hour)
	for i := range 3 {
		enqueue(t, s, fmt.Sprintf("mail-%d", i), slow, 5, t0.Add(time.Duration(i)*time.Second))
	}
	for i := range 3 {
		enqueue(t, s, fmt.Sprintf("tg-%d", i), fast, 5, t0.Add(time.Minute+time.Duration(i)*time.Second))
	}
	w := NewWorker(s, channels.NewRegistry(slow, fast), Options{
		Concurrency: 2, PollInterval: 10 * time.Millisecond, ShutdownGrace: 10 * time.Millisecond,
	}, quietLogger())

	cancel, wait := start(t, w)
	waitFor(t, "the telegram alerts", func() bool { return fast.calls.Load() == 3 })
	stop(cancel, wait)

	if n := slow.calls.Load(); n != 1 {
		t.Fatalf("the hung channel got %d sends at once, want 1 (its slot limit)", n)
	}
	for i := range 3 {
		if got := attempt(t, s, fmt.Sprintf("tg-%d", i)); got.State != domain.DeliverySent {
			t.Fatalf("tg-%d is %s, want sent", i, got.State)
		}
	}
}

func TestWorkerLimitsSlotsPerChannel(t *testing.T) {
	release := make(chan struct{})
	wait := func(context.Context) error { <-release; return nil }
	a := &fakeChannel{typ: domain.ChannelWebhook, name: "a", send: wait}
	b := &fakeChannel{typ: domain.ChannelWebhook, name: "b", send: wait}
	s := newStore(t)
	t0 := time.Now().Add(-time.Hour)
	for i := range 4 {
		enqueue(t, s, fmt.Sprintf("a-%d", i), a, 5, t0.Add(time.Duration(i)*time.Second))
	}
	enqueue(t, s, "b-0", b, 5, t0.Add(time.Minute))
	w := NewWorker(s, channels.NewRegistry(a, b), Options{Concurrency: 3}, quietLogger())

	ctx := context.Background()
	sch := w.newScheduler(ctx)
	defer sch.cancelSend()
	n, err := sch.fill(ctx)
	if err != nil {
		t.Fatalf("fill: %v", err)
	}
	if n != 3 {
		t.Fatalf("started %d sends, want 3", n)
	}
	sending := func(name string) int {
		p, err := s.ListDeliveryAttempts(ctx, storage.DeliveryFilter{State: domain.DeliverySending, ChannelName: name}, 50, "")
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		return len(p.Items)
	}
	if got := sending("a"); got != 2 {
		t.Fatalf("channel a holds %d slots, want 2 of 3", got)
	}
	if got := sending("b"); got != 1 {
		t.Fatalf("channel b holds %d slots, want 1", got)
	}
	close(release)
	for sch.total > 0 {
		sch.finished(<-sch.done)
	}
}

// With a single channel configured there is no other channel to keep a slot
// for, so it may use every slot.
func TestWorkerSingleChannelUsesEverySlot(t *testing.T) {
	release := make(chan struct{})
	a := &fakeChannel{typ: domain.ChannelWebhook, name: "a", send: func(context.Context) error { <-release; return nil }}
	s := newStore(t)
	t0 := time.Now().Add(-time.Hour)
	for i := range 4 {
		enqueue(t, s, fmt.Sprintf("a-%d", i), a, 5, t0.Add(time.Duration(i)*time.Second))
	}
	w := NewWorker(s, channels.NewRegistry(a), Options{Concurrency: 3}, quietLogger())

	ctx := context.Background()
	sch := w.newScheduler(ctx)
	defer sch.cancelSend()
	n, err := sch.fill(ctx)
	if err != nil {
		t.Fatalf("fill: %v", err)
	}
	if n != 3 {
		t.Fatalf("the only channel got %d of 3 slots, want all 3", n)
	}
	close(release)
	for sch.total > 0 {
		sch.finished(<-sch.done)
	}
}

// countingStore counts ClaimDue calls.
type countingStore struct {
	storage.Store
	claims atomic.Int32
}

func (c *countingStore) ClaimDue(ctx context.Context, now time.Time, limit int, skip ...string) ([]domain.DeliveryAttempt, error) {
	c.claims.Add(1)
	return c.Store.ClaimDue(ctx, now, limit, skip...)
}

// Every claim makes the database order the due attempts, so free slots are
// claimed in one query, not one query per slot.
func TestWorkerClaimsForFreeSlotsInOneQuery(t *testing.T) {
	release := make(chan struct{})
	a := &fakeChannel{typ: domain.ChannelWebhook, name: "a", send: func(context.Context) error { <-release; return nil }}
	s := &countingStore{Store: newStore(t)}
	t0 := time.Now().Add(-time.Hour)
	for i := range 10 {
		enqueue(t, s, fmt.Sprintf("a-%d", i), a, 5, t0.Add(time.Duration(i)*time.Second))
	}
	w := NewWorker(s, channels.NewRegistry(a), Options{Concurrency: 4}, quietLogger())

	ctx := context.Background()
	sch := w.newScheduler(ctx)
	defer sch.cancelSend()
	n, err := sch.fill(ctx)
	if err != nil {
		t.Fatalf("fill: %v", err)
	}
	if n != 4 || s.claims.Load() != 1 {
		t.Fatalf("started %d sends with %d claims, want 4 with 1", n, s.claims.Load())
	}
	close(release)
	for sch.total > 0 {
		sch.finished(<-sch.done)
	}
}

// Each attempt gets its channel's own timeout; a channel without one gets
// domain.DefaultChannelTimeout.
func TestWorkerAttemptGetsTheChannelTimeout(t *testing.T) {
	for _, tc := range []struct {
		name    string
		channel time.Duration
		want    time.Duration
	}{
		{"channel timeout", time.Hour, time.Hour},
		{"no channel timeout", 0, domain.DefaultChannelTimeout},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ch := &fakeChannel{typ: domain.ChannelWebhook, name: "wh", timeout: tc.channel}
			s, _ := setup(t, ch, 5)
			w := NewWorker(s, channels.NewRegistry(ch), Options{Concurrency: 1}, quietLogger())
			before := time.Now()
			step(t, w)
			ch.mu.Lock()
			got := ch.deadline.Sub(before)
			ch.mu.Unlock()
			if got < tc.want-time.Second || got > tc.want+time.Second {
				t.Fatalf("the send had %v, want about %v", got, tc.want)
			}
		})
	}
}

// The reaper must not take back a send that can still be running, so the stale
// window grows with the longest channel timeout and never drops below 5 min.
func TestWorkerStaleWindowCoversTheLongestChannelTimeout(t *testing.T) {
	short := &fakeChannel{typ: domain.ChannelWebhook, name: "short", timeout: 10 * time.Second}
	long := &fakeChannel{typ: domain.ChannelWebhook, name: "long", timeout: 10 * time.Minute}
	s := newStore(t)
	if got := NewWorker(s, channels.NewRegistry(short), Options{}, quietLogger()).opts.StaleAfter; got != 5*time.Minute {
		t.Fatalf("StaleAfter = %v with a 10s channel, want 5m", got)
	}
	if got := NewWorker(s, channels.NewRegistry(short, long), Options{}, quietLogger()).opts.StaleAfter; got != 20*time.Minute {
		t.Fatalf("StaleAfter = %v with a 10m channel, want 20m", got)
	}
}

func TestWorkerReaperRequeuesOnlyStaleAttempts(t *testing.T) {
	ch := &fakeChannel{typ: domain.ChannelWebhook, name: "wh"}
	s := newStore(t)
	now := time.Now().UTC()
	enqueue(t, s, "stale", ch, 5, now.Add(-2*time.Hour))
	enqueue(t, s, "live", ch, 5, now.Add(-time.Hour))
	ctx := context.Background()
	// ClaimDue stamps updated_at with the time it is given.
	if _, err := s.ClaimDue(ctx, now.Add(-10*time.Minute), 1); err != nil {
		t.Fatalf("claim stale: %v", err)
	}
	if _, err := s.ClaimDue(ctx, now, 1); err != nil {
		t.Fatalf("claim live: %v", err)
	}
	w := NewWorker(s, channels.NewRegistry(ch), Options{}, quietLogger())

	w.reap(ctx)

	if got := attempt(t, s, "stale").State; got != domain.DeliveryPending {
		t.Fatalf("stale attempt is %s, want pending", got)
	}
	if got := attempt(t, s, "live").State; got != domain.DeliverySending {
		t.Fatalf("live attempt is %s, want sending", got)
	}
}

// A send already under way when the worker is told to stop may finish, and its
// outcome is recorded.
func TestWorkerShutdownLetsSendsInFlightFinish(t *testing.T) {
	release := make(chan struct{})
	ch := &fakeChannel{typ: domain.ChannelWebhook, name: "wh", send: func(ctx context.Context) error {
		<-release
		return ctx.Err()
	}}
	s, id := setup(t, ch, 5)
	w := NewWorker(s, channels.NewRegistry(ch), Options{
		Concurrency: 1, PollInterval: 10 * time.Millisecond, ShutdownGrace: time.Minute,
	}, quietLogger())

	cancel, wait := start(t, w)
	waitFor(t, "the send to start", func() bool { return ch.calls.Load() == 1 })
	cancel()
	close(release)
	wait()

	if got := attempt(t, s, id); got.State != domain.DeliverySent || got.Attempts != 1 {
		t.Fatalf("expected sent/1, got %s/%d", got.State, got.Attempts)
	}
}

// A send that outlives the grace period is cancelled and goes back to the
// queue as it was, not counted as a failed attempt.
func TestWorkerShutdownRequeuesSendsThatOutliveTheGrace(t *testing.T) {
	ch := &fakeChannel{typ: domain.ChannelWebhook, name: "wh", send: func(ctx context.Context) error {
		<-ctx.Done()
		return ctx.Err()
	}}
	s, id := setup(t, ch, 5)
	w := NewWorker(s, channels.NewRegistry(ch), Options{
		Concurrency: 2, PollInterval: 10 * time.Millisecond, ShutdownGrace: 10 * time.Millisecond,
	}, quietLogger())

	cancel, wait := start(t, w)
	waitFor(t, "the send to start", func() bool { return ch.calls.Load() == 1 })
	stop(cancel, wait)

	got := attempt(t, s, id)
	if got.State != domain.DeliveryPending || got.Attempts != 0 || got.LastError != "" {
		t.Fatalf("expected pending/0 with no error, got %s/%d %q", got.State, got.Attempts, got.LastError)
	}
}

func TestWorkerDrainsManyAttemptsConcurrently(t *testing.T) {
	chans := []*fakeChannel{
		{typ: domain.ChannelWebhook, name: "a"},
		{typ: domain.ChannelWebhook, name: "b"},
		{typ: domain.ChannelWebhook, name: "c"},
	}
	s := newStore(t)
	t0 := time.Now().Add(-time.Hour)
	const total = 60
	for i := range total {
		enqueue(t, s, fmt.Sprintf("d-%02d", i), chans[i%3], 5, t0.Add(time.Duration(i)*time.Millisecond))
	}
	w := NewWorker(s, channels.NewRegistry(chans[0], chans[1], chans[2]), Options{
		Concurrency: 4, PollInterval: 10 * time.Millisecond,
	}, quietLogger())

	cancel, wait := start(t, w)
	sent := func() int32 { return chans[0].calls.Load() + chans[1].calls.Load() + chans[2].calls.Load() }
	waitFor(t, "every attempt to be sent", func() bool { return sent() == total })
	stop(cancel, wait)

	if n := sent(); n != total {
		t.Fatalf("%d sends for %d attempts", n, total)
	}
	for i := range total {
		if got := attempt(t, s, fmt.Sprintf("d-%02d", i)); got.State != domain.DeliverySent || got.Attempts != 1 {
			t.Fatalf("d-%02d is %s/%d, want sent/1", i, got.State, got.Attempts)
		}
	}
	for _, ch := range chans {
		if ch.maxInFlight > 3 {
			t.Fatalf("channel %s had %d sends at once, want at most 3", ch.name, ch.maxInFlight)
		}
	}
}

// mute mutes e1 the way the service does, cancelling its alerts not sent yet.
func mute(t *testing.T, s storage.Store) {
	t.Helper()
	if _, err := s.TransitionEvent(context.Background(), "e1", storage.StateChange{
		From: domain.StateNew, To: domain.StateMuted, At: time.Now(), CancelAlerts: true,
	}); err != nil {
		t.Fatalf("mute: %v", err)
	}
}

// An alert that was being sent when its incident was muted, and then fails,
// is cancelled: it does not come back for another try.
func TestWorkerAlertFailingAfterMuteIsCancelled(t *testing.T) {
	release := make(chan struct{})
	ch := &fakeChannel{typ: domain.ChannelWebhook, name: "wh", send: func(context.Context) error {
		<-release
		return errors.New("503 service unavailable")
	}}
	s, id := setup(t, ch, 5)
	w := NewWorker(s, channels.NewRegistry(ch), Options{Concurrency: 1}, quietLogger())

	ctx := context.Background()
	sched := w.newScheduler(ctx)
	defer sched.cancelSend()
	if n, err := sched.fill(ctx); err != nil || n != 1 {
		t.Fatalf("fill: %d started, err %v", n, err)
	}
	waitFor(t, "the send to start", func() bool { return ch.calls.Load() == 1 })
	mute(t, s)
	close(release)
	for sched.total > 0 {
		sched.finished(<-sched.done)
	}

	got := attempt(t, s, id)
	if got.State != domain.DeliveryCancelled || got.NextRetryAt != nil {
		t.Fatalf("alert failing after mute is %s (next retry %v), want cancelled with none", got.State, got.NextRetryAt)
	}
	w.now = func() time.Time { return time.Now().Add(24 * time.Hour) }
	if n := step(t, w); n != 0 || ch.calls.Load() != 1 {
		t.Fatalf("cancelled alert was retried: %d claimed, %d sends", n, ch.calls.Load())
	}
}

// Stopping the worker hands a send in flight back to the queue, unless its
// incident was muted meanwhile: then the alert is cancelled.
func TestWorkerShutdownCancelsAlertOfMutedIncident(t *testing.T) {
	ch := &fakeChannel{typ: domain.ChannelWebhook, name: "wh", send: func(ctx context.Context) error {
		<-ctx.Done()
		return ctx.Err()
	}}
	s, id := setup(t, ch, 5)
	w := NewWorker(s, channels.NewRegistry(ch), Options{
		Concurrency: 1, PollInterval: 10 * time.Millisecond, ShutdownGrace: 10 * time.Millisecond,
	}, quietLogger())

	cancel, wait := start(t, w)
	waitFor(t, "the send to start", func() bool { return ch.calls.Load() == 1 })
	mute(t, s)
	stop(cancel, wait)

	if got := attempt(t, s, id); got.State != domain.DeliveryCancelled || got.Attempts != 0 {
		t.Fatalf("expected cancelled/0, got %s/%d", got.State, got.Attempts)
	}
}

// The reaper cancels a stuck alert of a muted incident instead of requeueing
// it; a stuck recovery of the same incident is requeued as before.
func TestWorkerReaperCancelsStuckAlertOfMutedIncident(t *testing.T) {
	ch := &fakeChannel{typ: domain.ChannelWebhook, name: "wh"}
	s := newStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	enqueue(t, s, "alert", ch, 5, now.Add(-2*time.Hour))
	recovery := &domain.DeliveryAttempt{
		ID: "recovery", EventID: "e1", Channel: ch.typ, ChannelName: ch.name, Kind: domain.KindRecovery,
		State: domain.DeliverySending, MaxAttempts: 5, CreatedAt: now.Add(-time.Hour), UpdatedAt: now.Add(-time.Hour),
	}
	if err := s.CreateDeliveryAttempt(ctx, recovery); err != nil {
		t.Fatalf("create recovery: %v", err)
	}
	if _, err := s.ClaimDue(ctx, now.Add(-time.Hour), 1); err != nil {
		t.Fatalf("claim alert: %v", err)
	}
	mute(t, s)
	w := NewWorker(s, channels.NewRegistry(ch), Options{}, quietLogger())

	w.reap(ctx)

	if got := attempt(t, s, "alert").State; got != domain.DeliveryCancelled {
		t.Fatalf("stuck alert of a muted incident is %s, want cancelled", got)
	}
	if got := attempt(t, s, "recovery").State; got != domain.DeliveryPending {
		t.Fatalf("stuck recovery is %s, want pending", got)
	}
}

// tickStore counts RecordWorkerTick calls.
type tickStore struct {
	storage.Store
	ticks atomic.Int32
}

func (s *tickStore) RecordWorkerTick(ctx context.Context, at time.Time) error {
	s.ticks.Add(1)
	return s.Store.RecordWorkerTick(ctx, at)
}

// A running worker records its tick when it first polls the queue, and then
// not on every poll: at most once per tickEvery.
func TestWorkerRecordsItsTickSparingly(t *testing.T) {
	s := &tickStore{Store: newStore(t)}
	w := NewWorker(s, channels.NewRegistry(), Options{PollInterval: time.Millisecond}, quietLogger())
	cancel, wait := start(t, w)
	waitFor(t, "the first tick", func() bool { return s.ticks.Load() > 0 })
	time.Sleep(50 * time.Millisecond) // dozens of polls
	stop(cancel, wait)

	if n := s.ticks.Load(); n != 1 {
		t.Fatalf("ticks recorded = %d, want 1 within %s", n, tickEvery)
	}
	m, err := s.Monitoring(context.Background(), time.Now(), time.Now())
	if err != nil || m.LastWorkerTick == nil {
		t.Fatalf("stored tick = %v, err %v", m.LastWorkerTick, err)
	}
}

// One claim may take several attempts, all of the busiest channel still
// allowed more: the batch is capped so that channel stays within its limit.
func TestWorkerClaimBatchKeepsTheBusiestChannelWithinItsLimit(t *testing.T) {
	release := make(chan struct{})
	block := func(context.Context) error { <-release; return nil }
	a := &fakeChannel{typ: domain.ChannelWebhook, name: "a", send: block}
	b := &fakeChannel{typ: domain.ChannelWebhook, name: "b", send: block}
	s := newStore(t)
	t0 := time.Now().Add(-time.Hour)
	for i := range 10 {
		enqueue(t, s, fmt.Sprintf("a-%d", i), a, 5, t0.Add(time.Duration(i)*time.Second))
	}
	w := NewWorker(s, channels.NewRegistry(a, b), Options{Concurrency: 4}, quietLogger()) // 3 slots per channel

	ctx := context.Background()
	sch := w.newScheduler(ctx)
	defer sch.cancelSend()
	sch.inFlight["a"], sch.total = 1, 1 // a send of a already running
	if _, err := sch.fill(ctx); err != nil {
		t.Fatalf("fill: %v", err)
	}
	if got := sch.inFlight["a"]; got != 3 {
		t.Fatalf("channel a has %d sends in flight, want its limit 3", got)
	}
	close(release)
	for sch.total > 1 {
		sch.finished(<-sch.done)
	}
}

// An error the channel returns on its own during shutdown is a failed attempt,
// not a send the shutdown cut short: it is counted and keeps its error.
func TestWorkerShutdownCountsAChannelErrorAsAFailure(t *testing.T) {
	ch := &fakeChannel{typ: domain.ChannelWebhook, name: "wh", send: func(ctx context.Context) error {
		<-ctx.Done()
		return errors.New("webhook returned 500")
	}}
	s, id := setup(t, ch, 5)
	w := NewWorker(s, channels.NewRegistry(ch), Options{
		Concurrency: 2, PollInterval: 10 * time.Millisecond, ShutdownGrace: 10 * time.Millisecond,
	}, quietLogger())

	cancel, wait := start(t, w)
	waitFor(t, "the send to start", func() bool { return ch.calls.Load() == 1 })
	stop(cancel, wait)

	got := attempt(t, s, id)
	if got.State != domain.DeliveryFailed || got.Attempts != 1 || got.LastError == "" {
		t.Fatalf("expected failed/1 with an error, got %s/%d %q", got.State, got.Attempts, got.LastError)
	}
}

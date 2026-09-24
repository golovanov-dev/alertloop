// Package delivery contains the DB-backed delivery worker. It claims due
// delivery attempts, dispatches them to the matching channel, and records the
// outcome, applying capped exponential backoff and dead-lettering exhausted
// attempts.
package delivery

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"math/rand/v2"
	"net/url"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/golovanov-dev/alertloop/internal/channels"
	"github.com/golovanov-dev/alertloop/internal/domain"
	"github.com/golovanov-dev/alertloop/internal/storage"
)

// Options tunes worker behavior.
type Options struct {
	Concurrency  int
	PollInterval time.Duration
	BaseBackoff  time.Duration
	MaxBackoff   time.Duration
	// StaleAfter is how long an attempt may sit in `sending` before the reaper
	// assumes the worker that claimed it died and requeues it. NewWorker raises
	// it to twice the longest attempt timeout if that is longer.
	StaleAfter time.Duration
	// ShutdownGrace is how long sends in flight may finish after the worker is
	// told to stop. Sends still running then are cancelled and requeued.
	ShutdownGrace time.Duration
	// Fallbacks maps a channel name to the channel that gets its alerts once
	// they dead-letter (config.Channels.Fallbacks).
	Fallbacks map[string]string
	// PublicURL is config public_url; with it every notification carries a
	// link to its event in the admin console.
	PublicURL string
}

func (o *Options) applyDefaults() {
	if o.Concurrency <= 0 {
		o.Concurrency = 2
	}
	if o.PollInterval <= 0 {
		o.PollInterval = 2 * time.Second
	}
	if o.BaseBackoff <= 0 {
		o.BaseBackoff = 30 * time.Second
	}
	if o.MaxBackoff <= 0 {
		o.MaxBackoff = 30 * time.Minute
	}
	if o.StaleAfter <= 0 {
		o.StaleAfter = 5 * time.Minute
	}
	if o.ShutdownGrace <= 0 {
		// Fits inside the 10 s that `docker stop` waits before SIGKILL.
		o.ShutdownGrace = 5 * time.Second
	}
}

// Worker drains the delivery queue.
type Worker struct {
	store    storage.Store
	registry *channels.Registry
	opts     Options
	log      *slog.Logger
	now      func() time.Time
	// lastTick is when Run last recorded a tick. Run alone touches it.
	lastTick time.Time
}

// tickEvery is how often at most the worker records that it polled the queue.
// GET /v1/stats reports that time, so it is at most this plus PollInterval old
// while a worker runs.
const tickEvery = 15 * time.Second

// NewWorker builds a Worker.
func NewWorker(store storage.Store, registry *channels.Registry, opts Options, log *slog.Logger) *Worker {
	opts.applyDefaults()
	if log == nil {
		log = slog.Default()
	}
	w := &Worker{
		store:    store,
		registry: registry,
		opts:     opts,
		log:      log,
		now:      time.Now,
	}
	// An attempt stays `sending` for one send plus recording its result. The
	// reaper must not take back a send that is still running, so the stale
	// window is at least twice the longest attempt.
	for _, t := range registry.Targets() {
		ch, _ := registry.Get(t.Name)
		if d := 2 * w.attemptTimeout(ch); d > w.opts.StaleAfter {
			w.opts.StaleAfter = d
		}
	}
	return w
}

// perChannelLimit is how many slots one channel may occupy at once. With two
// or more channels configured, one slot always stays free for the others, so a
// single channel that hangs cannot hold up alerts to the rest. With one channel
// there is no one to keep a slot for, and it may use them all. With Concurrency
// 1 there is nothing to share.
func (w *Worker) perChannelLimit() int {
	if w.registry.Len() < 2 {
		return w.opts.Concurrency
	}
	return max(1, w.opts.Concurrency-1)
}

// attemptTimeout is how long one attempt to ch may take: the channel's own
// timeout, or domain.DefaultChannelTimeout for a channel that has none.
func (w *Worker) attemptTimeout(ch channels.Channel) time.Duration {
	if t := ch.Timeout(); t > 0 {
		return t
	}
	return domain.DefaultChannelTimeout
}

// Run delivers due attempts until ctx is cancelled. It blocks; callers
// typically run it in a goroutine. It returns only after every send it started
// has been recorded or handed back to the queue.
func (w *Worker) Run(ctx context.Context) {
	ticker := time.NewTicker(w.opts.PollInterval)
	defer ticker.Stop()

	w.log.Info("delivery worker started",
		"concurrency", w.opts.Concurrency,
		"per_channel_limit", w.perChannelLimit(),
		"poll_interval", w.opts.PollInterval.String(),
		"stale_after", w.opts.StaleAfter.String(),
		"channels", w.registry.Len(),
	)
	if w.opts.Concurrency == 1 && w.registry.Len() > 1 {
		w.log.Warn("worker.concurrency is 1 with several channels: one hung channel delays alerts to the others",
			"channels", w.registry.Len())
	}

	var reaper sync.WaitGroup
	reaper.Add(1)
	go func() {
		defer reaper.Done()
		w.runReaper(ctx)
	}()
	defer reaper.Wait()

	s := w.newScheduler(ctx)
	for {
		if _, err := s.fill(ctx); err != nil {
			if ctx.Err() == nil {
				w.log.Error("claiming due deliveries failed", "error", err)
			}
		} else {
			w.recordTick(ctx)
		}
		select {
		case <-ctx.Done():
			w.log.Info("delivery worker stopping", "in_flight", s.total)
			s.shutdown()
			return
		case name := <-s.done:
			s.finished(name)
		case <-ticker.C:
		}
	}
}

// recordTick stores that the queue was polled, at most once per tickEvery.
func (w *Worker) recordTick(ctx context.Context) {
	now := w.now()
	if now.Sub(w.lastTick) < tickEvery {
		return
	}
	if err := w.store.RecordWorkerTick(ctx, now.UTC()); err != nil {
		if ctx.Err() == nil {
			w.log.Error("recording the worker tick failed", "error", err)
		}
		return
	}
	w.lastTick = now
}

// scheduler keeps every worker slot busy: a slot that frees takes the next due
// attempt at once instead of waiting for the other slots.
type scheduler struct {
	w *Worker
	// sendCtx is not cancelled with the Run context: a send in flight at
	// shutdown gets ShutdownGrace to finish.
	sendCtx    context.Context
	cancelSend context.CancelFunc
	inFlight   map[string]int // channel name -> sends running
	total      int
	done       chan string // channel name of each finished send
	wg         sync.WaitGroup
}

func (w *Worker) newScheduler(ctx context.Context) *scheduler {
	sendCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	return &scheduler{
		w:          w,
		sendCtx:    sendCtx,
		cancelSend: cancel,
		inFlight:   map[string]int{},
		done:       make(chan string, w.opts.Concurrency),
	}
}

// fill claims due attempts and starts each, until every slot is busy or
// nothing claimable is left. A channel at its slot limit is skipped, so its
// backlog does not block the attempts queued behind it. Claiming only for free
// slots means no claimed attempt waits unstarted. It returns the number of
// sends started.
//
// One claim takes attempts for several free slots at once: the database orders
// the due attempts on every claim (SQLite sorts the whole due set), so claiming
// slot by slot would repeat that work Concurrency times. The batch is capped so
// that even if all of it goes to the busiest channel still allowed to take
// more, that channel stays within its slot limit.
func (s *scheduler) fill(ctx context.Context) (int, error) {
	started := 0
	limit := s.w.perChannelLimit()
	for s.total < s.w.opts.Concurrency && ctx.Err() == nil {
		var skip []string
		busiest := 0
		for name, n := range s.inFlight {
			if n >= limit {
				skip = append(skip, name)
			} else {
				busiest = max(busiest, n)
			}
		}
		want := min(s.w.opts.Concurrency-s.total, limit-busiest)
		claimed, err := s.claim(ctx, want, skip)
		if err != nil {
			return started, err
		}
		for _, att := range claimed {
			s.inFlight[att.ChannelName]++
			s.total++
			started++
			s.wg.Add(1)
			go func() {
				defer s.wg.Done()
				s.w.process(s.sendCtx, att)
				s.done <- att.ChannelName
			}()
		}
		if len(claimed) < want {
			return started, nil
		}
	}
	return started, nil
}

// claim runs one ClaimDue that a shutdown does not cut off at once: cancelled
// after the commit, the claimed rows would sit in `sending` until the reaper.
// A claim still running claimAfterStop after the stop is cancelled.
func (s *scheduler) claim(ctx context.Context, want int, skip []string) ([]domain.DeliveryAttempt, error) {
	claimCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	defer cancel()
	stop := context.AfterFunc(ctx, func() { time.AfterFunc(claimAfterStop, cancel) })
	defer stop()
	return s.w.store.ClaimDue(claimCtx, s.w.now().UTC(), want, skip...)
}

// claimAfterStop is how long a claim in progress may run on after the stop:
// with ShutdownGrace and the 3 s save, a stop stays inside the 10 s that
// docker stop waits before SIGKILL.
const claimAfterStop = time.Second

// finished frees the slot of a send whose outcome has been recorded.
func (s *scheduler) finished(name string) {
	s.total--
	s.inFlight[name]--
	if s.inFlight[name] <= 0 {
		delete(s.inFlight, name)
	}
}

// shutdown waits up to ShutdownGrace for the sends in flight, then cancels the
// rest; process hands a cancelled attempt back to the queue.
func (s *scheduler) shutdown() {
	defer s.cancelSend()
	grace := time.NewTimer(s.w.opts.ShutdownGrace)
	defer grace.Stop()
	for s.total > 0 {
		select {
		case name := <-s.done:
			s.finished(name)
		case <-grace.C:
			s.w.log.Warn("cancelling deliveries in flight at shutdown; they return to the queue",
				"count", s.total)
			s.cancelSend()
			for s.total > 0 {
				s.finished(<-s.done)
			}
		}
	}
	s.wg.Wait()
}

// runReaper periodically requeues attempts stuck in `sending` (their worker
// died mid-send) so no delivery is silently lost. It blocks until ctx is done.
func (w *Worker) runReaper(ctx context.Context) {
	interval := w.opts.StaleAfter / 2
	if interval < 30*time.Second {
		interval = 30 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.reap(ctx)
		}
	}
}

// reap requeues attempts that have been `sending` for longer than StaleAfter.
func (w *Worker) reap(ctx context.Context) {
	staleBefore := w.now().UTC().Add(-w.opts.StaleAfter)
	n, err := w.store.RequeueStuckSending(ctx, staleBefore)
	if err != nil {
		if ctx.Err() == nil {
			w.log.Error("reaper failed to requeue stuck deliveries", "error", err)
		}
		return
	}
	if n > 0 {
		w.log.Warn("took back deliveries stuck in sending from a dead worker: requeued, or cancelled if the incident is muted",
			"count", n)
	}
}

// process delivers one attempt and records the result.
//
// An attempt cut short by shutdown goes back to `pending` with its attempt
// count unchanged: it did not fail, the worker stopped. The receiver may still
// have got it, so it can arrive twice (at-least-once delivery).
//
// An alert that did not go out while its incident is muted is stored as
// `cancelled` instead of `failed`, `dead_letter` or `pending` (the store
// decides, see storage.MarkResult); the log reports what was stored.
func (w *Worker) process(ctx context.Context, att domain.DeliveryAttempt) {
	claimed := att
	att.Attempts++

	sendErr := w.deliver(ctx, att)
	// Only a send the shutdown cut short goes back uncounted; an error the
	// channel returned on its own is a failed attempt, even during shutdown.
	if sendErr != nil && ctx.Err() != nil && errors.Is(sendErr, context.Canceled) {
		claimed.State = domain.DeliveryPending
		w.save(ctx, &claimed, nil)
		if !w.logCancelled(claimed) {
			w.log.Info("delivery returned to the queue at shutdown",
				"id", att.ID, "channel", att.Channel, "channel_name", att.ChannelName)
		}
		return
	}
	if sendErr == nil {
		att.State = domain.DeliverySent
		att.NextRetryAt = nil
		att.LastError = ""
		w.save(ctx, &att, nil)
		w.log.Info("delivery sent",
			"id", att.ID, "channel", att.Channel, "channel_name", att.ChannelName, "attempts", att.Attempts)
		return
	}

	// By runes. A byte-cut through a multi-byte character produces text
	// PostgreSQL refuses to store, which used to leave the attempt stuck in
	// `sending` and requeued by the reaper every five minutes, forever.
	att.LastError = domain.TruncateRunes(sendErr.Error(), domain.MaxLastErrorRunes)
	var fallback *domain.DeliveryAttempt
	if att.Attempts >= att.MaxAttempts {
		att.State = domain.DeliveryDeadLetter
		att.NextRetryAt = nil
		fallback = w.fallbackFor(att)
	} else {
		next := w.now().UTC().Add(w.backoff(att.Attempts))
		att.State = domain.DeliveryFailed
		att.NextRetryAt = &next
	}
	w.save(ctx, &att, fallback)
	if w.logCancelled(att) {
		return
	}
	if att.State == domain.DeliveryDeadLetter {
		fb := ""
		if fallback != nil && fallback.FallbackOf != nil {
			fb = fallback.ChannelName
		}
		w.log.Warn("delivery dead-lettered",
			"id", att.ID, "channel", att.Channel, "channel_name", att.ChannelName,
			"attempts", att.Attempts, "fallback", fb, "error", sendErr)
		return
	}
	w.log.Warn("delivery failed, will retry",
		"id", att.ID, "channel", att.Channel, "channel_name", att.ChannelName, "attempts", att.Attempts,
		"next_retry_at", att.NextRetryAt.Format(time.RFC3339), "error", sendErr)
}

// fallbackFor builds the alert to queue on the fallback of att's channel when
// att dead-letters, or returns nil. Only an alert is redirected, and only once:
// a recovery follows its alert on its own (storage.MarkResult), and an alert
// that is itself a fallback goes nowhere further.
func (w *Worker) fallbackFor(att domain.DeliveryAttempt) *domain.DeliveryAttempt {
	if att.Kind.OrAlert() != domain.KindAlert || att.IsFallback() {
		return nil
	}
	ch, ok := w.registry.Get(w.opts.Fallbacks[att.ChannelName])
	if !ok {
		return nil
	}
	now := w.now().UTC()
	return &domain.DeliveryAttempt{
		ID: uuid.NewString(), EventID: att.EventID, Channel: ch.Type(), ChannelName: ch.Name(),
		Kind: domain.KindAlert, State: domain.DeliveryPending, MaxAttempts: att.MaxAttempts,
		CreatedAt: now, UpdatedAt: now,
	}
}

// logCancelled logs an attempt the store cancelled instead of recording the
// worker's outcome, and reports whether it did.
func (w *Worker) logCancelled(att domain.DeliveryAttempt) bool {
	if att.State != domain.DeliveryCancelled {
		return false
	}
	w.log.Info("delivery cancelled: the incident was muted while the alert was being sent",
		"id", att.ID, "channel", att.Channel, "channel_name", att.ChannelName, "error", att.LastError)
	return true
}

// deliver dispatches the attempt to its channel, giving it the channel's
// timeout.
func (w *Worker) deliver(ctx context.Context, att domain.DeliveryAttempt) error {
	ch, ok := w.registry.Get(att.ChannelName)
	if !ok {
		// Channel no longer configured; treat as a terminal-style failure so it
		// retries/dead-letters rather than silently disappearing.
		return errNoChannel{att.ChannelName}
	}
	event, err := w.store.GetEvent(ctx, att.EventID)
	if err != nil {
		return err
	}

	n := domain.Notification{Event: event, Kind: att.Kind.OrAlert()}
	if w.opts.PublicURL != "" {
		n.EventURL = w.opts.PublicURL + "/admin/#/events/" + url.PathEscape(event.ID)
	}
	if att.FallbackOf != nil {
		src, err := w.store.GetDeliveryAttempt(ctx, att.FallbackOf.ID)
		if err != nil {
			return fmt.Errorf("read the attempt this fallback redirects: %w", err)
		}
		n.Fallback = &domain.FallbackOrigin{Channel: src.ChannelName, Error: src.LastError, Attempt: src.ID}
	}

	sendCtx, cancel := context.WithTimeout(ctx, w.attemptTimeout(ch))
	defer cancel()
	return ch.Send(sendCtx, n)
}

// save records the outcome. When it is stored att holds what the store actually
// wrote, which differs from what the worker asked for when the store cancelled
// the alert of a muted incident.
func (w *Worker) save(ctx context.Context, att *domain.DeliveryAttempt, fallback *domain.DeliveryAttempt) {
	// Use a background-derived context so a cancelled worker still records the
	// outcome it just produced.
	// 3 s: with the 5 s ShutdownGrace a stop stays inside the 10 s that
	// docker stop waits before SIGKILL.
	saveCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer cancel()
	att.UpdatedAt = w.now().UTC()
	err := w.store.MarkResult(saveCtx, att, fallback)
	switch {
	case err == nil:
	case errors.Is(err, domain.ErrNotFound):
		// The row is no longer `sending`: the reaper decided this attempt was
		// stuck and took it back while we were still sending - requeued, or
		// cancelled when its incident is muted. Logged so a flood of these
		// points at a StaleAfter too close to the channel timeouts.
		w.log.Warn("delivery result discarded: the reaper took the attempt back while it was in flight",
			"id", att.ID, "channel", att.Channel, "channel_name", att.ChannelName)
	default:
		w.log.Error("failed to record delivery result",
			"id", att.ID, "channel", att.Channel, "channel_name", att.ChannelName, "error", err)
	}
}

// backoff returns the retry delay for the given attempt number using capped
// exponential growth with EQUAL jitter: the delay is drawn from [exp/2, exp].
//
// Equal, not full ([0, exp]). Full jitter spreads a thundering herd better, but
// it also lets a retry fire almost immediately, which for a channel that is
// down means burning an attempt for nothing. Half the interval is kept as a
// floor deliberately. (The comment here used to say "full jitter" while the
// code did this - the code was right.)
func (w *Worker) backoff(attempt int) time.Duration {
	exp := float64(w.opts.BaseBackoff) * math.Pow(2, float64(attempt-1))
	if exp > float64(w.opts.MaxBackoff) {
		exp = float64(w.opts.MaxBackoff)
	}
	// math/rand/v2's top-level Float64 is safe for concurrent use by multiple
	// worker goroutines.
	half := exp / 2
	return time.Duration(half + rand.Float64()*half)
}

type errNoChannel struct{ name string }

func (e errNoChannel) Error() string { return "no channel configured with name " + e.name }

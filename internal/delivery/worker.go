// Package delivery contains the DB-backed delivery worker. It claims due
// delivery attempts, dispatches them to the matching channel, and records the
// outcome, applying capped exponential backoff and dead-lettering exhausted
// attempts.
package delivery

import (
	"context"
	"errors"
	"log/slog"
	"math"
	"math/rand/v2"
	"time"

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
	// SendTimeout bounds a single channel send. Zero means no explicit bound
	// beyond the channel's own client timeout.
	SendTimeout time.Duration
	// StaleAfter is how long an attempt may sit in `sending` before the reaper
	// assumes the worker that claimed it died and requeues it.
	StaleAfter time.Duration
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
	if o.SendTimeout <= 0 {
		o.SendTimeout = 30 * time.Second
	}
	if o.StaleAfter <= 0 {
		o.StaleAfter = 5 * time.Minute
	}
	// The stale window must comfortably exceed a single send, or the reaper
	// could requeue an attempt that is still in flight.
	if o.StaleAfter < 2*o.SendTimeout {
		o.StaleAfter = 2 * o.SendTimeout
	}
}

// Worker drains the delivery queue.
type Worker struct {
	store    storage.Store
	registry *channels.Registry
	opts     Options
	log      *slog.Logger
	now      func() time.Time
}

// NewWorker builds a Worker.
func NewWorker(store storage.Store, registry *channels.Registry, opts Options, log *slog.Logger) *Worker {
	opts.applyDefaults()
	if log == nil {
		log = slog.Default()
	}
	return &Worker{
		store:    store,
		registry: registry,
		opts:     opts,
		log:      log,
		now:      time.Now,
	}
}

// Run polls for due deliveries until ctx is cancelled. It blocks; callers
// typically run it in a goroutine.
func (w *Worker) Run(ctx context.Context) {
	ticker := time.NewTicker(w.opts.PollInterval)
	defer ticker.Stop()

	w.log.Info("delivery worker started",
		"concurrency", w.opts.Concurrency,
		"poll_interval", w.opts.PollInterval.String(),
		"channels", w.registry.Len(),
	)

	go w.runReaper(ctx)

	for {
		// Drain as much as possible each tick before waiting again.
		for {
			n, err := w.tick(ctx)
			if err != nil {
				if errors.Is(err, context.Canceled) {
					return
				}
				w.log.Error("delivery tick failed", "error", err)
				break
			}
			if n == 0 {
				break
			}
		}

		select {
		case <-ctx.Done():
			w.log.Info("delivery worker stopping")
			return
		case <-ticker.C:
		}
	}
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
			staleBefore := w.now().UTC().Add(-w.opts.StaleAfter)
			n, err := w.store.RequeueStuckSending(ctx, staleBefore)
			if err != nil {
				if errors.Is(err, context.Canceled) {
					return
				}
				w.log.Error("reaper failed to requeue stuck deliveries", "error", err)
				continue
			}
			if n > 0 {
				w.log.Warn("requeued stuck deliveries from a dead worker", "count", n)
			}
		}
	}
}

// tick claims a batch of due attempts and processes them concurrently. It
// returns the number of attempts processed.
func (w *Worker) tick(ctx context.Context) (int, error) {
	claimed, err := w.store.ClaimDue(ctx, w.now().UTC(), w.opts.Concurrency)
	if err != nil {
		return 0, err
	}
	if len(claimed) == 0 {
		return 0, nil
	}

	sem := make(chan struct{}, w.opts.Concurrency)
	done := make(chan struct{}, len(claimed))
	for i := range claimed {
		att := claimed[i]
		sem <- struct{}{}
		go func() {
			defer func() { <-sem; done <- struct{}{} }()
			w.process(ctx, att)
		}()
	}
	for range claimed {
		<-done
	}
	return len(claimed), nil
}

// process delivers one attempt and records the result.
func (w *Worker) process(ctx context.Context, att domain.DeliveryAttempt) {
	att.Attempts++

	sendErr := w.deliver(ctx, att)
	if sendErr == nil {
		att.State = domain.DeliverySent
		att.NextRetryAt = nil
		att.LastError = ""
		w.save(ctx, &att)
		w.log.Info("delivery sent", "id", att.ID, "channel", att.Channel, "attempts", att.Attempts)
		return
	}

	att.LastError = truncate(sendErr.Error(), 1000)
	if att.Attempts >= att.MaxAttempts {
		att.State = domain.DeliveryDeadLetter
		att.NextRetryAt = nil
		w.log.Warn("delivery dead-lettered",
			"id", att.ID, "channel", att.Channel, "attempts", att.Attempts, "error", sendErr)
	} else {
		next := w.now().UTC().Add(w.backoff(att.Attempts))
		att.State = domain.DeliveryFailed
		att.NextRetryAt = &next
		w.log.Warn("delivery failed, will retry",
			"id", att.ID, "channel", att.Channel, "attempts", att.Attempts,
			"next_retry_at", next.Format(time.RFC3339), "error", sendErr)
	}
	w.save(ctx, &att)
}

// deliver dispatches the attempt to its channel.
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

	sendCtx := ctx
	if w.opts.SendTimeout > 0 {
		var cancel context.CancelFunc
		sendCtx, cancel = context.WithTimeout(ctx, w.opts.SendTimeout)
		defer cancel()
	}
	return ch.Send(sendCtx, event)
}

func (w *Worker) save(ctx context.Context, att *domain.DeliveryAttempt) {
	// Use a background-derived context so a cancelled worker still records the
	// outcome it just produced.
	saveCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := w.store.MarkResult(saveCtx, att); err != nil {
		w.log.Error("failed to record delivery result", "id", att.ID, "error", err)
	}
}

// backoff returns the retry delay for the given attempt number using capped
// exponential growth with full jitter.
func (w *Worker) backoff(attempt int) time.Duration {
	exp := float64(w.opts.BaseBackoff) * math.Pow(2, float64(attempt-1))
	if exp > float64(w.opts.MaxBackoff) {
		exp = float64(w.opts.MaxBackoff)
	}
	// Full jitter in [exp/2, exp]. math/rand/v2's top-level Float64 is safe for
	// concurrent use by multiple worker goroutines.
	half := exp / 2
	return time.Duration(half + rand.Float64()*half)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

type errNoChannel struct{ name string }

func (e errNoChannel) Error() string { return "no channel configured with name " + e.name }

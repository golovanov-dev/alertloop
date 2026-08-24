package service

import (
	"context"
	"log/slog"
	"time"

	"github.com/golovanov-dev/alertloop/internal/domain"
	"github.com/golovanov-dev/alertloop/internal/storage"
	"github.com/google/uuid"
)

// RecoveryNotifier enqueues a "resolved" notice when an incident closes.
//
// Without it the product tells you PostgreSQL is down and never tells you it
// came back, which leaves the reader to guess. With it, closing an incident is
// a notification in its own right.
//
// Who gets told is not a routing decision. It is the channels that received the
// alert — routing already ran once, at ingestion, and re-running it now would
// answer a different question: rules may have changed, and the event's severity
// may have moved since. A recovery goes to the people who were told about the
// problem, and to nobody else.
//
// A nil *RecoveryNotifier is valid and does nothing, which is what
// `notify_on_resolve: false` gives you.
type RecoveryNotifier struct {
	store       storage.Store
	maxAttempts int
	log         *slog.Logger
}

// NewRecoveryNotifier builds a notifier. It returns nil when enabled is false,
// so the disabled case is a nil pointer rather than a flag every caller has to
// remember to check.
func NewRecoveryNotifier(store storage.Store, enabled bool, maxAttempts int, log *slog.Logger) *RecoveryNotifier {
	if !enabled {
		return nil
	}
	if maxAttempts <= 0 {
		maxAttempts = domain.DefaultMaxAttempts
	}
	if log == nil {
		log = slog.Default()
	}
	return &RecoveryNotifier{store: store, maxAttempts: maxAttempts, log: log}
}

// Notify enqueues one recovery delivery per channel that was told about the
// incident. It returns how many were created.
//
// Failing to enqueue a recovery must never fail the resolve itself: the
// incident IS closed, that fact is already committed, and reporting an error to
// the caller would invite it to retry a resolve that already succeeded. The
// error is logged and swallowed.
func (n *RecoveryNotifier) Notify(ctx context.Context, e *domain.Event, at time.Time) int {
	if n == nil || e == nil {
		return 0
	}

	targets, err := n.store.AlertedChannels(ctx, e.ID)
	if err != nil {
		n.log.Error("could not determine who to notify about a recovery; the incident is still closed",
			"event_id", e.ID, "dedupe_key", e.DedupeKey, "error", err)
		return 0
	}
	if len(targets) == 0 {
		// Nobody was told about the problem — there is nothing to correct.
		// A silent no-op, not a warning: this is the normal case for an
		// installation with no channels configured.
		return 0
	}

	created := 0
	for _, t := range targets {
		d := &domain.DeliveryAttempt{
			ID:          uuid.NewString(),
			EventID:     e.ID,
			Channel:     t.Type,
			ChannelName: t.Name,
			Kind:        domain.KindRecovery,
			State:       domain.DeliveryPending,
			MaxAttempts: n.maxAttempts,
			CreatedAt:   at,
			UpdatedAt:   at,
		}
		if err := n.store.CreateDeliveryAttempt(ctx, d); err != nil {
			// One channel failing to enqueue must not cost the others theirs.
			n.log.Error("could not enqueue a recovery notice",
				"event_id", e.ID, "channel", t.Name, "error", err)
			continue
		}
		created++
	}

	n.log.Info("recovery notice queued",
		"event_id", e.ID, "dedupe_key", e.DedupeKey, "channels", created)
	return created
}

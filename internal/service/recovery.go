package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/golovanov-dev/alertloop/internal/domain"
	"github.com/golovanov-dev/alertloop/internal/storage"
)

// RecoveryNotifier decides whether closing an incident sends a "resolved"
// notice, and with how many attempts.
//
// Without it the product tells you PostgreSQL is down and never tells you it
// came back, which leaves the reader to guess. With it, closing an incident is
// a notification in its own right.
//
// Who gets told is not a routing decision. It is the channels that received the
// alert — routing already ran once, at ingestion, and re-running it now would
// answer a different question: rules may have changed, and the event's severity
// may have moved since. A recovery goes to the people who were told about the
// problem, and to nobody else. The attempts are queued by the store in the
// same transaction that closes the incident.
//
// A nil *RecoveryNotifier is valid and sends nothing, which is what
// `notify_on_resolve: false` gives you.
type RecoveryNotifier struct {
	maxAttempts int
	log         *slog.Logger
}

// NewRecoveryNotifier builds a notifier. It returns nil when enabled is false,
// so the disabled case is a nil pointer rather than a flag every caller has to
// remember to check.
func NewRecoveryNotifier(enabled bool, maxAttempts int, log *slog.Logger) *RecoveryNotifier {
	if !enabled {
		return nil
	}
	if maxAttempts <= 0 {
		maxAttempts = domain.DefaultMaxAttempts
	}
	if log == nil {
		log = slog.Default()
	}
	return &RecoveryNotifier{maxAttempts: maxAttempts, log: log}
}

// attemptsFor is the one decision whether closing e sends a recovery notice.
// It returns the attempt budget for each recovery, or 0 for none:
//   - only an incident has a problem that can be over; a business event or an
//     audit entry closed by its source tells nobody anything;
//   - a muted incident stays quiet, however it is closed — by hand, from the
//     console, or by its source. Mute means "stop telling me about this".
func (n *RecoveryNotifier) attemptsFor(e *domain.Event) int {
	if n == nil || e.Type != domain.EventIncident {
		return 0
	}
	if e.State == domain.StateMuted {
		n.log.Info("incident closed while muted; no recovery notice sent",
			"event_id", e.ID, "dedupe_key", e.DedupeKey)
		return 0
	}
	return n.maxAttempts
}

// logQueued says how many recovery notices closing e queued, so "the RESOLVED
// message never came" can be answered from the log: 0 means no channel had
// received the alert.
func (n *RecoveryNotifier) logQueued(ctx context.Context, store storage.Store, e *domain.Event) {
	page, err := store.ListDeliveryAttempts(context.WithoutCancel(ctx),
		storage.DeliveryFilter{EventID: e.ID, Kind: domain.KindRecovery}, 100, "")
	if err != nil {
		n.log.Warn("incident resolved; could not count its recovery notices",
			"event_id", e.ID, "dedupe_key", e.DedupeKey, "error", err)
		return
	}
	n.log.Info("incident resolved; recovery notices queued",
		"event_id", e.ID, "dedupe_key", e.DedupeKey, "channels", len(page.Items))
}

// maxTransitionTries bounds the re-read loop in transition. Each retry means
// another request changed the event first; three in a row is contention no
// operator produces by hand.
const maxTransitionTries = 3

// transition moves e to the state next returns for its current state, with a
// conditional update: when another request changed the event first, it
// re-reads the event and asks next again. next returning the current state
// means there is nothing to do, and e is returned unchanged with changed=false.
//
// It is the single path by which an incident closes, for the ingest path and
// the manual path alike: the recovery decision and the cancellation of queued
// alerts on mute are made here and committed with the state change.
func transition(ctx context.Context, store storage.Store, rec *RecoveryNotifier, e *domain.Event, at time.Time,
	next func(domain.EventState) (domain.EventState, error)) (updated *domain.Event, changed bool, err error) {
	for try := 0; try < maxTransitionTries; try++ {
		to, err := next(e.State)
		if err != nil {
			return nil, false, err
		}
		if to == e.State {
			return e, false, nil
		}
		c := storage.StateChange{From: e.State, To: to, At: at, CancelAlerts: to == domain.StateMuted}
		if to == domain.StateResolved {
			c.RecoveryMaxAttempts = rec.attemptsFor(e)
		}
		updated, err := store.TransitionEvent(ctx, e.ID, c)
		if err == nil {
			if c.RecoveryMaxAttempts > 0 {
				rec.logQueued(ctx, store, e)
			}
			return updated, true, nil
		}
		if !errors.Is(err, storage.ErrStateChanged) {
			return nil, false, err
		}
		if e, err = store.GetEvent(ctx, e.ID); err != nil {
			return nil, false, err
		}
	}
	return nil, false, fmt.Errorf("%w: the event kept changing while the action was applied; retry", domain.ErrInvalidTransition)
}

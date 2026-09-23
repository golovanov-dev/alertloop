// Package storage defines AlertLoop's persistence interfaces and a SQL-backed
// implementation that works against both SQLite (local/demo) and PostgreSQL
// (production).
package storage

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/golovanov-dev/alertloop/internal/domain"
)

// EventFilter narrows an event listing.
type EventFilter struct {
	Type     domain.EventType
	Severity domain.Severity
	State    domain.EventState
	Source   string
	// Search keeps events whose message contains it, ignoring case. `%` and
	// `_` in it are literal.
	Search string
}

// Monitoring is the state of AlertLoop itself as absolute values, so a monitor
// needs no memory of an earlier reading.
type Monitoring struct {
	// OpenIncidents counts incidents that are not resolved.
	OpenIncidents int64
	// OldestDueSince is when the longest-waiting attempt the worker could take
	// now became due; nil when none is due.
	OldestDueSince *time.Time
	// DeadLetters counts attempts dead-lettered since the time asked for and
	// still in dead_letter.
	DeadLetters int64
	// LastWorkerTick is the last time any worker polled the queue; nil when no
	// worker has since the heartbeat was added.
	LastWorkerTick *time.Time
}

// EventUpdate carries the fields a repeated `firing` refreshes on an incident
// that is already open. State is not among them: an incident an operator has
// acknowledged stays acknowledged while the underlying problem persists.
type EventUpdate struct {
	Severity   domain.Severity
	Message    string
	Payload    json.RawMessage
	LastSeenAt time.Time
	// AlertsOnRise are alert attempts for the channels the event routes to at
	// Severity. They are queued only when Severity ranks above the stored one
	// and the incident is not muted, and only to channels with no alert
	// attempt of this event yet, in any state.
	AlertsOnRise []*domain.DeliveryAttempt
}

// StateChange is a conditional event state transition and the delivery changes
// that must commit together with it.
type StateChange struct {
	From domain.EventState
	To   domain.EventState
	At   time.Time
	// RecoveryMaxAttempts, when positive, queues a pending recovery attempt for
	// every channel an alert of the event was queued to, cancelled alerts
	// excepted. A dead-lettered alert counts: ClaimDue holds its recovery until
	// the alert is replayed and sent.
	RecoveryMaxAttempts int
	// CancelAlerts moves the event's pending and failed alert attempts to
	// cancelled.
	CancelAlerts bool
}

// ErrStateChanged is returned by TransitionEvent when the event is no longer
// in the state the caller read.
var ErrStateChanged = errors.New("event state changed concurrently")

// DeliveryFilter narrows a delivery-attempt listing.
type DeliveryFilter struct {
	State       domain.DeliveryState
	Channel     domain.ChannelType
	ChannelName string
	EventID     string
	Kind        domain.DeliveryKind
}

// Page is a cursor-paginated result set.
type Page[T any] struct {
	Items      []T
	NextCursor string
}

// EventStore persists and queries events.
type EventStore interface {
	// CreateEvent inserts e. If e.DedupeKey is non-empty and an event with the
	// same key already exists, the existing event is returned with created set
	// to false and no new row is inserted. Test fixtures only: ingestion goes
	// through CreateEventWithDeliveries.
	CreateEvent(ctx context.Context, e *domain.Event) (stored *domain.Event, created bool, err error)
	// CreateEventWithDeliveries inserts e together with its delivery attempts in
	// a single transaction. On a dedupe hit the existing OPEN incident is
	// returned with created=false and no deliveries are inserted; a resolved
	// event never blocks a new one, so the same failure can recur.
	CreateEventWithDeliveries(ctx context.Context, e *domain.Event, deliveries []*domain.DeliveryAttempt) (stored *domain.Event, created bool, err error)
	// RefreshOpenEvent applies a repeated `firing` to the open incident id:
	// last_seen_at moves forward and severity, message, and payload adopt the
	// newest report. The only attempts it creates are u.AlertsOnRise, in the
	// same transaction. Returns domain.ErrNotFound if the incident was
	// resolved in the meantime.
	RefreshOpenEvent(ctx context.Context, id string, u EventUpdate) (*domain.Event, error)
	// EventByDedupe returns the open incident carrying key or, when none is
	// open, the most recent closed one. A key that has never been seen returns
	// domain.ErrNotFound.
	EventByDedupe(ctx context.Context, key string) (*domain.Event, error)
	GetEvent(ctx context.Context, id string) (*domain.Event, error)
	ListEvents(ctx context.Context, f EventFilter, limit int, cursor string) (Page[domain.Event], error)
	// TransitionEvent applies c to event id in one transaction, only if the
	// event is still in c.From. Otherwise nothing changes and it returns
	// ErrStateChanged, also when the event is gone: a caller that must tell
	// the two apart re-reads it with GetEvent.
	TransitionEvent(ctx context.Context, id string, c StateChange) (*domain.Event, error)
	// DeleteEventsBefore removes events whose resolved_at, or last_seen_at
	// while unresolved, is before cutoff, with their attempts, and returns the
	// number deleted: an incident still reported stays. Retention cleanup.
	DeleteEventsBefore(ctx context.Context, cutoff time.Time) (int64, error)
	// CountEventsByState returns the number of events in each lifecycle state.
	CountEventsByState(ctx context.Context) (map[string]int64, error)
}

// DeliveryStore persists and queries delivery attempts and backs the delivery
// queue.
type DeliveryStore interface {
	CreateDeliveryAttempt(ctx context.Context, d *domain.DeliveryAttempt) error
	GetDeliveryAttempt(ctx context.Context, id string) (*domain.DeliveryAttempt, error)
	ListDeliveryAttempts(ctx context.Context, f DeliveryFilter, limit int, cursor string) (Page[domain.DeliveryAttempt], error)
	// ClaimDue atomically claims up to limit deliverable attempts (pending or
	// failed with next_retry_at due), transitioning them to `sending`, and
	// returns them. Concurrent workers never claim the same row. A recovery is
	// held back until the alert of its event to its channel is sent. Attempts
	// to the channels in skipChannels are not claimed.
	ClaimDue(ctx context.Context, now time.Time, limit int, skipChannels ...string) ([]domain.DeliveryAttempt, error)
	// MarkResult records the outcome of a claimed (`sending`) delivery attempt:
	// `sent`, `failed` (with next_retry_at set), `dead_letter` when attempts are
	// exhausted, or `pending` when the worker hands back an attempt it could not
	// finish. An alert that is not `sent` while its event is `muted` is stored
	// as `cancelled` instead; d.State then reports what was stored. Returns
	// domain.ErrNotFound if the attempt is no longer `sending`.
	MarkResult(ctx context.Context, d *domain.DeliveryAttempt) error
	// Replay re-queues a dead_letter attempt as pending with attempts reset to
	// zero, so it gets a full new cycle of retries, and returns the updated
	// attempt.
	Replay(ctx context.Context, id string, at time.Time) (*domain.DeliveryAttempt, error)
	// RequeueStuckSending moves attempts that have been stuck in the `sending`
	// state since before staleBefore back to `pending`, so deliveries are not
	// lost when a worker dies mid-send (OOM, kill -9, redeploy). An alert of an
	// event in `muted` becomes `cancelled` instead. Returns the number of
	// attempts moved.
	RequeueStuckSending(ctx context.Context, staleBefore time.Time) (int64, error)
	// DeleteDeliveryAttemptsBefore removes ORPHANED attempts created before
	// cutoff (their event is gone); attempts of a retained event stay.
	DeleteDeliveryAttemptsBefore(ctx context.Context, cutoff time.Time) (int64, error)
	// CountDeliveriesByState returns the number of delivery attempts in each
	// delivery state.
	CountDeliveriesByState(ctx context.Context) (map[string]int64, error)
	// RecordWorkerTick records that a worker polled the queue at at. An earlier
	// time than the one stored does not replace it.
	RecordWorkerTick(ctx context.Context, at time.Time) error
}

// Store is the full persistence surface plus lifecycle management.
type Store interface {
	EventStore
	DeliveryStore
	// Migrate applies pending schema migrations.
	Migrate(ctx context.Context) error
	// Monitoring reads the absolute signals GET /v1/stats reports. now decides
	// what is due; dead letters are counted from deadLetterSince.
	Monitoring(ctx context.Context, now, deadLetterSince time.Time) (Monitoring, error)
	// Ping verifies connectivity (used by readiness checks).
	Ping(ctx context.Context) error
	Close() error
}

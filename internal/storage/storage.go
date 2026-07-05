// Package storage defines AlertLoop's persistence interfaces and a SQL-backed
// implementation that works against both SQLite (local/demo) and PostgreSQL
// (production).
package storage

import (
	"context"
	"time"

	"github.com/golovanov-dev/alertloop/internal/domain"
)

// EventFilter narrows an event listing.
type EventFilter struct {
	Type     domain.EventType
	Severity domain.Severity
	State    domain.EventState
	Source   string
}

// DeliveryFilter narrows a delivery-attempt listing.
type DeliveryFilter struct {
	State       domain.DeliveryState
	Channel     domain.ChannelType
	ChannelName string
	EventID     string
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
	// to false and no new row is inserted.
	CreateEvent(ctx context.Context, e *domain.Event) (stored *domain.Event, created bool, err error)
	// CreateEventWithDeliveries inserts e together with its delivery attempts in
	// a single transaction. On a dedupe hit the existing event is returned with
	// created=false and no deliveries are inserted.
	CreateEventWithDeliveries(ctx context.Context, e *domain.Event, deliveries []*domain.DeliveryAttempt) (stored *domain.Event, created bool, err error)
	GetEvent(ctx context.Context, id string) (*domain.Event, error)
	ListEvents(ctx context.Context, f EventFilter, limit int, cursor string) (Page[domain.Event], error)
	// UpdateEventState sets the event's state and updated_at, returning the
	// updated event.
	UpdateEventState(ctx context.Context, id string, state domain.EventState, at time.Time) (*domain.Event, error)
	// DeleteEventsBefore removes events created strictly before cutoff and
	// returns the number deleted. Used for retention cleanup.
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
	// returns them. Concurrent workers never claim the same row.
	ClaimDue(ctx context.Context, now time.Time, limit int) ([]domain.DeliveryAttempt, error)
	// MarkResult records the outcome of a delivery attempt. On success state
	// becomes `sent`; otherwise it becomes `failed` (with next_retry_at set) or
	// `dead_letter` when attempts are exhausted.
	MarkResult(ctx context.Context, d *domain.DeliveryAttempt) error
	// Replay re-queues a dead_letter attempt as pending for another try,
	// returning the updated attempt.
	Replay(ctx context.Context, id string, at time.Time) (*domain.DeliveryAttempt, error)
	// RequeueStuckSending moves attempts that have been stuck in the `sending`
	// state since before staleBefore back to `pending`, so deliveries are not
	// lost when a worker dies mid-send (OOM, kill -9, redeploy). Returns the
	// number of attempts requeued.
	RequeueStuckSending(ctx context.Context, staleBefore time.Time) (int64, error)
	// DeleteDeliveryAttemptsBefore removes attempts created before cutoff.
	DeleteDeliveryAttemptsBefore(ctx context.Context, cutoff time.Time) (int64, error)
	// ActiveChannelNames returns the distinct channel names among attempts that
	// still need delivery (pending/failed/sending). Used at startup to warn about
	// undeliverable jobs whose channel is no longer configured.
	ActiveChannelNames(ctx context.Context) ([]string, error)
	// CountDeliveriesByState returns the number of delivery attempts in each
	// delivery state.
	CountDeliveriesByState(ctx context.Context) (map[string]int64, error)
}

// Store is the full persistence surface plus lifecycle management.
type Store interface {
	EventStore
	DeliveryStore
	// Migrate applies pending schema migrations.
	Migrate(ctx context.Context) error
	// Ping verifies connectivity (used by readiness checks).
	Ping(ctx context.Context) error
	Close() error
}

package service

import (
	"context"
	"log/slog"
	"time"

	"github.com/golovanov-dev/alertloop/internal/domain"
	"github.com/golovanov-dev/alertloop/internal/storage"
)

// EventService handles event queries and manual state transitions.
type EventService struct {
	store    storage.Store
	recovery *RecoveryNotifier
	now      Clock
	log      *slog.Logger
}

// NewEventService builds an EventService. recovery may be nil, which disables
// the recovery notice on the manual resolve action.
func NewEventService(store storage.Store, recovery *RecoveryNotifier, now Clock) *EventService {
	if now == nil {
		now = time.Now
	}
	return &EventService{store: store, recovery: recovery, now: now, log: slog.Default()}
}

// Get returns an event by ID.
func (s *EventService) Get(ctx context.Context, id string) (*domain.Event, error) {
	return s.store.GetEvent(ctx, id)
}

// List returns a cursor-paginated page of events.
func (s *EventService) List(ctx context.Context, f storage.EventFilter, limit int, cursor string) (storage.Page[domain.Event], error) {
	return s.store.ListEvents(ctx, f, limit, cursor)
}

// Apply performs a manual state transition on an event and returns the updated
// event. Invalid transitions return domain.ErrInvalidTransition.
func (s *EventService) Apply(ctx context.Context, id string, action domain.EventAction) (*domain.Event, error) {
	e, err := s.store.GetEvent(ctx, id)
	if err != nil {
		return nil, err
	}
	next, err := domain.ApplyAction(e.State, action)
	if err != nil {
		return nil, err
	}
	if next == e.State {
		// No-op transition (e.g. re-muting a muted event); return as-is.
		return e, nil
	}
	at := s.now().UTC()
	updated, err := s.store.UpdateEventState(ctx, id, next, at)
	if err != nil {
		return nil, err
	}

	// Closing an incident by hand notifies exactly as an ingested recovery
	// does. The operator who clicked Resolve knows; the Telegram group that was
	// told the database was down does not, and it is the same group either way.
	// The guard is `next == resolved && e.State != resolved`, so re-resolving an
	// already-closed incident stays silent.
	//
	// A MUTED incident is the exception. Mute means "stop telling me about
	// this"; sending a channel the end of a story it was deliberately not told
	// the beginning of is the opposite of what was asked for.
	if next == domain.StateResolved {
		if e.State == domain.StateMuted {
			s.log.Info("incident resolved while muted; no recovery notice sent",
				"event_id", id, "dedupe_key", updated.DedupeKey)
		} else {
			s.recovery.Notify(ctx, updated, at)
		}
	}
	return updated, nil
}

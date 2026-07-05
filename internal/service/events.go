package service

import (
	"context"
	"time"

	"github.com/golovanov-dev/alertloop/internal/domain"
	"github.com/golovanov-dev/alertloop/internal/storage"
)

// EventService handles event queries and manual state transitions.
type EventService struct {
	store storage.Store
	now   Clock
}

// NewEventService builds an EventService.
func NewEventService(store storage.Store, now Clock) *EventService {
	if now == nil {
		now = time.Now
	}
	return &EventService{store: store, now: now}
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
	return s.store.UpdateEventState(ctx, id, next, s.now().UTC())
}

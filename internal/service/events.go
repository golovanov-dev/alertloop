package service

import (
	"context"
	"time"

	"github.com/golovanov-dev/alertloop/internal/domain"
	"github.com/golovanov-dev/alertloop/internal/storage"
)

// EventService handles event queries and manual state transitions.
type EventService struct {
	store    storage.Store
	recovery *RecoveryNotifier
	now      Clock
}

// NewEventService builds an EventService. recovery may be nil, which disables
// the recovery notice on the manual resolve action.
func NewEventService(store storage.Store, recovery *RecoveryNotifier, now Clock) *EventService {
	if now == nil {
		now = time.Now
	}
	return &EventService{store: store, recovery: recovery, now: now}
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
// event. Actions apply to incidents only; a disallowed transition, including
// resolving an event that is already resolved, returns
// domain.ErrInvalidTransition. Repeating an action whose target is the current
// state (acknowledging an acknowledged event) returns the event unchanged.
//
// Closing an incident by hand notifies exactly as an ingested recovery does,
// through the same transition; see RecoveryNotifier.attemptsFor.
func (s *EventService) Apply(ctx context.Context, id string, action domain.EventAction) (*domain.Event, error) {
	e, err := s.store.GetEvent(ctx, id)
	if err != nil {
		return nil, err
	}
	if err := domain.CheckActionAllowed(e.Type); err != nil {
		return nil, err
	}
	updated, _, err := transition(ctx, s.store, s.recovery, e, s.now().UTC(),
		func(current domain.EventState) (domain.EventState, error) {
			return domain.ApplyAction(current, action)
		})
	return updated, err
}

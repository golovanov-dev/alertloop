package service

import (
	"context"
	"time"

	"github.com/golovanov-dev/alertloop/internal/domain"
	"github.com/golovanov-dev/alertloop/internal/storage"
)

// DeliveryService handles delivery-attempt queries and dead-letter replay.
type DeliveryService struct {
	store storage.Store
	now   Clock
}

// NewDeliveryService builds a DeliveryService.
func NewDeliveryService(store storage.Store, now Clock) *DeliveryService {
	if now == nil {
		now = time.Now
	}
	return &DeliveryService{store: store, now: now}
}

// Get returns a delivery attempt by ID.
func (s *DeliveryService) Get(ctx context.Context, id string) (*domain.DeliveryAttempt, error) {
	return s.store.GetDeliveryAttempt(ctx, id)
}

// List returns a cursor-paginated page of delivery attempts.
func (s *DeliveryService) List(ctx context.Context, f storage.DeliveryFilter, limit int, cursor string) (storage.Page[domain.DeliveryAttempt], error) {
	return s.store.ListDeliveryAttempts(ctx, f, limit, cursor)
}

// Replay re-queues a dead-letter attempt for another try. Attempts not in the
// dead_letter state return domain.ErrNotReplayable.
func (s *DeliveryService) Replay(ctx context.Context, id string) (*domain.DeliveryAttempt, error) {
	return s.store.Replay(ctx, id, s.now().UTC())
}

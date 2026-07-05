package storage

import (
	"context"
	"fmt"
	"time"

	"github.com/golovanov-dev/alertloop/internal/domain"
)

// ClaimDue atomically transitions up to limit deliverable attempts to
// `sending` and returns them. "Deliverable" means state pending or failed with
// a due (or absent) next_retry_at. On PostgreSQL the claim uses
// FOR UPDATE SKIP LOCKED so multiple workers never grab the same row; on SQLite
// the single-writer connection provides the same guarantee.
func (s *sqlStore) ClaimDue(ctx context.Context, now time.Time, limit int) ([]domain.DeliveryAttempt, error) {
	if limit <= 0 {
		limit = 10
	}
	nowStr := formatTime(now)

	lock := ""
	if s.d.supportsSkipLocked() {
		lock = " FOR UPDATE SKIP LOCKED"
	}

	q := fmt.Sprintf(`UPDATE delivery_attempts
		SET state = '%s', updated_at = ?
		WHERE id IN (
			SELECT id FROM delivery_attempts
			WHERE state IN ('%s', '%s')
			  AND (next_retry_at IS NULL OR next_retry_at <= ?)
			ORDER BY created_at ASC, id ASC
			LIMIT ?%s
		)
		RETURNING %s`,
		domain.DeliverySending, domain.DeliveryPending, domain.DeliveryFailed, lock, deliveryColumns,
	)

	rows, err := s.db.QueryContext(ctx, s.d.rebind(q), nowStr, nowStr, limit)
	if err != nil {
		return nil, fmt.Errorf("claim due deliveries: %w", err)
	}
	defer rows.Close()

	var claimed []domain.DeliveryAttempt
	for rows.Next() {
		d, err := scanDelivery(rows)
		if err != nil {
			return nil, err
		}
		claimed = append(claimed, *d)
	}
	return claimed, rows.Err()
}

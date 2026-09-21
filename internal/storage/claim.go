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
//
// A recovery is not deliverable while the alert of the same event to the same
// channel is anything but `sent`: otherwise an alert waiting for a retry
// arrives after its own "resolved". Each occurrence of an incident is its own
// event, so a recovery only ever waits for the alert of its own occurrence.
// A dead-lettered alert keeps its recovery waiting until the alert is replayed
// and sent, so no channel receives a recovery without the alert.
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
		SET state = '%[1]s', updated_at = ?
		WHERE id IN (
			SELECT c.id FROM delivery_attempts c
			WHERE c.state IN ('%[2]s', '%[3]s')
			  AND (c.next_retry_at IS NULL OR c.next_retry_at <= ?)
			  AND (c.kind <> '%[4]s' OR NOT EXISTS (
				SELECT 1 FROM delivery_attempts a
				WHERE a.event_id = c.event_id AND a.kind = '%[5]s'
				  AND a.channel_name = c.channel_name AND a.state <> '%[6]s'
			  ))
			ORDER BY c.created_at ASC, c.id ASC
			LIMIT ?%[7]s
		)
		RETURNING %[8]s`,
		domain.DeliverySending, domain.DeliveryPending, domain.DeliveryFailed,
		domain.KindRecovery, domain.KindAlert, domain.DeliverySent,
		lock, deliveryColumns,
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

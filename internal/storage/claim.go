package storage

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/golovanov-dev/alertloop/internal/domain"
)

// deliverableCond selects the attempts, aliased c, that ClaimDue may take: it
// is also what "due" means in Monitoring. Its one placeholder is now.
//
// A recovery is deliverable only once the alert it follows (recovery_for) is
// `sent`. A recovery with no alert on record (written before 0.8.0 for a
// channel whose alert was never stored) waits for nothing, as it did then.
var deliverableCond = fmt.Sprintf(`c.state IN ('%[1]s', '%[2]s')
			  AND (c.next_retry_at IS NULL OR c.next_retry_at <= ?)
			  AND (c.kind <> '%[3]s' OR c.recovery_for IS NULL OR EXISTS (
				SELECT 1 FROM delivery_attempts a
				WHERE a.id = c.recovery_for AND a.state = '%[4]s'
			  ))`,
	domain.DeliveryPending, domain.DeliveryFailed,
	domain.KindRecovery, domain.DeliverySent)

// ClaimDue atomically transitions up to limit deliverable attempts to
// `sending` and returns them. "Deliverable" means state pending or failed with
// a due (or absent) next_retry_at. On PostgreSQL the claim uses
// FOR UPDATE SKIP LOCKED so multiple workers never grab the same row; on SQLite
// the single-writer connection provides the same guarantee.
//
// A recovery is not deliverable until its own alert (recovery_for) is `sent`:
// otherwise an alert waiting for a retry arrives after its own "resolved".
// Only that alert counts, not other alerts to the same channel: a direct alert
// that dead-lettered or was cancelled does not hold the recovery of a fallback
// copy that got through. A dead-lettered alert keeps its own recovery waiting
// until the alert is replayed and sent, so no recovery goes out ahead of its
// alert.
//
// Attempts to the channels named in skipChannels are left in the queue: the
// worker passes the channels whose share of its slots is used up.
func (s *sqlStore) ClaimDue(ctx context.Context, now time.Time, limit int, skipChannels ...string) ([]domain.DeliveryAttempt, error) {
	if limit <= 0 {
		limit = 10
	}
	nowStr := formatTime(now)
	args := []any{nowStr, nowStr}

	skip := ""
	if len(skipChannels) > 0 {
		skip = " AND c.channel_name NOT IN (?" + strings.Repeat(", ?", len(skipChannels)-1) + ")"
		for _, name := range skipChannels {
			args = append(args, name)
		}
	}
	args = append(args, limit)

	lock := ""
	if s.d.supportsSkipLocked() {
		lock = " FOR UPDATE SKIP LOCKED"
	}

	q := fmt.Sprintf(`UPDATE delivery_attempts
		SET state = '%[1]s', updated_at = ?
		WHERE id IN (
			SELECT c.id FROM delivery_attempts c
			WHERE %[2]s%[5]s
			ORDER BY c.created_at ASC, c.id ASC
			LIMIT ?%[3]s
		)
		RETURNING %[4]s`,
		domain.DeliverySending, deliverableCond, lock, deliveryColumns, skip,
	)

	rows, err := s.db.QueryContext(ctx, s.d.rebind(q), args...)
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

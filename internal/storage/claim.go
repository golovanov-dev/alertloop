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
var deliverableCond = fmt.Sprintf(`c.state IN ('%[1]s', '%[2]s')
			  AND (c.next_retry_at IS NULL OR c.next_retry_at <= ?)
			  AND (c.kind <> '%[3]s' OR NOT EXISTS (
				SELECT 1 FROM delivery_attempts a
				WHERE a.event_id = c.event_id AND a.kind = '%[4]s'
				  AND a.channel_name = c.channel_name AND a.state <> '%[5]s'
			  ))`,
	domain.DeliveryPending, domain.DeliveryFailed,
	domain.KindRecovery, domain.KindAlert, domain.DeliverySent)

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

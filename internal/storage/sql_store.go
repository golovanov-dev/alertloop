package storage

import (
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/golovanov-dev/alertloop/internal/domain"
)

// sqlStore is a Store backed by database/sql. It supports SQLite and PostgreSQL
// via the dialect abstraction. Timestamps are stored as RFC3339Nano UTC text so
// that ordering and comparison behave identically across both engines.
type sqlStore struct {
	db *sql.DB
	d  dialect
}

// Open opens a Store for the given driver and DSN.
func Open(driver, dsn string) (Store, error) {
	db, d, err := open(driver, dsn)
	if err != nil {
		return nil, err
	}
	return &sqlStore{db: db, d: d}, nil
}

func (s *sqlStore) Migrate(ctx context.Context) error { return migrate(ctx, s.db, s.d) }
func (s *sqlStore) Ping(ctx context.Context) error    { return s.db.PingContext(ctx) }
func (s *sqlStore) Close() error                      { return s.db.Close() }

// timeLayout is a FIXED-WIDTH RFC3339 variant with a constant 9-digit
// nanosecond fraction. Unlike time.RFC3339Nano (which trims trailing zeros),
// this keeps timestamps lexicographically ordered as strings, which the store
// relies on for ORDER BY, keyset cursors, ClaimDue comparisons, and retention.
const timeLayout = "2006-01-02T15:04:05.000000000Z07:00"

// parseLayouts also accepts the legacy RFC3339Nano form for reading rows that
// may predate the fixed-width format.
var parseLayouts = []string{timeLayout, time.RFC3339Nano}

func nowString() string { return time.Now().UTC().Format(timeLayout) }

func formatTime(t time.Time) string { return t.UTC().Format(timeLayout) }

func parseTime(s string) time.Time {
	for _, layout := range parseLayouts {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC()
		}
	}
	// Fall back to a zero time rather than failing a whole read; stored values
	// are always written by formatTime so this should not occur.
	return time.Time{}
}

// --- Events ---------------------------------------------------------------

// execer is satisfied by both *sql.DB and *sql.Tx, so insert helpers work
// inside or outside a transaction.
type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

const insertEventSQL = `INSERT INTO events
	(id, type, severity, state, source, category, message, entity_type, entity_id, trace_id, dedupe_key, payload, created_at, updated_at)
	VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`

func (s *sqlStore) insertEvent(ctx context.Context, ex execer, e *domain.Event) error {
	payload := e.Payload
	if len(payload) == 0 {
		payload = []byte("{}")
	}
	_, err := ex.ExecContext(ctx, s.d.rebind(insertEventSQL),
		e.ID, e.Type, e.Severity, e.State, e.Source, e.Category, e.Message,
		e.EntityType, e.EntityID, e.TraceID, e.DedupeKey, string(payload),
		formatTime(e.CreatedAt), formatTime(e.UpdatedAt),
	)
	return err
}

const insertDeliverySQL = `INSERT INTO delivery_attempts
	(id, event_id, channel, channel_name, state, attempts, max_attempts, next_retry_at, last_error, created_at, updated_at)
	VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`

func (s *sqlStore) insertDelivery(ctx context.Context, ex execer, d *domain.DeliveryAttempt) error {
	_, err := ex.ExecContext(ctx, s.d.rebind(insertDeliverySQL),
		d.ID, d.EventID, d.Channel, d.ChannelName, d.State, d.Attempts, d.MaxAttempts,
		nullableTime(d.NextRetryAt), d.LastError, formatTime(d.CreatedAt), formatTime(d.UpdatedAt),
	)
	return err
}

func (s *sqlStore) CreateEvent(ctx context.Context, e *domain.Event) (*domain.Event, bool, error) {
	return s.CreateEventWithDeliveries(ctx, e, nil)
}

// CreateEventWithDeliveries stores an event and its delivery attempts in a
// single transaction, so an event is never persisted without its delivery jobs.
// Idempotency: if the dedupe_key already exists, the existing event is returned
// with created=false and no deliveries are inserted.
func (s *sqlStore) CreateEventWithDeliveries(ctx context.Context, e *domain.Event, deliveries []*domain.DeliveryAttempt) (*domain.Event, bool, error) {
	// Fast path: a known dedupe_key returns the existing event without a tx.
	if e.DedupeKey != "" {
		existing, err := s.eventByDedupe(ctx, e.DedupeKey)
		if err != nil && !errors.Is(err, domain.ErrNotFound) {
			return nil, false, err
		}
		if err == nil {
			return existing, false, nil
		}
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, false, err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after a successful commit

	if err := s.insertEvent(ctx, tx, e); err != nil {
		// A concurrent insert with the same dedupe_key can race the check above.
		if e.DedupeKey != "" && isUniqueViolation(err) {
			_ = tx.Rollback()
			if existing, gerr := s.eventByDedupe(ctx, e.DedupeKey); gerr == nil {
				return existing, false, nil
			}
		}
		return nil, false, fmt.Errorf("insert event: %w", err)
	}

	for _, d := range deliveries {
		if err := s.insertDelivery(ctx, tx, d); err != nil {
			return nil, false, fmt.Errorf("insert delivery attempt: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return nil, false, fmt.Errorf("commit event: %w", err)
	}
	return e, true, nil
}

const eventColumns = `id, type, severity, state, source, category, message, entity_type, entity_id, trace_id, dedupe_key, payload, created_at, updated_at`

func scanEvent(sc interface{ Scan(...any) error }) (*domain.Event, error) {
	var e domain.Event
	var payload []byte
	var created, updated string
	if err := sc.Scan(
		&e.ID, &e.Type, &e.Severity, &e.State, &e.Source, &e.Category, &e.Message,
		&e.EntityType, &e.EntityID, &e.TraceID, &e.DedupeKey, &payload, &created, &updated,
	); err != nil {
		return nil, err
	}
	e.Payload = payload
	e.CreatedAt = parseTime(created)
	e.UpdatedAt = parseTime(updated)
	return &e, nil
}

func (s *sqlStore) GetEvent(ctx context.Context, id string) (*domain.Event, error) {
	q := s.d.rebind(`SELECT ` + eventColumns + ` FROM events WHERE id = ?`)
	e, err := scanEvent(s.db.QueryRowContext(ctx, q, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, domain.ErrNotFound
	}
	return e, err
}

func (s *sqlStore) eventByDedupe(ctx context.Context, key string) (*domain.Event, error) {
	q := s.d.rebind(`SELECT ` + eventColumns + ` FROM events WHERE dedupe_key = ?`)
	e, err := scanEvent(s.db.QueryRowContext(ctx, q, key))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, domain.ErrNotFound
	}
	return e, err
}

func (s *sqlStore) ListEvents(ctx context.Context, f EventFilter, limit int, cursor string) (Page[domain.Event], error) {
	limit = clampLimit(limit)
	var where []string
	var args []any
	if f.Type != "" {
		where = append(where, "type = ?")
		args = append(args, f.Type)
	}
	if f.Severity != "" {
		where = append(where, "severity = ?")
		args = append(args, f.Severity)
	}
	if f.State != "" {
		where = append(where, "state = ?")
		args = append(args, f.State)
	}
	if f.Source != "" {
		where = append(where, "source = ?")
		args = append(args, f.Source)
	}
	if cursor != "" {
		ct, cid, err := decodeCursor(cursor)
		if err != nil {
			return Page[domain.Event]{}, err
		}
		// Keyset pagination on (created_at, id) descending.
		where = append(where, "(created_at < ? OR (created_at = ? AND id < ?))")
		args = append(args, ct, ct, cid)
	}

	q := `SELECT ` + eventColumns + ` FROM events`
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	q += " ORDER BY created_at DESC, id DESC LIMIT ?"
	args = append(args, limit+1)

	rows, err := s.db.QueryContext(ctx, s.d.rebind(q), args...)
	if err != nil {
		return Page[domain.Event]{}, fmt.Errorf("list events: %w", err)
	}
	defer rows.Close()

	var items []domain.Event
	for rows.Next() {
		e, err := scanEvent(rows)
		if err != nil {
			return Page[domain.Event]{}, err
		}
		items = append(items, *e)
	}
	if err := rows.Err(); err != nil {
		return Page[domain.Event]{}, err
	}

	page := Page[domain.Event]{Items: items}
	if len(items) > limit {
		last := items[limit-1]
		page.Items = items[:limit]
		page.NextCursor = encodeCursor(formatTime(last.CreatedAt), last.ID)
	}
	return page, nil
}

func (s *sqlStore) UpdateEventState(ctx context.Context, id string, state domain.EventState, at time.Time) (*domain.Event, error) {
	q := s.d.rebind(`UPDATE events SET state = ?, updated_at = ? WHERE id = ?`)
	res, err := s.db.ExecContext(ctx, q, state, formatTime(at), id)
	if err != nil {
		return nil, fmt.Errorf("update event state: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, domain.ErrNotFound
	}
	return s.GetEvent(ctx, id)
}

func (s *sqlStore) DeleteEventsBefore(ctx context.Context, cutoff time.Time) (int64, error) {
	return s.deleteBefore(ctx, "events", cutoff)
}

// deleteBefore removes rows created before cutoff in bounded batches so a large
// retention sweep never holds a single long lock (which on SQLite would stall
// the whole API, and on Postgres would bloat WAL/locks). table is an internal
// constant, never user input.
func (s *sqlStore) deleteBefore(ctx context.Context, table string, cutoff time.Time) (int64, error) {
	const batch = 1000
	cut := formatTime(cutoff)
	q := s.d.rebind(fmt.Sprintf(
		`DELETE FROM %s WHERE id IN (SELECT id FROM %s WHERE created_at < ? LIMIT ?)`, table, table))
	var total int64
	for {
		res, err := s.db.ExecContext(ctx, q, cut, batch)
		if err != nil {
			return total, fmt.Errorf("delete from %s: %w", table, err)
		}
		n, _ := res.RowsAffected()
		total += n
		if n < batch {
			return total, nil
		}
	}
}

// --- Delivery attempts ----------------------------------------------------

const deliveryColumns = `id, event_id, channel, channel_name, state, attempts, max_attempts, next_retry_at, last_error, created_at, updated_at`

func scanDelivery(sc interface{ Scan(...any) error }) (*domain.DeliveryAttempt, error) {
	var d domain.DeliveryAttempt
	var nextRetry sql.NullString
	var created, updated string
	if err := sc.Scan(
		&d.ID, &d.EventID, &d.Channel, &d.ChannelName, &d.State, &d.Attempts, &d.MaxAttempts,
		&nextRetry, &d.LastError, &created, &updated,
	); err != nil {
		return nil, err
	}
	if nextRetry.Valid && nextRetry.String != "" {
		t := parseTime(nextRetry.String)
		d.NextRetryAt = &t
	}
	d.CreatedAt = parseTime(created)
	d.UpdatedAt = parseTime(updated)
	return &d, nil
}

func (s *sqlStore) CreateDeliveryAttempt(ctx context.Context, d *domain.DeliveryAttempt) error {
	if err := s.insertDelivery(ctx, s.db, d); err != nil {
		return fmt.Errorf("insert delivery attempt: %w", err)
	}
	return nil
}

func (s *sqlStore) GetDeliveryAttempt(ctx context.Context, id string) (*domain.DeliveryAttempt, error) {
	q := s.d.rebind(`SELECT ` + deliveryColumns + ` FROM delivery_attempts WHERE id = ?`)
	d, err := scanDelivery(s.db.QueryRowContext(ctx, q, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, domain.ErrNotFound
	}
	return d, err
}

func (s *sqlStore) ListDeliveryAttempts(ctx context.Context, f DeliveryFilter, limit int, cursor string) (Page[domain.DeliveryAttempt], error) {
	limit = clampLimit(limit)
	var where []string
	var args []any
	if f.State != "" {
		where = append(where, "state = ?")
		args = append(args, f.State)
	}
	if f.Channel != "" {
		where = append(where, "channel = ?")
		args = append(args, f.Channel)
	}
	if f.ChannelName != "" {
		where = append(where, "channel_name = ?")
		args = append(args, f.ChannelName)
	}
	if f.EventID != "" {
		where = append(where, "event_id = ?")
		args = append(args, f.EventID)
	}
	if cursor != "" {
		ct, cid, err := decodeCursor(cursor)
		if err != nil {
			return Page[domain.DeliveryAttempt]{}, err
		}
		where = append(where, "(created_at < ? OR (created_at = ? AND id < ?))")
		args = append(args, ct, ct, cid)
	}

	q := `SELECT ` + deliveryColumns + ` FROM delivery_attempts`
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	q += " ORDER BY created_at DESC, id DESC LIMIT ?"
	args = append(args, limit+1)

	rows, err := s.db.QueryContext(ctx, s.d.rebind(q), args...)
	if err != nil {
		return Page[domain.DeliveryAttempt]{}, fmt.Errorf("list delivery attempts: %w", err)
	}
	defer rows.Close()

	var items []domain.DeliveryAttempt
	for rows.Next() {
		d, err := scanDelivery(rows)
		if err != nil {
			return Page[domain.DeliveryAttempt]{}, err
		}
		items = append(items, *d)
	}
	if err := rows.Err(); err != nil {
		return Page[domain.DeliveryAttempt]{}, err
	}

	page := Page[domain.DeliveryAttempt]{Items: items}
	if len(items) > limit {
		last := items[limit-1]
		page.Items = items[:limit]
		page.NextCursor = encodeCursor(formatTime(last.CreatedAt), last.ID)
	}
	return page, nil
}

func (s *sqlStore) MarkResult(ctx context.Context, d *domain.DeliveryAttempt) error {
	q := s.d.rebind(`UPDATE delivery_attempts
		SET state = ?, attempts = ?, next_retry_at = ?, last_error = ?, updated_at = ?
		WHERE id = ?`)
	res, err := s.db.ExecContext(ctx, q,
		d.State, d.Attempts, nullableTime(d.NextRetryAt), d.LastError, formatTime(time.Now()), d.ID,
	)
	if err != nil {
		return fmt.Errorf("mark delivery result: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return domain.ErrNotFound
	}
	return nil
}

func (s *sqlStore) Replay(ctx context.Context, id string, at time.Time) (*domain.DeliveryAttempt, error) {
	current, err := s.GetDeliveryAttempt(ctx, id)
	if err != nil {
		return nil, err
	}
	if current.State != domain.DeliveryDeadLetter {
		return nil, domain.ErrNotReplayable
	}
	q := s.d.rebind(`UPDATE delivery_attempts
		SET state = ?, next_retry_at = ?, last_error = '', updated_at = ?
		WHERE id = ? AND state = ?`)
	res, err := s.db.ExecContext(ctx, q,
		domain.DeliveryPending, formatTime(at), formatTime(at), id, domain.DeliveryDeadLetter,
	)
	if err != nil {
		return nil, fmt.Errorf("replay delivery attempt: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		// State changed concurrently.
		return nil, domain.ErrNotReplayable
	}
	return s.GetDeliveryAttempt(ctx, id)
}

func (s *sqlStore) RequeueStuckSending(ctx context.Context, staleBefore time.Time) (int64, error) {
	q := s.d.rebind(`UPDATE delivery_attempts
		SET state = ?, updated_at = ?
		WHERE state = ? AND updated_at < ?`)
	res, err := s.db.ExecContext(ctx, q,
		domain.DeliveryPending, formatTime(time.Now()), domain.DeliverySending, formatTime(staleBefore),
	)
	if err != nil {
		return 0, fmt.Errorf("requeue stuck sending: %w", err)
	}
	return res.RowsAffected()
}

func (s *sqlStore) DeleteDeliveryAttemptsBefore(ctx context.Context, cutoff time.Time) (int64, error) {
	return s.deleteBefore(ctx, "delivery_attempts", cutoff)
}

func (s *sqlStore) CountEventsByState(ctx context.Context) (map[string]int64, error) {
	return s.countByColumn(ctx, "events", "state")
}

func (s *sqlStore) CountDeliveriesByState(ctx context.Context) (map[string]int64, error) {
	return s.countByColumn(ctx, "delivery_attempts", "state")
}

// countByColumn returns a value->count map for a grouped count. table and
// column are internal constants, never user input.
func (s *sqlStore) countByColumn(ctx context.Context, table, column string) (map[string]int64, error) {
	q := fmt.Sprintf(`SELECT %s, COUNT(*) FROM %s GROUP BY %s`, column, table, column)
	rows, err := s.db.QueryContext(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("count %s by %s: %w", table, column, err)
	}
	defer rows.Close()
	out := map[string]int64{}
	for rows.Next() {
		var k string
		var n int64
		if err := rows.Scan(&k, &n); err != nil {
			return nil, err
		}
		out[k] = n
	}
	return out, rows.Err()
}

func (s *sqlStore) ActiveChannelNames(ctx context.Context) ([]string, error) {
	q := s.d.rebind(`SELECT DISTINCT channel_name FROM delivery_attempts
		WHERE state IN (?, ?, ?)`)
	rows, err := s.db.QueryContext(ctx, q,
		domain.DeliveryPending, domain.DeliveryFailed, domain.DeliverySending)
	if err != nil {
		return nil, fmt.Errorf("active channel names: %w", err)
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		names = append(names, n)
	}
	return names, rows.Err()
}

// --- Helpers --------------------------------------------------------------

func nullableTime(t *time.Time) any {
	if t == nil {
		return nil
	}
	return formatTime(*t)
}

func clampLimit(limit int) int {
	const def, max = 50, 200
	if limit <= 0 {
		return def
	}
	if limit > max {
		return max
	}
	return limit
}

func encodeCursor(createdAt, id string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(createdAt + "\x00" + id))
}

func decodeCursor(cursor string) (createdAt, id string, err error) {
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return "", "", fmt.Errorf("%w: invalid cursor", domain.ErrValidation)
	}
	parts := strings.SplitN(string(raw), "\x00", 2)
	if len(parts) != 2 {
		return "", "", fmt.Errorf("%w: invalid cursor", domain.ErrValidation)
	}
	return parts[0], parts[1], nil
}

func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	// SQLite: "UNIQUE constraint failed"; PostgreSQL: "duplicate key value" /
	// SQLSTATE 23505.
	return strings.Contains(msg, "unique constraint") ||
		strings.Contains(msg, "duplicate key") ||
		strings.Contains(msg, "23505")
}

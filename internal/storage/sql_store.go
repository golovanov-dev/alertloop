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
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

// sqlStore is a Store backed by database/sql. It supports SQLite and PostgreSQL
// via the dialect abstraction. Timestamps are stored as RFC3339Nano UTC text so
// that ordering and comparison behave identically across both engines.
type sqlStore struct {
	db *sql.DB
	d  dialect
	// percentPassword: the PostgreSQL password contains a %XX sequence, so a
	// refused login gets a hint (see explainAuthFailure).
	percentPassword bool
}

// Open opens a Store for the given driver and DSN.
func Open(driver, dsn string) (Store, error) {
	db, d, err := open(driver, dsn)
	if err != nil {
		return nil, err
	}
	return &sqlStore{db: db, d: d, percentPassword: d.name == "postgres" && passwordLooksPercentEncoded(dsn)}, nil
}

// Migrate runs at startup and is the first thing to connect, so a refused
// login surfaces here.
func (s *sqlStore) Migrate(ctx context.Context) error {
	return explainAuthFailure(migrate(ctx, s.db, s.d), s.percentPassword)
}

func (s *sqlStore) Ping(ctx context.Context) error { return s.db.PingContext(ctx) }
func (s *sqlStore) Close() error                   { return s.db.Close() }

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
	(id, type, severity, state, source, category, message, entity_type, entity_id, trace_id, dedupe_key, payload, created_at, updated_at, last_seen_at, resolved_at)
	VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`

func (s *sqlStore) insertEvent(ctx context.Context, ex execer, e *domain.Event) error {
	payload := e.Payload
	if len(payload) == 0 {
		payload = []byte("{}")
	}
	lastSeen := e.LastSeenAt
	if lastSeen.IsZero() {
		// An event stored without an explicit sighting was seen once, when it
		// was created. The column is never left empty: age and ordering
		// queries treat it as a timestamp.
		lastSeen = e.CreatedAt
	}
	_, err := ex.ExecContext(ctx, s.d.rebind(insertEventSQL),
		e.ID, e.Type, e.Severity, e.State, e.Source, e.Category, e.Message,
		e.EntityType, e.EntityID, e.TraceID, e.DedupeKey, string(payload),
		formatTime(e.CreatedAt), formatTime(e.UpdatedAt),
		formatTime(lastSeen), nullableTime(e.ResolvedAt),
	)
	return err
}

const insertDeliverySQL = `INSERT INTO delivery_attempts
	(id, event_id, channel, channel_name, kind, state, attempts, max_attempts, next_retry_at, last_error, created_at, updated_at, fallback_of, recovery_for)
	VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`

func (s *sqlStore) insertDelivery(ctx context.Context, ex execer, d *domain.DeliveryAttempt) error {
	_, err := ex.ExecContext(ctx, s.d.rebind(insertDeliverySQL), deliveryArgs(d)...)
	return err
}

func deliveryArgs(d *domain.DeliveryAttempt) []any {
	var fallbackOf, recoveryFor any
	if d.FallbackOf != nil {
		fallbackOf = d.FallbackOf.ID
	}
	if d.RecoveryFor != nil {
		recoveryFor = d.RecoveryFor.ID
	}
	return []any{
		d.ID, d.EventID, d.Channel, d.ChannelName, d.Kind.OrAlert(), d.State, d.Attempts, d.MaxAttempts,
		nullableTime(d.NextRetryAt), d.LastError, formatTime(d.CreatedAt), formatTime(d.UpdatedAt), fallbackOf, recoveryFor,
	}
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

const eventColumns = `id, type, severity, state, source, category, message, entity_type, entity_id, trace_id, dedupe_key, payload, created_at, updated_at, last_seen_at, resolved_at`

func scanEvent(sc interface{ Scan(...any) error }) (*domain.Event, error) {
	var e domain.Event
	var payload []byte
	var created, updated, lastSeen string
	var resolved sql.NullString
	if err := sc.Scan(
		&e.ID, &e.Type, &e.Severity, &e.State, &e.Source, &e.Category, &e.Message,
		&e.EntityType, &e.EntityID, &e.TraceID, &e.DedupeKey, &payload, &created, &updated,
		&lastSeen, &resolved,
	); err != nil {
		return nil, err
	}
	e.Payload = payload
	e.CreatedAt = parseTime(created)
	e.UpdatedAt = parseTime(updated)
	e.LastSeenAt = parseTime(lastSeen)
	if e.LastSeenAt.IsZero() {
		e.LastSeenAt = e.CreatedAt
	}
	if resolved.Valid && resolved.String != "" {
		t := parseTime(resolved.String)
		e.ResolvedAt = &t
	}
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

// eventByDedupe returns the OPEN incident carrying key. Resolved events are
// excluded on purpose: closing an incident frees its key, so a failure that
// recurs after being fixed becomes a new event instead of being swallowed as a
// duplicate of the closed one. The partial unique index guarantees at most one
// open row per key.
func (s *sqlStore) eventByDedupe(ctx context.Context, key string) (*domain.Event, error) {
	q := s.d.rebind(`SELECT ` + eventColumns + ` FROM events WHERE dedupe_key = ? AND state <> ?`)
	e, err := scanEvent(s.db.QueryRowContext(ctx, q, key, domain.StateResolved))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, domain.ErrNotFound
	}
	return e, err
}

// latestEventByDedupe returns the most recent event carrying key regardless of
// state. It answers a repeated `resolved` idempotently: the incident is already
// closed, and the caller should see the event that closed it rather than an
// error about there being nothing to resolve.
func (s *sqlStore) latestEventByDedupe(ctx context.Context, key string) (*domain.Event, error) {
	q := s.d.rebind(`SELECT ` + eventColumns + ` FROM events WHERE dedupe_key = ? ORDER BY created_at DESC, id DESC LIMIT 1`)
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
	if f.Search != "" {
		// Both sides go through the same function, so case folding is the
		// same on either side of LIKE.
		where = append(where, s.d.lower()+"(message) LIKE "+s.d.lower()+`(?) ESCAPE '\'`)
		args = append(args, "%"+likeEscaper.Replace(f.Search)+"%")
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

// TransitionEvent is the one write path for an event's state. The UPDATE is
// conditional on the state the caller read, so of two concurrent requests
// exactly one changes the event, and the recovery attempts it queues commit or
// roll back together with the close: a request cut off after the close has
// either closed the incident and queued its recovery, or done neither.
func (s *sqlStore) TransitionEvent(ctx context.Context, id string, c StateChange) (*domain.Event, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after a successful commit

	var resolvedAt *time.Time
	if c.To == domain.StateResolved {
		resolvedAt = &c.At
	}
	q := s.d.rebind(`UPDATE events SET state = ?, updated_at = ?, resolved_at = ?
		WHERE id = ? AND state = ?
		RETURNING ` + eventColumns)
	e, err := scanEvent(tx.QueryRowContext(ctx, q, c.To, formatTime(c.At), nullableTime(resolvedAt), id, c.From))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrStateChanged
	}
	if err != nil {
		return nil, fmt.Errorf("update event state: %w", err)
	}

	if c.CancelAlerts {
		q := s.d.rebind(`UPDATE delivery_attempts SET state = ?, next_retry_at = NULL, updated_at = ?
			WHERE event_id = ? AND kind = ? AND state IN (?, ?)`)
		if _, err := tx.ExecContext(ctx, q, domain.DeliveryCancelled, formatTime(c.At),
			id, domain.KindAlert, domain.DeliveryPending, domain.DeliveryFailed); err != nil {
			return nil, fmt.Errorf("cancel alert attempts: %w", err)
		}
	}

	if c.RecoveryMaxAttempts > 0 {
		alerts, err := alertsToRecover(ctx, tx, s.d, id)
		if err != nil {
			return nil, err
		}
		for _, a := range alerts {
			if err := s.insertDelivery(ctx, tx, recoveryOf(a, c.RecoveryMaxAttempts, c.At)); err != nil {
				return nil, fmt.Errorf("insert recovery attempt: %w", err)
			}
		}
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit event state: %w", err)
	}
	return e, nil
}

// RefreshOpenEvent applies a repeated `firing` to an already-open incident: it
// moves last_seen_at forward and adopts the newest severity, message, and
// payload. It deliberately does NOT touch state — an incident an operator has
// acknowledged stays acknowledged while the problem keeps firing. A repeat at
// the same or a lower severity creates no delivery attempts, so a check that
// fails every minute does not notify anyone every minute; a rise alerts the
// channels the new severity routes to that were not alerted yet.
//
// The stored severity and state are read under a row lock in the same
// transaction as the update, so of two concurrent rises only the first queues
// alerts, and a mute or close waits for the alerts it must cancel or follow.
func (s *sqlStore) RefreshOpenEvent(ctx context.Context, id string, u EventUpdate) (*domain.Event, error) {
	payload := u.Payload
	if len(payload) == 0 {
		payload = []byte("{}")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after a successful commit

	var (
		oldSeverity domain.Severity
		oldState    domain.EventState
	)
	q := s.d.rebind(`SELECT severity, state FROM events WHERE id = ? AND state <> ?` + s.d.rowLock())
	err = tx.QueryRowContext(ctx, q, id, domain.StateResolved).Scan(&oldSeverity, &oldState)
	if errors.Is(err, sql.ErrNoRows) {
		// The incident was resolved between the lookup and this update.
		return nil, domain.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("read event for refresh: %w", err)
	}

	q = s.d.rebind(`UPDATE events SET severity = ?, message = ?, payload = ?, last_seen_at = ?, updated_at = ?
		WHERE id = ?
		RETURNING ` + eventColumns)
	e, err := scanEvent(tx.QueryRowContext(ctx, q,
		u.Severity, u.Message, string(payload), formatTime(u.LastSeenAt), formatTime(u.LastSeenAt), id))
	if err != nil {
		return nil, fmt.Errorf("refresh event: %w", err)
	}

	if len(u.AlertsOnRise) > 0 && oldState != domain.StateMuted &&
		domain.SeverityRank(u.Severity) > domain.SeverityRank(oldSeverity) {
		if err := s.insertMissingAlerts(ctx, tx, id, u.AlertsOnRise); err != nil {
			return nil, err
		}
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit event refresh: %w", err)
	}
	return e, nil
}

// insertMissingAlerts queues the alerts whose channel has no alert attempt of
// eventID yet. A cancelled attempt counts: the channel was alerted, then
// muted.
func (s *sqlStore) insertMissingAlerts(ctx context.Context, tx *sql.Tx, eventID string, alerts []*domain.DeliveryAttempt) error {
	alerted, err := s.alertChannelNames(ctx, tx, eventID)
	if err != nil {
		return err
	}
	for _, d := range alerts {
		if alerted[d.ChannelName] {
			continue
		}
		if err := s.insertDelivery(ctx, tx, d); err != nil {
			return fmt.Errorf("insert alert attempt: %w", err)
		}
	}
	return nil
}

// alertChannelNames lists the channels with an alert attempt of eventID, in
// any state.
func (s *sqlStore) alertChannelNames(ctx context.Context, tx *sql.Tx, eventID string) (map[string]bool, error) {
	q := s.d.rebind(`SELECT DISTINCT channel_name FROM delivery_attempts WHERE event_id = ? AND kind = ?`)
	rows, err := tx.QueryContext(ctx, q, eventID, domain.KindAlert)
	if err != nil {
		return nil, fmt.Errorf("list alerted channels: %w", err)
	}
	defer rows.Close()

	out := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		out[name] = true
	}
	return out, rows.Err()
}

// EventByDedupe returns the open incident carrying key, or the latest closed
// one when none is open, or domain.ErrNotFound for a key never seen.
func (s *sqlStore) EventByDedupe(ctx context.Context, key string) (*domain.Event, error) {
	if key == "" {
		return nil, domain.ErrNotFound
	}
	e, err := s.eventByDedupe(ctx, key)
	if !errors.Is(err, domain.ErrNotFound) {
		return e, err
	}
	return s.latestEventByDedupe(ctx, key)
}

// DeleteEventsBefore removes events that stopped mattering before cutoff.
//
// The age of an event is NOT its creation time. A resolved incident ages from
// when it was resolved; an incident still open ages from when it was last
// reported. Before 0.4.0 the two were the same thing, because every report was
// a separate row - but a repeated `firing` now moves last_seen_at and leaves
// created_at where it was, so ageing by creation would delete precisely the
// incident that has been burning longest, while it is still burning.
//
// What that cost, concretely: the incident vanishes from the API while the
// problem continues; the eventual `status: resolved` then finds nothing to
// close and notifies nobody; and the next report opens a fresh incident and
// wakes everyone again.
//
// COALESCE gives one rule for both cases and keeps pre-0.4.0 behaviour intact:
// for an event reported once and never resolved, last_seen_at IS created_at.
func (s *sqlStore) DeleteEventsBefore(ctx context.Context, cutoff time.Time) (int64, error) {
	return s.deleteBatched(ctx, "events",
		"COALESCE(resolved_at, last_seen_at) < ?", formatTime(cutoff))
}

// deleteBatched removes rows matching where in bounded batches, so a large
// retention sweep never holds a single long lock (which on SQLite would stall
// the whole API, and on Postgres would bloat WAL/locks). table and where are
// internal constants, never user input.
//
// It yields between batches. On SQLite there is exactly ONE connection by
// design, so a sweep that loops without pausing holds it for the whole run and
// every API request and every delivery queues behind garbage collection. It
// also checks ctx: a shutdown during a long sweep should stop, not finish
// deleting a million rows first.
func (s *sqlStore) deleteBatched(ctx context.Context, table, where string, args ...any) (int64, error) {
	const (
		batch = 1000
		// Long enough for a waiting query to be served between batches, short
		// enough that a large sweep still finishes in one retention run.
		pause = 50 * time.Millisecond
	)
	q := s.d.rebind(fmt.Sprintf(
		`DELETE FROM %s WHERE id IN (SELECT id FROM %s WHERE %s LIMIT ?)`, table, table, where))
	params := make([]any, 0, len(args)+1)
	params = append(params, args...)
	params = append(params, batch)

	var total int64
	for {
		// A cancelled context means shutdown, not failure: report how much was
		// deleted and stop. Surfacing it as an error would log a scary line
		// every time the service restarts during a sweep.
		select {
		case <-ctx.Done():
			return total, nil
		default:
		}
		res, err := s.db.ExecContext(ctx, q, params...)
		if err != nil {
			return total, fmt.Errorf("delete from %s: %w", table, err)
		}
		n, _ := res.RowsAffected()
		total += n
		if n < batch {
			return total, nil
		}
		select {
		case <-ctx.Done():
			return total, nil
		case <-time.After(pause):
		}
	}
}

// --- Delivery attempts ----------------------------------------------------

const deliveryColumns = `id, event_id, channel, channel_name, kind, state, attempts, max_attempts, next_retry_at, last_error, created_at, updated_at, fallback_of, recovery_for`

func scanDelivery(sc interface{ Scan(...any) error }) (*domain.DeliveryAttempt, error) {
	var d domain.DeliveryAttempt
	var nextRetry, fallbackOf, recoveryFor sql.NullString
	var created, updated string
	if err := sc.Scan(
		&d.ID, &d.EventID, &d.Channel, &d.ChannelName, &d.Kind, &d.State, &d.Attempts, &d.MaxAttempts,
		&nextRetry, &d.LastError, &created, &updated, &fallbackOf, &recoveryFor,
	); err != nil {
		return nil, err
	}
	if fallbackOf.Valid {
		d.FallbackOf = &domain.AttemptLink{ID: fallbackOf.String}
	}
	if recoveryFor.Valid {
		d.RecoveryFor = &domain.AttemptLink{ID: recoveryFor.String}
	}
	d.Kind = d.Kind.OrAlert()
	if nextRetry.Valid && nextRetry.String != "" {
		t := parseTime(nextRetry.String)
		d.NextRetryAt = &t
	}
	d.CreatedAt = parseTime(created)
	d.UpdatedAt = parseTime(updated)
	return &d, nil
}

// alertsToRecover lists the alert attempts of eventID that closing it owes a
// recovery notice: every one but a cancelled alert, which was never sent. A
// dead-lettered alert counts, and so does a fallback copy: each gets its own
// recovery, which ClaimDue holds until that very alert is sent.
func alertsToRecover(ctx context.Context, tx *sql.Tx, d dialect, eventID string) ([]domain.DeliveryAttempt, error) {
	q := d.rebind(`SELECT id, channel, channel_name FROM delivery_attempts
		WHERE event_id = ? AND kind = ? AND state <> ?
		ORDER BY created_at, id`)
	rows, err := tx.QueryContext(ctx, q, eventID, domain.KindAlert, domain.DeliveryCancelled)
	if err != nil {
		return nil, fmt.Errorf("list alerts to recover: %w", err)
	}
	defer rows.Close()

	var out []domain.DeliveryAttempt
	for rows.Next() {
		a := domain.DeliveryAttempt{EventID: eventID}
		if err := rows.Scan(&a.ID, &a.Channel, &a.ChannelName); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// recoveryOf is the pending recovery notice that follows alert a.
func recoveryOf(a domain.DeliveryAttempt, maxAttempts int, at time.Time) *domain.DeliveryAttempt {
	return &domain.DeliveryAttempt{
		ID: uuid.NewString(), EventID: a.EventID, Channel: a.Channel, ChannelName: a.ChannelName,
		Kind: domain.KindRecovery, State: domain.DeliveryPending,
		MaxAttempts: maxAttempts, CreatedAt: at, UpdatedAt: at,
		RecoveryFor: &domain.AttemptLink{ID: a.ID},
	}
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
	if err != nil {
		return nil, err
	}
	items := []domain.DeliveryAttempt{*d}
	if err := s.linkAttempts(ctx, items); err != nil {
		return nil, err
	}
	return &items[0], nil
}

// linkAttempts fills the channel and state of FallbackOf and RecoveryFor, and
// FallbackTo, of items with one query for the whole page.
func (s *sqlStore) linkAttempts(ctx context.Context, items []domain.DeliveryAttempt) error {
	var ids, sources []any
	for _, d := range items {
		ids = append(ids, d.ID)
		if d.FallbackOf != nil {
			sources = append(sources, d.FallbackOf.ID)
		}
		if d.RecoveryFor != nil {
			sources = append(sources, d.RecoveryFor.ID)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	in := func(n int) string { return "(?" + strings.Repeat(", ?", n-1) + ")" }
	q := `SELECT id, channel_name, state, fallback_of FROM delivery_attempts WHERE fallback_of IN ` + in(len(ids))
	args := ids
	if len(sources) > 0 {
		q += ` OR id IN ` + in(len(sources))
		args = append(args, sources...)
	}
	rows, err := s.db.QueryContext(ctx, s.d.rebind(q), args...)
	if err != nil {
		return fmt.Errorf("link delivery attempts: %w", err)
	}
	defer rows.Close()
	linked := map[string]domain.AttemptLink{}
	to := map[string]*domain.AttemptLink{}
	for rows.Next() {
		var l domain.AttemptLink
		var of sql.NullString
		if err := rows.Scan(&l.ID, &l.ChannelName, &l.State, &of); err != nil {
			return err
		}
		linked[l.ID] = l
		if of.Valid {
			to[of.String] = &l
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for i := range items {
		for _, l := range []*domain.AttemptLink{items[i].FallbackOf, items[i].RecoveryFor} {
			if l != nil {
				l.ChannelName, l.State = linked[l.ID].ChannelName, linked[l.ID].State
			}
		}
		items[i].FallbackTo = to[items[i].ID]
	}
	return nil
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
	if f.Kind != "" {
		where = append(where, "kind = ?")
		args = append(args, f.Kind)
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
	if err := s.linkAttempts(ctx, page.Items); err != nil {
		return Page[domain.DeliveryAttempt]{}, err
	}
	return page, nil
}

// MarkResult records the outcome of an attempt the caller claimed.
//
// `AND state = 'sending'` is the important part. If saving the result failed
// once (a too-long error text used to do it) the reaper eventually returns the
// row to `pending`, and a late-arriving write would otherwise stamp a stale
// outcome over a job already queued for another try. The row is only ours while
// it is still `sending`; anything else means someone took it back, and
// ErrNotFound says so.
//
// An alert that did not go out (failed, dead-lettered, or handed back at
// shutdown) while its incident is muted becomes `cancelled` instead: mute
// cancels alerts not sent yet, and one that was mid-send at the moment of mute
// is exactly that. The check sits in the UPDATE itself, see mutedAlertCond, so
// a mute cannot slip in between reading the event and writing the result. A
// sent alert stays `sent`: it was delivered. d.State and d.NextRetryAt are set
// to what was actually stored.
//
// last_error is truncated by runes, not bytes: a cut through a multi-byte
// character produces text PostgreSQL refuses to store, which is what made the
// requeue loop above more than theoretical.
//
// fallback, when not nil, is queued in the same transaction if what is stored
// is `dead_letter` (see queueFallback).
func (s *sqlStore) MarkResult(ctx context.Context, d *domain.DeliveryAttempt, fallback *domain.DeliveryAttempt) error {
	at := d.UpdatedAt
	if at.IsZero() {
		at = time.Now()
	}
	stateExpr, retryExpr := "?", "?"
	args := []any{d.State}
	if d.State != domain.DeliverySent {
		cond := s.mutedAlertCond()
		stateExpr = "CASE WHEN " + cond + " THEN ? ELSE ? END"
		retryExpr = "CASE WHEN " + cond + " THEN NULL ELSE ? END"
		args = []any{domain.DeliveryCancelled, d.State}
	}
	q := s.d.rebind(`UPDATE delivery_attempts
		SET state = ` + stateExpr + `, next_retry_at = ` + retryExpr + `,
			attempts = ?, last_error = ?, updated_at = ?
		WHERE id = ? AND state = ?
		RETURNING state`)
	args = append(args, nullableTime(d.NextRetryAt), d.Attempts,
		domain.TruncateRunes(d.LastError, domain.MaxLastErrorRunes),
		formatTime(at), d.ID, domain.DeliverySending)
	// One statement on the hot path; a transaction only when a fallback may
	// have to be queued together with the result.
	var qr interface {
		QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	} = s.db
	var tx *sql.Tx
	if fallback != nil {
		var err error
		if tx, err = s.db.BeginTx(ctx, nil); err != nil {
			return fmt.Errorf("mark delivery result: %w", err)
		}
		defer tx.Rollback() //nolint:errcheck // no-op after a successful commit
		qr = tx
	}
	var state string
	err := qr.QueryRowContext(ctx, q, args...).Scan(&state)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("mark delivery result: %w", err)
	}
	if tx != nil {
		if domain.DeliveryState(state) == domain.DeliveryDeadLetter {
			if err := s.queueFallback(ctx, tx, d.ID, fallback); err != nil {
				return err
			}
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit delivery result: %w", err)
		}
	}
	d.State = domain.DeliveryState(state)
	if d.State == domain.DeliveryCancelled {
		d.NextRetryAt = nil
	}
	return nil
}

// queueFallback queues f, the fallback of the dead-lettered alert sourceID,
// unless one was queued for it already: an alert that is replayed and
// dead-letters again is not redirected twice (unique index on fallback_of).
// The fallback channel gets the copy even if it has this alert of its own: the
// copy is what says which channel is broken. f is queued if and only if
// f.FallbackOf is set on return.
//
// When the incident is already resolved, its recoveries were queued before f
// existed, and the fallback channel would hear "down" after "back up". So the
// copy gets its recovery here, if the incident has recoveries at all
// (notify_on_resolve); like every recovery it follows its own alert, the copy,
// and ClaimDue holds it until the copy is sent. The event row is share-locked:
// a resolve either waits and then queues the copy's recovery itself, or
// committed before and is seen here. Either way the copy gets exactly one.
func (s *sqlStore) queueFallback(ctx context.Context, tx *sql.Tx, sourceID string, f *domain.DeliveryAttempt) error {
	f.FallbackOf = &domain.AttemptLink{ID: sourceID}
	res, err := tx.ExecContext(ctx, s.d.rebind(insertDeliverySQL+` ON CONFLICT (fallback_of) DO NOTHING`), deliveryArgs(f)...)
	if err != nil {
		return fmt.Errorf("queue fallback attempt: %w", err)
	}
	if n, err := res.RowsAffected(); err != nil || n == 0 {
		f.FallbackOf = nil // not queued: the caller must not report it
		return err
	}

	var state string
	q := s.d.rebind(`SELECT state FROM events WHERE id = ?` + s.d.shareLock())
	if err := tx.QueryRowContext(ctx, q, f.EventID).Scan(&state); err != nil {
		return fmt.Errorf("read event of fallback attempt: %w", err)
	}
	if domain.EventState(state) != domain.StateResolved {
		return nil
	}
	var recoveries int
	q = s.d.rebind(`SELECT COUNT(*) FROM delivery_attempts WHERE event_id = ? AND kind = ?`)
	if err := tx.QueryRowContext(ctx, q, f.EventID, domain.KindRecovery).Scan(&recoveries); err != nil {
		return fmt.Errorf("count recoveries of fallback attempt: %w", err)
	}
	if recoveries == 0 {
		return nil
	}
	if err := s.insertDelivery(ctx, tx, recoveryOf(*f, f.MaxAttempts, f.CreatedAt)); err != nil {
		return fmt.Errorf("insert recovery of fallback attempt: %w", err)
	}
	return nil
}

// mutedAlertCond is true for a delivery_attempts row that is an alert of an
// event currently in `muted`. It is written into the UPDATE statements that
// hand a `sending` attempt back (MarkResult, RequeueStuckSending), so the
// decision to cancel and the write happen in one statement.
//
// On PostgreSQL the sub-SELECT share-locks the event row. Without the lock a
// mute committed after this statement took its snapshot, but before it
// commits, would be missed here, while the mute's own cancel would skip the
// row because it is still `sending`: the alert would stay queued. With the
// lock either the mute waits for this statement (and its cancel then sees the
// `failed` or `pending` row) or this statement waits for the mute (and then
// reads `muted`).
func (s *sqlStore) mutedAlertCond() string {
	return fmt.Sprintf(`(kind = '%s' AND (SELECT e.state FROM events e
			WHERE e.id = delivery_attempts.event_id%s) = '%s')`,
		domain.KindAlert, s.d.shareLock(), domain.StateMuted)
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
		SET state = ?, attempts = 0, next_retry_at = ?, last_error = '', updated_at = ?
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

// RequeueStuckSending returns abandoned `sending` attempts to `pending`, except
// alerts of a muted incident, which become `cancelled` (see MarkResult).
func (s *sqlStore) RequeueStuckSending(ctx context.Context, staleBefore time.Time) (int64, error) {
	cond := s.mutedAlertCond()
	q := s.d.rebind(`UPDATE delivery_attempts
		SET state = CASE WHEN ` + cond + ` THEN ? ELSE ? END,
			next_retry_at = CASE WHEN ` + cond + ` THEN NULL ELSE next_retry_at END,
			updated_at = ?
		WHERE state = ? AND updated_at < ?`)
	res, err := s.db.ExecContext(ctx, q,
		domain.DeliveryCancelled, domain.DeliveryPending, formatTime(time.Now()),
		domain.DeliverySending, formatTime(staleBefore),
	)
	if err != nil {
		return 0, fmt.Errorf("requeue stuck sending: %w", err)
	}
	return res.RowsAffected()
}

// DeleteDeliveryAttemptsBefore removes ORPHANED delivery attempts older than
// cutoff - ones whose event is already gone.
//
// Deleting an event cascades to its attempts, so this sweep exists only for
// rows the cascade missed: databases written while a DSN with its own `?`
// dropped foreign_keys (fixed by requiredPragmas, dialect.go), and manual
// deletes with foreign keys off. On a database created since then it finds
// nothing. Kept on purpose (decision R14, 2026-09-21 audit): it costs one
// indexed query per retention run. It deliberately does not touch attempts whose event
// is still retained: the delivery history of a live incident is exactly what an
// operator opens when asking why a notification never arrived.
func (s *sqlStore) DeleteDeliveryAttemptsBefore(ctx context.Context, cutoff time.Time) (int64, error) {
	return s.deleteBatched(ctx, "delivery_attempts",
		`created_at < ? AND NOT EXISTS (SELECT 1 FROM events e WHERE e.id = delivery_attempts.event_id)`,
		formatTime(cutoff))
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

// Monitoring reads every signal in one statement, so they describe one moment.
func (s *sqlStore) Monitoring(ctx context.Context, now, deadLetterSince time.Time) (Monitoring, error) {
	q := fmt.Sprintf(`SELECT
		(SELECT COUNT(*) FROM events WHERE type = '%[1]s' AND state <> '%[2]s'),
		(SELECT MIN(COALESCE(c.next_retry_at, c.created_at)) FROM delivery_attempts c WHERE %[3]s),
		(SELECT COUNT(*) FROM delivery_attempts WHERE state = '%[4]s' AND updated_at >= ?),
		(SELECT last_tick_at FROM worker_heartbeat WHERE id = 1)`,
		domain.EventIncident, domain.StateResolved, deliverableCond, domain.DeliveryDeadLetter)
	var (
		m              Monitoring
		dueSince, tick sql.NullString
	)
	err := s.db.QueryRowContext(ctx, s.d.rebind(q), formatTime(now), formatTime(deadLetterSince)).
		Scan(&m.OpenIncidents, &dueSince, &m.DeadLetters, &tick)
	if err != nil {
		return Monitoring{}, fmt.Errorf("read monitoring signals: %w", err)
	}
	if dueSince.Valid {
		t := parseTime(dueSince.String)
		m.OldestDueSince = &t
	}
	if tick.Valid {
		t := parseTime(tick.String)
		m.LastWorkerTick = &t
	}
	return m, nil
}

// RecordWorkerTick keeps the latest tick of any worker: with several workers,
// one that writes a moment late does not move it back.
func (s *sqlStore) RecordWorkerTick(ctx context.Context, at time.Time) error {
	q := s.d.rebind(`INSERT INTO worker_heartbeat (id, last_tick_at) VALUES (1, ?)
		ON CONFLICT (id) DO UPDATE SET last_tick_at = excluded.last_tick_at
		WHERE worker_heartbeat.last_tick_at < excluded.last_tick_at`)
	if _, err := s.db.ExecContext(ctx, q, formatTime(at)); err != nil {
		return fmt.Errorf("record worker tick: %w", err)
	}
	return nil
}

// --- Helpers --------------------------------------------------------------

// likeEscaper makes a search string match itself literally in a LIKE pattern
// with ESCAPE '\'.
var likeEscaper = strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)

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

// isUniqueViolation reports whether err is a unique-constraint violation, by
// the driver's error code: SQLSTATE 23505 on PostgreSQL, the extended result
// code SQLITE_CONSTRAINT_UNIQUE on SQLite.
func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code == pgUniqueViolation
	}
	var liteErr *sqlite.Error
	if errors.As(err, &liteErr) {
		return liteErr.Code() == sqlite3.SQLITE_CONSTRAINT_UNIQUE
	}
	return false
}

// pgUniqueViolation is the PostgreSQL SQLSTATE of unique_violation.
const pgUniqueViolation = "23505"

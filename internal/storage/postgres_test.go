package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/golovanov-dev/alertloop/internal/domain"
)

// PostgreSQL is the production database and, until 0.4.0, nothing was ever
// tested against it — every test ran on SQLite. The two differ in exactly the
// places that matter: placeholder syntax, JSONB versus TEXT payloads, partial
// indexes, and `FOR UPDATE SKIP LOCKED`, which is the whole basis of the
// delivery queue under concurrent workers and is a no-op on SQLite.
//
// These tests run when ALERTLOOP_TEST_POSTGRES_DSN points at a throwaway
// database, and are skipped otherwise so `go test ./...` still works offline.
// CI always sets it.
func postgresStore(t *testing.T) Store {
	t.Helper()
	dsn := os.Getenv("ALERTLOOP_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("ALERTLOOP_TEST_POSTGRES_DSN is not set; skipping PostgreSQL integration tests")
	}
	s, err := Open("postgres", dsn)
	if err != nil {
		t.Fatalf("open postgres: %v", err)
	}
	ctx := context.Background()
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	// Every test starts from an empty database. CASCADE also clears
	// delivery_attempts, which references events.
	if _, err := s.(*sqlStore).db.ExecContext(ctx, `TRUNCATE events CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestPostgresMigrationsAreIdempotent(t *testing.T) {
	s := postgresStore(t)
	// A second run must be a no-op: this is what happens on every restart of
	// every container, and an ALTER that is not guarded would fail the second
	// time — on an upgrade, in production, on someone else's machine.
	if err := s.Migrate(context.Background()); err != nil {
		t.Fatalf("second migrate: %v", err)
	}
}

func TestPostgresEventRoundTrip(t *testing.T) {
	s := postgresStore(t)
	ctx := context.Background()
	now := time.Date(2026, 8, 22, 9, 0, 0, 0, time.UTC)

	// A payload with the characters that break naive quoting, stored as JSONB
	// on PostgreSQL and TEXT on SQLite.
	payload := json.RawMessage(`{"quote":"it's","newline":"a\nb","unicode":"привет","nested":{"n":1.5}}`)
	e := sampleEvent("pg-1", "pg:key:1", now)
	e.Payload = payload
	e.LastSeenAt = now

	stored, created, err := s.CreateEvent(ctx, e)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if !created {
		t.Fatal("expected the first insert to create")
	}

	got, err := s.GetEvent(ctx, stored.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	var back map[string]any
	if err := json.Unmarshal(got.Payload, &back); err != nil {
		t.Fatalf("payload did not survive the round trip: %v (%s)", err, got.Payload)
	}
	if back["quote"] != "it's" || back["unicode"] != "привет" {
		t.Fatalf("payload changed in storage: %s", got.Payload)
	}
	if !got.CreatedAt.Equal(now) || !got.LastSeenAt.Equal(now) {
		t.Fatalf("timestamps changed: created=%s last_seen=%s", got.CreatedAt, got.LastSeenAt)
	}
	if got.ResolvedAt != nil {
		t.Fatal("a new event carries a resolved_at")
	}
}

// The partial unique index and the lifecycle SQL are written once and run on
// both engines. This is the PostgreSQL half of TestFailureRecursAfterResolve.
func TestPostgresIncidentLifecycle(t *testing.T) {
	s := postgresStore(t)
	ctx := context.Background()
	now := time.Date(2026, 8, 22, 9, 0, 0, 0, time.UTC)
	const key = "pg:postgres:availability"

	first := sampleEvent("pg-open-1", key, now)
	first.LastSeenAt = now
	if _, created, err := s.CreateEvent(ctx, first); err != nil || !created {
		t.Fatalf("create first: created=%v err=%v", created, err)
	}

	// A duplicate while the incident is open returns the open one.
	dup := sampleEvent("pg-open-2", key, now.Add(time.Minute))
	stored, created, err := s.CreateEvent(ctx, dup)
	if err != nil {
		t.Fatalf("duplicate: %v", err)
	}
	if created || stored.ID != "pg-open-1" {
		t.Fatalf("duplicate created=%v id=%s; want the open incident", created, stored.ID)
	}

	// Refresh it.
	refreshed, err := s.RefreshOpenEvent(ctx, "pg-open-1", EventUpdate{
		Severity:   domain.SeverityCritical,
		Message:    "still down",
		Payload:    json.RawMessage(`{"cycles":5}`),
		LastSeenAt: now.Add(5 * time.Minute),
	})
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if refreshed.Severity != domain.SeverityCritical || refreshed.Message != "still down" {
		t.Fatalf("refresh did not apply: %+v", refreshed)
	}
	if !refreshed.LastSeenAt.Equal(now.Add(5 * time.Minute)) {
		t.Fatalf("last_seen_at = %s, want it moved forward", refreshed.LastSeenAt)
	}

	// Close it.
	closed, didClose, err := s.ResolveByDedupe(ctx, key, now.Add(10*time.Minute))
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if !didClose || closed.State != domain.StateResolved || closed.ResolvedAt == nil {
		t.Fatalf("resolve did not close the incident: closed=%v %+v", didClose, closed)
	}

	// A repeat is idempotent.
	again, didClose, err := s.ResolveByDedupe(ctx, key, now.Add(11*time.Minute))
	if err != nil {
		t.Fatalf("second resolve: %v", err)
	}
	if didClose {
		t.Fatal("the second resolve closed something")
	}
	if !again.ResolvedAt.Equal(*closed.ResolvedAt) {
		t.Fatal("resolved_at moved on a repeat")
	}

	// And the key is free for the next outage.
	next := sampleEvent("pg-open-3", key, now.Add(20*time.Minute))
	next.LastSeenAt = now.Add(20 * time.Minute)
	stored, created, err = s.CreateEvent(ctx, next)
	if err != nil {
		t.Fatalf("recurrence: %v", err)
	}
	if !created || stored.ID != "pg-open-3" {
		t.Fatal("a resolved incident still blocks its own recurrence on PostgreSQL")
	}

	// An unknown key resolves to nothing, not to an error the caller must
	// special-case beyond ErrNotFound.
	if _, _, err := s.ResolveByDedupe(ctx, "pg:never:seen", now); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("resolve of an unknown key: err = %v, want ErrNotFound", err)
	}
}

// The delivery queue's reason for existing: two workers claiming concurrently
// must never get the same row. On PostgreSQL that is `FOR UPDATE SKIP LOCKED`,
// a code path SQLite never executes.
func TestPostgresClaimDueNeverDoubleClaims(t *testing.T) {
	s := postgresStore(t)
	ctx := context.Background()
	now := time.Date(2026, 8, 22, 9, 0, 0, 0, time.UTC)

	const jobs = 40
	e := sampleEvent("pg-queue-1", "pg:queue", now)
	e.LastSeenAt = now
	if _, _, err := s.CreateEvent(ctx, e); err != nil {
		t.Fatalf("create event: %v", err)
	}
	for i := 0; i < jobs; i++ {
		d := &domain.DeliveryAttempt{
			ID: fmt.Sprintf("pg-d-%02d", i), EventID: "pg-queue-1",
			Channel: domain.ChannelWebhook, ChannelName: "wh",
			State: domain.DeliveryPending, MaxAttempts: 3,
			CreatedAt: now, UpdatedAt: now,
		}
		if err := s.CreateDeliveryAttempt(ctx, d); err != nil {
			t.Fatalf("create attempt %d: %v", i, err)
		}
	}

	const workers = 4
	var (
		mu     sync.Mutex
		seen   = map[string]int{}
		total  int
		wg     sync.WaitGroup
		errsMu sync.Mutex
		errs   []error
	)
	wg.Add(workers)
	for w := 0; w < workers; w++ {
		go func() {
			defer wg.Done()
			for {
				claimed, err := s.ClaimDue(ctx, now.Add(time.Hour), 7)
				if err != nil {
					errsMu.Lock()
					errs = append(errs, err)
					errsMu.Unlock()
					return
				}
				if len(claimed) == 0 {
					return
				}
				mu.Lock()
				for _, c := range claimed {
					seen[c.ID]++
					total++
				}
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	if len(errs) > 0 {
		t.Fatalf("claim errors: %v", errs)
	}
	if total != jobs {
		t.Fatalf("claimed %d attempts in total, want %d", total, jobs)
	}
	for id, n := range seen {
		if n != 1 {
			t.Fatalf("attempt %s was claimed %d times; concurrent workers double-claimed", id, n)
		}
	}
}

func TestPostgresListPaginationAndCounts(t *testing.T) {
	s := postgresStore(t)
	ctx := context.Background()
	base := time.Date(2026, 8, 22, 9, 0, 0, 0, time.UTC)

	for i := 0; i < 12; i++ {
		e := sampleEvent(fmt.Sprintf("pg-list-%02d", i), fmt.Sprintf("pg:list:%02d", i),
			base.Add(time.Duration(i)*time.Minute))
		e.LastSeenAt = e.CreatedAt
		if i%3 == 0 {
			e.Severity = domain.SeverityCritical
		}
		if _, _, err := s.CreateEvent(ctx, e); err != nil {
			t.Fatalf("create %d: %v", i, err)
		}
	}

	// Walk the whole set through the keyset cursor and check nothing is
	// repeated or dropped — the cursor is base64 over timestamp+id and is
	// exactly where a dialect difference in text ordering would show up.
	seen := map[string]bool{}
	cursor := ""
	for pages := 0; ; pages++ {
		if pages > 10 {
			t.Fatal("pagination did not terminate")
		}
		page, err := s.ListEvents(ctx, EventFilter{}, 5, cursor)
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		for _, e := range page.Items {
			if seen[e.ID] {
				t.Fatalf("event %s returned on two pages", e.ID)
			}
			seen[e.ID] = true
		}
		if page.NextCursor == "" {
			break
		}
		cursor = page.NextCursor
	}
	if len(seen) != 12 {
		t.Fatalf("pagination returned %d of 12 events", len(seen))
	}

	filtered, err := s.ListEvents(ctx, EventFilter{Severity: domain.SeverityCritical}, 50, "")
	if err != nil {
		t.Fatalf("filtered list: %v", err)
	}
	if len(filtered.Items) != 4 {
		t.Fatalf("critical events = %d, want 4", len(filtered.Items))
	}

	counts, err := s.CountEventsByState(ctx)
	if err != nil {
		t.Fatalf("counts: %v", err)
	}
	if counts[string(domain.StateNew)] != 12 {
		t.Fatalf("new events = %d, want 12", counts[string(domain.StateNew)])
	}
}

func TestPostgresRetentionDeletesInBatches(t *testing.T) {
	s := postgresStore(t)
	ctx := context.Background()
	old := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	recent := time.Date(2026, 8, 22, 0, 0, 0, 0, time.UTC)

	for i := 0; i < 25; i++ {
		created := old
		if i >= 20 {
			created = recent
		}
		e := sampleEvent(fmt.Sprintf("pg-ret-%02d", i), fmt.Sprintf("pg:ret:%02d", i), created)
		e.LastSeenAt = created
		if _, _, err := s.CreateEvent(ctx, e); err != nil {
			t.Fatalf("create %d: %v", i, err)
		}
	}

	n, err := s.DeleteEventsBefore(ctx, recent)
	if err != nil {
		t.Fatalf("retention: %v", err)
	}
	if n != 20 {
		t.Fatalf("deleted %d events, want 20", n)
	}
	page, err := s.ListEvents(ctx, EventFilter{}, 50, "")
	if err != nil {
		t.Fatalf("list after retention: %v", err)
	}
	if len(page.Items) != 5 {
		t.Fatalf("%d events survived retention, want 5", len(page.Items))
	}
}

// The DSN form docker-compose.yml now builds, against a real server. The unit
// tests prove the driver parses the password intact; this proves PostgreSQL
// then accepts it — for a role whose password carries every character the URL
// form broke on, plus a literal "%2F" that a URL would have decoded.
func TestPostgresKeywordValueDSNAuthenticatesASpecialPassword(t *testing.T) {
	s := postgresStore(t)
	ctx := context.Background()
	admin, err := pgx.ParseConfig(os.Getenv("ALERTLOOP_TEST_POSTGRES_DSN"))
	if err != nil {
		t.Fatalf("parse the test DSN: %v", err)
	}

	const role = "alertloop_dsn_check"
	const password = "Ab+/=@:#?!$&'()*,;~%2F-._9"
	db := s.(*sqlStore).db
	if _, err := db.ExecContext(ctx, "DROP ROLE IF EXISTS "+role); err != nil {
		t.Fatalf("drop leftover role: %v", err)
	}
	// A role name and password cannot be bind parameters in CREATE ROLE; the
	// literal is built by doubling quotes, which is PostgreSQL's own escaping.
	literal := "'" + strings.ReplaceAll(password, "'", "''") + "'"
	if _, err := db.ExecContext(ctx, "CREATE ROLE "+role+" LOGIN PASSWORD "+literal); err != nil {
		t.Fatalf("create role (the test DSN needs CREATEROLE): %v", err)
	}
	t.Cleanup(func() { _, _ = db.ExecContext(context.Background(), "DROP ROLE IF EXISTS "+role) })

	dsn := fmt.Sprintf("host=%s port=%d user=%s dbname=%s sslmode=disable password=%s",
		admin.Host, admin.Port, role, admin.Database, password)
	if err := Check(ctx, "postgres", dsn); err != nil {
		t.Fatalf("keyword/value DSN with a special password did not authenticate: %v", err)
	}
}

// check-db, the worker's health check, against a real server.
func TestPostgresCheckReachesTheDatabase(t *testing.T) {
	postgresStore(t)
	if err := Check(context.Background(), "postgres", os.Getenv("ALERTLOOP_TEST_POSTGRES_DSN")); err != nil {
		t.Fatalf("Check: %v", err)
	}
}

package storage

import (
	"context"
	"database/sql"
	"os"
	"strings"
	"testing"
	"time"
)

// Migration 0008 links every recovery written before it to the alert of the
// same event and the same channel, the latest one when the channel has
// several, and changes nothing else.
func TestMigration0008LinksRecoveriesToTheirAlerts(t *testing.T) {
	t.Run("sqlite", func(t *testing.T) {
		db, d, err := open("sqlite", ":memory:")
		if err != nil {
			t.Fatalf("open: %v", err)
		}
		defer db.Close()
		checkMigration0008Backfill(t, db, d)
	})

	t.Run("postgres", func(t *testing.T) {
		dsn := os.Getenv("ALERTLOOP_TEST_POSTGRES_DSN")
		if dsn == "" {
			t.Skip("ALERTLOOP_TEST_POSTGRES_DSN is not set")
		}
		db, d, err := open("postgres", dsn)
		if err != nil {
			t.Fatalf("open: %v", err)
		}
		defer db.Close()
		// Start from nothing: this test owns the database while it runs.
		for _, table := range []string{"sessions", "users", "delivery_attempts", "events", "worker_heartbeat", "schema_migrations"} {
			if _, err := db.Exec(`DROP TABLE IF EXISTS ` + table + ` CASCADE`); err != nil {
				t.Fatalf("reset: %v", err)
			}
		}
		checkMigration0008Backfill(t, db, d)
	})
}

func checkMigration0008Backfill(t *testing.T, db *sql.DB, d dialect) {
	t.Helper()
	ctx := context.Background()
	if _, err := db.ExecContext(ctx,
		`CREATE TABLE IF NOT EXISTS schema_migrations (version TEXT PRIMARY KEY, applied_at TEXT NOT NULL)`); err != nil {
		t.Fatalf("ledger: %v", err)
	}
	names, err := migrationNames(d)
	if err != nil {
		t.Fatal(err)
	}
	apply := func(name string) {
		t.Helper()
		body, err := migrationFS.ReadFile("migrations/" + d.name + "/" + name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if err := applyMigration(ctx, db, d, strings.TrimSuffix(name, ".sql"), string(body)); err != nil {
			t.Fatalf("apply %s: %v", name, err)
		}
	}
	var rest []string
	for _, name := range names {
		if name < "0008" {
			apply(name)
		} else {
			rest = append(rest, name)
		}
	}

	// --- the 0.7.x world ---------------------------------------------------
	s := &sqlStore{db: db, d: d}
	now := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	for _, id := range []string{"e1", "e2"} {
		if _, _, err := s.CreateEvent(ctx, sampleEvent(id, "", now)); err != nil {
			t.Fatalf("create event %s: %v", id, err)
		}
	}
	rows := []struct {
		id, event, channel, kind, state string
		created                         time.Time
	}{
		{"a-tg", "e1", "tg", "alert", "sent", now},
		{"a-mail", "e1", "mail", "alert", "dead_letter", now},
		{"a-mail-copy", "e1", "mail", "alert", "sent", now.Add(time.Minute)},
		{"r-tg", "e1", "tg", "recovery", "sent", now.Add(2 * time.Minute)},
		{"r-mail", "e1", "mail", "recovery", "pending", now.Add(2 * time.Minute)},
		{"r-ops", "e1", "ops", "recovery", "pending", now.Add(2 * time.Minute)}, // no alert on record
		{"a2-tg", "e2", "tg", "alert", "sent", now},
		{"r2-tg", "e2", "tg", "recovery", "pending", now.Add(time.Minute)},
	}
	for _, r := range rows {
		if _, err := db.ExecContext(ctx, d.rebind(`INSERT INTO delivery_attempts
			(id, event_id, channel, channel_name, kind, state, attempts, max_attempts, created_at, updated_at)
			VALUES (?, ?, 'telegram', ?, ?, ?, 1, 3, ?, ?)`),
			r.id, r.event, r.channel, r.kind, r.state, formatTime(r.created), formatTime(r.created)); err != nil {
			t.Fatalf("insert %s: %v", r.id, err)
		}
	}

	for _, name := range rest {
		apply(name)
	}

	want := map[string]string{
		"a-tg": "", "a-mail": "", "a-mail-copy": "", "a2-tg": "",
		"r-tg": "a-tg", "r-mail": "a-mail-copy", "r-ops": "", "r2-tg": "a2-tg",
	}
	for id, alert := range want {
		got, err := s.GetDeliveryAttempt(ctx, id)
		if err != nil {
			t.Fatalf("get %s after 0008: %v", id, err)
		}
		link := ""
		if got.RecoveryFor != nil {
			link = got.RecoveryFor.ID
		}
		if link != alert {
			t.Errorf("%s: recovery_for = %q, want %q", id, link, alert)
		}
	}
	var n int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM delivery_attempts`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != len(rows) {
		t.Fatalf("%d attempts after 0008, want %d", n, len(rows))
	}
}

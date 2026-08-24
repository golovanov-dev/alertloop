package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/golovanov-dev/alertloop/internal/domain"
)

// A v0.1.0 installation has only migration 0001 applied and rows written in the
// shape that release produced: no channel_name on delivery attempts, no
// last_seen_at or resolved_at on events. Upgrading must add the columns, keep
// every row, and leave the data readable through the current store API.
//
// This runs the migration chain in-process. `scripts/upgrade-test.sh` does the
// same thing with the real v0.1.0 binary, which additionally proves that the
// 0001 migration checked in here is the schema that release actually wrote.
func TestUpgradeFromV010Schema(t *testing.T) {
	t.Run("sqlite", func(t *testing.T) {
		db, d, err := open("sqlite", ":memory:")
		if err != nil {
			t.Fatalf("open: %v", err)
		}
		defer db.Close()
		assertUpgradeKeepsData(t, db, d)
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
		for _, stmt := range []string{
			`DROP TABLE IF EXISTS delivery_attempts`,
			`DROP TABLE IF EXISTS events`,
			`DROP TABLE IF EXISTS schema_migrations`,
		} {
			if _, err := db.Exec(stmt); err != nil {
				t.Fatalf("reset: %v", err)
			}
		}
		assertUpgradeKeepsData(t, db, d)
	})
}

func assertUpgradeKeepsData(t *testing.T, db *sql.DB, d dialect) {
	t.Helper()
	ctx := context.Background()

	// --- the v0.1.0 world -------------------------------------------------
	if _, err := db.ExecContext(ctx,
		`CREATE TABLE IF NOT EXISTS schema_migrations (version TEXT PRIMARY KEY, applied_at TEXT NOT NULL)`); err != nil {
		t.Fatalf("ledger: %v", err)
	}
	body, err := migrationFS.ReadFile("migrations/" + d.name + "/0001_init.sql")
	if err != nil {
		t.Fatalf("read 0001: %v", err)
	}
	if err := applyMigration(ctx, db, d, "0001_init", string(body)); err != nil {
		t.Fatalf("apply 0001: %v", err)
	}

	created := time.Date(2026, 7, 5, 12, 0, 0, 0, time.UTC)
	events := []struct {
		id, state, dedupe string
		updated           time.Time
	}{
		{"v010-open", "new", "legacy:open", created},
		{"v010-ack", "acknowledged", "legacy:ack", created.Add(time.Hour)},
		{"v010-done", "resolved", "legacy:done", created.Add(2 * time.Hour)},
		{"v010-nokey", "new", "", created.Add(3 * time.Hour)},
	}
	insertEvent := d.rebind(`INSERT INTO events
		(id, type, severity, state, source, category, message, entity_type, entity_id,
		 trace_id, dedupe_key, payload, created_at, updated_at)
		VALUES (?, 'incident', 'error', ?, 'legacy-app', 'billing', ?, 'invoice', '42',
		        'trace-1', ?, ?, ?, ?)`)
	for _, e := range events {
		msg := "written by v0.1.0: " + e.id
		if _, err := db.ExecContext(ctx, insertEvent,
			e.id, e.state, msg, e.dedupe, `{"legacy":true,"amount":19.5}`,
			formatTime(created), formatTime(e.updated)); err != nil {
			t.Fatalf("insert %s: %v", e.id, err)
		}
	}

	// v0.1.0 delivery attempts have no channel_name at all.
	insertAttempt := d.rebind(`INSERT INTO delivery_attempts
		(id, event_id, channel, state, attempts, max_attempts, next_retry_at, last_error, created_at, updated_at)
		VALUES (?, ?, 'telegram', ?, 1, 5, NULL, '', ?, ?)`)
	for i, st := range []string{"sent", "dead_letter"} {
		if _, err := db.ExecContext(ctx, insertAttempt,
			fmt.Sprintf("v010-att-%d", i), "v010-open", st,
			formatTime(created), formatTime(created)); err != nil {
			t.Fatalf("insert attempt %d: %v", i, err)
		}
	}

	// --- upgrade ----------------------------------------------------------
	if err := migrate(ctx, db, d); err != nil {
		t.Fatalf("upgrade: %v", err)
	}
	// Twice, because a restart runs it again.
	if err := migrate(ctx, db, d); err != nil {
		t.Fatalf("second upgrade run: %v", err)
	}

	s := &sqlStore{db: db, d: d}

	// --- everything survived ---------------------------------------------
	page, err := s.ListEvents(ctx, EventFilter{}, 100, "")
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	if len(page.Items) != len(events) {
		t.Fatalf("%d events after upgrade, want %d", len(page.Items), len(events))
	}

	for _, want := range events {
		got, err := s.GetEvent(ctx, want.id)
		if err != nil {
			t.Fatalf("get %s: %v", want.id, err)
		}
		if !strings.Contains(got.Message, want.id) {
			t.Fatalf("%s: message = %q, want it preserved", want.id, got.Message)
		}
		if got.Source != "legacy-app" || got.Category != "billing" || got.EntityID != "42" {
			t.Fatalf("%s: columns changed: %+v", want.id, got)
		}
		var payload map[string]any
		if err := json.Unmarshal(got.Payload, &payload); err != nil {
			t.Fatalf("%s: payload unreadable after upgrade: %v", want.id, err)
		}
		if payload["legacy"] != true || payload["amount"] != 19.5 {
			t.Fatalf("%s: payload changed: %s", want.id, got.Payload)
		}
		// The new columns are backfilled, not left empty.
		if !got.LastSeenAt.Equal(created) {
			t.Fatalf("%s: last_seen_at = %s, want the creation time %s", want.id, got.LastSeenAt, created)
		}
		if want.state == "resolved" {
			if got.ResolvedAt == nil || !got.ResolvedAt.Equal(want.updated) {
				t.Fatalf("%s: resolved_at = %v, want %s", want.id, got.ResolvedAt, want.updated)
			}
		} else if got.ResolvedAt != nil {
			t.Fatalf("%s: an unresolved event was given a resolved_at", want.id)
		}
	}

	// Delivery attempts keep their rows, and 0002 backfills channel_name from
	// the channel type rather than leaving it blank.
	attempts, err := s.ListDeliveryAttempts(ctx, DeliveryFilter{}, 100, "")
	if err != nil {
		t.Fatalf("list attempts: %v", err)
	}
	if len(attempts.Items) != 2 {
		t.Fatalf("%d delivery attempts after upgrade, want 2", len(attempts.Items))
	}
	for _, a := range attempts.Items {
		if a.ChannelName != string(domain.ChannelTelegram) {
			t.Fatalf("attempt %s: channel_name = %q, want it backfilled to %q",
				a.ID, a.ChannelName, domain.ChannelTelegram)
		}
	}

	// The upgraded database supports the new lifecycle: the resolved legacy
	// event no longer holds its key, and the open one still does.
	next := &domain.Event{
		ID: "post-upgrade-1", Type: domain.EventIncident, Severity: domain.SeverityError,
		State: domain.StateNew, Source: "monit", Message: "recurrence",
		DedupeKey: "legacy:done", CreatedAt: created.Add(24 * time.Hour),
		UpdatedAt: created.Add(24 * time.Hour), LastSeenAt: created.Add(24 * time.Hour),
	}
	if _, createdNow, err := s.CreateEvent(ctx, next); err != nil || !createdNow {
		t.Fatalf("recurrence on a migrated resolved key: created=%v err=%v", createdNow, err)
	}
	blocked := &domain.Event{
		ID: "post-upgrade-2", Type: domain.EventIncident, Severity: domain.SeverityError,
		State: domain.StateNew, Source: "monit", Message: "duplicate",
		DedupeKey: "legacy:open", CreatedAt: created.Add(24 * time.Hour),
		UpdatedAt: created.Add(24 * time.Hour), LastSeenAt: created.Add(24 * time.Hour),
	}
	stored, createdNow, err := s.CreateEvent(ctx, blocked)
	if err != nil {
		t.Fatalf("duplicate on a migrated open key: %v", err)
	}
	if createdNow || stored.ID != "v010-open" {
		t.Fatalf("a migrated OPEN incident stopped deduplicating: created=%v id=%s", createdNow, stored.ID)
	}
}

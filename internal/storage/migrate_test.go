package storage

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/golovanov-dev/alertloop/internal/domain"
)

// The splitter used to cut on every `;` in the file, including ones inside
// comments — which turned an explanatory sentence into a statement and failed
// the migration at run time with a syntax error pointing at English prose.
// Migrations are the one thing that runs against a customer's data on upgrade,
// so this is a regression test, not a style test.
func TestSplitStatements(t *testing.T) {
	cases := []struct {
		name string
		body string
		want []string
	}{
		{
			name: "semicolon inside a line comment does not split",
			body: "-- reads through this index; the lookup is hot.\nCREATE INDEX a ON t (c);",
			want: []string{"-- reads through this index; the lookup is hot.\nCREATE INDEX a ON t (c)"},
		},
		{
			name: "semicolon inside a string literal does not split",
			body: "UPDATE t SET c = 'a;b' WHERE d = 1;",
			want: []string{"UPDATE t SET c = 'a;b' WHERE d = 1"},
		},
		{
			name: "escaped quote inside a literal",
			body: "UPDATE t SET c = 'it''s; fine';\nSELECT 1;",
			want: []string{"UPDATE t SET c = 'it''s; fine'", "SELECT 1"},
		},
		{
			name: "ordinary statements still split",
			body: "ALTER TABLE t ADD COLUMN a TEXT;\nALTER TABLE t ADD COLUMN b TEXT;\n",
			want: []string{"ALTER TABLE t ADD COLUMN a TEXT", "ALTER TABLE t ADD COLUMN b TEXT"},
		},
		{
			name: "trailing statement without a semicolon",
			body: "SELECT 1",
			want: []string{"SELECT 1"},
		},
		{
			name: "comment-only body yields no statements",
			body: "-- nothing here; really nothing\n",
			want: nil,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := splitStatements(c.body)
			if len(got) == 0 && len(c.want) == 0 {
				return
			}
			if !reflect.DeepEqual(got, c.want) {
				t.Fatalf("got %#v\nwant %#v", got, c.want)
			}
		})
	}
}

// Every shipped migration must survive the splitter. A `;` in a comment made
// this fail before the splitter was fixed, and nothing would have caught it
// until an upgrade ran.
func TestShippedMigrationsSplitIntoValidStatements(t *testing.T) {
	for _, dialect := range []string{"sqlite", "postgres"} {
		entries, err := migrationFS.ReadDir("migrations/" + dialect)
		if err != nil {
			t.Fatalf("read %s migrations: %v", dialect, err)
		}
		for _, e := range entries {
			body, err := migrationFS.ReadFile("migrations/" + dialect + "/" + e.Name())
			if err != nil {
				t.Fatalf("read %s: %v", e.Name(), err)
			}
			for i, stmt := range splitStatements(string(body)) {
				// Strip leading comment lines; what remains must start with SQL.
				var code []string
				for _, line := range strings.Split(stmt, "\n") {
					if s := strings.TrimSpace(line); s != "" && !strings.HasPrefix(s, "--") {
						code = append(code, s)
					}
				}
				if len(code) == 0 {
					t.Errorf("%s/%s statement %d is comment-only; the splitter cut inside a comment",
						dialect, e.Name(), i)
					continue
				}
				switch first := strings.ToUpper(strings.Fields(code[0])[0]); first {
				case "CREATE", "ALTER", "DROP", "UPDATE", "INSERT", "DELETE":
				default:
					t.Errorf("%s/%s statement %d starts with %q, which is not SQL:\n%s",
						dialect, e.Name(), i, first, stmt)
				}
			}
		}
	}
}

// The 0003 migration adds last_seen_at and resolved_at to a table that already
// holds rows. Those rows must come out with sensible values, not empty strings
// that later parse as a zero time.
func TestIncidentLifecycleMigrationBackfill(t *testing.T) {
	ctx := context.Background()
	db, d, err := open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	// Bring the schema up to 0002 only, then insert a pre-0.4.0 row by hand.
	if _, err := db.ExecContext(ctx,
		`CREATE TABLE IF NOT EXISTS schema_migrations (version TEXT PRIMARY KEY, applied_at TEXT NOT NULL)`); err != nil {
		t.Fatalf("ledger: %v", err)
	}
	for _, name := range []string{"0001_init.sql", "0002_channel_name.sql"} {
		body, err := migrationFS.ReadFile("migrations/sqlite/" + name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if err := applyMigration(ctx, db, d, strings.TrimSuffix(name, ".sql"), string(body)); err != nil {
			t.Fatalf("apply %s: %v", name, err)
		}
	}

	created := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	closed := time.Date(2026, 8, 1, 13, 0, 0, 0, time.UTC)
	insert := `INSERT INTO events (id, type, severity, state, source, category, message,
		entity_type, entity_id, trace_id, dedupe_key, payload, created_at, updated_at)
		VALUES (?, 'incident', 'error', ?, 'legacy', '', 'm', '', '', '', ?, '{}', ?, ?)`
	if _, err := db.ExecContext(ctx, insert, "open-1", "new", "key-open",
		formatTime(created), formatTime(created)); err != nil {
		t.Fatalf("insert open row: %v", err)
	}
	if _, err := db.ExecContext(ctx, insert, "closed-1", "resolved", "key-closed",
		formatTime(created), formatTime(closed)); err != nil {
		t.Fatalf("insert resolved row: %v", err)
	}

	// Now run the rest of the migrations, 0003 among them.
	if err := migrate(ctx, db, d); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	s := &sqlStore{db: db, d: d}
	open1, err := s.GetEvent(ctx, "open-1")
	if err != nil {
		t.Fatalf("get open row: %v", err)
	}
	if !open1.LastSeenAt.Equal(created) {
		t.Fatalf("last_seen_at = %s, want the creation time %s", open1.LastSeenAt, created)
	}
	if open1.ResolvedAt != nil {
		t.Fatal("an open row came out of the migration with a resolved_at")
	}

	closed1, err := s.GetEvent(ctx, "closed-1")
	if err != nil {
		t.Fatalf("get resolved row: %v", err)
	}
	if closed1.ResolvedAt == nil {
		t.Fatal("a resolved row came out of the migration without a resolved_at")
	}
	if !closed1.ResolvedAt.Equal(closed) {
		t.Fatalf("resolved_at = %s, want the last update %s", closed1.ResolvedAt, closed)
	}

	// The resolved row must no longer hold its key against a new incident.
	e := &domain.Event{
		ID: "new-1", Type: domain.EventIncident, Severity: domain.SeverityError,
		State: domain.StateNew, Source: "monit", Message: "again", DedupeKey: "key-closed",
		CreatedAt: closed, UpdatedAt: closed, LastSeenAt: closed,
	}
	stored, createdNow, err := s.CreateEvent(ctx, e)
	if err != nil {
		t.Fatalf("recurrence after migration: %v", err)
	}
	if !createdNow || stored.ID != "new-1" {
		t.Fatal("a migrated resolved row still blocks a new incident on the same key")
	}
}

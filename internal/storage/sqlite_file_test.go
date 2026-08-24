package storage

import (
	"context"
	"errors"
	"net/url"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/golovanov-dev/alertloop/internal/domain"
)

func TestSQLiteDSNKeepsOperatorSettingsAndAddsOurs(t *testing.T) {
	pragmas := func(dsn string) []string {
		_, query, _ := strings.Cut(dsn, "?")
		v, err := url.ParseQuery(query)
		if err != nil {
			t.Fatalf("result is not a parsable DSN: %q (%v)", dsn, err)
		}
		out := append([]string(nil), v["_pragma"]...)
		sort.Strings(out)
		return out
	}

	cases := []struct {
		name string
		in   string
		want []string
	}{
		{
			name: "a plain path gets both pragmas",
			in:   "/var/lib/alertloop/alertloop.db",
			want: []string{"busy_timeout(5000)", "foreign_keys(1)"},
		},
		{
			// The regression: this used to return the DSN untouched, silently
			// dropping foreign_keys and with it ON DELETE CASCADE.
			name: "a tuned busy_timeout keeps foreign_keys",
			in:   "file:/var/lib/alertloop/alertloop.db?_pragma=busy_timeout(10000)",
			want: []string{"busy_timeout(10000)", "foreign_keys(1)"},
		},
		{
			name: "an operator's foreign_keys setting is respected, not duplicated",
			in:   "file:app.db?_pragma=foreign_keys(0)",
			want: []string{"busy_timeout(5000)", "foreign_keys(0)"},
		},
		{
			name: "both set: nothing is added",
			in:   "file:app.db?_pragma=busy_timeout(1)&_pragma=foreign_keys(1)",
			want: []string{"busy_timeout(1)", "foreign_keys(1)"},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := pragmas(sqliteDSN(c.in))
			if len(got) != len(c.want) {
				t.Fatalf("pragmas = %v, want %v", got, c.want)
			}
			for i := range got {
				if got[i] != c.want[i] {
					t.Fatalf("pragmas = %v, want %v", got, c.want)
				}
			}
		})
	}

	// Unrelated parameters must survive.
	out := sqliteDSN("file:app.db?_txlock=immediate&mode=rwc")
	for _, want := range []string{"_txlock=immediate", "mode=rwc", "foreign_keys"} {
		if !strings.Contains(out, want) {
			t.Fatalf("%q lost %q", out, want)
		}
	}

	// :memory: is left alone: it is what tests use, and it has no file to tune.
	if got := sqliteDSN(":memory:"); got != ":memory:" {
		t.Fatalf("in-memory DSN was rewritten to %q", got)
	}
}

// Every other SQLite test runs on `:memory:`, where foreign keys are off, so
// ON DELETE CASCADE was not covered by a single test on the engine most
// installations actually use. This one runs against a real file.
func TestSQLiteFileEnforcesCascadeDelete(t *testing.T) {
	ctx := context.Background()
	path := filepath.ToSlash(filepath.Join(t.TempDir(), "alertloop.db"))

	s, err := Open("sqlite", path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	now := time.Now().UTC()
	old := now.AddDate(0, 0, -60)
	e := &domain.Event{
		ID: "e1", Type: domain.EventIncident, Severity: domain.SeverityError,
		State: domain.StateResolved, Source: "monit", Message: "m",
		Payload: []byte(`{}`), CreatedAt: old, UpdatedAt: old, LastSeenAt: old,
		ResolvedAt: &old,
	}
	if _, _, err := s.CreateEvent(ctx, e); err != nil {
		t.Fatalf("create event: %v", err)
	}
	att := &domain.DeliveryAttempt{
		ID: "d1", EventID: "e1", Channel: domain.ChannelWebhook, ChannelName: "wh",
		Kind: domain.KindRecovery, State: domain.DeliveryPending, MaxAttempts: 5,
		// Created NOW, for an event that is about to be swept: this is exactly
		// the row that used to survive both cleanup passes as an orphan and
		// then retry five times against an event that no longer exists.
		CreatedAt: now, UpdatedAt: now,
	}
	if err := s.CreateDeliveryAttempt(ctx, att); err != nil {
		t.Fatalf("create attempt: %v", err)
	}

	if _, err := s.DeleteEventsBefore(ctx, now.AddDate(0, 0, -30)); err != nil {
		t.Fatalf("retention: %v", err)
	}

	if _, err := s.GetEvent(ctx, "e1"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("the old resolved event survived retention: %v", err)
	}
	_, err = s.GetDeliveryAttempt(ctx, "d1")
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("delivery attempt outlived its event (foreign keys are off): %v", err)
	}
}

// The full store surface, once, against a file rather than :memory: - the
// configuration real installations run.
func TestSQLiteFileRoundTrip(t *testing.T) {
	ctx := context.Background()
	path := filepath.ToSlash(filepath.Join(t.TempDir(), "roundtrip.db"))

	s, err := Open("sqlite", path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	// Twice: a restart runs migrations again.
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("second migrate: %v", err)
	}

	now := time.Now().UTC()
	e := &domain.Event{
		ID: "f1", Type: domain.EventIncident, Severity: domain.SeverityCritical,
		State: domain.StateNew, Source: "monit", Message: "PostgreSQL не отвечает",
		DedupeKey: "server-01:postgresql:availability",
		Payload:   []byte(`{"labels":{"host":"server-01"},"note":"it's down"}`),
		CreatedAt: now, UpdatedAt: now, LastSeenAt: now,
	}
	if _, _, err := s.CreateEvent(ctx, e); err != nil {
		t.Fatalf("create: %v", err)
	}

	got, err := s.GetEvent(ctx, "f1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Message != "PostgreSQL не отвечает" {
		t.Fatalf("message did not survive: %q", got.Message)
	}

	// Full lifecycle on a file database.
	closed, didClose, err := s.ResolveByDedupe(ctx, e.DedupeKey, now.Add(time.Minute))
	if err != nil || !didClose {
		t.Fatalf("resolve: closed=%v err=%v", didClose, err)
	}
	if closed.ID != "f1" || closed.ResolvedAt == nil {
		t.Fatalf("resolve returned %+v", closed)
	}
	recur := *e
	recur.ID = "f2"
	recur.CreatedAt = now.Add(2 * time.Minute)
	recur.LastSeenAt = recur.CreatedAt
	if _, created, err := s.CreateEvent(ctx, &recur); err != nil || !created {
		t.Fatalf("recurrence after resolve: created=%v err=%v", created, err)
	}
}

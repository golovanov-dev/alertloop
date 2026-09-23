package storage

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const futureMigration = "9999_from_a_newer_version"

// recordFutureMigration makes the ledger look as if a newer AlertLoop had
// migrated the database.
func recordFutureMigration(t *testing.T, s Store) {
	t.Helper()
	st := s.(*sqlStore)
	if _, err := st.db.ExecContext(context.Background(), st.d.rebind(
		`INSERT INTO schema_migrations (version, applied_at) VALUES (?, ?)`), futureMigration, nowString()); err != nil {
		t.Fatal(err)
	}
}

// An older binary on a schema a newer one migrated would read and write
// tables it does not know. Startup and check-db both refuse, naming the
// migration.
func TestAnOlderBinaryRefusesANewerSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "alertloop.db")
	s, err := Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	if err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	recordFutureMigration(t, s)

	if err := s.Migrate(ctx); err == nil || !strings.Contains(err.Error(), futureMigration) {
		t.Errorf("startup on a newer schema: err = %v, want a refusal naming %s", err, futureMigration)
	}
	if err := Check(ctx, "sqlite", path); err == nil || !strings.Contains(err.Error(), futureMigration) {
		t.Errorf("check-db on a newer schema: err = %v, want a refusal naming %s", err, futureMigration)
	}
}

// check-db runs as the worker's health check before anything has migrated the
// database: no ledger yet is not a newer schema.
func TestCheckPassesADatabaseWithNoLedgerYet(t *testing.T) {
	path := filepath.Join(t.TempDir(), "empty.db")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Check(context.Background(), "sqlite", path); err != nil {
		t.Fatalf("check-db refused a database nothing has migrated yet: %v", err)
	}
}

func TestPostgresCheckRefusesANewerSchema(t *testing.T) {
	s := postgresStore(t)
	recordFutureMigration(t, s)
	t.Cleanup(func() {
		_, _ = s.(*sqlStore).db.ExecContext(context.Background(),
			`DELETE FROM schema_migrations WHERE version = $1`, futureMigration)
	})
	err := Check(context.Background(), "postgres", os.Getenv("ALERTLOOP_TEST_POSTGRES_DSN"))
	if err == nil || !strings.Contains(err.Error(), futureMigration) {
		t.Fatalf("err = %v, want a refusal naming %s", err, futureMigration)
	}
}

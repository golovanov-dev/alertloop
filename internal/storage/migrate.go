package storage

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"sort"
	"strings"
)

//go:embed migrations/sqlite/*.sql migrations/postgres/*.sql
var migrationFS embed.FS

// migrate applies any migration files for the dialect that have not yet been
// recorded in the schema_migrations table. Migration files are named
// NNNN_description.sql and applied in ascending order.
func migrate(ctx context.Context, db *sql.DB, d dialect) error {
	if _, err := db.ExecContext(ctx,
		`CREATE TABLE IF NOT EXISTS schema_migrations (version TEXT PRIMARY KEY, applied_at TEXT NOT NULL)`,
	); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}

	applied, err := appliedVersions(ctx, db)
	if err != nil {
		return err
	}

	dir := "migrations/" + d.name
	entries, err := fs.ReadDir(migrationFS, dir)
	if err != nil {
		return fmt.Errorf("read migrations dir %q: %w", dir, err)
	}

	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".sql") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)

	for _, name := range names {
		version := strings.TrimSuffix(name, ".sql")
		if applied[version] {
			continue
		}
		body, err := migrationFS.ReadFile(dir + "/" + name)
		if err != nil {
			return fmt.Errorf("read migration %q: %w", name, err)
		}
		if err := applyMigration(ctx, db, d, version, string(body)); err != nil {
			return fmt.Errorf("apply migration %q: %w", name, err)
		}
	}
	return nil
}

func appliedVersions(ctx context.Context, db *sql.DB) (map[string]bool, error) {
	rows, err := db.QueryContext(ctx, `SELECT version FROM schema_migrations`)
	if err != nil {
		return nil, fmt.Errorf("query schema_migrations: %w", err)
	}
	defer rows.Close()

	applied := map[string]bool{}
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		applied[v] = true
	}
	return applied, rows.Err()
}

func applyMigration(ctx context.Context, db *sql.DB, d dialect, version, body string) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // rolled back only if commit fails

	for _, stmt := range splitStatements(body) {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("statement failed: %w\n%s", err, stmt)
		}
	}

	if _, err := tx.ExecContext(ctx,
		d.rebind(`INSERT INTO schema_migrations (version, applied_at) VALUES (?, ?)`),
		version, nowString(),
	); err != nil {
		return err
	}
	return tx.Commit()
}

// splitStatements splits a migration file on `;` at end of line. It is a simple
// splitter that suits the migrations shipped with AlertLoop (no procedural
// blocks or embedded semicolons).
func splitStatements(body string) []string {
	parts := strings.Split(body, ";")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if s := strings.TrimSpace(p); s != "" {
			out = append(out, s)
		}
	}
	return out
}

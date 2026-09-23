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
	names, err := migrationNames(d)
	if err != nil {
		return err
	}
	// Before anything is applied: a binary older than the schema would run
	// against tables it does not know.
	if err := refuseNewerSchema(applied, names); err != nil {
		return err
	}

	for _, name := range names {
		version := strings.TrimSuffix(name, ".sql")
		if applied[version] {
			continue
		}
		body, err := migrationFS.ReadFile("migrations/" + d.name + "/" + name)
		if err != nil {
			return fmt.Errorf("read migration %q: %w", name, err)
		}
		if err := applyMigration(ctx, db, d, version, string(body)); err != nil {
			return fmt.Errorf("apply migration %q: %w", name, err)
		}
	}
	return nil
}

// migrationNames lists the dialect's migration files in the order they apply.
func migrationNames(d dialect) ([]string, error) {
	dir := "migrations/" + d.name
	entries, err := fs.ReadDir(migrationFS, dir)
	if err != nil {
		return nil, fmt.Errorf("read migrations dir %q: %w", dir, err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".sql") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	return names, nil
}

// refuseNewerSchema fails when the database records a migration this binary
// does not ship, which means a newer AlertLoop has upgraded it.
func refuseNewerSchema(applied map[string]bool, names []string) error {
	known := make(map[string]bool, len(names))
	for _, n := range names {
		known[strings.TrimSuffix(n, ".sql")] = true
	}
	var unknown []string
	for v := range applied {
		if !known[v] {
			unknown = append(unknown, v)
		}
	}
	if len(unknown) == 0 {
		return nil
	}
	sort.Strings(unknown)
	return fmt.Errorf("the database was migrated by a newer AlertLoop (migrations %s are unknown to this binary); "+
		"downgrading is not supported: run the newer version, or restore the backup taken before the upgrade",
		strings.Join(unknown, ", "))
}

// checkSchemaVersion is refuseNewerSchema for a database that is only being
// checked: a database with no migration ledger yet passes.
func checkSchemaVersion(ctx context.Context, db *sql.DB, d dialect) error {
	ledger := `SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = 'schema_migrations'`
	if d.name == "postgres" {
		ledger = `SELECT count(*) FROM pg_tables WHERE tablename = 'schema_migrations' AND schemaname = current_schema()`
	}
	var n int
	if err := db.QueryRowContext(ctx, ledger).Scan(&n); err != nil {
		return fmt.Errorf("look for schema_migrations: %w", err)
	}
	if n == 0 {
		return nil
	}
	applied, err := appliedVersions(ctx, db)
	if err != nil {
		return err
	}
	names, err := migrationNames(d)
	if err != nil {
		return err
	}
	return refuseNewerSchema(applied, names)
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

// splitStatements splits a migration file into statements on `;`.
//
// Semicolons inside `--` line comments and inside single-quoted string literals
// do not split. That is not hypothetical tidiness: a `;` in an explanatory
// comment used to cut a migration in half and hand SQLite the English prose as
// a statement, which failed at run time with a syntax error pointing at a
// comment. Migrations are the one thing that runs against a customer's data on
// upgrade, so the splitter must not depend on how a comment is punctuated.
//
// It remains a simple splitter: no procedural blocks, no dollar-quoted bodies.
// AlertLoop ships neither, and adding one should mean revisiting this function
// rather than discovering it in production.
func splitStatements(body string) []string {
	var (
		out      []string
		cur      strings.Builder
		inLine   bool // inside a `--` comment, until end of line
		inString bool // inside a '...' literal
	)
	runes := []rune(body)
	for i := 0; i < len(runes); i++ {
		c := runes[i]
		switch {
		case inLine:
			if c == '\n' {
				inLine = false
			}
		case inString:
			// '' inside a literal is an escaped quote, not the end of it.
			if c == '\'' {
				if i+1 < len(runes) && runes[i+1] == '\'' {
					cur.WriteRune(c)
					i++
					c = runes[i]
				} else {
					inString = false
				}
			}
		case c == '-' && i+1 < len(runes) && runes[i+1] == '-':
			inLine = true
		case c == '\'':
			inString = true
		case c == ';':
			out = appendStatement(out, cur.String())
			cur.Reset()
			continue
		}
		cur.WriteRune(c)
	}
	// A file that ends in a trailing comment leaves a chunk with no SQL in it.
	out = appendStatement(out, cur.String())
	return out
}

// appendStatement adds stmt to out unless it carries no SQL. A chunk of pure
// comment or whitespace is not a statement: handing one to the driver is at
// best a no-op and at worst an error, and it would happen only for whoever
// wrote the trailing comment.
func appendStatement(out []string, stmt string) []string {
	trimmed := strings.TrimSpace(stmt)
	if trimmed == "" || !hasSQL(trimmed) {
		return out
	}
	return append(out, trimmed)
}

// hasSQL reports whether s contains anything besides `--` comments and
// whitespace.
func hasSQL(s string) bool {
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line != "" && !strings.HasPrefix(line, "--") {
			return true
		}
	}
	return false
}

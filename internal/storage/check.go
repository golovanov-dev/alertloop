package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
)

// Check reports whether the configured database can be reached. It is what
// `alertloop check-db` runs, and that is the health check of the worker
// container, which has no HTTP listener to probe.
//
// It opens a connection and pings — nothing else. No migration runs, nothing
// in the database changes, and a SQLite file that does not exist is an error
// rather than something to create: a check that quietly created an empty
// database at a mistyped path would report a healthy install that is looking
// at nothing.
func Check(ctx context.Context, driver, dsn string) error {
	var (
		db  *sql.DB
		err error
	)
	switch driver {
	case "sqlite":
		if err := sqliteFileExists(dsn); err != nil {
			return err
		}
		// Not open(): that switches the database to WAL with a statement that
		// writes and takes no context. The running process has already done
		// it; a check has no business doing it again, or outside its timeout.
		db, err = sql.Open("sqlite", sqliteDSN(dsn))
	default:
		db, _, err = open(driver, dsn)
	}
	if err != nil {
		return err
	}
	// Nothing was written through this handle; an error releasing it says
	// nothing about the database the check is asking about.
	defer func() { _ = db.Close() }()
	if err := db.PingContext(ctx); err != nil {
		err = explainAuthFailure(err, driver == "postgres" && passwordLooksPercentEncoded(dsn))
		return fmt.Errorf("database is unreachable: %w", err)
	}
	return nil
}

// sqliteFileExists fails for a SQLite DSN whose file is not there, and for an
// in-memory database, which another process cannot see at all.
func sqliteFileExists(dsn string) error {
	path, query, _ := strings.Cut(dsn, "?")
	path = strings.TrimPrefix(path, "file:")
	if path == "" || strings.Contains(path, ":memory:") || strings.Contains(query, "mode=memory") {
		return errors.New("an in-memory SQLite database cannot be checked from another process")
	}
	if _, err := os.Stat(path); err != nil {
		return fmt.Errorf("SQLite database file: %w", err)
	}
	return nil
}

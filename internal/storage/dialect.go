package storage

import (
	"database/sql"
	"fmt"
	"strconv"
	"strings"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib" // postgres driver ("pgx")
	_ "modernc.org/sqlite"             // pure-Go sqlite driver ("sqlite")
)

// dialect captures the small differences between SQLite and PostgreSQL that the
// SQL store needs to account for.
type dialect struct {
	name string // "sqlite" or "postgres"
}

// rebind rewrites `?` placeholders into the dialect's positional form. SQLite
// keeps `?`; PostgreSQL uses `$1`, `$2`, ...
func (d dialect) rebind(query string) string {
	if d.name != "postgres" {
		return query
	}
	var b strings.Builder
	n := 0
	for i := 0; i < len(query); i++ {
		if query[i] == '?' {
			n++
			b.WriteByte('$')
			b.WriteString(strconv.Itoa(n))
			continue
		}
		b.WriteByte(query[i])
	}
	return b.String()
}

// supportsSkipLocked reports whether FOR UPDATE SKIP LOCKED can be used for the
// delivery queue. SQLite serializes writers and does not need (or support) it.
func (d dialect) supportsSkipLocked() bool {
	return d.name == "postgres"
}

// open resolves the driver and DSN for a driver name and opens a *sql.DB.
func open(driver, dsn string) (*sql.DB, dialect, error) {
	switch driver {
	case "sqlite":
		db, err := sql.Open("sqlite", sqliteDSN(dsn))
		if err != nil {
			return nil, dialect{}, err
		}
		// SQLite tolerates only a single writer; keep one connection to avoid
		// "database is locked" errors under the DB-backed queue.
		db.SetMaxOpenConns(1)
		if _, err := db.Exec("PRAGMA journal_mode=WAL;"); err != nil {
			// Non-fatal for :memory: databases.
			_ = err
		}
		return db, dialect{name: "sqlite"}, nil
	case "postgres":
		db, err := sql.Open("pgx", dsn)
		if err != nil {
			return nil, dialect{}, err
		}
		// Bound the pool so a request spike cannot exhaust Postgres
		// max_connections, and keep a warm idle set to avoid reconnect churn.
		db.SetMaxOpenConns(10)
		db.SetMaxIdleConns(5)
		db.SetConnMaxLifetime(30 * time.Minute)
		return db, dialect{name: "postgres"}, nil
	default:
		return nil, dialect{}, fmt.Errorf("unsupported database driver %q", driver)
	}
}

// sqliteDSN enables foreign keys and busy-timeout on file-based SQLite while
// leaving in-memory DSNs untouched.
func sqliteDSN(dsn string) string {
	if strings.Contains(dsn, "?") || strings.Contains(dsn, ":memory:") {
		return dsn
	}
	return dsn + "?_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)"
}

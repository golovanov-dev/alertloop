package storage

import (
	"database/sql"
	"database/sql/driver"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib" // postgres driver ("pgx")
	"modernc.org/sqlite"               // pure-Go sqlite driver ("sqlite")
)

// sqliteLower is the SQLite function lower() is on PostgreSQL. SQLite's own
// lower() folds ASCII only, so a search for "диск" would miss "Диск".
const sqliteLower = "alertloop_lower"

func init() {
	sqlite.MustRegisterDeterministicScalarFunction(sqliteLower, 1,
		func(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
			switch v := args[0].(type) {
			case string:
				return strings.ToLower(v), nil
			case []byte:
				return strings.ToLower(string(v)), nil
			default:
				return v, nil
			}
		})
}

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

// rowLock is the clause that locks a row read before it is updated, so the
// read value holds until commit. SQLite serialises transactions on its single
// connection and has no such clause.
func (d dialect) rowLock() string {
	if d.name == "postgres" {
		return " FOR UPDATE"
	}
	return ""
}

// shareLock is the row-lock clause for a sub-SELECT whose answer must hold
// until the statement commits. PostgreSQL needs it: under READ COMMITTED a
// plain sub-SELECT reads the snapshot taken when the statement started, so a
// concurrent change committed a moment later would be missed. SQLite runs one
// statement at a time on its single connection and has no such clause.
func (d dialect) shareLock() string {
	if d.name == "postgres" {
		return " FOR SHARE"
	}
	return ""
}

// lower is the function that lower-cases text, Unicode included.
func (d dialect) lower() string {
	if d.name == "postgres" {
		return "lower"
	}
	return sqliteLower
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
		//nolint:noctx // Runs once while opening the database, before any
		// request context exists to attach it to.
		if _, err := db.Exec("PRAGMA journal_mode=WAL;"); err != nil {
			// Non-fatal for :memory: databases.
			_ = err
		}
		return db, dialect{name: "sqlite"}, nil
	case "postgres":
		// sql.Open does not parse the DSN; the driver does, on the first
		// connection. Parse it here so a bad one fails at startup with an
		// explanation that does not quote the password.
		if err := checkPostgresDSN(dsn); err != nil {
			return nil, dialect{}, err
		}
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
// requiredPragmas are the SQLite settings AlertLoop depends on. They are MERGED
// into whatever the operator wrote, never replaced wholesale.
//
// The old implementation bailed out entirely when the DSN already contained a
// `?`, which meant that tuning one setting silently dropped the others. Somebody
// raising busy_timeout lost foreign_keys, and with it ON DELETE CASCADE: the
// delivery attempts of a deleted event stayed behind as orphans, each retrying
// five times against an event that no longer exists before dead-lettering.
// Nothing reported that, because nothing was broken enough to notice.
var requiredPragmas = map[string]string{
	"busy_timeout": "busy_timeout(5000)",
	"foreign_keys": "foreign_keys(1)",
}

// sqliteDSN returns dsn with AlertLoop's required pragmas present, preserving
// everything the operator set - including their own value for a pragma we also
// care about.
func sqliteDSN(dsn string) string {
	if dsn == "" || strings.Contains(dsn, ":memory:") {
		return dsn
	}

	base, query, hasQuery := strings.Cut(dsn, "?")
	values, err := url.ParseQuery(query)
	if err != nil {
		// Unparsable query string: leave it exactly as given rather than
		// mangling a DSN we do not understand. Opening will fail loudly.
		return dsn
	}

	existing := values["_pragma"]
	present := map[string]bool{}
	for _, p := range existing {
		// A pragma is written as `name(value)`; compare on the name so an
		// operator's own busy_timeout(10000) counts as set.
		name, _, _ := strings.Cut(p, "(")
		present[strings.TrimSpace(name)] = true
	}
	// Deterministic order so the DSN is stable across runs (tests, logs).
	for _, name := range []string{"busy_timeout", "foreign_keys"} {
		if !present[name] {
			values.Add("_pragma", requiredPragmas[name])
		}
	}

	encoded := values.Encode()
	if encoded == "" {
		if hasQuery {
			return base + "?"
		}
		return base
	}
	return base + "?" + encoded
}

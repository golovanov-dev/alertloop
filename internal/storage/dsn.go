package storage

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// A PostgreSQL DSN carries the password, so nothing in this file ever puts the
// DSN, or text derived from the part of it that holds the password, into an
// error. The driver's own errors are not safe for that: pgx masks the password
// in the connection string it quotes, but the reason it appends comes from
// net/url, and for a password containing "/" that reason is "invalid port
// \":<the start of the password>\" after host". An error meant to explain a
// failed start would carry a piece of the database password into the log.

// dsnNotRepeated is said by every DSN error, so an operator knows the omission
// is deliberate and not a bug in the message.
const dsnNotRepeated = "the DSN is not repeated here because it contains the password"

// errPostgresURL is returned for a URL-form DSN that does not parse, or parses
// into something other than what its author wrote. It is a fixed text on
// purpose: every detail the parser could add is a detail of the password.
var errPostgresURL = errors.New("database.dsn is not a valid PostgreSQL URL (" + dsnNotRepeated + ")." +
	" The usual cause is a password containing a character that has a meaning in a URL" +
	" (/ ? # @ % or a space): percent-encode each one (/ becomes %2F, @ becomes %40)," +
	" or write the DSN in keyword/value form, where the password needs no encoding:" +
	" host=HOST port=5432 user=USER dbname=DB sslmode=disable password=PASSWORD")

// checkPostgresDSN parses dsn the way the driver will, so that a DSN it cannot
// use stops AlertLoop at startup with a message saying what to do about it.
//
// Without this the driver would parse the DSN on the first connection, which
// surfaced as "run migrations: create schema_migrations: cannot parse ..." and
// quoted the password fragment described above.
func checkPostgresDSN(dsn string) error {
	isURL := strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://")

	var password string
	if isURL {
		u, err := url.Parse(dsn)
		if err != nil || strayAt(dsn) {
			return errPostgresURL
		}
		password, _ = u.User.Password()
	}

	if _, err := pgx.ParseConfig(dsn); err != nil {
		return explainDSNError(err, isURL, password)
	}
	return nil
}

// strayAt reports a URL DSN with an "@" where only a misplaced password puts
// one: anywhere in the path or the fragment, or in a query parameter's NAME.
//
// That is what a password containing "/", "?" or "#" does to a URL when the
// part before that character happens to be a valid port — "12/cd" or a password
// that starts with "/". The URL then PARSES: the user name becomes the host, and
// the rest of the password becomes the database name. The driver accepts it,
// connects nowhere, and prints the database name — that is, the password — in
// the connection error. Caught here, it fails like any other unparsable DSN.
//
// The path and the fragment are checked whole, not up to the first "=": a
// base64 password ends in "=", and "12/QxYz=@host:5432/db" puts that "=" before
// the "@". In the query only names are checked, pair by pair, so an "@" in a
// value — ?user=name@domain — is left alone.
//
// The one legitimate spelling this rejects is an unencoded "@" in a database
// name; the error's own advice (%40) fixes it.
func strayAt(dsn string) bool {
	_, rest, _ := strings.Cut(dsn, "://")
	end := strings.IndexAny(rest, "/?#")
	if end < 0 {
		return false
	}
	tail := rest[end:]
	tail, fragment, _ := strings.Cut(tail, "#")
	if strings.Contains(fragment, "@") {
		return true
	}
	path, query, _ := strings.Cut(tail, "?")
	if strings.Contains(path, "@") {
		return true
	}
	for _, pair := range strings.Split(query, "&") {
		name, _, _ := strings.Cut(pair, "=")
		if strings.Contains(name, "@") {
			return true
		}
	}
	return false
}

// percentEscape is a %XX sequence as a URL would have decoded it.
var percentEscape = regexp.MustCompile(`%[0-9A-Fa-f]{2}`)

// passwordLooksPercentEncoded reports a PostgreSQL password, as the driver will
// send it, that contains a %XX sequence. A URL DSN decodes those; the
// keyword/value DSN the Compose profile builds does not. A password that was
// percent-encoded in .env to get the URL working therefore reaches PostgreSQL
// verbatim and is refused.
func passwordLooksPercentEncoded(dsn string) bool {
	cfg, err := pgx.ParseConfig(dsn)
	return err == nil && percentEscape.MatchString(cfg.Password)
}

// percentPasswordHint is added to a refused login when the password contains
// %XX. It names the likely cause and where the fix is, and nothing about the
// password itself.
const percentPasswordHint = "the password in database.dsn contains percent-encoding (%XX)," +
	" which a keyword/value DSN sends as written: if it was encoded to make a URL work," +
	" write it decoded (see \"Upgrading to 0.5.1\" in OPERATIONS.md)"

// explainAuthFailure adds percentPasswordHint to err when PostgreSQL refused the
// password (SQLSTATE 28P01) and the password contains %XX.
func explainAuthFailure(err error, percentPassword bool) error {
	if err == nil || !percentPassword {
		return err
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "28P01" {
		return fmt.Errorf("%w; %s", err, percentPasswordHint)
	}
	return err
}

// explainDSNError turns a pgx parse error into one that is safe to log.
//
// Two classes, told apart by pgx's own wording:
//
//   - The DSN could not be split into settings at all ("failed to parse as URL"
//     / "... as keyword/value"). For a URL nothing of the reason is kept: it is
//     net/url's, and it quotes the text around the break. For keyword/value the
//     reason is kept — pgx's keyword/value parser reports only fixed strings.
//   - The settings split fine and one of them is wrong: a port that is not a
//     number, an unknown sslmode, an unreadable certificate. The password is in
//     its own setting by then, so the reason names other values; it is kept,
//     and dropped anyway if a URL's password appears in it.
//
// A reason is always taken from after the last "`: " of pgx's message, which
// is where the quoted — and only partly masked — connection string ends.
func explainDSNError(err error, isURL bool, password string) error {
	var pce *pgconn.ParseConfigError
	if !errors.As(err, &pce) {
		return errors.New("database.dsn was rejected by the PostgreSQL driver (" + dsnNotRepeated + ")")
	}
	full := pce.Error()
	reason := full
	if i := strings.LastIndex(full, "`: "); i >= 0 {
		reason = full[i+len("`: "):]
	}

	if strings.HasPrefix(reason, "failed to parse as ") {
		if isURL {
			return errPostgresURL
		}
		detail := ""
		if inner := errors.Unwrap(pce); inner != nil {
			detail = ": " + inner.Error()
		}
		return fmt.Errorf("database.dsn is not a valid keyword/value connection string%s (%s)."+
			" A value that contains a space or a backslash, or starts with a single quote, goes in single quotes,"+
			` with ' and \ inside it written as \' and \\.`+
			" The form is key=value pairs separated by spaces:"+
			" host=HOST port=5432 user=USER dbname=DB sslmode=disable password=PASSWORD", detail, dsnNotRepeated)
	}

	// Dropped, not masked, if the password shows up in it: masking a short
	// password ("s") would mangle every word containing it and, by showing
	// where the mask landed, give the password away.
	if password != "" && strings.Contains(reason, password) {
		return errors.New("database.dsn was rejected by the PostgreSQL driver (" + dsnNotRepeated + ")")
	}
	return fmt.Errorf("database.dsn was rejected by the PostgreSQL driver: %s (%s)", reason, dsnNotRepeated)
}

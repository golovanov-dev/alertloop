package storage

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"gopkg.in/yaml.v3"
)

// oldComposeDSN is the DSN docker-compose.yml built up to 0.5.0. It is kept
// here, and only here, so the tests can show what it did with the same
// passwords the current one is given.
const oldComposeDSN = "postgres://alertloop:${POSTGRES_PASSWORD}@postgres:5432/alertloop?sslmode=disable"

// composeDSNTemplate returns the DSN the shipped docker-compose.yml hands to
// the api and the worker, with ${POSTGRES_PASSWORD} still in it. Reading the
// real file is the point: a test of a copy would stay green while the file
// went back to a URL.
func composeDSNTemplate(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "docker-compose.yml"))
	if err != nil {
		t.Fatalf("read docker-compose.yml: %v", err)
	}
	var doc struct {
		PGEnv map[string]string `yaml:"x-pg-env"`
	}
	if err := yaml.Unmarshal(data, &doc); err != nil {
		t.Fatalf("parse docker-compose.yml: %v", err)
	}
	dsn := doc.PGEnv["ALERTLOOP_DB_DSN"]
	if dsn == "" {
		t.Fatal("docker-compose.yml: x-pg-env.ALERTLOOP_DB_DSN is missing")
	}
	// Compose substitutes variables into it; POSTGRES_PASSWORD must be the only
	// one, so that the test below substitutes everything Compose would.
	refs := regexp.MustCompile(`\$\{[^}]*\}`).FindAllString(dsn, -1)
	if len(refs) != 1 || refs[0] != "${POSTGRES_PASSWORD}" {
		t.Fatalf("the compose DSN references %v; want exactly ${POSTGRES_PASSWORD}", refs)
	}
	return dsn
}

// passwordOf reads the password of a parse result that may be nil.
func passwordOf(c *pgx.ConnConfig) string {
	if c == nil {
		return "<no config>"
	}
	return c.Password
}

// interpolate does what Compose does with the template: plain text
// substitution, no escaping of any kind.
func interpolate(template, password string) string {
	return strings.ReplaceAll(template, "${POSTGRES_PASSWORD}", password)
}

// The defect class: `openssl rand -base64 32` — the command the docs used to
// give — produces "+", "/" and "=". Pasted into a URL, "/" ends the authority,
// the parser reads "alertloop:<first part of the password>" as host:port, and
// api and worker restart in a loop. The same passwords, in the DSN the compose
// file builds now, must come out of the driver unchanged.
func TestComposeDSNCarriesPasswordsTheURLBroke(t *testing.T) {
	tmpl := composeDSNTemplate(t)

	cases := []struct {
		password  string
		urlBreaks bool // what the pre-0.5.1 URL did with it
	}{
		{"Zq7RmW2kLp/Xv9Tn4Hs8Bc1Dy6Fg3Jk5Nq0Uw+Ea2Ci=", true}, // base64-shaped: "/" breaks it
		{"plus+and=equals", false},
		{"slash/inside", true},
		{"hash#inside", true},
		{"question?inside", true},
		{"at@inside", false},
		{"colon:inside", false},
		{"12/starts-with-a-port", true}, // parses, but into the wrong host and database
		{"/leading-slash", true},        // likewise
		{"quote'inside", false},
		{"subdelims!$&()*,;~-._", false},
		{"every+/=@:#?at-once", true},
		{"a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0c1d2e3f4a5b6c7d8e9f0a1b2", false}, // openssl rand -hex 32
	}
	for _, tc := range cases {
		t.Run(tc.password, func(t *testing.T) {
			cfg, err := pgx.ParseConfig(interpolate(tmpl, tc.password))
			if err != nil {
				t.Fatalf("the compose DSN does not parse with this password: %v", err)
			}
			if cfg.Password != tc.password {
				t.Fatalf("password = %q, want %q", cfg.Password, tc.password)
			}
			if cfg.Host != "postgres" || cfg.Port != 5432 || cfg.User != "alertloop" || cfg.Database != "alertloop" {
				t.Fatalf("connection target = %s@%s:%d/%s, want alertloop@postgres:5432/alertloop",
					cfg.User, cfg.Host, cfg.Port, cfg.Database)
			}
			if cfg.TLSConfig != nil {
				t.Fatal("sslmode=disable was not applied")
			}

			old, err := pgx.ParseConfig(interpolate(oldComposeDSN, tc.password))
			oldWorks := err == nil && old.Password == tc.password && old.Host == "postgres" && old.Database == "alertloop"
			if oldWorks == tc.urlBreaks {
				t.Fatalf("the URL form was expected to break=%v on this password and did not (err=%v)", tc.urlBreaks, err)
			}
		})
	}
}

// userinfoChars are the characters Go's URL parser accepts, unencoded, in the
// user:password part of a URL — every password that could have worked in the
// pre-0.5.1 DSN is written in them. "%" is left out: in a URL it starts an
// escape, so it never meant itself (see the next test).
const userinfoChars = "abcXYZ0189-._~!$&'()*+,;=:@"

// The upgrade guarantee. A password that worked in the URL DSN must work,
// unchanged, in the keyword/value DSN that replaces it — or an upgrade would
// stop an installation that was running. The value is written unquoted, so
// characters are exercised at the start, in the middle and at the end.
//
// The one exception is a password that STARTS with a single quote: keyword/value
// reads that as the opening of a quoted value. It does not silently become a
// different password — the DSN fails to parse, and startup says why.
func TestKeywordValueKeepsEveryPasswordTheURLAccepted(t *testing.T) {
	tmpl := composeDSNTemplate(t)

	for _, c := range userinfoChars {
		for _, pw := range []string{"a" + string(c) + "b", string(c) + "ab", "ab" + string(c)} {
			old, err := pgx.ParseConfig(interpolate(oldComposeDSN, pw))
			if err != nil || old.Password != pw || old.Host != "postgres" {
				continue // never worked as a URL: nothing to keep working
			}
			cfg, err := pgx.ParseConfig(interpolate(tmpl, pw))
			if strings.HasPrefix(pw, "'") {
				if err == nil {
					t.Fatalf("%q: a leading quote parsed (password %q); it must fail loudly instead", pw, cfg.Password)
				}
				continue
			}
			if err != nil {
				t.Fatalf("%q worked in the URL DSN and does not parse in the keyword/value one: %v", pw, err)
			}
			if cfg.Password != pw {
				t.Fatalf("%q worked in the URL DSN and arrives as %q in the keyword/value one", pw, cfg.Password)
			}
		}
	}
}

// The upgrade case that does NOT carry over, pinned so the documentation of it
// stays true. A password percent-encoded in .env to get the URL working
// ("abc%2Fdef" for "abc/def") was decoded by the URL parser; keyword/value
// sends it as written. Such an installation has to put the decoded password in
// .env when it upgrades — which the upgrade guide and CHANGELOG say.
func TestKeywordValueSendsPercentEncodingLiterally(t *testing.T) {
	tmpl := composeDSNTemplate(t)
	old, err := pgx.ParseConfig(interpolate(oldComposeDSN, "abc%2Fdef"))
	if err != nil || old.Password != "abc/def" {
		t.Fatalf("URL form: password %q, err %v; expected it to decode to abc/def", passwordOf(old), err)
	}
	cfg, err := pgx.ParseConfig(interpolate(tmpl, "abc%2Fdef"))
	if err != nil || cfg.Password != "abc%2Fdef" {
		t.Fatalf("keyword/value form: password %q, err %v; expected it verbatim", passwordOf(cfg), err)
	}
}

// Why the password is not wrapped in single quotes in the compose file. Quoting
// would allow spaces and backslashes — which never worked in the URL either —
// and would break every password containing a quote, which did.
func TestQuotingThePasswordWouldBreakAQuote(t *testing.T) {
	quoted := "host=postgres port=5432 user=alertloop dbname=alertloop sslmode=disable password='${POSTGRES_PASSWORD}'"
	const pw = "it's"
	if old, err := pgx.ParseConfig(interpolate(oldComposeDSN, pw)); err != nil || old.Password != pw {
		t.Fatalf("premise: the URL form accepted %q (err %v)", pw, err)
	}
	if cfg, err := pgx.ParseConfig(interpolate(quoted, pw)); err == nil && cfg.Password == pw {
		t.Fatal("the quoted form accepted a quote after all; the choice of the unquoted form deserves a second look")
	}
	if cfg, err := pgx.ParseConfig(interpolate(composeDSNTemplate(t), pw)); err != nil || cfg.Password != pw {
		t.Fatalf("the shipped form lost %q: password %q, err %v", pw, passwordOf(cfg), err)
	}
}

// secretFragments lists every 4-character piece of a password. An error that
// contains any of them has leaked part of it. Masking the password where the
// driver quotes the DSN is not enough: the reason it appends can quote the
// text around the break, which for a URL password is the password itself.
func secretFragments(password string) []string {
	var out []string
	for i := 0; i+4 <= len(password); i++ {
		out = append(out, password[i:i+4])
	}
	return out
}

func assertNoFragment(t *testing.T, msg, password string) {
	t.Helper()
	for _, f := range secretFragments(password) {
		if strings.Contains(msg, f) {
			t.Fatalf("the error contains %q, a piece of the password %q:\n%s", f, password, msg)
		}
	}
}

// A DSN that cannot be used stops Open, before any connection is attempted,
// with an error that neither quotes the password nor any piece of it.
func TestOpenRejectsAnUnusableDSNWithoutLeakingThePassword(t *testing.T) {
	cases := []struct {
		name     string
		dsn      string
		password string
		want     []string // phrases the operator needs to see
	}{
		{
			name:     "slash in a base64-shaped URL password",
			dsn:      "postgres://alertloop:Wd4Tn8Ks2VqYj/Rq8Kz+Vt3=@postgres:5432/alertloop?sslmode=disable",
			password: "Wd4Tn8Ks2VqYj/Rq8Kz+Vt3=",
			want:     []string{"not a valid PostgreSQL URL", "percent-encode", "keyword/value", "%2F"},
		},
		{
			name:     "hash in a URL password",
			dsn:      "postgres://alertloop:Qx7wHy#Kp9mZt@postgres:5432/alertloop",
			password: "Qx7wHy#Kp9mZt",
			want:     []string{"not a valid PostgreSQL URL"},
		},
		{
			name:     "question mark in a URL password",
			dsn:      "postgres://alertloop:Qx7wYr?Kp9mZt@postgres:5432/alertloop",
			password: "Qx7wYr?Kp9mZt",
			want:     []string{"not a valid PostgreSQL URL"},
		},
		{
			name:     "a URL that parses into the wrong host and a database named after the password",
			dsn:      "postgres://alertloop:12/Qx7wTq3Kp9mZt@postgres:5432/alertloop?sslmode=disable",
			password: "12/Qx7wTq3Kp9mZt",
			want:     []string{"not a valid PostgreSQL URL"},
		},
		{
			name:     "a password that starts with a slash",
			dsn:      "postgres://alertloop:/Qx7wLd8Kp9mZt@postgres:5432/alertloop",
			password: "/Qx7wLd8Kp9mZt",
			want:     []string{"not a valid PostgreSQL URL"},
		},
		{
			// base64 padding puts "=" before the "@": an "up to the first ="
			// rule would let this through and the tail would become the database.
			name:     "a base64 password with a leading slash and padding",
			dsn:      "postgres://alertloop:/Hk4Rw9Zp2Tm8Vb=@postgres:5432/alertloop?sslmode=disable",
			password: "/Hk4Rw9Zp2Tm8Vb=",
			want:     []string{"not a valid PostgreSQL URL"},
		},
		{
			name:     "a base64 password that looks like a port, then a slash, then padding",
			dsn:      "postgres://alertloop:12/QxYz7Wd3Mn5Gh=@postgres:5432/alertloop?sslmode=disable",
			password: "12/QxYz7Wd3Mn5Gh=",
			want:     []string{"not a valid PostgreSQL URL"},
		},
		{
			name:     "a port-like password followed by ? puts the rest in a query parameter name",
			dsn:      "postgres://alertloop:12?Jb6Xq2Lf@postgres:5432/alertloop",
			password: "12?Jb6Xq2Lf",
			want:     []string{"not a valid PostgreSQL URL"},
		},
		{
			name:     "a port-like password followed by # puts the rest in the fragment",
			dsn:      "postgres://alertloop:12#Jb6Xq2Lf=@postgres:5432/alertloop",
			password: "12#Jb6Xq2Lf=",
			want:     []string{"not a valid PostgreSQL URL"},
		},
		{
			name:     "an invalid escape in a URL password",
			dsn:      "postgres://alertloop:Qx7w%zzKp9mZt@postgres:5432/alertloop",
			password: "Qx7w%zzKp9mZt",
			want:     []string{"not a valid PostgreSQL URL"},
		},
		{
			name:     "a space in a URL password",
			dsn:      "postgres://alertloop:Qx7w Zr4Kp9mZt@postgres:5432/alertloop",
			password: "Qx7w Zr4Kp9mZt",
			want:     []string{"not a valid PostgreSQL URL"},
		},
		{
			name:     "a keyword/value password that starts with a quote",
			dsn:      "host=postgres port=5432 user=alertloop dbname=alertloop sslmode=disable password='Qx7wJv2Kp9mZt",
			password: "'Qx7wJv2Kp9mZt",
			want:     []string{"not a valid keyword/value connection string", "single quotes"},
		},
		{
			name:     "a keyword/value password with a space",
			dsn:      "host=postgres user=alertloop dbname=alertloop password=Qx7w Zr4Kp9mZt",
			password: "Qx7w Zr4Kp9mZt",
			want:     []string{"not a valid keyword/value connection string"},
		},
		{
			name:     "a keyword/value password ending in a backslash",
			dsn:      `host=postgres user=alertloop dbname=alertloop password=Qx7wBn5Kp9mZt\`,
			password: `Qx7wBn5Kp9mZt\`,
			want:     []string{"not a valid keyword/value connection string"},
		},
		{
			name:     "a setting the driver rejects, in a URL",
			dsn:      "postgres://alertloop:Qx7wGd6Kp9mZt@postgres:5432/alertloop?sslmode=bogus",
			password: "Qx7wGd6Kp9mZt",
			want:     []string{"rejected by the PostgreSQL driver", "sslmode"},
		},
		{
			name:     "a setting the driver rejects, in keyword/value",
			dsn:      "host=postgres port=notaport user=alertloop dbname=alertloop password=Qx7wGd6Kp9mZt",
			password: "Qx7wGd6Kp9mZt",
			want:     []string{"rejected by the PostgreSQL driver", "port"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, err := Open("postgres", tc.dsn)
			if err == nil {
				s.Close()
				t.Fatal("Open accepted a DSN the driver cannot use")
			}
			msg := err.Error()
			assertNoFragment(t, msg, tc.password)
			for _, w := range tc.want {
				if !strings.Contains(msg, w) {
					t.Errorf("the error does not say %q:\n%s", w, msg)
				}
			}
			if !strings.Contains(msg, "not repeated here") {
				t.Errorf("the error does not say why the DSN is missing from it:\n%s", msg)
			}
		})
	}
}

// Parsing up front must not turn away anything the driver accepts: both forms,
// and a URL whose "@" sits in a query value where it belongs.
func TestOpenAcceptsWhatTheDriverAccepts(t *testing.T) {
	for _, dsn := range []string{
		"postgres://alertloop:alertloop@127.0.0.1:5432/alertloop?sslmode=disable",
		"postgresql://alertloop:pa%2Fss@db.internal/alertloop",
		"postgres://db.internal:5432/alertloop?user=svc@corp&sslmode=disable",
		"postgres://alertloop:alertloop@[::1]:5432/alertloop",
		"postgres://alertloop:alertloop@h1:5432,h2:5433/alertloop",
		"postgres://alertloop:pa%40ss@db.internal:5432/alertloop",     // encoded "@" in the password
		"postgres://alertloop:pa@ss@db.internal:5432/alertloop",       // unencoded: the last "@" ends the userinfo
		"postgres:///alertloop?host=/var/run/postgresql",              // unix socket, no authority
		"postgres://alertloop:pw@/alertloop?host=/var/run/postgresql", // unix socket with credentials
		"postgres://db.internal/alertloop?options=-c%20app=x&application_name=ops@corp",
		// The two forms alertloop.example.yaml shows, placeholders as written.
		"host=HOST port=5432 user=USER dbname=alertloop sslmode=disable password=PASS",
		"postgres://USER:PASS@HOST:5432/alertloop?sslmode=disable",
		"host=postgres port=5432 user=alertloop dbname=alertloop sslmode=disable password=Pz3Lm/Wq+B=",
		"host=/var/run/postgresql dbname=alertloop",
		"host=postgres user=alertloop dbname=alertloop password='with a space'",
	} {
		s, err := Open("postgres", dsn)
		if err != nil {
			t.Errorf("Open(%q) = %v", dsn, err)
			continue
		}
		s.Close()
	}
}

// check-db, the worker's health check, must not create what it is checking.
func TestCheckDoesNotCreateAMissingSQLiteFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.db")
	if err := Check(context.Background(), "sqlite", path); err == nil {
		t.Fatal("Check passed for a database file that does not exist")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("Check created %s (stat err %v); a health check must not create the database it checks", path, err)
	}
}

func TestCheckPassesForAnExistingSQLiteDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "alertloop.db")
	s, err := Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	if err := Check(context.Background(), "sqlite", path); err != nil {
		t.Fatalf("Check failed on a live database: %v", err)
	}
	if err := Check(context.Background(), "sqlite", "file:"+path+"?_pragma=busy_timeout(1000)"); err != nil {
		t.Fatalf("Check failed on the file: form of the same database: %v", err)
	}
}

func TestCheckRefusesAnInMemoryDatabase(t *testing.T) {
	if err := Check(context.Background(), "sqlite", ":memory:"); err == nil {
		t.Fatal("Check passed for :memory:, which no other process can see")
	}
}

// A driver reason that happens to contain the password is dropped, not masked.
// Masking would print "sslmode is xxxxx" — which tells the reader the password
// is "invalid" — and a one-letter password would turn every word into a mask.
func TestADriverReasonContainingThePasswordIsDropped(t *testing.T) {
	_, err := Open("postgres", "postgres://alertloop:invalid@postgres:5432/alertloop?sslmode=bogus")
	if err == nil {
		t.Fatal("Open accepted sslmode=bogus")
	}
	msg := err.Error()
	if strings.Contains(msg, "invalid") || strings.Contains(msg, "xxxxx") {
		t.Fatalf("the reason was kept or masked rather than dropped: %s", msg)
	}
	if !strings.Contains(msg, "rejected by the PostgreSQL driver") {
		t.Fatalf("unexpected message: %s", msg)
	}
}

func TestCheckReportsAnUnusablePostgresDSNWithoutThePassword(t *testing.T) {
	const pw = "Wd4Tn8Ks2VqYj/Rq8Kz+Vt3="
	err := Check(context.Background(), "postgres", "postgres://alertloop:"+pw+"@postgres:5432/alertloop")
	if err == nil {
		t.Fatal("Check passed for an unparsable DSN")
	}
	assertNoFragment(t, err.Error(), pw)
}

// A password percent-encoded in .env for the old URL DSN reaches PostgreSQL
// verbatim through the keyword/value one and is refused. The refusal says why —
// and says it without the password or any piece of it.
func TestRefusedLoginWithAPercentEncodedPasswordGetsAHint(t *testing.T) {
	const pw = "Rk7%2FVq3Ws9"
	dsn := "host=postgres port=5432 user=alertloop dbname=alertloop sslmode=disable password=" + pw
	if !passwordLooksPercentEncoded(dsn) {
		t.Fatal("a %2F in a keyword/value password was not noticed")
	}
	if passwordLooksPercentEncoded("host=postgres user=alertloop dbname=alertloop password=a1b2c3d4e5f6") {
		t.Fatal("a hex password was taken for percent-encoding")
	}
	if passwordLooksPercentEncoded("postgres://alertloop:ab%2Fcd@postgres:5432/alertloop") {
		t.Fatal("a URL decodes %2F itself; its password contains no escape by the time it is sent")
	}

	refused := fmt.Errorf("run migrations: %w", &pgconn.PgError{
		Severity: "FATAL", Code: "28P01", Message: `password authentication failed for user "alertloop"`,
	})
	msg := explainAuthFailure(refused, true).Error()
	if !strings.Contains(msg, "percent-encoding") || !strings.Contains(msg, "Upgrading to 0.5.1") {
		t.Fatalf("no hint on a refused login: %s", msg)
	}
	assertNoFragment(t, msg, pw)
	var pgErr *pgconn.PgError
	if !errors.As(explainAuthFailure(refused, true), &pgErr) {
		t.Fatal("the hint hid the PostgreSQL error from errors.As")
	}

	if got := explainAuthFailure(refused, false).Error(); strings.Contains(got, "percent-encoding") {
		t.Fatalf("hint added for a password without %%XX: %s", got)
	}
	other := &pgconn.PgError{Code: "3D000", Message: `database "alertloop" does not exist`}
	if got := explainAuthFailure(other, true).Error(); strings.Contains(got, "percent-encoding") {
		t.Fatalf("hint added to an error that is not a refused login: %s", got)
	}
}

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

	for _, password := range []string{
		"Zq7RmW2kLp/Xv9Tn4Hs8Bc1Dy6Fg3Jk5Nq0Uw+Ea2Ci=", // base64-shaped: the defect itself
		"every+/=@:#?at-once",
		"a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0c1d2e3f4a5b6c7d8e9f0a1b2", // openssl rand -hex 32
	} {
		t.Run(password, func(t *testing.T) {
			cfg, err := pgx.ParseConfig(interpolate(tmpl, password))
			if err != nil {
				t.Fatalf("the compose DSN does not parse with this password: %v", err)
			}
			if cfg.Password != password {
				t.Fatalf("password = %q, want %q", cfg.Password, password)
			}
			if cfg.Host != "postgres" || cfg.Port != 5432 || cfg.User != "alertloop" || cfg.Database != "alertloop" {
				t.Fatalf("connection target = %s@%s:%d/%s, want alertloop@postgres:5432/alertloop",
					cfg.User, cfg.Host, cfg.Port, cfg.Database)
			}
			if cfg.TLSConfig != nil {
				t.Fatal("sslmode=disable was not applied")
			}
		})
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
			// base64 padding puts "=" before the "@": an "up to the first ="
			// rule would let this through and the tail would become the database.
			name:     "a base64 password with a leading slash and padding",
			dsn:      "postgres://alertloop:/Hk4Rw9Zp2Tm8Vb=@postgres:5432/alertloop?sslmode=disable",
			password: "/Hk4Rw9Zp2Tm8Vb=",
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
			name:     "a keyword/value password that starts with a quote",
			dsn:      "host=postgres port=5432 user=alertloop dbname=alertloop sslmode=disable password='Qx7wJv2Kp9mZt",
			password: "'Qx7wJv2Kp9mZt",
			want:     []string{"not a valid keyword/value connection string", "single quotes"},
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
	if !strings.Contains(msg, "percent-encoding") || !strings.Contains(msg, "0.5.1 upgrade note in CHANGELOG.md") {
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

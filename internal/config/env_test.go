package config

import (
	"strings"
	"testing"
)

func TestSecretSubstitution(t *testing.T) {
	t.Setenv("ALERTLOOP_ADMIN_TOKEN", "s3cr3t-token")
	t.Setenv("TG_TOKEN", "123456:ABC-DEF")

	cfg := loadYAML(t, `
admin_token: ${ALERTLOOP_ADMIN_TOKEN}
database:
  driver: sqlite
  dsn: ${ALERTLOOP_DB_DSN:-alertloop.db}
channels:
  telegram:
    - name: alerts
      bot_token: ${TG_TOKEN}
      chat_id: "-100"
`)
	if cfg.AdminToken != "s3cr3t-token" {
		t.Fatalf("admin_token = %q", cfg.AdminToken)
	}
	// Substitution reaches nested lists, not just top-level scalars.
	if cfg.Channels.Telegram[0].BotToken != "123456:ABC-DEF" {
		t.Fatalf("bot_token = %q", cfg.Channels.Telegram[0].BotToken)
	}
	// The default applies when the variable is unset.
	if cfg.Database.DSN != "alertloop.db" {
		t.Fatalf("dsn = %q, want the ${VAR:-default} fallback", cfg.Database.DSN)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
}

// Only a whole value is substituted. Partial interpolation stays literal, which
// is what keeps a password containing "$" intact.
func TestSubstitutionIsWholeValueOnly(t *testing.T) {
	t.Setenv("HOST", "db.example.com")
	cfg := loadYAML(t, `
database:
  driver: postgres
  dsn: "postgres://user:pa$$w0rd@${HOST}:5432/alertloop"
`)
	const want = "postgres://user:pa$$w0rd@${HOST}:5432/alertloop"
	if cfg.Database.DSN != want {
		t.Fatalf("dsn = %q, want it left verbatim (%q)", cfg.Database.DSN, want)
	}
}

// A substituted value is data, never YAML to re-parse: a password with ": " or
// "#" must not turn into a mapping or a comment.
func TestSubstitutedValueIsNotReparsedAsYAML(t *testing.T) {
	t.Setenv("PW", "a: b #c")
	cfg := loadYAML(t, `
channels:
  email:
    - name: ops
      host: smtp.example.com
      from: a@example.com
      to: ["b@example.com"]
      password: ${PW}
`)
	if got := cfg.Channels.Email[0].Password; got != "a: b #c" {
		t.Fatalf("password = %q, want the literal environment value", got)
	}
}

// An unset variable with no default is a startup error: an empty admin_token
// caused by a typo in a variable name would leave the API open.
func TestMissingVariableIsAnError(t *testing.T) {
	_, err := loadYAMLErr(t, "admin_token: ${DEFINITELY_NOT_SET_ANYWHERE}\n")
	if err == nil {
		t.Fatal("expected an error for an unset variable with no default")
	}
	if !strings.Contains(err.Error(), "DEFINITELY_NOT_SET_ANYWHERE") {
		t.Fatalf("error should name the variable: %v", err)
	}
}

func TestEmptyDefaultIsAllowed(t *testing.T) {
	cfg := loadYAML(t, "log:\n  file: ${ALERTLOOP_LOG_FILE:-}\n")
	if cfg.Log.File != "" {
		t.Fatalf("log.file = %q, want the empty default", cfg.Log.File)
	}
}

// Pre-0.3.0 variables are refused, not ignored: a stale ALERTLOOP_DB_DSN would
// otherwise let the process run on a database its operator did not choose.
func TestLegacyEnvVarIsRefused(t *testing.T) {
	t.Setenv("ALERTLOOP_DB_DSN", "postgres://stale/db")
	_, err := loadYAMLErr(t, "database:\n  driver: sqlite\n  dsn: file.db\n")
	if err == nil {
		t.Fatal("expected startup to fail on a leftover ALERTLOOP_DB_DSN")
	}
	if !strings.Contains(err.Error(), "ALERTLOOP_DB_DSN") || !strings.Contains(err.Error(), "database.dsn") {
		t.Fatalf("error must name the variable and its replacement: %v", err)
	}
}

// The same name is fine when the file actually uses it to inject a secret.
func TestReferencedVariableIsNotLegacy(t *testing.T) {
	t.Setenv("ALERTLOOP_ADMIN_TOKEN", "tok")
	cfg := loadYAML(t, "admin_token: ${ALERTLOOP_ADMIN_TOKEN}\n")
	if cfg.AdminToken != "tok" {
		t.Fatalf("admin_token = %q", cfg.AdminToken)
	}
}

// Compose clears variables by setting them empty (FOO: ${FOO:-}); that is not a
// leftover configuration attempt.
func TestEmptyLegacyVariableIsIgnored(t *testing.T) {
	t.Setenv("ALERTLOOP_ADDR", "")
	if _, err := loadYAMLErr(t, "addr: \":8080\"\n"); err != nil {
		t.Fatalf("an empty legacy variable should not fail the load: %v", err)
	}
}

// With no config file at all, defaults still load — and a leftover variable is
// still refused.
func TestNoConfigFile(t *testing.T) {
	cfg, err := Load("")
	if err != nil {
		t.Fatalf("load without a file: %v", err)
	}
	if cfg.Addr != ":8080" {
		t.Fatalf("defaults not applied: %+v", cfg)
	}

	t.Setenv("ALERTLOOP_LOG_LEVEL", "debug")
	if _, err := Load(""); err == nil {
		t.Fatal("expected a leftover variable to fail even with no config file")
	}
}

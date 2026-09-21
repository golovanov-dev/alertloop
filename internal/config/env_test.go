package config

import (
	"strings"
	"testing"
	"time"
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

// With no config file at all, defaults still load.
func TestNoConfigFile(t *testing.T) {
	cfg, err := Load("")
	if err != nil {
		t.Fatalf("load without a file: %v", err)
	}
	if cfg.Addr != ":8080" {
		t.Fatalf("defaults not applied: %+v", cfg)
	}
}

// Substitution must work for every field type, not just strings. Forcing the
// !!str tag on substituted scalars made ${VAR} unusable for retention_days,
// worker.*, rate_limit.* and SMTP ports — the documentation promised
// otherwise.
func TestSubstitutionWorksForNonStringFields(t *testing.T) {
	t.Setenv("RET", "45")
	t.Setenv("CONC", "7")
	t.Setenv("RL", "false")
	t.Setenv("RPS", "12.5")
	t.Setenv("POLL", "5s")
	t.Setenv("TOK", "12345") // a secret that looks like a number stays a string

	cfg := loadYAML(t, `
admin_token: ${TOK}
retention_days: ${RET}
worker:
  concurrency: ${CONC}
  poll_interval: ${POLL}
rate_limit:
  enabled: ${RL}
  per_ip_per_second: ${RPS}
`)
	if cfg.RetentionDays != 45 {
		t.Errorf("retention_days = %d, want 45", cfg.RetentionDays)
	}
	if cfg.Worker.Concurrency != 7 {
		t.Errorf("worker.concurrency = %d, want 7", cfg.Worker.Concurrency)
	}
	if cfg.Worker.PollInterval != 5*time.Second {
		t.Errorf("worker.poll_interval = %v, want 5s", cfg.Worker.PollInterval)
	}
	if cfg.RateLimit.Enabled {
		t.Error("rate_limit.enabled = true, want false")
	}
	if cfg.RateLimit.PerIPPerSecond != 12.5 {
		t.Errorf("rate_limit.per_ip_per_second = %v, want 12.5", cfg.RateLimit.PerIPPerSecond)
	}
	if cfg.AdminToken != "12345" {
		t.Errorf("admin_token = %q, want the digits as a string", cfg.AdminToken)
	}
}

// Two adjacent references are not one value with a default: the default must
// not swallow the second reference.
func TestAdjacentReferencesAreLeftAlone(t *testing.T) {
	cfg := loadYAML(t, "admin_token: ${A:-tok}${B}\n")
	if cfg.AdminToken != "${A:-tok}${B}" {
		t.Fatalf("admin_token = %q, want the value left verbatim", cfg.AdminToken)
	}
}

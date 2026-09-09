package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDefaults(t *testing.T) {
	cfg := Default()
	if cfg.Addr != ":8080" || cfg.Database.Driver != "sqlite" || cfg.RetentionDays != 30 {
		t.Fatalf("unexpected defaults: %+v", cfg)
	}
}

func TestYAMLIsTheOnlyConfigurationSource(t *testing.T) {
	const doc = `
addr: ":9000"
admin_token: fromfile
database:
  driver: sqlite
  dsn: file.db
api_keys:
  - key: k1
    scope: ingest
  - key: k2
`
	cfg := loadYAML(t, doc)
	if cfg.Addr != ":9000" || cfg.AdminToken != "fromfile" {
		t.Fatalf("file values not applied: addr=%q admin_token=%q", cfg.Addr, cfg.AdminToken)
	}
	// Keys load from YAML; an omitted scope defaults to full.
	if len(cfg.APIKeys) != 2 {
		t.Fatalf("expected 2 API keys, got %v", cfg.APIKeys)
	}
	if cfg.APIKeys[0].Scope != ScopeIngest || cfg.APIKeys[1].Scope != ScopeFull {
		t.Fatalf("unexpected scopes: %+v", cfg.APIKeys)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("expected valid config, got %v", err)
	}
}

func TestInferDriver(t *testing.T) {
	cases := map[string]string{
		"postgres://u:p@localhost/db": "postgres",
		"host=localhost user=x":       "postgres",
		"alertloop.db":                "sqlite",
		":memory:":                    "sqlite",
	}
	for dsn, want := range cases {
		if got := inferDriver(dsn); got != want {
			t.Errorf("inferDriver(%q) = %q, want %q", dsn, got, want)
		}
	}
}

func TestValidate(t *testing.T) {
	cfg := Default()
	if err := cfg.Validate(); err != nil {
		t.Fatalf("valid default should pass: %v", err)
	}
	cfg.Channels.Telegram = []TelegramChannel{{Name: "tg"}} // missing bot_token/chat_id
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error for incomplete telegram channel")
	}

	// Duplicate names across channels are rejected.
	dup := Default()
	dup.Channels.Webhook = []WebhookChannel{
		{Name: "same", URL: "http://a"},
		{Name: "same", URL: "http://b"},
	}
	if err := dup.Validate(); err == nil {
		t.Fatal("expected error for duplicate channel names")
	}
}

func TestYAMLChannelsLoadedAndNormalized(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "alertloop.yaml")
	yaml := `
database:
  driver: sqlite
  dsn: x.db
channels:
  telegram:
    - name: tg-ru
      bot_token: t1
      chat_id: "-100"
    - name: tg-en
      bot_token: t2
      chat_id: "-200"
  webhook:
    - name: siem
      url: https://example.com/hook
`
	if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(cfg.Channels.Telegram) != 2 || len(cfg.Channels.Webhook) != 1 {
		t.Fatalf("unexpected channel counts: %+v", cfg.Channels)
	}
	// Normalization fills defaults (Telegram API base, timeouts).
	if cfg.Channels.Telegram[0].APIBase == "" || cfg.Channels.Telegram[0].Timeout == 0 {
		t.Fatalf("telegram defaults not normalized: %+v", cfg.Channels.Telegram[0])
	}
	if cfg.Channels.Webhook[0].Timeout == 0 {
		t.Fatal("webhook timeout not normalized")
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("expected valid config, got %v", err)
	}
}

// Rotation is on by default: a log file that nothing rotates is one of the
// three causes listed in the "disk filled up" runbook, and the operator who
// sets log.file is rarely the one who remembers to add a logrotate snippet.
func TestLogRotationDefaults(t *testing.T) {
	cfg := Default()
	if cfg.Log.MaxSizeMB != 50 || cfg.Log.MaxFiles != 5 {
		t.Fatalf("log rotation defaults = %d MB / %d files, want 50 / 5", cfg.Log.MaxSizeMB, cfg.Log.MaxFiles)
	}

	// A config file that predates these fields keeps the defaults rather than
	// silently getting an unrotated log.
	older := loadYAML(t, `
database:
  driver: sqlite
  dsn: x.db
log:
  level: "debug"
  file: "/var/log/alertloop/alertloop.log"
`)
	if older.Log.MaxSizeMB != 50 || older.Log.MaxFiles != 5 {
		t.Fatalf("a log section without rotation keys gave %d MB / %d files", older.Log.MaxSizeMB, older.Log.MaxFiles)
	}
	if older.Log.Level != "debug" || older.Log.File != "/var/log/alertloop/alertloop.log" {
		t.Fatalf("log section not applied: %+v", older.Log)
	}
}

func TestLogRotationSettingsLoadFromYAML(t *testing.T) {
	cfg := loadYAML(t, `
database:
  driver: sqlite
  dsn: x.db
log:
  file: "/tmp/alertloop.log"
  max_size_mb: 10
  max_files: 2
`)
	if cfg.Log.MaxSizeMB != 10 || cfg.Log.MaxFiles != 2 {
		t.Fatalf("log rotation = %d MB / %d files, want 10 / 2", cfg.Log.MaxSizeMB, cfg.Log.MaxFiles)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("expected valid config, got %v", err)
	}

	// Zero is a supported choice, not an error: it means something else rotates
	// the file (logrotate, a shipping agent).
	off := loadYAML(t, `
database:
  driver: sqlite
  dsn: x.db
log:
  file: "/tmp/alertloop.log"
  max_size_mb: 0
  max_files: 0
`)
	if off.Log.MaxSizeMB != 0 || off.Log.MaxFiles != 0 {
		t.Fatalf("explicit zeros were overwritten: %+v", off.Log)
	}
	if err := off.Validate(); err != nil {
		t.Fatalf("explicit zeros must be valid, got %v", err)
	}
}

// A negative value is a typo. Guessing what it meant would either lose history
// or fill the disk, so it stops startup with the setting named.
func TestNegativeLogRotationSettingsAreRejected(t *testing.T) {
	size := Default()
	size.Log.MaxSizeMB = -1
	err := size.Validate()
	if err == nil {
		t.Fatal("a negative log.max_size_mb was accepted")
	}
	if !strings.Contains(err.Error(), "log.max_size_mb") {
		t.Fatalf("error does not name the setting: %v", err)
	}

	files := Default()
	files.Log.MaxFiles = -2
	err = files.Validate()
	if err == nil {
		t.Fatal("a negative log.max_files was accepted")
	}
	if !strings.Contains(err.Error(), "log.max_files") {
		t.Fatalf("error does not name the setting: %v", err)
	}
}

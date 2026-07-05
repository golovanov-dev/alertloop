package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDefaults(t *testing.T) {
	cfg := Default()
	if cfg.Addr != ":8080" || cfg.Database.Driver != "sqlite" || cfg.RetentionDays != 30 {
		t.Fatalf("unexpected defaults: %+v", cfg)
	}
}

func TestEnvOverridesYAML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "alertloop.yaml")
	yaml := "addr: \":9000\"\nadmin_token: fromfile\ndatabase:\n  driver: sqlite\n  dsn: file.db\n" +
		"api_keys:\n  - key: k1\n    scope: ingest\n  - key: k2\n"
	if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Setenv("ALERTLOOP_ADDR", ":7777")

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Addr != ":7777" { // env beats file
		t.Fatalf("expected env addr :7777, got %q", cfg.Addr)
	}
	if cfg.AdminToken != "fromfile" { // file beats default
		t.Fatalf("expected admin_token fromfile, got %q", cfg.AdminToken)
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

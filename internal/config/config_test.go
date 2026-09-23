package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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

// sources narrows an ingest key to its own events. A list on any other scope,
// or an empty list, would read as a restriction that does not exist.
func TestAPIKeySources(t *testing.T) {
	const head = "admin_token: t\ndatabase: {driver: sqlite, dsn: file.db}\napi_keys:\n"
	cfg := loadYAML(t, head+"  - {key: k1, scope: ingest, sources: [\" web-01 \", db-01]}\n")
	if err := cfg.Validate(); err != nil {
		t.Fatalf("valid ingest key with sources rejected: %v", err)
	}
	if got := cfg.APIKeys[0].Sources; len(got) != 2 || got[0] != "web-01" {
		t.Fatalf("sources = %q, want trimmed [web-01 db-01]", got)
	}

	for doc, want := range map[string]string{
		"  - {key: k1, scope: read, sources: [web-01]}\n": "scope ingest only",
		"  - {key: k1, scope: ingest, sources: []}\n":     "remove it to allow every source",
	} {
		err := loadYAML(t, head+doc).Validate()
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: error = %v, want one containing %q", strings.TrimSpace(doc), err, want)
		}
	}

	// sources with no value, written or substituted, is refused like [] rather
	// than decoded as "every source".
	t.Setenv("ALERTLOOP_TEST_SOURCES", "")
	for _, doc := range []string{
		"  - key: k1\n    scope: ingest\n    sources:\n",
		"  - key: k1\n    scope: ingest\n    sources: ${ALERTLOOP_TEST_SOURCES:-}\n",
	} {
		_, err := loadYAMLErr(t, head+doc)
		if err == nil || !strings.Contains(err.Error(), "sources has no value") {
			t.Errorf("%s: error = %v, want sources has no value", strings.TrimSpace(doc), err)
		}
	}
}

// A value that start would refuse is refused by Validate, which check-db runs
// too: the pre-flight check must not pass a config the start then rejects.
func TestValidateRefusesWhatStartWouldRefuse(t *testing.T) {
	for name, tc := range map[string]struct {
		set  func(*Config)
		want string
	}{
		"trusted_proxies": {func(c *Config) { c.RateLimit.TrustedProxies = []string{"10.0.0.0/8", "nginx"} }, "trusted_proxies: invalid trusted proxy address: nginx"},
		"log.level":       {func(c *Config) { c.Log.Level = "verbose" }, `log.level "verbose"`},
		"log.format":      {func(c *Config) { c.Log.Format = "yaml" }, `log.format "yaml"`},
	} {
		cfg := Default()
		tc.set(&cfg)
		if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want one naming %s", name, err, tc.want)
		}
	}
}

package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// loadYAML writes cfg to a temp file and loads it the way the binary does.
func loadYAML(t *testing.T, yaml string) Config {
	t.Helper()
	path := filepath.Join(t.TempDir(), "alertloop.yaml")
	if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	return cfg
}

// loadYAMLErr is loadYAML for the cases where the load itself must fail.
func loadYAMLErr(t *testing.T, yaml string) (Config, error) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "alertloop.yaml")
	if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	return Load(path)
}

const routingChannels = `
database:
  driver: sqlite
  dsn: x.db
channels:
  telegram:
    - name: dev-telegram
      bot_token: t
      chat_id: "-100"
    - name: customer-telegram
      bot_token: t
      chat_id: "-200"
`

func TestRoutingSectionLoads(t *testing.T) {
	cfg := loadYAML(t, routingChannels+`
routing:
  rules:
    - name: incidents-to-dev
      match:
        type: [incident]
        min_severity: warning
      channels: [dev-telegram]
    - name: orders-to-customer
      match:
        type: [business_event]
        category: ["order.*"]
      channels: [customer-telegram]
    - name: silence-healthchecks
      match:
        source: [healthcheck]
      channels: []
  default: [dev-telegram]
`)
	if cfg.Routing == nil {
		t.Fatal("routing section did not load")
	}
	if len(cfg.Routing.Rules) != 3 {
		t.Fatalf("expected 3 rules, got %d", len(cfg.Routing.Rules))
	}
	first := cfg.Routing.Rules[0]
	if first.Name != "incidents-to-dev" || first.Match.MinSeverity != "warning" ||
		strings.Join(first.Match.Type, ",") != "incident" || strings.Join(first.Channels, ",") != "dev-telegram" {
		t.Fatalf("unexpected first rule: %+v", first)
	}
	if len(cfg.Routing.Rules[2].Channels) != 0 {
		t.Fatalf("an explicit empty channel list must stay empty: %+v", cfg.Routing.Rules[2])
	}
	if strings.Join(cfg.Routing.Default, ",") != "dev-telegram" {
		t.Fatalf("unexpected default: %v", cfg.Routing.Default)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("valid routing rejected: %v", err)
	}
}

// A 0.1.x config has no routing section at all; that must stay distinguishable
// from an empty one, because it means "deliver to every channel".
func TestMissingRoutingSectionIsNil(t *testing.T) {
	cfg := loadYAML(t, routingChannels)
	if cfg.Routing != nil {
		t.Fatalf("expected no routing section, got %+v", cfg.Routing)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("0.1.x config rejected: %v", err)
	}
}

func TestValidateRejectsUnknownChannelInRule(t *testing.T) {
	cfg := loadYAML(t, routingChannels+`
routing:
  rules:
    - name: incidents-to-dev
      match:
        type: [incident]
      channels: [dev-telgram]
`)
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected startup to fail on an unknown channel name")
	}
	if !strings.Contains(err.Error(), "incidents-to-dev") || !strings.Contains(err.Error(), "dev-telgram") {
		t.Fatalf("error must name the rule and the channel: %v", err)
	}
}

func TestValidateRejectsUnknownChannelInDefault(t *testing.T) {
	cfg := loadYAML(t, routingChannels+`
routing:
  default: [nowhere]
`)
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected startup to fail on an unknown channel in default")
	}
}

func TestValidateRejectsDuplicateRuleNames(t *testing.T) {
	cfg := loadYAML(t, routingChannels+`
routing:
  rules:
    - name: same
      channels: [dev-telegram]
    - name: same
      channels: [customer-telegram]
`)
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected startup to fail on duplicate rule names")
	}
	if !strings.Contains(err.Error(), "same") {
		t.Fatalf("error must name the duplicate: %v", err)
	}
}

func TestValidateRejectsUnnamedRule(t *testing.T) {
	cfg := loadYAML(t, routingChannels+`
routing:
  rules:
    - channels: [dev-telegram]
`)
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected startup to fail on a rule without a name")
	}
}

func TestValidateRejectsBadMatchConditions(t *testing.T) {
	cases := map[string]string{
		"unknown type":         "      match:\n        type: [incidents]\n",
		"unknown severity":     "      match:\n        severity: [fatal]\n",
		"unknown min_severity": "      match:\n        min_severity: fatal\n",
		"leading wildcard":     "      match:\n        category: [\"*.created\"]\n",
		"embedded wildcard":    "      match:\n        source: [\"feeds_*_worker\"]\n",
	}
	for name, match := range cases {
		cfg := loadYAML(t, routingChannels+"routing:\n  rules:\n    - name: r\n"+match+"      channels: [dev-telegram]\n")
		if err := cfg.Validate(); err == nil {
			t.Errorf("%s: expected a configuration error", name)
		}
	}
}

// A trailing wildcard, an empty match, and an empty channel list are all valid.
func TestValidateAcceptsWildcardsSuppressionAndCatchAll(t *testing.T) {
	cfg := loadYAML(t, routingChannels+`
routing:
  rules:
    - name: orders
      match:
        category: ["order.*"]
        source: ["shop_*"]
      channels: [customer-telegram]
    - name: silence
      match:
        source: [healthcheck]
      channels: []
    - name: rest
      channels: [dev-telegram]
`)
	if err := cfg.Validate(); err != nil {
		t.Fatalf("valid routing rejected: %v", err)
	}
}

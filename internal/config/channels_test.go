package config

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/golovanov-dev/alertloop/internal/domain"
)

func TestNewChannelTypesLoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), "alertloop.yaml")
	yaml := `
public_url: https://alerts.example.com/
channels:
  slack:
    - {name: ops-slack, url: "https://hooks.slack.com/services/T0/B0/x", fallback: phone}
  teams:
    - {name: ops-teams, url: "https://prod.westeurope.logic.azure.com/workflows/x?sig=y", timeout: 5s}
  discord:
    - {name: ops-discord, url: "https://discord.com/api/webhooks/1/x"}
  ntfy:
    - {name: phone, topic: ops-alerts}
  pushover:
    - {name: pager, token: app, user_key: usr}
`
	if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
	if cfg.PublicURL != "https://alerts.example.com" {
		t.Fatalf("public_url = %q, want it without the trailing slash", cfg.PublicURL)
	}
	if cfg.Channels.Ntfy[0].Server != "https://ntfy.sh" || cfg.Channels.Pushover[0].Timeout != domain.DefaultChannelTimeout ||
		cfg.Channels.Teams[0].Timeout != 5*time.Second || cfg.Channels.Discord[0].Timeout != domain.DefaultChannelTimeout {
		t.Fatalf("defaults not applied: %+v", cfg.Channels)
	}
	if cfg.Channels.Fallbacks()["ops-slack"] != "phone" {
		t.Fatalf("fallbacks = %v", cfg.Channels.Fallbacks())
	}
	want := []string{"slack:ops-slack", "teams:ops-teams", "discord:ops-discord", "ntfy:phone", "pushover:pager"}
	if got := cfg.EnabledChannels(); !slices.Equal(got, want) {
		t.Fatalf("enabled channels = %v, want %v", got, want)
	}
}

func TestNewChannelTypesRefuseIncompleteSettings(t *testing.T) {
	cases := []struct {
		name string
		set  func(*Config)
		want string
	}{
		{"slack without url", func(c *Config) { c.Channels.Slack = []ChatChannel{{Name: "s"}} }, `slack channel "s" is incomplete (need url)`},
		{"teams url not http", func(c *Config) {
			c.Channels.Teams = []ChatChannel{{Name: "t", URL: "ftp://example.com/SECRET"}}
		}, `teams channel "t": url must be an absolute http:// or https:// address`},
		{"ntfy without topic", func(c *Config) { c.Channels.Ntfy = []NtfyChannel{{Name: "n", Server: defaultNtfyServer}} }, "need topic"},
		{"ntfy token and login", func(c *Config) {
			c.Channels.Ntfy = []NtfyChannel{{Name: "n", Server: defaultNtfyServer, Topic: "t", Token: "k", Username: "u", Password: "p"}}
		}, "not both"},
		{"ntfy login without password", func(c *Config) {
			c.Channels.Ntfy = []NtfyChannel{{Name: "n", Server: defaultNtfyServer, Topic: "t", Username: "u"}}
		}, "go together"},
		{"pushover without user key", func(c *Config) { c.Channels.Pushover = []PushoverChannel{{Name: "p", Token: "k"}} }, "need token, user_key"},
		{"public_url with a fragment", func(c *Config) { c.PublicURL = "https://alerts.example.com/#/x" }, "must not have a query or fragment"},
		{"public_url not absolute", func(c *Config) { c.PublicURL = "alerts.example.com" }, "must be an absolute"},
		{"public_url of the console", func(c *Config) { c.PublicURL = "https://alerts.example.com/admin/" }, "base address of AlertLoop, without /admin"},
	}
	for _, c := range cases {
		cfg := Default()
		c.set(&cfg)
		err := cfg.Validate()
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: error = %v, want it to contain %q", c.name, err, c.want)
			continue
		}
		if strings.Contains(err.Error(), "SECRET") {
			t.Errorf("%s: the error quotes the webhook URL: %v", c.name, err)
		}
	}
}

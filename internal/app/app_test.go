package app

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/golovanov-dev/alertloop/internal/config"
)

// newTestConfig is a runnable in-memory config with two channels.
func newTestConfig() config.Config {
	cfg := config.Default()
	cfg.Database.DSN = ":memory:"
	cfg.Channels.Telegram = []config.TelegramChannel{
		{Name: "dev-telegram", BotToken: "t", ChatID: "-100"},
		{Name: "customer-telegram", BotToken: "t", ChatID: "-200"},
	}
	return cfg
}

// startApp builds an App and returns everything it logged while starting.
func startApp(t *testing.T, cfg config.Config) (string, error) {
	t.Helper()
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	app, err := New(context.Background(), cfg, "test", log)
	if app != nil {
		t.Cleanup(func() { app.Close() })
	}
	return buf.String(), err
}

// The operator must be able to read the routing that took effect out of the
// startup log, without opening the config file.
func TestStartupLogsRoutingTable(t *testing.T) {
	cfg := newTestConfig()
	cfg.Routing = &config.Routing{
		Rules: []config.RoutingRule{
			{Name: "incidents-to-dev", Match: config.RoutingMatch{
				Type: []string{"incident"}, MinSeverity: "warning",
			}, Channels: []string{"dev-telegram"}},
			{Name: "orders-to-customer", Match: config.RoutingMatch{
				Type: []string{"business_event"}, Category: []string{"order.*"},
			}, Channels: []string{"customer-telegram"}},
		},
		Default: []string{"dev-telegram"},
	}
	logged, err := startApp(t, cfg)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	for _, want := range []string{
		`rule=incidents-to-dev`,
		`min_severity=warning`,
		`rule=orders-to-customer`,
		`category=[order.*]`,
		`routing default`,
	} {
		if !strings.Contains(logged, want) {
			t.Errorf("startup log is missing %q:\n%s", want, logged)
		}
	}
}

func TestStartupWarnsAboutUnreachableRulesAndUnusedChannels(t *testing.T) {
	cfg := newTestConfig()
	cfg.Routing = &config.Routing{
		Rules: []config.RoutingRule{
			{Name: "everything", Channels: []string{"dev-telegram"}},
			{Name: "orders", Match: config.RoutingMatch{Type: []string{"business_event"}},
				Channels: []string{"dev-telegram"}},
		},
	}
	logged, err := startApp(t, cfg)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if !strings.Contains(logged, "unreachable_rules=[orders]") {
		t.Errorf("no warning about the rule below the catch-all:\n%s", logged)
	}
	// customer-telegram is configured but nothing routes to it.
	if !strings.Contains(logged, "unused_channels=[customer-telegram]") {
		t.Errorf("no warning about the unused channel:\n%s", logged)
	}
}

// Without a routing section the log says so, and the App keeps delivering to
// every channel.
func TestStartupLogsUnconfiguredRouting(t *testing.T) {
	logged, err := startApp(t, newTestConfig())
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if !strings.Contains(logged, "routing not configured") {
		t.Errorf("expected the unconfigured-routing notice:\n%s", logged)
	}
	if strings.Contains(logged, "unused_channels") {
		t.Errorf("unconfigured routing must not report unused channels:\n%s", logged)
	}
}

// A routing rule pointing at a channel that does not exist stops the process:
// it would otherwise silently drop every event it matches.
func TestStartupFailsOnUnknownChannelInRule(t *testing.T) {
	cfg := newTestConfig()
	cfg.Routing = &config.Routing{Rules: []config.RoutingRule{
		{Name: "typo", Channels: []string{"dev-telgram"}},
	}}
	if _, err := startApp(t, cfg); err == nil {
		t.Fatal("expected startup to fail on an unknown channel name")
	}
}

func TestStartupFailsOnUnsupportedProxyScheme(t *testing.T) {
	cfg := newTestConfig()
	cfg.Channels.Telegram[0].Proxy = "mtproto://127.0.0.1:443"
	_, err := startApp(t, cfg)
	if err == nil {
		t.Fatal("expected startup to fail on an unsupported proxy scheme")
	}
	if !strings.Contains(err.Error(), "dev-telegram") {
		t.Fatalf("error should name the channel: %v", err)
	}
}

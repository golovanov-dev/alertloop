package app

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
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

// --- a config file must carry a credential --------------------------------

// loadFile writes a config file and loads it exactly as the binary does, so the
// tests below judge the situation an operator is actually in: a file on disk,
// passed with --config.
func loadFile(t *testing.T, yaml string) config.Config {
	t.Helper()
	path := filepath.Join(t.TempDir(), "alertloop.yaml")
	if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	return cfg
}

const inMemoryDB = "database:\n  driver: sqlite\n  dsn: \":memory:\"\n"

// A config file that names neither an admin token nor an api key stops the
// start. Until 0.6.0 it produced one WARN line and a JSON API that accepted
// every request from anyone who could reach it, with full scope — and an
// operator got there by following advice: writing admin_token as a reference
// with a fallback, which resolved to nothing when the variable was unset.
// Nothing about the running service showed it, because every request succeeded.
func TestAConfigFileWithNoCredentialRefusesToStart(t *testing.T) {
	cfg := loadFile(t, inMemoryDB)

	err := RequireCredential(cfg, "all")
	if err == nil {
		t.Fatal("a config file with no admin_token and no api_keys started; the API would be open to anyone")
	}
	for _, want := range []string{"admin_token", "api_keys", "--config"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not mention %q:\n%v", want, err)
		}
	}
	// The advice must not be the one that caused the incident: a ${VAR:-...}
	// fallback on admin_token resolves to an empty token and an open API.
	if strings.Contains(err.Error(), ":-") {
		t.Errorf("the refusal suggests a fallback for admin_token, which is how the API was left open:\n%v", err)
	}
	// And it prints no line to paste. Both ways a printed line goes wrong
	// have already cost a release: prose beside a ${VAR} reference becomes
	// part of the token (0.4.0-0.5.2, in the README), and a flow mapping
	// holding one does not parse at all. The message says what is missing
	// and in which file, and stops there.
	for _, pasteable := range []string{"${", "[{"} {
		if strings.Contains(err.Error(), pasteable) {
			t.Errorf("the refusal prints config text to copy (%q):\n%v", pasteable, err)
		}
	}
}

// An admin token is enough, and so is an api key on its own: the refusal is
// about having no credential at all, not about which one.
func TestAConfigFileWithEitherCredentialStarts(t *testing.T) {
	withToken := loadFile(t, inMemoryDB+"admin_token: a-real-token\n")
	if err := RequireCredential(withToken, "all"); err != nil {
		t.Fatalf("a config file with an admin token must start: %v", err)
	}

	withKey := loadFile(t, inMemoryDB+"api_keys:\n  - key: k-ingest\n    scope: ingest\n")
	if withKey.AdminToken != "" {
		t.Fatalf("this case is about api_keys alone; admin_token = %q", withKey.AdminToken)
	}
	if err := RequireCredential(withKey, "all"); err != nil {
		t.Fatalf("a config file with an api key and no admin token must start: %v", err)
	}
}

// Which modes the credential is required for: the ones that put an HTTP
// listener on the network. A worker serves nothing — refusing to run one over a
// credential it never uses would stop a split deployment for no reason — and
// check-db is a probe that exits. Documented deployments run `server` and
// `worker` as separate processes, so this distinction is not hypothetical.
func TestOnlyTheModesThatServeHTTPNeedACredential(t *testing.T) {
	cfg := loadFile(t, inMemoryDB)

	for mode, wantRefusal := range map[string]bool{
		"server":   true,
		"all":      true,
		"worker":   false,
		"check-db": false,
	} {
		err := RequireCredential(cfg, mode)
		if wantRefusal && err == nil {
			t.Errorf("mode %q serves HTTP and was allowed to start with no credential", mode)
		}
		if !wantRefusal && err != nil {
			t.Errorf("mode %q serves no HTTP and must not be stopped over a credential: %v", mode, err)
		}
	}
}

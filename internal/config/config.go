// Package config loads AlertLoop configuration from, in decreasing priority:
// explicit command flags, environment variables, a YAML config file, and
// built-in defaults.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Config is the full runtime configuration for an AlertLoop process.
type Config struct {
	// Addr is the HTTP listen address for the server mode.
	Addr string `yaml:"addr"`
	// AdminToken protects the events web page and grants full API access (it is
	// the admin console credential).
	AdminToken string `yaml:"admin_token"`
	// APIKeys are the accepted service/API keys, each with a scope limiting what
	// it can do. Configured in the YAML file only.
	APIKeys []APIKey `yaml:"api_keys"`

	Database  Database  `yaml:"database"`
	Worker    Worker    `yaml:"worker"`
	Channels  Channels  `yaml:"channels"`
	Log       Logging   `yaml:"log"`
	RateLimit RateLimit `yaml:"rate_limit"`

	// RetentionDays is the fixed Community event retention window.
	RetentionDays int `yaml:"retention_days"`

	// CORSOrigins are allowed browser origins for the JSON API. Needed only when
	// the admin console is served from a different origin (standalone). Use "*"
	// to allow any origin. Empty disables CORS (same-origin only).
	CORSOrigins []string `yaml:"cors_origins"`
}

// RateLimit configures in-process request rate limiting.
type RateLimit struct {
	// Enabled turns rate limiting on. When behind a reverse proxy you may
	// prefer to disable this and limit at the proxy instead.
	Enabled bool `yaml:"enabled"`
	// PerIPPerSecond / PerIPBurst bound requests from a single client IP
	// (brute-force and general abuse protection).
	PerIPPerSecond float64 `yaml:"per_ip_per_second"`
	PerIPBurst     int     `yaml:"per_ip_burst"`
	// IngestPerSecond / IngestBurst bound the overall event ingestion rate for
	// the whole process.
	IngestPerSecond float64 `yaml:"ingest_per_second"`
	IngestBurst     int     `yaml:"ingest_burst"`
}

// Scope constants for API keys. Higher-privilege operations require a broader
// scope; ScopeFull satisfies everything.
const (
	ScopeIngest = "ingest" // may only create events (POST /v1/events)
	ScopeRead   = "read"   // may only read (GET events, deliveries, stats, info)
	ScopeFull   = "full"   // full API access (ingest + read + actions + replay)
)

// APIKey is a service credential with a scope limiting what it can do.
type APIKey struct {
	Key   string `yaml:"key"`
	Scope string `yaml:"scope"` // ingest | read | full (default full)
}

// Logging configures application log output.
type Logging struct {
	// Level is one of debug, info, warn, error.
	Level string `yaml:"level"`
	// Format is "text" (human-readable) or "json" (for log processors such as
	// Loki, Elasticsearch, or Vector).
	Format string `yaml:"format"`
	// File is the path to write logs to. Empty means stdout. When set, logs are
	// appended so external tools (tail, journald, log shippers) can read them.
	File string `yaml:"file"`
}

// Database configures the backing store.
type Database struct {
	// Driver is "sqlite" or "postgres". When empty it is inferred from DSN.
	Driver string `yaml:"driver"`
	// DSN is the data source name. For sqlite this is a file path (or
	// ":memory:"); for postgres a standard connection URL/DSN.
	DSN string `yaml:"dsn"`
}

// Worker configures delivery worker behavior.
type Worker struct {
	// Concurrency is the number of parallel delivery workers.
	Concurrency int `yaml:"concurrency"`
	// PollInterval is how often idle workers look for new jobs.
	PollInterval time.Duration `yaml:"poll_interval"`
	// MaxAttempts caps delivery tries before dead-lettering.
	MaxAttempts int `yaml:"max_attempts"`
	// BaseBackoff is the first retry delay; it grows exponentially.
	BaseBackoff time.Duration `yaml:"base_backoff"`
	// MaxBackoff caps the exponential retry delay.
	MaxBackoff time.Duration `yaml:"max_backoff"`
}

// Channels holds the global channel configuration. Multiple channels of each
// type may be configured; in Community every event is delivered to every
// configured channel (no per-event routing). Each channel has a unique name.
type Channels struct {
	Email    []EmailChannel    `yaml:"email"`
	Telegram []TelegramChannel `yaml:"telegram"`
	Webhook  []WebhookChannel  `yaml:"webhook"`
}

// EmailChannel configures one SMTP delivery target.
type EmailChannel struct {
	Name     string        `yaml:"name"`
	Host     string        `yaml:"host"`
	Port     int           `yaml:"port"`
	Username string        `yaml:"username"`
	Password string        `yaml:"password"`
	From     string        `yaml:"from"`
	To       []string      `yaml:"to"`
	STARTTLS bool          `yaml:"starttls"` // require STARTTLS upgrade (plaintext port, e.g. 587)
	TLS      bool          `yaml:"tls"`      // implicit TLS / SMTPS (e.g. port 465)
	Timeout  time.Duration `yaml:"timeout"`
}

// TelegramChannel configures one Telegram Bot API delivery target.
type TelegramChannel struct {
	Name     string        `yaml:"name"`
	BotToken string        `yaml:"bot_token"`
	ChatID   string        `yaml:"chat_id"`
	APIBase  string        `yaml:"api_base"`
	Timeout  time.Duration `yaml:"timeout"`
}

// WebhookChannel configures one generic outbound webhook target. Deliveries are
// HMAC-signed over the request body with Secret.
type WebhookChannel struct {
	Name    string        `yaml:"name"`
	URL     string        `yaml:"url"`
	Secret  string        `yaml:"secret"`
	Timeout time.Duration `yaml:"timeout"`
}

// Default returns a Config populated with built-in defaults.
func Default() Config {
	return Config{
		Addr:          ":8080",
		RetentionDays: 30,
		Log:           Logging{Level: "info", Format: "text"},
		Database: Database{
			Driver: "sqlite",
			DSN:    "alertloop.db",
		},
		Worker: Worker{
			Concurrency:  2,
			PollInterval: 2 * time.Second,
			MaxAttempts:  5,
			BaseBackoff:  30 * time.Second,
			MaxBackoff:   30 * time.Minute,
		},
		Channels: Channels{}, // configured via YAML lists only
		RateLimit: RateLimit{
			Enabled:         true,
			PerIPPerSecond:  20,
			PerIPBurst:      40,
			IngestPerSecond: 100,
			IngestBurst:     200,
		},
	}
}

// channel field defaults applied per configured channel by normalizeChannels.
const (
	defaultSMTPPort     = 587
	defaultChanTimeout  = 10 * time.Second
	defaultTelegramBase = "https://api.telegram.org"
)

// Load builds a Config from defaults, then a YAML file (if configPath is set),
// then environment variables. Command flags are applied by the caller after
// Load, since they have the highest priority.
func Load(configPath string) (Config, error) {
	cfg := Default()

	if configPath != "" {
		data, err := os.ReadFile(configPath)
		if err != nil {
			return cfg, fmt.Errorf("read config file: %w", err)
		}
		if err := yaml.Unmarshal(data, &cfg); err != nil {
			return cfg, fmt.Errorf("parse config file: %w", err)
		}
	}

	applyEnv(&cfg)
	normalizeChannels(&cfg.Channels)
	for i := range cfg.APIKeys {
		if cfg.APIKeys[i].Scope == "" {
			cfg.APIKeys[i].Scope = ScopeFull
		}
	}

	if cfg.Database.Driver == "" {
		cfg.Database.Driver = inferDriver(cfg.Database.DSN)
	}
	return cfg, nil
}

// normalizeChannels fills per-channel field defaults (SMTP port, timeouts,
// Telegram API base) so each configured channel is complete.
func normalizeChannels(c *Channels) {
	for i := range c.Email {
		if c.Email[i].Port == 0 {
			c.Email[i].Port = defaultSMTPPort
		}
		if c.Email[i].Timeout == 0 {
			c.Email[i].Timeout = defaultChanTimeout
		}
	}
	for i := range c.Telegram {
		if c.Telegram[i].APIBase == "" {
			c.Telegram[i].APIBase = defaultTelegramBase
		}
		if c.Telegram[i].Timeout == 0 {
			c.Telegram[i].Timeout = defaultChanTimeout
		}
	}
	for i := range c.Webhook {
		if c.Webhook[i].Timeout == 0 {
			c.Webhook[i].Timeout = defaultChanTimeout
		}
	}
}

// applyEnv overlays ALERTLOOP_-prefixed environment variables onto cfg.
func applyEnv(cfg *Config) {
	setString(&cfg.Addr, "ALERTLOOP_ADDR")
	setString(&cfg.AdminToken, "ALERTLOOP_ADMIN_TOKEN")
	// API keys are structured (each has a scope) and are configured in YAML only,
	// like delivery channels. There is no ALERTLOOP_API_KEYS env var.
	setString(&cfg.Database.Driver, "ALERTLOOP_DB_DRIVER")
	setString(&cfg.Database.DSN, "ALERTLOOP_DB_DSN")
	setInt(&cfg.RetentionDays, "ALERTLOOP_RETENTION_DAYS")

	setString(&cfg.Log.Level, "ALERTLOOP_LOG_LEVEL")
	setString(&cfg.Log.Format, "ALERTLOOP_LOG_FORMAT")
	setString(&cfg.Log.File, "ALERTLOOP_LOG_FILE")

	if v := os.Getenv("ALERTLOOP_CORS_ORIGINS"); v != "" {
		cfg.CORSOrigins = splitList(v)
	}

	setInt(&cfg.Worker.Concurrency, "ALERTLOOP_WORKER_CONCURRENCY")
	setInt(&cfg.Worker.MaxAttempts, "ALERTLOOP_WORKER_MAX_ATTEMPTS")

	if v := os.Getenv("ALERTLOOP_RATELIMIT_ENABLED"); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			cfg.RateLimit.Enabled = b
		}
	}

	// Delivery channels are configured in the YAML file only (a single, list-
	// based source of truth). Infrastructure settings above stay env-tunable for
	// container/12-factor deploys.
}

// Validate checks that the configuration is internally consistent enough to
// start the requested mode. Every configured channel must have a unique,
// non-empty name and complete required fields.
func (c Config) Validate() error {
	switch c.Database.Driver {
	case "sqlite", "postgres":
	default:
		return fmt.Errorf("unsupported database driver %q (want sqlite or postgres)", c.Database.Driver)
	}
	if c.Database.DSN == "" {
		return fmt.Errorf("database dsn is required")
	}

	for _, k := range c.APIKeys {
		if k.Key == "" {
			return fmt.Errorf("an api_keys entry is missing its key")
		}
		switch k.Scope {
		case ScopeIngest, ScopeRead, ScopeFull:
		default:
			return fmt.Errorf("api key has invalid scope %q (want ingest, read, or full)", k.Scope)
		}
	}

	seen := map[string]bool{}
	checkName := func(kind, name string) error {
		if name == "" {
			return fmt.Errorf("%s channel is missing a name", kind)
		}
		if seen[name] {
			return fmt.Errorf("duplicate channel name %q (names must be unique across all channels)", name)
		}
		seen[name] = true
		return nil
	}

	for _, e := range c.Channels.Email {
		if err := checkName("email", e.Name); err != nil {
			return err
		}
		if e.Host == "" || e.From == "" || len(e.To) == 0 {
			return fmt.Errorf("email channel %q is incomplete (need host, from, to)", e.Name)
		}
	}
	for _, t := range c.Channels.Telegram {
		if err := checkName("telegram", t.Name); err != nil {
			return err
		}
		if t.BotToken == "" || t.ChatID == "" {
			return fmt.Errorf("telegram channel %q is incomplete (need bot_token, chat_id)", t.Name)
		}
	}
	for _, w := range c.Channels.Webhook {
		if err := checkName("webhook", w.Name); err != nil {
			return err
		}
		if w.URL == "" {
			return fmt.Errorf("webhook channel %q is incomplete (need url)", w.Name)
		}
	}
	return nil
}

// EnabledChannels lists the configured channel names, prefixed by type, for
// startup logging (e.g. "email:ops", "telegram:alerts").
func (c Config) EnabledChannels() []string {
	var out []string
	for _, e := range c.Channels.Email {
		out = append(out, "email:"+e.Name)
	}
	for _, t := range c.Channels.Telegram {
		out = append(out, "telegram:"+t.Name)
	}
	for _, w := range c.Channels.Webhook {
		out = append(out, "webhook:"+w.Name)
	}
	return out
}

func inferDriver(dsn string) string {
	if strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://") ||
		strings.Contains(dsn, "host=") {
		return "postgres"
	}
	return "sqlite"
}

func splitList(v string) []string {
	parts := strings.Split(v, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if s := strings.TrimSpace(p); s != "" {
			out = append(out, s)
		}
	}
	return out
}

func setString(dst *string, env string) {
	if v := os.Getenv(env); v != "" {
		*dst = v
	}
}

func setInt(dst *int, env string) {
	if v := os.Getenv(env); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			*dst = n
		}
	}
}

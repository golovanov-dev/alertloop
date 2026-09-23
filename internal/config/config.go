// Package config loads AlertLoop configuration from a single YAML file, on top
// of built-in defaults. The environment is not a second configuration layer: it
// only fills ${VAR} references inside that file, so secrets need not be written
// down (see env.go).
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/url"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/golovanov-dev/alertloop/internal/domain"
	"gopkg.in/yaml.v3"
)

// Config is the full runtime configuration for an AlertLoop process.
type Config struct {
	// Addr is the HTTP listen address for the server mode.
	Addr string `yaml:"addr"`
	// AdminToken signs in to the admin console and grants full API access.
	AdminToken string `yaml:"admin_token"`
	// APIKeys are the accepted service/API keys, each with a scope limiting what
	// it can do. Configured in the YAML file only.
	APIKeys []APIKey `yaml:"api_keys"`

	Database  Database  `yaml:"database"`
	Worker    Worker    `yaml:"worker"`
	Channels  Channels  `yaml:"channels"`
	Log       Logging   `yaml:"log"`
	RateLimit RateLimit `yaml:"rate_limit"`

	// Routing selects which channels each event is delivered to. A nil pointer
	// means the section is absent from the file, which keeps the pre-0.2.0
	// behavior: every event goes to every configured channel.
	Routing *Routing `yaml:"routing"`

	// NotifyOnResolve controls whether closing an incident sends a "resolved"
	// notice to the channels that were told about it — whether it was closed by
	// an ingested `status: resolved` or by the manual resolve action.
	//
	// It defaults to true. A product that tells you the database is down and
	// never tells you it came back leaves the reader to guess, and guessing is
	// what an alerting service exists to remove. Turn it off if your channel is
	// a ticketing webhook that opens an issue per message.
	//
	// A pointer so an absent key can be told from an explicit `false`: with a
	// plain bool the zero value would silently disable it for every config file
	// written before 0.4.0.
	NotifyOnResolve *bool `yaml:"notify_on_resolve"`

	// RetentionDays is the event retention window in days: events older than
	// this are pruned by the worker. Defaults to 30, with no upper bound.
	// (Retention
	// *policies* — per-project/per-event-type rules with UI management — are a
	// planned paid capability; the plain number is not.)
	RetentionDays int `yaml:"retention_days"`

	// Warnings are notes produced while loading (never read from the file): a
	// ${VAR:-default} that fell back because the variable was empty, for
	// instance. The caller logs them at startup.
	Warnings []string `yaml:"-"`
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
	// TrustedProxies are the addresses (IP or CIDR) whose X-Forwarded-For may
	// be believed when identifying the client for the per-IP limit.
	//
	// Empty means trust nothing, which is right for a directly exposed
	// instance. Behind the documented nginx setup it is WRONG to leave empty:
	// every request then arrives from 127.0.0.1, the whole internet shares one
	// bucket, brute-forcing the admin token is effectively unlimited, and one
	// noisy client can exhaust the bucket and get everyone else a 429.
	//
	// Only set this for proxies you actually run. The header is trivially
	// forged by anyone talking to AlertLoop directly, so trusting it
	// unconditionally is worse than ignoring it.
	TrustedProxies []string `yaml:"trusted_proxies"`
}

// Scope constants for API keys. Higher-privilege operations require a broader
// scope; ScopeFull satisfies everything.
const (
	// ScopeIngest may only use POST /v1/events: it creates, refreshes and
	// resolves events by dedupe_key, of every source or of its Sources only.
	ScopeIngest = "ingest"
	ScopeRead   = "read" // may only read (GET events, deliveries, stats, info)
	ScopeFull   = "full" // full API access (ingest + read + actions + replay)
)

// DemoAdminToken is the fallback admin token of the Compose demo profile. It is
// published in this repository.
const DemoAdminToken = "change-me-admin"

// APIKey is a service credential with a scope limiting what it can do.
type APIKey struct {
	Key   string `yaml:"key"`
	Scope string `yaml:"scope"` // ingest | read | full (default full)
	// Sources limits an ingest key to events with these exact `source` values,
	// new or existing. Omitted: every source. Only for scope ingest.
	Sources []string `yaml:"sources"`
}

// Logging configures application log output.
type Logging struct {
	// Level is one of debug, info, warn, error.
	Level string `yaml:"level"`
	// Format is "text" (human-readable) or "json" (for log processors such as
	// Loki, Elasticsearch, or Vector).
	Format string `yaml:"format"`
	// File is the path to write logs to, IN ADDITION to stdout. Empty means
	// stdout only. The file is opened for appending and its directory is
	// created if missing; rotating it is left to logrotate (copytruncate).
	//
	// stdout is never given up for a file: under Docker that would silence
	// `docker compose logs` and every log shipper reading the container's
	// output, and under systemd it would empty the journal.
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
// type may be configured, each under a unique name. Without a routing section
// every event is delivered to every configured channel; with one, the rules in
// Routing decide (see the Routing type).
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
	Name     string `yaml:"name"`
	BotToken string `yaml:"bot_token"`
	ChatID   string `yaml:"chat_id"`
	APIBase  string `yaml:"api_base"`
	// Proxy sends this channel's Bot API requests through an HTTP, HTTPS, or
	// SOCKS5 proxy — for hosts that cannot reach api.telegram.org directly.
	// Credentials may be embedded (socks5://user:pass@host:1080). Empty keeps
	// the process-wide HTTP_PROXY/HTTPS_PROXY/NO_PROXY behavior. The setting is
	// per channel on purpose: one instance may need a proxy for Telegram and a
	// direct route for a webhook into an internal network.
	Proxy   string        `yaml:"proxy"`
	Timeout time.Duration `yaml:"timeout"`
}

// WebhookChannel configures one generic outbound webhook target. Deliveries are
// HMAC-signed over the request body with Secret.
type WebhookChannel struct {
	Name    string        `yaml:"name"`
	URL     string        `yaml:"url"`
	Secret  string        `yaml:"secret"`
	Timeout time.Duration `yaml:"timeout"`
}

// Routing decides which channels an event is delivered to. Rules are evaluated
// top to bottom and the FIRST match wins — channels from several rules are
// never merged, so an operator reading the file top-down can tell where a given
// event goes without simulating the whole set.
//
// Omitting the whole section (a nil *Routing) keeps the pre-0.2.0 behavior:
// every event is delivered to every configured channel.
type Routing struct {
	Rules []RoutingRule `yaml:"rules"`
	// Default receives events that matched no rule. Empty means such events are
	// stored but not delivered, which is logged per event as a warning.
	Default []string `yaml:"default"`
}

// RoutingRule is one entry of the routing table.
type RoutingRule struct {
	Name string `yaml:"name"`
	// Match limits which events the rule applies to. An absent or empty match
	// makes the rule a catch-all.
	Match RoutingMatch `yaml:"match"`
	// Channels are the channel names the event goes to. An explicit empty list
	// is deliberate suppression: the event is stored, nothing is delivered.
	Channels []string `yaml:"channels"`
}

// RoutingMatch holds the conditions of a rule. Values inside one field are
// OR-ed, the fields themselves are AND-ed, and an absent field constrains
// nothing. Comparison is case-insensitive and ignores surrounding whitespace.
type RoutingMatch struct {
	Type     []string `yaml:"type"`
	Severity []string `yaml:"severity"`
	// MinSeverity matches events at or above the given alarm level.
	MinSeverity string `yaml:"min_severity"`
	// Source and Category accept a trailing "*" as a prefix wildcard
	// ("order.*"); a "*" anywhere else is a configuration error.
	Source   []string `yaml:"source"`
	Category []string `yaml:"category"`
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

// ShouldNotifyOnResolve reports whether closing an incident sends a recovery
// notice. An absent `notify_on_resolve` key means yes: it is the behaviour that
// makes the lifecycle complete, and a config file written before 0.4.0 must get
// it without being edited.
func (c Config) ShouldNotifyOnResolve() bool {
	return c.NotifyOnResolve == nil || *c.NotifyOnResolve
}

// channel field defaults applied per configured channel by normalizeChannels.
const (
	defaultSMTPPort     = 587
	defaultTelegramBase = "https://api.telegram.org"
)

// proxySchemes are the proxy URL schemes a channel may use. "socks5h" is
// accepted as a synonym of "socks5": Go's http.Transport treats them
// identically and hands the target host name to the proxy, which resolves it.
var proxySchemes = map[string]bool{"http": true, "https": true, "socks5": true, "socks5h": true}

// ParseProxyURL validates a channel proxy setting and returns the parsed URL,
// or (nil, nil) when the setting is empty. It runs at config load time so a
// broken proxy stops the process at startup instead of failing during the first
// incident. Its errors never echo the raw value, which may carry credentials.
func ParseProxyURL(raw string) (*url.URL, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return nil, nil
	}
	u, err := url.Parse(s)
	if err != nil {
		return nil, errors.New("proxy is not a valid URL (want scheme://[user:pass@]host:port)")
	}
	scheme := strings.ToLower(u.Scheme)
	if !proxySchemes[scheme] {
		return nil, fmt.Errorf("unsupported proxy scheme %q (want http, https, socks5, or socks5h)", u.Scheme)
	}
	if u.Hostname() == "" {
		return nil, fmt.Errorf("proxy %s:// is missing a host", scheme)
	}
	// http/https fall back to ports 80/443; SOCKS has no default, so a missing
	// port would only surface as a dial error on the first delivery.
	if u.Port() == "" && strings.HasPrefix(scheme, "socks5") {
		return nil, fmt.Errorf("proxy %s://%s is missing a port (e.g. :1080)", scheme, u.Hostname())
	}
	return u, nil
}

// SafeProxyURL renders a proxy URL for logs, the API, and the web UI as
// scheme://host:port — never the user name or password.
func SafeProxyURL(u *url.URL) string {
	if u == nil {
		return ""
	}
	return u.Scheme + "://" + u.Host
}

// Load builds a Config from built-in defaults overlaid with a YAML file, if one
// is given. ${VAR} references in the file are resolved from the environment
// first.
func Load(configPath string) (Config, error) {
	cfg := Default()

	if configPath != "" {
		data, err := os.ReadFile(configPath)
		if err != nil {
			return cfg, fmt.Errorf("read config file: %w", err)
		}
		// Decode explicitly rather than yaml.Unmarshal: a file with a second
		// "---" document used to have everything past the separator silently
		// dropped, which is exactly the quiet loss the single-source rule is
		// supposed to prevent.
		dec := yaml.NewDecoder(bytes.NewReader(data))
		var doc yaml.Node
		if err := dec.Decode(&doc); err != nil && !errors.Is(err, io.EOF) {
			return cfg, fmt.Errorf("parse config file: %w", err)
		}
		var extra yaml.Node
		if err := dec.Decode(&extra); err == nil {
			return cfg, errors.New("config file contains more than one YAML document; " +
				"everything after the \"---\" separator would be ignored")
		}
		// An empty file leaves a zero node, which Decode would reject; defaults
		// alone are a valid configuration.
		if doc.Kind != 0 {
			s := configSchema()
			warnings, subErr := substituteEnv(&doc)
			// Decoded before the tree is walked: yaml.v3 bounds alias
			// expansion here, so what the walks below cross is a tree it has
			// already crossed itself, and they need no budget of their own.
			// Its error is held rather than returned, because AlertLoop has
			// something better to say about the same file below.
			decErr := doc.Decode(&cfg)
			// Substitution fills in values; the key check judges keys. They are
			// independent, and both are reported together rather than one per
			// restart: an operator who mistyped a key AND forgot a variable
			// would otherwise fix the first, restart, and only then learn about
			// the second.
			if err := errors.Join(subErr, checkUnknownKeys(&doc, s), checkNullSources(&doc, s)); err != nil {
				return cfg, err
			}
			// The type error comes last, and only when nothing above fired.
			// An unresolved ${VAR} in a numeric field fails both: the text
			// stayed in the node, so yaml.v3 reports a string it cannot turn
			// into an int. That line sends the operator after a quoting
			// problem that is not there, while the message above already
			// names the variable and the line to fix. One refusal per run.
			if decErr != nil {
				return cfg, fmt.Errorf("parse config file: %w", decErr)
			}
			cfg.Warnings = warnings
		}
	}

	normalizeChannels(&cfg.Channels)
	for i := range cfg.APIKeys {
		if cfg.APIKeys[i].Scope == "" {
			cfg.APIKeys[i].Scope = ScopeFull
		}
		for j, s := range cfg.APIKeys[i].Sources {
			cfg.APIKeys[i].Sources[j] = strings.TrimSpace(s)
		}
	}

	if cfg.Database.Driver == "" {
		cfg.Database.Driver = inferDriver(cfg.Database.DSN)
	}
	return cfg, nil
}

// checkNullSources refuses an api_keys entry whose sources key is there with
// no value ("sources:" or an empty ${VAR:-}). It decodes as nil, which means
// "every source", while the operator wrote a restriction; "sources: []" is
// refused by Validate for the same reason.
func checkNullSources(doc *yaml.Node, s schema) error {
	var errs []error
	walkKeys(doc, func(path string, key, value *yaml.Node, plain bool) bool {
		if plain && path == "api_keys.sources" && value.Kind == yaml.ScalarNode && value.ShortTag() == "!!null" {
			errs = append(errs, fmt.Errorf("line %d: api_keys sources has no value; remove it to allow every source", key.Line))
		}
		return s.descend(path, plain)
	})
	return errors.Join(errs...)
}

// normalizeChannels fills per-channel field defaults (SMTP port, timeouts,
// Telegram API base) so each configured channel is complete.
func normalizeChannels(c *Channels) {
	for i := range c.Email {
		if c.Email[i].Port == 0 {
			c.Email[i].Port = defaultSMTPPort
		}
		if c.Email[i].Timeout == 0 {
			c.Email[i].Timeout = domain.DefaultChannelTimeout
		}
	}
	for i := range c.Telegram {
		if c.Telegram[i].APIBase == "" {
			c.Telegram[i].APIBase = defaultTelegramBase
		}
		if c.Telegram[i].Timeout == 0 {
			c.Telegram[i].Timeout = domain.DefaultChannelTimeout
		}
	}
	for i := range c.Webhook {
		if c.Webhook[i].Timeout == 0 {
			c.Webhook[i].Timeout = domain.DefaultChannelTimeout
		}
	}
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

	for i, k := range c.APIKeys {
		if k.Key == "" {
			return fmt.Errorf("an api_keys entry is missing its key")
		}
		switch k.Scope {
		case ScopeIngest, ScopeRead, ScopeFull:
		default:
			return fmt.Errorf("api key has invalid scope %q (want ingest, read, or full)", k.Scope)
		}
		if k.Sources == nil {
			continue
		}
		if k.Scope != ScopeIngest {
			return fmt.Errorf("api_keys[%d]: sources applies to scope ingest only, this key has scope %s", i, k.Scope)
		}
		if len(k.Sources) == 0 {
			return fmt.Errorf("api_keys[%d]: sources is empty; remove it to allow every source", i)
		}
		if slices.Contains(k.Sources, "") {
			return fmt.Errorf("api_keys[%d]: sources has an empty entry", i)
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
		if _, err := ParseProxyURL(t.Proxy); err != nil {
			return fmt.Errorf("telegram channel %q: %w", t.Name, err)
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

	if c.Routing != nil {
		if err := c.Routing.validate(seen); err != nil {
			return err
		}
	}
	if _, err := ParseTrustedProxies(c.RateLimit.TrustedProxies); err != nil {
		return fmt.Errorf("rate_limit.trusted_proxies: %w", err)
	}
	if _, err := c.Log.SlogLevel(); err != nil {
		return err
	}
	if _, err := c.Log.JSON(); err != nil {
		return err
	}
	return nil
}

// ParseTrustedProxies parses rate_limit.trusted_proxies: CIDR blocks and bare
// IPs, blank entries skipped.
func ParseTrustedProxies(entries []string) ([]*net.IPNet, error) {
	var nets []*net.IPNet
	for _, raw := range entries {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		if _, block, err := net.ParseCIDR(raw); err == nil {
			nets = append(nets, block)
			continue
		}
		ip := net.ParseIP(raw)
		if ip == nil {
			return nil, &net.ParseError{Type: "trusted proxy address", Text: raw}
		}
		bits := 32
		if ip.To4() == nil {
			bits = 128
		}
		nets = append(nets, &net.IPNet{IP: ip, Mask: net.CIDRMask(bits, bits)})
	}
	return nets, nil
}

// SlogLevel is log.level as a slog level; empty means info.
func (l Logging) SlogLevel() (slog.Level, error) {
	switch strings.ToLower(l.Level) {
	case "debug":
		return slog.LevelDebug, nil
	case "", "info":
		return slog.LevelInfo, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	}
	return slog.LevelInfo, fmt.Errorf("log.level %q is not one of debug, info, warn, error", l.Level)
}

// JSON reports whether log.format is json; empty means text.
func (l Logging) JSON() (bool, error) {
	switch strings.ToLower(l.Format) {
	case "", "text":
		return false, nil
	case "json":
		return true, nil
	}
	return false, fmt.Errorf("log.format %q is not one of text, json", l.Format)
}

// validate checks the routing section against the set of configured channel
// names. Every failure here stops the process: a routing table that points at a
// channel that does not exist would silently drop the events it matches.
func (r Routing) validate(channels map[string]bool) error {
	names := map[string]bool{}
	for i, rule := range r.Rules {
		name := strings.TrimSpace(rule.Name)
		if name == "" {
			return fmt.Errorf("routing rule #%d is missing a name (names appear in logs and in the routing preview)", i+1)
		}
		if names[name] {
			return fmt.Errorf("duplicate routing rule name %q (rule names must be unique)", name)
		}
		names[name] = true

		if err := rule.Match.validate(name); err != nil {
			return err
		}
		for _, ch := range rule.Channels {
			if !channels[strings.TrimSpace(ch)] {
				return fmt.Errorf("routing rule %q references unknown channel %q", name, ch)
			}
		}
	}
	for _, ch := range r.Default {
		if !channels[strings.TrimSpace(ch)] {
			return fmt.Errorf("routing default references unknown channel %q", ch)
		}
	}
	return nil
}

// validate checks one rule's conditions: known enum values and wildcards only
// where they are supported.
func (m RoutingMatch) validate(rule string) error {
	for _, t := range m.Type {
		if !ValidRoutingType(t) {
			return fmt.Errorf("routing rule %q: unknown event type %q (want incident, business_event, or audit)", rule, t)
		}
	}
	for _, s := range m.Severity {
		if !ValidRoutingSeverity(s) {
			return fmt.Errorf("routing rule %q: unknown severity %q (want info, success, warning, error, or critical)", rule, s)
		}
	}
	if strings.TrimSpace(m.MinSeverity) != "" && !ValidRoutingSeverity(m.MinSeverity) {
		return fmt.Errorf("routing rule %q: unknown min_severity %q (want info, success, warning, error, or critical)", rule, m.MinSeverity)
	}
	for _, field := range []struct {
		name   string
		values []string
	}{{"source", m.Source}, {"category", m.Category}} {
		for _, v := range field.values {
			if err := validPattern(v); err != nil {
				return fmt.Errorf("routing rule %q, %s %q: %w", rule, field.name, v, err)
			}
		}
	}
	return nil
}

// validPattern accepts a literal value or one ending in "*". Anything richer
// (a leading or embedded "*", a regular expression) is rejected rather than
// quietly treated as a literal.
func validPattern(v string) error {
	s := strings.TrimSpace(v)
	if i := strings.IndexByte(s, '*'); i >= 0 && i != len(s)-1 {
		return errors.New(`only a trailing "*" wildcard is supported (e.g. "order.*")`)
	}
	return nil
}

// ValidRoutingType reports whether s names an event family, tolerating case and
// surrounding whitespace as rule matching does.
func ValidRoutingType(s string) bool {
	return domain.ValidEventType(domain.EventType(normalizeMatchValue(s)))
}

// ValidRoutingSeverity reports whether s names a severity.
func ValidRoutingSeverity(s string) bool {
	return domain.ValidSeverity(domain.Severity(normalizeMatchValue(s)))
}

// normalizeMatchValue is the comparison form of a routing value: trimmed and
// lower-cased, so "Incident " and "incident" mean the same thing.
func normalizeMatchValue(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}

// EnabledChannels lists the configured channel names, prefixed by type, for
// startup logging (e.g. "email:ops", "telegram:alerts"). A Telegram proxy is
// shown as scheme://host:port so the operator can confirm it is in effect;
// credentials in the proxy URL are never included.
func (c Config) EnabledChannels() []string {
	var out []string
	for _, e := range c.Channels.Email {
		out = append(out, "email:"+e.Name)
	}
	for _, t := range c.Channels.Telegram {
		entry := "telegram:" + t.Name
		if u, err := ParseProxyURL(t.Proxy); err == nil && u != nil {
			entry += " (proxy " + SafeProxyURL(u) + ")"
		}
		out = append(out, entry)
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

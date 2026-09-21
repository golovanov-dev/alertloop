package config

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// What the config loader does between the file and the struct: substitution,
// the shapes yaml.v3 alone cannot get right (a pointer field, a variable
// holding the text "null"), and the refusal of a key AlertLoop does not read.
//
// One case per branch. Decoding "45" into an int is yaml.v3's business, not
// AlertLoop's — a row added for symmetry only costs the time of every run.
//
// Values are compared against typed expectations, never against their string
// form: a field that arrives as the right text in the wrong type is exactly the
// failure this file exists to catch.

// eq compares one field against a typed expectation and prints both types on
// failure, because "45" against 45 is the whole point of this file.
func eq[T comparable](t *testing.T, field string, got, want T) {
	t.Helper()
	if got != want {
		t.Errorf("%s = %#v (%T), want %#v (%T)", field, got, got, want, want)
	}
}

func eqList(t *testing.T, field string, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Errorf("%s = %#v, want %#v", field, got, want)
		return
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("%s[%d] = %q, want %q", field, i, got[i], want[i])
		}
	}
}

// fullSurface sets every field AlertLoop reads, as literals. It doubles as the
// fixture for "does the whole surface still load and validate".
const fullSurface = `
addr: ":9100"
admin_token: literal-token
retention_days: 45
notify_on_resolve: false
cors_origins:
  - https://console.example.com
  - https://ops.example.com
api_keys:
  - key: k-ingest
    scope: ingest
database:
  driver: postgres
  dsn: postgres://u:p@db/alertloop
worker:
  concurrency: 7
  poll_interval: 3s
  max_attempts: 9
  base_backoff: 15s
  max_backoff: 45m
log:
  level: debug
  format: json
  file: /var/log/alertloop/api.log
  max_size_mb: 10
  max_files: 3
rate_limit:
  enabled: false
  per_ip_per_second: 12.5
  per_ip_burst: 25
  ingest_per_second: 250.5
  ingest_burst: 500
  trusted_proxies:
    - 10.0.0.0/8
    - 172.18.0.1
channels:
  email:
    - name: ops-mail
      host: smtp.example.com
      port: 2525
      username: mailer
      password: mail-pass
      from: alerts@example.com
      to:
        - oncall@example.com
        - duty@example.com
      starttls: true
      tls: false
      timeout: 20s
  telegram:
    - name: ops-telegram
      bot_token: 123456:ABC-DEF
      chat_id: "-1001234567890"
      api_base: https://tg.internal
      proxy: socks5://127.0.0.1:1080
      timeout: 25s
  webhook:
    - name: siem
      url: https://siem.example.com/hook
      secret: hook-secret
      timeout: 30s
routing:
  rules:
    - name: critical-only
      match:
        type:
          - incident
          - audit
        severity:
          - error
          - critical
        min_severity: warning
        source:
          - billing.*
        category:
          - payments
      channels:
        - ops-telegram
        - siem
  default:
    - ops-mail
`

// --- string ---------------------------------------------------------------

func TestStringFieldsLoadFromYAML(t *testing.T) {
	cfg := loadYAML(t, fullSurface)

	eq(t, "addr", cfg.Addr, ":9100")
	eq(t, "admin_token", cfg.AdminToken, "literal-token")
	eq(t, "database.driver", cfg.Database.Driver, "postgres")
	eq(t, "database.dsn", cfg.Database.DSN, "postgres://u:p@db/alertloop")
	eq(t, "log.level", cfg.Log.Level, "debug")
	eq(t, "log.format", cfg.Log.Format, "json")
	eq(t, "log.file", cfg.Log.File, "/var/log/alertloop/api.log")
	eq(t, "api_keys[0].key", cfg.APIKeys[0].Key, "k-ingest")
	eq(t, "api_keys[0].scope", cfg.APIKeys[0].Scope, ScopeIngest)
	eq(t, "email.username", cfg.Channels.Email[0].Username, "mailer")
	eq(t, "email.password", cfg.Channels.Email[0].Password, "mail-pass")
	eq(t, "telegram.bot_token", cfg.Channels.Telegram[0].BotToken, "123456:ABC-DEF")
	// A chat_id is a string field holding digits: quoting is what keeps the
	// leading minus and the full length intact instead of an int64 round trip.
	eq(t, "telegram.chat_id", cfg.Channels.Telegram[0].ChatID, "-1001234567890")
	eq(t, "telegram.api_base", cfg.Channels.Telegram[0].APIBase, "https://tg.internal")
	eq(t, "webhook.url", cfg.Channels.Webhook[0].URL, "https://siem.example.com/hook")
	eq(t, "webhook.secret", cfg.Channels.Webhook[0].Secret, "hook-secret")
	eq(t, "routing.rules[0].name", cfg.Routing.Rules[0].Name, "critical-only")
	eq(t, "routing.rules[0].match.min_severity", cfg.Routing.Rules[0].Match.MinSeverity, "warning")

	if err := cfg.Validate(); err != nil {
		t.Fatalf("the full surface must be a valid configuration: %v", err)
	}
}

// --- *bool ----------------------------------------------------------------

// notify_on_resolve is a pointer so that "absent" and "explicitly false" stay
// distinguishable through the YAML path, not only in the struct: with a plain
// bool every config file written before 0.4.0 would silently lose recovery
// notices. TestNotifyOnResolveDefaultsToOn covers the accessor; this covers
// what the parser produces.
func TestNotifyOnResolvePointerHasThreeStatesInYAML(t *testing.T) {
	absent := loadYAML(t, "addr: \":8080\"\n")
	if absent.NotifyOnResolve != nil {
		t.Errorf("an absent key must decode to nil, got %v", *absent.NotifyOnResolve)
	}
	if !absent.ShouldNotifyOnResolve() {
		t.Error("an absent notify_on_resolve must mean on")
	}

	off := loadYAML(t, "notify_on_resolve: false\n")
	if off.NotifyOnResolve == nil || *off.NotifyOnResolve {
		t.Errorf("an explicit false must decode to a pointer to false, got %v", off.NotifyOnResolve)
	}
	if off.ShouldNotifyOnResolve() {
		t.Error("an explicit false was ignored")
	}

	on := loadYAML(t, "notify_on_resolve: true\n")
	if on.NotifyOnResolve == nil || !*on.NotifyOnResolve {
		t.Errorf("an explicit true must decode to a pointer to true, got %v", on.NotifyOnResolve)
	}

	// The same three states through the environment.
	t.Setenv("NOR", "false")
	viaEnv := loadYAML(t, "notify_on_resolve: ${NOR}\n")
	if viaEnv.NotifyOnResolve == nil || *viaEnv.NotifyOnResolve {
		t.Errorf("${VAR} = false must decode to a pointer to false, got %v", viaEnv.NotifyOnResolve)
	}
	t.Setenv("NOR", "true")
	viaEnv = loadYAML(t, "notify_on_resolve: ${NOR}\n")
	if viaEnv.NotifyOnResolve == nil || !*viaEnv.NotifyOnResolve {
		t.Errorf("${VAR} = true must decode to a pointer to true, got %v", viaEnv.NotifyOnResolve)
	}
	// An unset variable with a default is still an explicit value, not absence.
	viaDefault := loadYAML(t, "notify_on_resolve: ${NOR_UNSET:-false}\n")
	if viaDefault.NotifyOnResolve == nil || *viaDefault.NotifyOnResolve {
		t.Errorf("${VAR:-false} must decode to a pointer to false, got %v", viaDefault.NotifyOnResolve)
	}
}

// --- nested structs and lists of structs ----------------------------------

func TestNestedStructsAndListsOfStructsLoadFromYAML(t *testing.T) {
	// Several channels of each type, so an index bug cannot hide behind a
	// single-element list, plus the per-element defaults normalizeChannels
	// fills in.
	cfg := loadYAML(t, `
channels:
  email:
    - name: first-mail
      host: smtp1.example.com
      from: a@example.com
      to: ["x@example.com"]
    - name: second-mail
      host: smtp2.example.com
      port: 465
      from: b@example.com
      to: ["y@example.com"]
      tls: true
      timeout: 5s
  telegram:
    - name: first-tg
      bot_token: t1
      chat_id: "-100"
    - name: second-tg
      bot_token: t2
      chat_id: "-200"
      api_base: https://tg.internal
  webhook:
    - name: first-hook
      url: https://a.example.com
    - name: second-hook
      url: https://b.example.com
      timeout: 1m
routing:
  rules:
    - name: first-rule
      match:
        severity: [critical]
      channels: [first-tg]
    - name: second-rule
      channels: []
  default: [first-mail, second-hook]
`)

	eq(t, "len(channels.email)", len(cfg.Channels.Email), 2)
	eq(t, "len(channels.telegram)", len(cfg.Channels.Telegram), 2)
	eq(t, "len(channels.webhook)", len(cfg.Channels.Webhook), 2)

	// Element 0 takes the normalized defaults while element 1 keeps its own
	// values: defaults are applied per element, not to the list as a whole.
	eq(t, "email[0].port", cfg.Channels.Email[0].Port, defaultSMTPPort)
	eq(t, "email[0].timeout", cfg.Channels.Email[0].Timeout, defaultChanTimeout)
	eq(t, "email[1].port", cfg.Channels.Email[1].Port, 465)
	eq(t, "email[1].timeout", cfg.Channels.Email[1].Timeout, 5*time.Second)
	eq(t, "email[1].tls", cfg.Channels.Email[1].TLS, true)
	eq(t, "telegram[0].api_base", cfg.Channels.Telegram[0].APIBase, defaultTelegramBase)
	eq(t, "telegram[1].api_base", cfg.Channels.Telegram[1].APIBase, "https://tg.internal")
	eq(t, "webhook[0].timeout", cfg.Channels.Webhook[0].Timeout, defaultChanTimeout)
	eq(t, "webhook[1].timeout", cfg.Channels.Webhook[1].Timeout, time.Minute)

	// A struct nested two levels inside a list element (rules[].match).
	eqList(t, "routing.rules[0].match.severity", cfg.Routing.Rules[0].Match.Severity, []string{"critical"})
	// An absent match is the zero struct, which means catch-all, and an
	// explicit empty channels list is suppression: both must survive decoding.
	eq(t, "len(routing.rules[1].match.severity)", len(cfg.Routing.Rules[1].Match.Severity), 0)
	if cfg.Routing.Rules[1].Channels == nil || len(cfg.Routing.Rules[1].Channels) != 0 {
		t.Errorf("rules[1].channels = %#v, want an empty non-nil list (suppression)", cfg.Routing.Rules[1].Channels)
	}

	if err := cfg.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
}

// --- *Routing -------------------------------------------------------------

// The nil pointer is load-bearing: it is what keeps the pre-0.2.0 behaviour of
// delivering every event to every channel. A section that exists but is empty
// means the opposite — no rule matches and there is no default — so the two
// must never collapse into one another.
func TestRoutingPointerTellsAnAbsentSectionFromAnEmptyOne(t *testing.T) {
	if cfg := loadYAML(t, "addr: \":8080\"\n"); cfg.Routing != nil {
		t.Errorf("no routing section must decode to nil, got %+v", cfg.Routing)
	}
	if cfg := loadYAML(t, "routing: {}\n"); cfg.Routing == nil {
		t.Error("routing: {} must decode to a non-nil empty section")
	}
	if cfg := loadYAML(t, "routing:\n  rules: []\n"); cfg.Routing == nil {
		t.Error("routing with an empty rules list must decode to a non-nil section")
	}
	// A key with no value at all is YAML null, and null is absence: `routing:`
	// on its own keeps the deliver-to-everything path instead of suppressing
	// every event. Recorded because one character — `routing:` against
	// `routing: {}` — separates two opposite outcomes.
	if cfg := loadYAML(t, "routing:\n"); cfg.Routing != nil {
		t.Errorf("a valueless routing key is null, want nil, got %+v", cfg.Routing)
	}
}

// A variable set to an EMPTY value counts as unset, in every field type. That
// is deliberate: Compose blanks variables it has no value for (FOO: ${FOO:-}),
// and an empty admin_token or DSN reaching the process is the failure the
// missing-variable check exists to prevent. The cost is that an intentionally
// empty value must be written as ${VAR:-} in the file, not exported as empty.
func TestEmptyVariableCountsAsUnset(t *testing.T) {
	t.Setenv("AL_EMPTY", "")

	// Without a default: refused, exactly like an unset variable.
	for _, doc := range []string{
		"admin_token: ${AL_EMPTY}\n",
		"retention_days: ${AL_EMPTY}\n",
	} {
		_, err := loadYAMLErr(t, doc)
		if err == nil {
			t.Fatalf("an empty variable must be refused like an unset one: %q", doc)
		}
		if !strings.Contains(err.Error(), "AL_EMPTY") {
			t.Fatalf("the error must name the variable: %v", err)
		}
	}

	// With a default: the DEFAULT wins over the empty value, in both a string
	// and a non-string field.
	cfg := loadYAML(t, "admin_token: ${AL_EMPTY:-fallback}\nretention_days: ${AL_EMPTY:-45}\n")
	eq(t, "admin_token", cfg.AdminToken, "fallback")
	eq(t, "retention_days", cfg.RetentionDays, 45)
}

// ${VAR:-} is an explicitly empty default. In a string field it means the empty
// string (log.file: ${ALERTLOOP_LOG_FILE:-} is how logging to a file is left
// off); in a non-string field the empty value is YAML null, so the field keeps
// its built-in default rather than becoming zero.
func TestExplicitlyEmptyDefaultMeansEmptyForStringsAndNoChangeElsewhere(t *testing.T) {
	cfg := loadYAML(t, `
log:
  file: ${AL_UNSET_FILE:-}
admin_token: ${AL_UNSET_TOKEN2:-}
retention_days: ${AL_UNSET_RET2:-}
worker:
  poll_interval: ${AL_UNSET_POLL2:-}
notify_on_resolve: ${AL_UNSET_NOR2:-}
`)
	eq(t, "log.file", cfg.Log.File, "")
	eq(t, "admin_token", cfg.AdminToken, "")
	eq(t, "retention_days", cfg.RetentionDays, Default().RetentionDays)
	eq(t, "worker.poll_interval", cfg.Worker.PollInterval, Default().Worker.PollInterval)
	if cfg.NotifyOnResolve != nil {
		t.Errorf("notify_on_resolve = %v, want nil (absent) for an empty default", *cfg.NotifyOnResolve)
	}
}

// A variable holding the TEXT "null" must arrive as that text. Until 0.5.4 it
// did not: the substituted scalar was left for YAML to resolve, YAML reads
// null / Null / NULL / ~ as "no value", and the field was ERASED instead of
// filled — ${ALERTLOOP_ADMIN_TOKEN} with the value "null" produced an empty
// admin token and an open API, which is precisely what the unset-variable
// refusal exists to prevent. Tools produce that value without anyone trying:
// `jq -r` on a key it cannot find prints "null", and so do most templating
// engines reading a missing secret.
func TestVariableHoldingTheTextNullIsNotSwallowed(t *testing.T) {
	for _, v := range []string{"null", "Null", "NULL", "~"} {
		t.Setenv("AL_NULLISH", v)
		cfg := loadYAML(t, "admin_token: ${AL_NULLISH}\n")
		eq(t, "admin_token from "+v, cfg.AdminToken, v)

		// In a non-string field the same text is now a visible type error
		// instead of a silent fall back to the built-in default.
		if _, err := loadYAMLErr(t, "retention_days: ${AL_NULLISH}\n"); err == nil {
			t.Fatalf("retention_days: %q must fail to parse, not keep the default", v)
		}
	}
}

// The pin applies to a value that came from a VARIABLE, and to nothing else. A
// default written in the file is the config author's own YAML: they wrote "~"
// to mean "no value", and 0.5.4 briefly read it as a file called "~" in the
// working directory.
func TestADefaultWrittenInTheFileIsYAMLAndNotText(t *testing.T) {
	// log.file: the difference between "no log file" and a file named "~".
	cfg := loadYAML(t, "log:\n  file: ${AL_UNSET_TILDE:-~}\n")
	eq(t, "log.file", cfg.Log.File, "")

	// A whole section switched off by a default: this stopped loading at all.
	cfg = loadYAML(t, "routing: ${AL_UNSET_ROUTING:-null}\n")
	if cfg.Routing != nil {
		t.Errorf("routing = %+v, want the absent section a null default means", cfg.Routing)
	}
	cfg = loadYAML(t, "notify_on_resolve: ${AL_UNSET_NOR3:-~}\n")
	if cfg.NotifyOnResolve != nil {
		t.Errorf("notify_on_resolve = %v, want nil", *cfg.NotifyOnResolve)
	}

	// And the variable that holds the text stays text, as 0.5.4 made it.
	t.Setenv("AL_NULL_TEXT", "null")
	eq(t, "admin_token", loadYAML(t, "admin_token: ${AL_NULL_TEXT}\n").AdminToken, "null")
}

// The refusal has to name the FIELD, not only the variable. The promise is
// explicit — an empty admin_token from a typo would mean an open API, so the
// operator is told which line the missing variable was standing in.
func TestTheMissingVariableErrorNamesTheField(t *testing.T) {
	cases := []struct{ name, yaml, field string }{
		{"a scalar", "admin_token: ${AL_NOT_SET}\n", "admin_token"},
		{"a numeric field", "retention_days: ${AL_NOT_SET}\n", "retention_days"},
		{"a list element", "cors_origins:\n  - ${AL_NOT_SET}\n", "cors_origins"},
		{
			"a field inside a list element",
			"channels:\n  email:\n    - name: ops\n      password: ${AL_NOT_SET}\n",
			"channels.email.password",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := loadYAMLErr(t, tc.yaml)
			if err == nil {
				t.Fatal("an unset variable with no default must stop the load")
			}
			mustContain(t, err, tc.field, "AL_NOT_SET", "line ")
			// In a non-string field the unsubstituted text is also something
			// yaml.v3 cannot decode, and its "cannot unmarshal !!str" sends
			// the operator after a quoting problem that is not there. One
			// refusal per run, and it is the one naming the variable.
			if strings.Contains(err.Error(), "cannot unmarshal") {
				t.Errorf("yaml.v3's type error is printed on top of the refusal:\n%v", err)
			}
		})
	}
}

// admin_token is the one field the loader must not offer a fallback for. An
// operator who took that advice literally — admin_token: ${VAR:-} — got a stack
// that came up green with the JSON API open to anyone who could reach it.
func TestTheAdviceToUseAFallbackIsNotGivenForTheAdminToken(t *testing.T) {
	_, err := loadYAMLErr(t, "admin_token: ${AL_NOT_SET}\n")
	if err == nil {
		t.Fatal("an unset admin token variable must stop the load")
	}
	if strings.Contains(err.Error(), ":-default") {
		t.Errorf("the message tells the operator to give admin_token a fallback:\n%v", err)
	}
	mustContain(t, err, "admin_token", "no fallback")

	// Every other field may have one, and the message still says so.
	_, err = loadYAMLErr(t, "database:\n  dsn: ${AL_NOT_SET}\n")
	if err == nil {
		t.Fatal("an unset dsn variable must stop the load")
	}
	mustContain(t, err, "${VAR:-default}")
}

// A variable that is present but EMPTY is not a variable nobody arranged: some
// file passes it, and it arrived blank. The fallback in the config file then
// runs instead, which is how a service comes up healthy on a local SQLite file
// while its operator is certain it is on PostgreSQL. One line says so.
func TestABlankedVariableSaysWhichFallbackIsInEffect(t *testing.T) {
	const doc = "database:\n  driver: sqlite\n  dsn: ${ALERTLOOP_DB_DSN:-alertloop.db}\n"

	t.Setenv("ALERTLOOP_DB_DSN", "")
	cfg := loadYAML(t, doc)
	eq(t, "database.dsn", cfg.Database.DSN, "alertloop.db")
	joined := strings.Join(cfg.Warnings, "\n")
	for _, want := range []string{"database.dsn", "ALERTLOOP_DB_DSN", "line 3"} {
		if !strings.Contains(joined, want) {
			t.Errorf("the blanked variable produced no warning naming %s: %v", want, cfg.Warnings)
		}
	}

	// A variable nobody set says nothing: that is the shipped example running
	// on its own defaults, and a line printed at every normal startup is a line
	// nobody reads by the time it matters.
	cfg = loadYAML(t, "retention_days: ${AL_NEVER_SET_AT_ALL:-45}\n")
	if len(cfg.Warnings) != 0 {
		t.Errorf("an absent variable warned: %v", cfg.Warnings)
	}
}

// The warning names the field, the variable and the line — never the value.
//
// It used to print the value unless the field's name looked secret, and two
// fields whose names do not: a Telegram proxy URL carries `user:password@`, and
// a webhook URL carries the token in its path (Slack, Teams and Mattermost are
// all built that way). Both went to the log in full, one line below the log
// line where AlertLoop deliberately redacts the same proxy. SECURITY.md
// promises that secrets never reach the log and calls a break in that promise a
// vulnerability, so the value is not printed for ANY field: a list of secret
// field names goes stale exactly as a hand-written list of config keys would.
func TestTheBlankedVariableWarningNeverPrintsTheValue(t *testing.T) {
	const proxy = "socks5://operator:hunter2@10.0.0.5:1080"
	const hook = "https://hooks.example.com/T000/B111/SUPERSECRETTOKEN"

	t.Setenv("AL_PROXY", "")
	t.Setenv("AL_HOOK", "")
	t.Setenv("AL_RET", "")
	cfg := loadYAML(t, `
retention_days: ${AL_RET:-45}
channels:
  telegram:
    - name: tg
      bot_token: t
      chat_id: "-100"
      proxy: ${AL_PROXY:-`+proxy+`}
  webhook:
    - name: hook
      url: ${AL_HOOK:-`+hook+`}
`)
	// The fallbacks did take effect — this is the case that warns.
	eq(t, "channels.telegram[0].proxy", cfg.Channels.Telegram[0].Proxy, proxy)
	eq(t, "channels.webhook[0].url", cfg.Channels.Webhook[0].URL, hook)
	if len(cfg.Warnings) != 3 {
		t.Fatalf("want a warning per blanked variable, got %v", cfg.Warnings)
	}

	joined := strings.Join(cfg.Warnings, "\n")
	for _, leaked := range []string{"hunter2", proxy, hook, "SUPERSECRETTOKEN", "45"} {
		if strings.Contains(joined, leaked) {
			t.Errorf("the warning prints a value from the config file (%q):\n%s", leaked, joined)
		}
	}
	for _, want := range []string{"channels.telegram.proxy", "AL_PROXY", "channels.webhook.url", "AL_HOOK"} {
		if !strings.Contains(joined, want) {
			t.Errorf("the warning does not name %s:\n%s", want, joined)
		}
	}
}

// --- malformed input ------------------------------------------------------

// A value of the wrong shape stops the load with the line and the target type.
// Durations are the trap worth naming: "5" and "abc" are both rejected, because
// a bare number has no unit and silently reading it as nanoseconds would make
// the worker poll 5ns apart.
func TestMalformedScalarsAreRejectedWithTheirLine(t *testing.T) {
	_, err := loadYAMLErr(t, "worker:\n  poll_interval: 5\n")
	if err == nil {
		t.Fatal("a duration without a unit must be rejected")
	}
	mustContain(t, err, "time.Duration", "line ")
}

// --- unknown keys ---------------------------------------------------------

// Up to 0.5.3 an unknown key was ignored, and these two spellings are why that
// had to change. `retention_day: 5` ran on the built-in 30 days, and
// `trusted_proxy:` left the list empty — which behind a reverse proxy puts the
// whole internet in one rate-limit bucket. In both cases the operator had
// written the setting, the process disagreed, and nothing said so. Now the file
// is refused, naming the full path, the line, and the spelling that was meant.
func TestMisspelledKeysAreRefusedInsteadOfLosingTheSetting(t *testing.T) {
	_, err := loadYAMLErr(t, `
retention_day: 5
rate_limit:
  trusted_proxy:
    - 10.0.0.1
`)
	if err == nil {
		t.Fatal("a misspelled key loaded silently; the setting it was meant to be is still at its default")
	}
	mustContain(t, err, "retention_day", "line 2", "did you mean retention_days",
		"rate_limit.trusted_proxy", "line 4", "did you mean rate_limit.trusted_proxies")
}

// An unknown key is named by its FULL path. "trusted_proxy" alone would send an
// operator searching a file where several sections have similar keys.
func TestUnknownKeysAreNamedByTheirFullPath(t *testing.T) {
	cases := []struct {
		name, yaml, path, hint string
	}{
		{
			name: "inside a nested struct",
			yaml: "log:\n  levl: info\n",
			path: "log.levl", hint: "did you mean log.level",
		},
		{
			name: "inside an element of a list of structs",
			yaml: "channels:\n  email:\n    - name: ops\n      hst: smtp.example.com\n",
			path: "channels.email.hst", hint: "did you mean channels.email.host",
		},
		{
			name: "a known name in the wrong section",
			yaml: "trusted_proxies:\n  - 10.0.0.1\n",
			path: "trusted_proxies", hint: "did you mean rate_limit.trusted_proxies",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := loadYAMLErr(t, tc.yaml)
			if err == nil {
				t.Fatalf("%s loaded without complaint", tc.path)
			}
			mustContain(t, err, tc.path, tc.hint)
		})
	}
}

// Every unknown key is reported in ONE error. Handing them over one restart at
// a time is the same information delivered worse, and on a production instance
// each restart costs a window of missed events.
func TestEveryUnknownKeyIsReportedAtOnce(t *testing.T) {
	_, err := loadYAMLErr(t, `
addrr: ":8080"
worker:
  concurrensy: 4
channels:
  telegram:
    - name: ops
      chat: "-100"
`)
	if err == nil {
		t.Fatal("three unknown keys loaded without complaint")
	}
	mustContain(t, err, "addrr", "worker.concurrensy", "channels.telegram.chat")
	if n := strings.Count(err.Error(), "\n  line "); n != 3 {
		t.Fatalf("want all three keys listed once each, got %d lines:\n%v", n, err)
	}
}

// A whole unknown section is one mistake, not one per line inside it: listing
// every child would bury the one line that has to be edited.
func TestAnUnknownSectionIsReportedOnce(t *testing.T) {
	_, err := loadYAMLErr(t, "slack:\n  token: t\n  channel: '#ops'\n")
	if err == nil {
		t.Fatal("an unknown section loaded without complaint")
	}
	mustContain(t, err, "line 1", "slack")
	if strings.Contains(err.Error(), "slack.token") || strings.Contains(err.Error(), "slack.channel") {
		t.Fatalf("the children of an unknown section must not be listed separately:\n%v", err)
	}
}

// A mapping where a scalar belongs is a type error, and the type error is the
// better message: it says what the field is, not merely that "addr.port" is not
// a setting. So the key check does not descend into a field that has no keys.
func TestAMappingInAScalarFieldIsATypeErrorNotAnUnknownKey(t *testing.T) {
	_, err := loadYAMLErr(t, "addr:\n  port: 8080\n")
	if err == nil {
		t.Fatal("a mapping in a string field loaded without complaint")
	}
	if strings.Contains(err.Error(), "not AlertLoop settings") {
		t.Fatalf("want the type error, got the unknown-key error:\n%v", err)
	}
	mustContain(t, err, "string")
}

// Warnings is a runtime field (`yaml:"-"`), not a setting. Deriving the known
// keys from the struct without honouring that tag would quietly accept
// "warnings:" in a file and then ignore it — the failure this check exists to
// remove, reintroduced by the check itself.
func TestWarningsIsNotAConfigurationKey(t *testing.T) {
	_, err := loadYAMLErr(t, "warnings:\n  - anything\n")
	if err == nil {
		t.Fatal("`warnings:` was accepted from the file; it is a field the loader fills, not a setting")
	}
	mustContain(t, err, "warnings")
}

// An unknown key and an unset variable are two independent faults, and an
// operator who has both must learn about both from one run.
func TestAnUnknownKeyAndAnUnsetVariableAreReportedTogether(t *testing.T) {
	_, err := loadYAMLErr(t, "retention_day: 5\nadmin_token: ${AL_NO_SUCH_VARIABLE}\n")
	if err == nil {
		t.Fatal("neither fault stopped the load")
	}
	mustContain(t, err, "retention_day", "AL_NO_SUCH_VARIABLE")
}

// YAML anchors keep working. A reusable value has to be declared somewhere, and
// the place for it is a top-level "x-" key — the same convention Compose uses,
// and the only key prefix this check leaves alone. Keys merged in from an
// anchor are checked where they are merged, so the escape hatch cannot be used
// to smuggle an unknown setting in.
func TestAnchorsUnderAnXKeyAreAllowedAndMergedKeysAreStillChecked(t *testing.T) {
	cfg := loadYAML(t, `
x-defaults: &d
  timeout: 12s
channels:
  webhook:
    - name: hook
      url: https://example.com/hook
      <<: *d
`)
	eq(t, "channels.webhook[0].timeout", cfg.Channels.Webhook[0].Timeout, 12*time.Second)

	_, err := loadYAMLErr(t, `
x-defaults: &d
  timeoutt: 12s
channels:
  webhook:
    - name: hook
      url: https://example.com/hook
      <<: *d
`)
	if err == nil {
		t.Fatal("an unknown key merged in from an anchor was accepted")
	}
	mustContain(t, err, "channels.webhook.timeoutt")
}

// A key with a dot in it is ONE key name, whatever it looks like. `log.level:
// debug` in the root of the file is not the log level: YAML has no such key,
// yaml.v3 drops it, and the process runs on info while the file says debug —
// the "written but not in effect" failure the whole check exists to remove. So
// it is refused, and the message says that a key name is not a path. Not the
// value: that text is the operator's, it may be a bot token written on the
// wrong line, and this message goes to the log.
func TestADottedKeyIsRefusedBecauseAKeyNameIsNotAPath(t *testing.T) {
	cases := map[string]string{
		"a scalar setting":              "log.level: debug\n",
		"a list setting":                "rate_limit.trusted_proxies:\n  - 10.0.0.1\n",
		"a misspelling written as path": "log.levl: info\n",
		"resembling no setting at all":  "totally.bogus.key: 1\n",
	}
	for name, doc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := loadYAMLErr(t, doc)
			if err == nil {
				t.Fatal("a dotted key loaded cleanly and set nothing")
			}
			mustContain(t, err, "not AlertLoop settings", "line 1: ", "a key name is not a path")
			if strings.Contains(err.Error(), "debug") {
				t.Errorf("the message prints the value written in the file:\n%v", err)
			}
		})
	}
}

// A leftover variable that carried a secret is answered by name: the message
// goes to the log, and `dsn: postgres://u:hunter2@db/x` printed there is the
// leak SECURITY.md says AlertLoop does not have. It names the variable and the
// setting that replaced it and stops — the line to write lives in
// alertloop.example.yaml, not in an error message.
func TestTheRefusalForASecretCarryingVariableNeverPrintsItsValue(t *testing.T) {
	for _, tc := range []struct{ variable, setting string }{
		{"ALERTLOOP_ADMIN_TOKEN", "admin_token"},
		{"ALERTLOOP_DB_DSN", "database.dsn"},
	} {
		const secret = "s3cr3t-value-that-must-not-be-logged"
		t.Setenv(tc.variable, secret)

		_, err := loadYAMLErr(t, "notify_on_resolve: true\n")
		if err == nil {
			t.Fatalf("%s: a leftover pre-0.3.0 variable must stop the load", tc.variable)
		}
		if strings.Contains(err.Error(), secret) {
			t.Errorf("the refusal prints the value of %s:\n%v", tc.variable, err)
		}
		mustContain(t, err, tc.variable, tc.setting)
	}
}

// Swapping two neighbouring letters is the commonest typo there is, and the
// message promises a suggestion rather than sometimes offering one.
func TestATransposedLetterStillGetsASuggestion(t *testing.T) {
	cases := map[string]string{
		"channels:\n  email:\n    - name: ops\n      tsl: true\n":         "channels.email.tls",
		"channels:\n  email:\n    - name: ops\n      ot: [a@b.example]\n": "channels.email.to",
		"routing:\n  defualt:\n    - ops\n":                               "routing.default",
	}
	for doc, want := range cases {
		_, err := loadYAMLErr(t, doc)
		if err == nil {
			t.Fatalf("%q was accepted", doc)
		}
		mustContain(t, err, "did you mean "+want)
	}
}

// A config file must not be able to cost minutes before anything starts, and
// the walk over its keys carries no bound of its own: the document is decoded
// first, which is where yaml.v3 applies its alias budget, and the walk then
// descends only along paths the schema knows, which are finitely deep.
func TestPathologicalAnchorsAreRefusedRatherThanFollowedForever(t *testing.T) {
	// Seven levels of anchors, eight references each: a 500-byte file that
	// expands into two million paths.
	var bomb strings.Builder
	bomb.WriteString("x-l0: &l0\n  timeout: 12s\n")
	for i := 1; i <= 7; i++ {
		fmt.Fprintf(&bomb, "x-l%d: &l%d [", i, i)
		for j := 0; j < 8; j++ {
			if j > 0 {
				bomb.WriteString(",")
			}
			fmt.Fprintf(&bomb, "*l%d", i-1)
		}
		bomb.WriteString("]\n")
	}
	bomb.WriteString("channels:\n  email: *l7\n")

	cases := map[string]string{
		"expanding into millions of paths": bomb.String(),
		"an anchor that contains itself":   "channels: &x\n  email: [*x]\n",
	}
	for name, doc := range cases {
		t.Run(name, func(t *testing.T) {
			done := make(chan error, 1)
			go func() { done <- func() error { _, err := loadYAMLErr(t, doc); return err }() }()
			select {
			case err := <-done:
				if err == nil {
					t.Fatalf("%s was accepted", name)
				}
			case <-time.After(10 * time.Second):
				t.Fatalf("loading a tiny config file took over ten seconds: %s", name)
			}
		})
	}
}

// A large but perfectly ordinary file loads. The two walks over it must cover
// the same ground: one told the operator to write a setting the file already
// contained, because it stopped where the other had gone on.
func TestALargeHonestFileIsWalkedToTheEndByBothPasses(t *testing.T) {
	var b strings.Builder
	b.WriteString("log:\n  level: debug\nchannels:\n  email:\n")
	for i := 0; i < 20000; i++ {
		fmt.Fprintf(&b, "    - name: ops-%d\n      host: smtp.example.com\n"+
			"      from: a@example.com\n      to: [\"b@example.com\"]\n", i)
	}

	// The file sets log.level itself, so the leftover variable is a warning and
	// the load succeeds. That is what breaks when the second walk stops early.
	t.Setenv("ALERTLOOP_LOG_LEVEL", "debug")

	cfg, err := loadYAMLErr(t, b.String())
	if err != nil {
		t.Fatalf("a large but perfectly ordinary config was refused: %v", err)
	}
	if len(cfg.Warnings) != 1 || !strings.Contains(cfg.Warnings[0], "ALERTLOOP_LOG_LEVEL") {
		t.Fatalf("want one warning naming the shadowed variable, got %v", cfg.Warnings)
	}
	eq(t, "log.level", cfg.Log.Level, "debug")
}

// A key with no name to print is still pointed at by its line; printing
// nothing gave "line 1:" followed by empty space. A key that is not a scalar
// at all never reaches the key check — yaml.v3 refuses to decode it first, and
// that message names the line too.
func TestAKeyWithNoNameIsStillPointedAt(t *testing.T) {
	_, err := loadYAMLErr(t, "? [a, b]\n: v\n")
	if err == nil {
		t.Fatal("a non-scalar key was accepted")
	}
	mustContain(t, err, "line 1")

	_, err = loadYAMLErr(t, "\"\": v\n")
	if err == nil {
		t.Fatal("an empty key was accepted")
	}
	mustContain(t, err, `line 1: ""`)
}

// mustContain checks that an error says each of the things an operator needs in
// order to fix the file without guessing.
func mustContain(t *testing.T, err error, want ...string) {
	t.Helper()
	for _, w := range want {
		if !strings.Contains(err.Error(), w) {
			t.Errorf("error does not mention %q:\n%v", w, err)
		}
	}
}

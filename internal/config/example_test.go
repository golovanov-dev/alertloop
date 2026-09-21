package config

import (
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// examplePath is the config file shipped with the release. It is the one every
// installation starts from, so its behaviour is part of the product.
func examplePath() string { return filepath.Join("..", "..", "alertloop.example.yaml") }

// The 0.3.0 defect, as a test: a production deployment must not come up on a
// credential published in this repository. The example references
// ${ALERTLOOP_ADMIN_TOKEN} with no fallback, so an unset token stops startup
// with a message naming it.
func TestExampleConfigRefusesWithoutAnAdminToken(t *testing.T) {
	t.Setenv("ALERTLOOP_ADMIN_TOKEN", "")

	_, err := Load(examplePath())
	if err == nil {
		t.Fatal("the example config loaded with no admin token set; a placeholder credential has crept back in")
	}
	if !strings.Contains(err.Error(), "ALERTLOOP_ADMIN_TOKEN") {
		t.Fatalf("error does not name the missing variable, so nobody will know what to set: %v", err)
	}
}

func TestExampleConfigLoadsWithAnAdminToken(t *testing.T) {
	t.Setenv("ALERTLOOP_ADMIN_TOKEN", "a-real-token")

	cfg, err := Load(examplePath())
	if err != nil {
		t.Fatalf("the shipped example config does not load: %v", err)
	}
	if cfg.AdminToken != "a-real-token" {
		t.Fatalf("admin_token = %q, want the value from the environment", cfg.AdminToken)
	}
	if cfg.Database.Driver != "sqlite" {
		t.Fatalf("database.driver = %q, want the sqlite default", cfg.Database.Driver)
	}
	if cfg.RetentionDays != 30 {
		t.Fatalf("retention_days = %d, want 30", cfg.RetentionDays)
	}
}

// The example claims to list every key AlertLoop reads, and two places send an
// operator there on that claim: the unknown-key error ("alertloop.example.yaml
// lists every key AlertLoop reads") and the upgrade procedure, which is
// "compare your file with the example and remove what is not there". A key
// missing from the example turns that procedure into advice to delete a working
// setting — routing.rules[].match.severity was missing exactly that way.
func TestTheShippedExampleShowsEveryKey(t *testing.T) {
	data, err := os.ReadFile(examplePath())
	if err != nil {
		t.Fatalf("read the example: %v", err)
	}
	shown := pathsShownIn(string(data))
	known, _, _ := knownYAMLPaths(reflect.TypeOf(Config{}))

	var missing []string
	for _, path := range sortedKeys(known) {
		if !shown[path] {
			missing = append(missing, path)
		}
	}
	if len(missing) > 0 {
		t.Errorf("alertloop.example.yaml does not show %d of the %d configuration keys: %v\n"+
			"Add them, in a comment if they should not be on by default: the file is what operators are "+
			"told to check theirs against.", len(missing), len(known), missing)
	}
}

// pathsShownIn collects the dotted paths an example file shows, comments
// included — a channel the example shipped enabled would be a channel every new
// installation delivers to, so the keys that matter most are commented out.
//
// Whole paths, not leaf names. Matching the leaf alone passed
// `routing.rules.match.host` because `host:` appears under `channels.email`,
// which is exactly the kind of miss this test exists to catch.
//
// Uncommenting in this file is removing "# ", by construction: the indentation
// of a commented block is the indentation the lines have once it is gone. Lines
// that are prose rather than YAML do not match the key pattern (prose has
// spaces before its colon) and are skipped.
func pathsShownIn(text string) map[string]bool {
	key := regexp.MustCompile(`^(\s*)(- )?([A-Za-z_][A-Za-z0-9_.-]*):(\s|$)`)

	out := map[string]bool{}
	type level struct {
		indent int
		name   string
	}
	var stack []level

	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimRight(line, " \t\r")
		// Repeatedly: a key can sit inside a commented-out block that is itself
		// commented out one level deeper (the Telegram proxy is written that
		// way), and it is shown in the file all the same.
		for {
			i := strings.Index(line, "#")
			if i < 0 || strings.TrimSpace(line[:i]) != "" {
				break
			}
			line = line[:i] + strings.TrimPrefix(line[i+1:], " ")
		}
		m := key.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		indent := len(m[1]) + len(m[2]) // a "- " marker indents its own keys
		name := m[3]
		for len(stack) > 0 && stack[len(stack)-1].indent >= indent {
			stack = stack[:len(stack)-1]
		}
		path := name
		if len(stack) > 0 {
			path = stack[len(stack)-1].name + "." + name
		}
		out[path] = true
		stack = append(stack, level{indent: indent, name: path})
	}
	return out
}

// The same example file serves a binary install and the Compose postgres
// profile: the profile selects the driver and DSN through the environment
// rather than shipping a second, divergent copy of this file. If these
// references stop working, that consolidation silently breaks and the profile
// runs on SQLite inside a container while its operator is certain it is on
// PostgreSQL.
func TestExampleConfigTakesTheDatabaseFromTheEnvironment(t *testing.T) {
	const dsn = "postgres://alertloop:pw@postgres:5432/alertloop?sslmode=disable"
	t.Setenv("ALERTLOOP_ADMIN_TOKEN", "a-real-token")
	t.Setenv("ALERTLOOP_DB_DRIVER", "postgres")
	t.Setenv("ALERTLOOP_DB_DSN", dsn)

	cfg, err := Load(examplePath())
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Database.Driver != "postgres" {
		t.Fatalf("database.driver = %q, want postgres", cfg.Database.Driver)
	}
	if cfg.Database.DSN != dsn {
		t.Fatalf("database.dsn = %q, want the DSN from the environment", cfg.Database.DSN)
	}
}

// notify_on_resolve is a pointer so an absent key can be told from an explicit
// false. With a plain bool the zero value would silently disable recovery
// notices for every config file written before 0.4.0 — the exact class of
// regression that makes an upgrade quietly worse.
func TestNotifyOnResolveDefaultsToOn(t *testing.T) {
	t.Setenv("ALERTLOOP_ADMIN_TOKEN", "a-real-token")

	cfg, err := Load(examplePath())
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !cfg.ShouldNotifyOnResolve() {
		t.Fatal("the shipped example disables recovery notices")
	}

	// A config that never heard of the setting still gets it.
	if !Default().ShouldNotifyOnResolve() {
		t.Fatal("an absent notify_on_resolve must mean on")
	}

	off := false
	if (Config{NotifyOnResolve: &off}).ShouldNotifyOnResolve() {
		t.Fatal("an explicit false was ignored")
	}
	on := true
	if !(Config{NotifyOnResolve: &on}).ShouldNotifyOnResolve() {
		t.Fatal("an explicit true was ignored")
	}
}

// envExamplePath is the .env template the install instructions tell people to
// copy. It carries secrets and nothing else, so its VALUES are part of the
// product's security behaviour just as the config file is.
func envExamplePath() string { return filepath.Join("..", "..", ".env.example") }

// nonSecretEnvExampleVars may carry a value in .env.example: they select a
// deployment, they do not authenticate anything. Everything else must be empty.
//
// An allowlist rather than a search for "change-me": a rule that only rejects
// the exact placeholder this repository once shipped would let
// `POSTGRES_PASSWORD=secret` through without a word, and the next credential to
// leak this way will not be called change-me either.
var nonSecretEnvExampleVars = map[string]bool{
	"COMPOSE_PROFILES": true, // which deployment `docker compose up` starts
}

// The 2026-08-24 defect, as a test: `cp .env.example .env` supplied a non-empty
// admin token placeholder, so the refusal to start without a token never fired
// and the installation came up on a credential published in this repository.
// POSTGRES_PASSWORD is the same risk without a config reference (Compose builds
// the DSN from it). The rule is therefore blunt — every assignment in
// .env.example is empty unless it is on the allowlist. A variable that needs a
// value belongs in a comment showing the value, not in an assignment supplying
// one, because `cp .env.example .env` copies assignments and not intentions.
func TestEnvExampleAssignsNoValuesBesidesTheAllowlist(t *testing.T) {
	values := parseDotenv(t, envExamplePath())
	if len(values) == 0 {
		t.Fatal(".env.example has no assignments at all; the parser or the file is wrong")
	}
	for name, v := range values {
		if nonSecretEnvExampleVars[name] {
			continue
		}
		if strings.TrimSpace(v) != "" {
			t.Errorf(".env.example sets %s=%q. Everything but %v must be empty there: `cp .env.example .env` "+
				"is the documented install step, so any value here becomes a live credential on somebody's "+
				"server. Show it in a comment instead, or add the variable to nonSecretEnvExampleVars if it "+
				"is not a secret.", name, v, keysOf(nonSecretEnvExampleVars))
		}
	}
}

func keysOf(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// parseDotenv reads a .env file the way Compose does for the purposes of this
// test: NAME=value lines, comments and blanks ignored. Commented-out examples
// are not assignments and are deliberately not returned.
func parseDotenv(t *testing.T, path string) map[string]string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	out := map[string]string{}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(strings.TrimSuffix(line, "\r"))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		name, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		out[strings.TrimSpace(name)] = strings.Trim(strings.TrimSpace(value), `"'`)
	}
	return out
}

// The example must not hand out a working ingest key either: whoever holds one
// can create events, and through them wake every configured channel. Keys are
// optional (the admin token already grants full access), so the example shows
// them commented out.
func TestExampleConfigShipsNoAPIKeys(t *testing.T) {
	t.Setenv("ALERTLOOP_ADMIN_TOKEN", "a-real-token")

	cfg, err := Load(examplePath())
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(cfg.APIKeys) != 0 {
		t.Fatalf("the shipped example configures %d API key(s): %+v", len(cfg.APIKeys), cfg.APIKeys)
	}
}

// The example logs to stdout unless a file is asked for. The Compose postgres
// profile relies on the reference: it gives the api and the worker separate
// files without a second config file.
func TestExampleConfigLogsToStdoutUnlessGivenAFile(t *testing.T) {
	t.Setenv("ALERTLOOP_ADMIN_TOKEN", "a-real-token")

	cfg, err := Load(examplePath())
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Log.File != "" {
		t.Fatalf("log.file = %q with ALERTLOOP_LOG_FILE unset, want stdout only", cfg.Log.File)
	}

	t.Setenv("ALERTLOOP_LOG_FILE", "/var/log/alertloop/worker.log")
	cfg, err = Load(examplePath())
	if err != nil {
		t.Fatalf("load with a log file: %v", err)
	}
	if cfg.Log.File != "/var/log/alertloop/worker.log" {
		t.Fatalf("log.file = %q, want the value from the environment", cfg.Log.File)
	}
}

package config

import (
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// The YAML file is the single source of configuration. The environment is not a
// parallel configuration layer: its only job is to keep secrets out of the file,
// by filling in ${VAR} references found in it.
//
// Substitution replaces the WHOLE value or nothing: a scalar equal to exactly
// "${VAR}" or "${VAR:-default}" is replaced. Interpolation inside a longer
// string is deliberately unsupported — supporting it would require escaping
// rules, and an SMTP password containing a literal "$" would be silently eaten.
// The default may not contain "}": a greedy match turned "${A:-tok}${B}" —
// two references, which the whole-value rule says to leave alone — into the
// single default "tok}${B".
var envRef = regexp.MustCompile(`^\$\{([A-Za-z_][A-Za-z0-9_]*)(?::-([^}]*))?\}$`)

// substituteEnv walks a parsed YAML tree and resolves every ${VAR} scalar in
// place. Names that were resolved are recorded in seen (the caller uses them to
// tell a referenced variable from a leftover pre-0.3.0 one). A reference with no
// value and no default is an error: an empty admin_token caused by a typo in a
// variable name would leave the API open.
func substituteEnv(n *yaml.Node, seen map[string]bool) error {
	var missing []string
	var walk func(*yaml.Node)
	walk = func(n *yaml.Node) {
		if n == nil {
			return
		}
		if n.Kind == yaml.ScalarNode {
			// Submatch indices, not strings: they distinguish "${VAR}" (no
			// default) from "${VAR:-}" (an explicitly empty default).
			loc := envRef.FindStringSubmatchIndex(n.Value)
			if loc == nil {
				return
			}
			name := n.Value[loc[2]:loc[3]]
			seen[name] = true
			switch v, ok := os.LookupEnv(name); {
			case ok && v != "":
				n.Value = v
			case loc[4] >= 0: // ${VAR:-default}
				n.Value = n.Value[loc[4]:loc[5]]
			default:
				missing = append(missing, name)
				return
			}
			// Clear the tag so the value resolves to its natural type: forcing
			// !!str here made ${VAR} unusable in every non-string field
			// (retention_days, worker.*, rate_limit.*, an SMTP port). Nothing is
			// re-parsed as YAML either way — the value lands in an already
			// parsed scalar node, so a password containing ": " or "#" cannot
			// turn into a mapping or a comment.
			n.Tag = ""
			n.Style = 0
			return
		}
		for _, c := range n.Content {
			walk(c)
		}
	}
	walk(n)

	if len(missing) > 0 {
		sort.Strings(missing)
		return fmt.Errorf("config references environment variable(s) that are unset or empty and have no default: %s "+
			"(set them, or write ${VAR:-default} to allow a fallback)", strings.Join(dedupe(missing), ", "))
	}
	return nil
}

// legacyVar is a pre-0.3.0 configuration variable: the config field it used to
// control, and the line to write in the file instead.
type legacyVar struct {
	field string
	hint  string
}

// legacyEnvVars are the pre-0.3.0 configuration variables. They are gone, and a
// leftover one is refused rather than ignored: silently dropping a stale
// ALERTLOOP_DB_DSN would let a process run on SQLite while its operator is
// certain it runs on PostgreSQL — the 0.1.1 failure, in reverse.
var legacyEnvVars = map[string]legacyVar{
	"ALERTLOOP_ADDR":                {"addr", `addr: "..."`},
	"ALERTLOOP_ADMIN_TOKEN":         {"admin_token", `admin_token: ${ALERTLOOP_ADMIN_TOKEN}`},
	"ALERTLOOP_DB_DRIVER":           {"database.driver", `database.driver: "..."`},
	"ALERTLOOP_DB_DSN":              {"database.dsn", `database.dsn: ${ALERTLOOP_DB_DSN}`},
	"ALERTLOOP_RETENTION_DAYS":      {"retention_days", `retention_days: 30`},
	"ALERTLOOP_LOG_LEVEL":           {"log.level", `log.level: "info"`},
	"ALERTLOOP_LOG_FORMAT":          {"log.format", `log.format: "text"`},
	"ALERTLOOP_LOG_FILE":            {"log.file", `log.file: "..."`},
	"ALERTLOOP_CORS_ORIGINS":        {"cors_origins", `cors_origins: ["..."]`},
	"ALERTLOOP_WORKER_CONCURRENCY":  {"worker.concurrency", `worker.concurrency: 2`},
	"ALERTLOOP_WORKER_MAX_ATTEMPTS": {"worker.max_attempts", `worker.max_attempts: 5`},
	"ALERTLOOP_RATELIMIT_ENABLED":   {"rate_limit.enabled", `rate_limit.enabled: true`},
}

// checkLegacyEnv reports what to do about pre-0.3.0 variables still present in
// the environment.
//
// A variable is REFUSED when nothing else supplies its setting: the operator
// believes it is in effect, and starting anyway is how a process ends up on a
// database nobody chose. It is only WARNED about when the config file sets the
// same field itself — the file wins, the outcome is unambiguous, and refusing
// there would block a perfectly correct configuration (mounting your own file
// into a container that still exports the variable).
//
// A variable the file references via ${VAR} is a secret being injected, which
// is the supported use. An empty value counts as unset: Compose blanks
// variables (FOO: ${FOO:-}) as a way of clearing them.
func checkLegacyEnv(referenced, present map[string]bool) ([]string, error) {
	var refused, shadowed []string
	for name, lv := range legacyEnvVars {
		if referenced[name] {
			continue
		}
		if v, ok := os.LookupEnv(name); !ok || v == "" {
			continue
		}
		if present[lv.field] {
			shadowed = append(shadowed, name)
			continue
		}
		refused = append(refused, name)
	}
	sort.Strings(refused)
	sort.Strings(shadowed)

	var warnings []string
	for _, name := range shadowed {
		warnings = append(warnings, fmt.Sprintf(
			"%s is set but no longer configures anything; %s from the config file is what runs",
			name, legacyEnvVars[name].field))
	}
	if len(refused) == 0 {
		return warnings, nil
	}

	var b strings.Builder
	b.WriteString("these environment variables no longer configure AlertLoop (0.3.0 made the YAML file the single source):\n")
	for _, name := range refused {
		fmt.Fprintf(&b, "  %s — write it in the config file instead:  %s\n", name, legacyEnvVars[name].hint)
	}
	b.WriteString("Refusing to start rather than ignoring them, so the process cannot run on settings you believe are in effect. " +
		"Unset them once the config file carries the values.")
	return warnings, fmt.Errorf("%s", b.String())
}

// collectFields records the dotted paths a config file actually sets, so a
// leftover variable can be told from one the file overrides.
func collectFields(n *yaml.Node) map[string]bool {
	out := map[string]bool{}
	var walk func(*yaml.Node, string)
	walk = func(n *yaml.Node, prefix string) {
		if n == nil {
			return
		}
		switch n.Kind {
		case yaml.DocumentNode:
			for _, c := range n.Content {
				walk(c, prefix)
			}
		case yaml.MappingNode:
			for i := 0; i+1 < len(n.Content); i += 2 {
				key, value := n.Content[i], n.Content[i+1]
				path := key.Value
				if prefix != "" {
					path = prefix + "." + key.Value
				}
				out[path] = true
				walk(value, path)
			}
		}
	}
	walk(n, "")
	return out
}

func dedupe(in []string) []string {
	seen := map[string]bool{}
	out := in[:0]
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

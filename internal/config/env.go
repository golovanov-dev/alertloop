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
var envRef = regexp.MustCompile(`^\$\{([A-Za-z_][A-Za-z0-9_]*)(?::-(.*))?\}$`)

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
			// A substituted value is data, never YAML to re-parse: a password
			// containing ": " or "#" must not become a mapping or a comment.
			n.Tag = "!!str"
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
		return fmt.Errorf("config references environment variable(s) that are not set and have no default: %s "+
			"(set them, or write ${VAR:-default} to allow a fallback)", strings.Join(dedupe(missing), ", "))
	}
	return nil
}

// legacyEnvVars are the pre-0.3.0 configuration variables. They are gone, and a
// leftover one is refused rather than ignored: silently dropping a stale
// ALERTLOOP_DB_DSN would let a process run on SQLite while its operator is
// certain it runs on PostgreSQL — the 0.1.1 failure, in reverse.
var legacyEnvVars = map[string]string{
	"ALERTLOOP_ADDR":                `addr: "..."`,
	"ALERTLOOP_ADMIN_TOKEN":         `admin_token: ${ALERTLOOP_ADMIN_TOKEN}`,
	"ALERTLOOP_DB_DRIVER":           `database.driver: "..."`,
	"ALERTLOOP_DB_DSN":              `database.dsn: ${ALERTLOOP_DB_DSN}`,
	"ALERTLOOP_RETENTION_DAYS":      `retention_days: 30`,
	"ALERTLOOP_LOG_LEVEL":           `log.level: "info"`,
	"ALERTLOOP_LOG_FORMAT":          `log.format: "text"`,
	"ALERTLOOP_LOG_FILE":            `log.file: "..."`,
	"ALERTLOOP_CORS_ORIGINS":        `cors_origins: ["..."]`,
	"ALERTLOOP_WORKER_CONCURRENCY":  `worker.concurrency: 2`,
	"ALERTLOOP_WORKER_MAX_ATTEMPTS": `worker.max_attempts: 5`,
	"ALERTLOOP_RATELIMIT_ENABLED":   `rate_limit.enabled: true`,
}

// checkLegacyEnv fails when a pre-0.3.0 variable is set in the environment and
// the config file does not reference it. A variable the file does reference via
// ${VAR} is a secret being injected, which is the supported use.
//
// An empty value counts as unset: Docker Compose blanks variables
// (FOO: ${FOO:-}) as a way of clearing them, and that is not a leftover.
func checkLegacyEnv(referenced map[string]bool) error {
	var found []string
	for name := range legacyEnvVars {
		if referenced[name] {
			continue
		}
		if v, ok := os.LookupEnv(name); ok && v != "" {
			found = append(found, name)
		}
	}
	if len(found) == 0 {
		return nil
	}
	sort.Strings(found)

	var b strings.Builder
	b.WriteString("these environment variables no longer configure AlertLoop (0.3.0 made the YAML file the single source):\n")
	for _, name := range found {
		fmt.Fprintf(&b, "  %s — write it in the config file instead:  %s\n", name, legacyEnvVars[name])
	}
	b.WriteString("Refusing to start rather than ignoring them, so the process cannot run on settings you believe are in effect. " +
		"Unset them once the config file carries the values.")
	return fmt.Errorf("%s", b.String())
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

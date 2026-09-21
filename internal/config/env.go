package config

import (
	"fmt"
	"os"
	"reflect"
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

// noFallbackField is the one field a ${VAR:-default} must never be offered
// for. An empty admin_token with no api_keys leaves the JSON API open, so the
// shipped example forbids a fallback there in so many words; the loader must
// not advise the opposite.
const noFallbackField = "admin_token"

// substituteEnv walks a parsed YAML tree and resolves every ${VAR} scalar VALUE
// in place. Names that were resolved are recorded in seen (the caller uses them
// to tell a referenced variable from a leftover pre-0.3.0 one). A reference with
// no value and no default is an error naming the field and the variable: an
// empty admin_token caused by a typo in a variable name would leave the API
// open.
//
// Values only. A key is not a place to inject a secret: substitution is defined
// over "a value equal to exactly ${VAR}", and a reference standing in a key
// position is judged as the key it literally is.
//
// The returned warnings report the fallbacks that actually fired: see
// fallbackWarning.
func substituteEnv(n *yaml.Node, seen map[string]bool) ([]string, error) {
	type ref struct {
		field, name string
		line        int
	}
	var missing []ref
	var warnings []string

	var walk func(n *yaml.Node, path string)
	walk = func(n *yaml.Node, path string) {
		if n == nil {
			return
		}
		switch n.Kind {
		case yaml.ScalarNode:
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
				resolveScalar(n, v, true)
			case loc[4] >= 0: // ${VAR:-default}
				def := n.Value[loc[4]:loc[5]]
				if ok && def != "" {
					warnings = append(warnings, fallbackWarning(path, name, n.Line))
				}
				resolveScalar(n, def, false)
			default:
				missing = append(missing, ref{field: path, name: name, line: n.Line})
			}
		case yaml.MappingNode:
			for i := 0; i+1 < len(n.Content); i += 2 {
				key, value := n.Content[i], n.Content[i+1]
				if key.Tag == "!!merge" {
					// The merged node is substituted where it is defined.
					continue
				}
				name, _ := keyName(key)
				walk(value, joinPath(path, name))
			}
		default:
			// Documents and sequences carry no name of their own; a list
			// element is named by the field holding the list, as it is
			// everywhere else in these messages.
			for _, c := range n.Content {
				walk(c, path)
			}
		}
	}
	walk(n, "")

	if len(missing) == 0 {
		return warnings, nil
	}
	sort.SliceStable(missing, func(i, j int) bool { return missing[i].line < missing[j].line })

	var b strings.Builder
	b.WriteString("config references environment variable(s) that are unset or empty and have no default:\n")
	mayFallBack, noFallback := false, false
	for _, r := range missing {
		switch {
		case r.field == "":
			fmt.Fprintf(&b, "  line %d: ${%s}\n", r.line, r.name)
		default:
			fmt.Fprintf(&b, "  line %d: %s — ${%s}\n", r.line, r.field, r.name)
		}
		if r.field == noFallbackField {
			noFallback = true
		} else {
			mayFallBack = true
		}
	}
	if mayFallBack {
		b.WriteString("Set them in the environment, or write ${VAR:-default} in the config file to allow a fallback.")
	}
	if noFallback {
		if mayFallBack {
			b.WriteString("\n")
		}
		b.WriteString("admin_token has no fallback: put a value (openssl rand -hex 32) in the environment " +
			"this process reads.")
	}
	return warnings, fmt.Errorf("%s", b.String())
}

// resolveScalar puts a resolved value into an already parsed scalar node.
//
// The tag is cleared so the value resolves to its natural type: forcing !!str
// made ${VAR} unusable in every non-string field (retention_days, worker.*,
// rate_limit.*, an SMTP port). Nothing is re-parsed as YAML either way — the
// value lands in an already parsed node, so a password containing ": " or "#"
// cannot turn into a mapping or a comment.
func resolveScalar(n *yaml.Node, v string, fromVariable bool) {
	n.Value = v
	n.Tag = ""
	n.Style = 0
	if fromVariable && isYAMLNull(v) {
		// The variable held text, so the field must get text. Left unpinned,
		// YAML's four spellings of "no value" ERASED the field instead of
		// filling it: ${ALERTLOOP_ADMIN_TOKEN} with the value "null" produced
		// an empty admin_token and an open API — the outcome the
		// missing-variable check exists to prevent. Tools reach that value
		// without trying: `jq -r` on a key it cannot find prints "null", and so
		// do most template engines. Pinning !!str keeps the text in string
		// fields and turns it into a type error in the others, instead of
		// silently falling back to a built-in default.
		n.Tag = "!!str"
	}
}

// isYAMLNull reports whether s is one of YAML's spellings of "no value".
//
// The empty string is deliberately not one of them: "${VAR:-}" means "fall back
// to nothing", which is how log.file is switched off, and that must keep
// resolving to null.
//
// Only a value that came from a VARIABLE is pinned as text. A default written
// in the file (${VAR:-~}) is the config author's own YAML, not an injected
// secret: they wrote "~" to mean "no value", and reading it as the filename "~"
// would answer a question nobody asked.
func isYAMLNull(s string) bool {
	switch s {
	case "~", "null", "Null", "NULL":
		return true
	}
	return false
}

// fallbackWarning says that a ${VAR:-default} fell back although the variable
// was there: it arrived EMPTY, so the value in effect is the one written in the
// file, not the one whoever set the variable had in mind.
//
// ALERTLOOP_DB_DSN="" with dsn: ${ALERTLOOP_DB_DSN:-alertloop.db} in the file
// starts a healthy process on a local SQLite file while its operator is certain
// it runs on PostgreSQL — the 0.1.1 failure in reverse, and Compose blanks a
// variable it has no value for as a matter of course.
//
// The VALUE is never printed, only where to look: field, variable and line. The
// first version of this warning printed it and put a proxy password and a
// webhook URL with a token in it straight into the log, because it decided what
// was secret from a list of field names. Any such list goes stale exactly as
// the hand-written list of config keys would have — and a log line is the one
// place where being late costs a leaked credential. SECURITY.md promises
// secrets never reach the log; this keeps that true by construction.
//
// Two cases deliberately stay quiet, because a line printed on every normal
// startup is a line nobody reads by the time it matters. A variable that is
// simply absent says nothing was ever arranged for it, and the fallback in the
// file is then the configuration as written — that is how the shipped example
// runs on SQLite. An empty default (${VAR:-}) says nothing either: that is how
// the same example switches log.file off.
func fallbackWarning(field, name string, line int) string {
	if field == "" {
		field = "a value in the config file"
	}
	return fmt.Sprintf("line %d: %s — %s is set but empty, so the fallback written in the config file is "+
		"what runs (open the file at that line to see it)", line, field, name)
}

// joinPath appends one key name to a dotted field path.
func joinPath(prefix, name string) string {
	if prefix == "" {
		return name
	}
	return prefix + "." + name
}

// legacyEnvVars maps each pre-0.3.0 configuration variable to the name of the
// config file setting that replaced it. The variables are gone, and a
// leftover one is refused rather than ignored: silently dropping a stale
// ALERTLOOP_DB_DSN would let a process run on SQLite while its operator is
// certain it runs on PostgreSQL — the 0.1.1 failure, in reverse.
var legacyEnvVars = map[string]string{
	"ALERTLOOP_ADDR":                "addr",
	"ALERTLOOP_ADMIN_TOKEN":         "admin_token",
	"ALERTLOOP_DB_DRIVER":           "database.driver",
	"ALERTLOOP_DB_DSN":              "database.dsn",
	"ALERTLOOP_RETENTION_DAYS":      "retention_days",
	"ALERTLOOP_LOG_LEVEL":           "log.level",
	"ALERTLOOP_LOG_FORMAT":          "log.format",
	"ALERTLOOP_LOG_FILE":            "log.file",
	"ALERTLOOP_CORS_ORIGINS":        "cors_origins",
	"ALERTLOOP_WORKER_CONCURRENCY":  "worker.concurrency",
	"ALERTLOOP_WORKER_MAX_ATTEMPTS": "worker.max_attempts",
	"ALERTLOOP_RATELIMIT_ENABLED":   "rate_limit.enabled",
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
	for name, setting := range legacyEnvVars {
		if referenced[name] {
			continue
		}
		if v, ok := os.LookupEnv(name); !ok || v == "" {
			continue
		}
		if present[setting] {
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
			name, legacyEnvVars[name]))
	}
	if len(refused) == 0 {
		return warnings, nil
	}

	var b strings.Builder
	b.WriteString("these environment variables no longer configure AlertLoop (0.3.0 made the YAML file the single source):\n")
	for _, name := range refused {
		fmt.Fprintf(&b, "  %s — replaced by the %s setting in the config file\n", name, legacyEnvVars[name])
	}
	b.WriteString("Refusing to start rather than ignoring them, so the process cannot run on settings you believe are in effect. " +
		"Unset each variable once the config file carries its setting.")
	return warnings, fmt.Errorf("%s", b.String())
}

// walkKeys calls visit for every key/value pair in a parsed config file, with
// its dotted path ("rate_limit.trusted_proxies", "channels.email.host"); visit
// reports whether the value should be descended into. Elements of a list carry
// no index — a list of structs has one shape, and both callers care about the
// shape rather than the position. visit receives plain=false when some segment
// of the path is not a plain key name, so the dotted string does not mean what
// it looks like.
//
// One walk, used by both readers of the tree (collectFields and
// checkUnknownKeys): two walks written separately would drift, and the one that
// drifted would be the one deciding whether a key is known.
//
// It carries no budget of its own, because it runs AFTER the document has been
// decoded. yaml.v3 bounds alias expansion at Decode, so a file whose anchors
// blow up never reaches here; and descent stops at every path the schema does
// not know, while known paths are finitely deep — an anchor that leads back
// into itself lengthens the path until it is no longer a known one.
func walkKeys(doc *yaml.Node, visit func(path string, key, value *yaml.Node, plain bool) bool) {
	var walk func(n *yaml.Node, prefix string, plain bool)
	walk = func(n *yaml.Node, prefix string, plain bool) {
		if n == nil {
			return
		}
		switch n.Kind {
		case yaml.DocumentNode, yaml.SequenceNode:
			for _, c := range n.Content {
				walk(c, prefix, plain)
			}
		case yaml.AliasNode:
			walk(n.Alias, prefix, plain)
		case yaml.MappingNode:
			for i := 0; i+1 < len(n.Content); i += 2 {
				key, value := n.Content[i], n.Content[i+1]
				// A merge key ("<<: *defaults") is not a setting: its content
				// belongs to the mapping doing the merging, so it is walked at
				// this level rather than under a "<<" segment.
				if key.Tag == "!!merge" {
					walk(value, prefix, plain)
					continue
				}
				name, keyPlain := keyName(key)
				path, keysPlain := joinPath(prefix, name), plain && keyPlain
				if visit(path, key, value, keysPlain) {
					walk(value, path, keysPlain)
				}
			}
		}
	}
	walk(doc, "", true)
}

// keyName is the text of a mapping key and whether it is a plain key name.
//
// A key containing a dot is ONE name, however much it looks like a path:
// "log.level: debug" in the root of the file sets nothing, because there is no
// key called "log.level" — the setting is two lines deep. Such a key is
// therefore never "known", and the caller says so instead of accepting a line
// that would have no effect. A key that is not a plain scalar, or is empty, has
// no name to print, and printing nothing left the operator with "line 4:" and
// no idea which line to fix.
func keyName(n *yaml.Node) (name string, plain bool) {
	switch {
	case n.Kind != yaml.ScalarNode:
		return "<non-scalar key>", false
	case n.Value == "":
		return `""`, false
	case strings.Contains(n.Value, "."):
		return n.Value, false
	}
	return n.Value, true
}

// schema is what the Config types say a file may contain, derived once per
// load and shared by both readers of the tree.
type schema struct {
	known      map[string]bool // every path yaml.v3 can decode
	containers map[string]bool // the ones with keys below them
	freeform   map[string]bool // prefixes under which any key is allowed
}

func configSchema() schema {
	known, containers, freeform := knownYAMLPaths(reflect.TypeOf(Config{}))
	return schema{known: known, containers: containers, freeform: freeform}
}

// descend reports whether a walk should look below this key. Both walks use
// this one rule, so neither can see more of the file than the other.
func (s schema) descend(path string, plain bool) bool {
	// A top-level "x-" key holds YAML anchors and nothing else — the convention
	// Compose uses for the same purpose. It is the only way to declare a
	// reusable value, and there is no AlertLoop setting it could be a
	// misspelling of.
	if strings.HasPrefix(path, "x-") || isFreeform(path, s.freeform) {
		return false
	}
	// Below a scalar field there is nothing to find: "addr: {port: 1}" is a
	// type error, and the type error says far more than "unknown key addr.port"
	// would. An unknown or non-plain key is a mistake reported once, not once
	// per line under it.
	return plain && s.known[path] && s.containers[path]
}

// collectFields records the dotted paths a config file actually sets, so a
// leftover variable can be told from one the file overrides. A path that is not
// plain is not recorded: "log.level: x" in the root does not set log.level, and
// treating it as if it did would turn a refused leftover variable into a
// warning.
func collectFields(n *yaml.Node, s schema) map[string]bool {
	out := map[string]bool{}
	walkKeys(n, func(path string, _, _ *yaml.Node, plain bool) bool {
		if plain {
			out[path] = true
		}
		return s.descend(path, plain)
	})
	return out
}

// --- unknown keys ---------------------------------------------------------

// checkUnknownKeys refuses a config file that contains a key AlertLoop does not
// read.
//
// Ignoring such a key is how "retention_day: 5" runs on the built-in 30 days
// without a word, and how a "trusted_proxy" list leaves every client behind a
// proxy sharing one rate-limit bucket. The operator wrote the line, so the
// setting is in effect as far as they are concerned; the process disagreeing in
// silence is the failure this exists to prevent.
//
// Every unknown key in the file is reported at once, with its full path and the
// line it sits on: fixing typos one restart at a time is the same information
// delivered worse. maxReportedKeys bounds the list, so a generated file full of
// unknown paths cannot answer with a megabyte of them.
const maxReportedKeys = 50

func checkUnknownKeys(doc *yaml.Node, s schema) error {
	type unknown struct {
		path, hint string
		line       int
		dotted     bool
	}
	var found []unknown

	walkKeys(doc, func(path string, key, value *yaml.Node, plain bool) bool {
		if s.descend(path, plain) {
			return true
		}
		if strings.HasPrefix(path, "x-") || isFreeform(path, s.freeform) {
			return false
		}
		if plain && s.known[path] {
			return false // a known scalar field: nothing below it to judge
		}
		u := unknown{path: path, line: key.Line}
		if plain {
			u.hint = suggestKey(path, s.known)
		} else {
			// "log.level: debug" is one key name, not a path: there is no such
			// key, so the line sets nothing. Saying that is the whole advice —
			// the value is the operator's own text and this goes to a log.
			u.dotted = key.Kind == yaml.ScalarNode && strings.Contains(key.Value, ".")
		}
		found = append(found, u)
		return false
	})
	if len(found) == 0 {
		return nil
	}

	var b strings.Builder
	b.WriteString("these keys in the config file are not AlertLoop settings:\n")
	for i, u := range found {
		// The list is for reading. Past a screenful the operator is looking at
		// a file with a different problem, and the remaining lines only bury
		// the first one.
		if i == maxReportedKeys {
			fmt.Fprintf(&b, "  ... and %d more\n", len(found)-maxReportedKeys)
			break
		}
		fmt.Fprintf(&b, "  line %d: %s", u.line, u.path)
		switch {
		case u.hint != "":
			fmt.Fprintf(&b, " — did you mean %s?", u.hint)
		case u.dotted:
			b.WriteString(" — a key name is not a path; write one key per line, nested")
		}
		b.WriteString("\n")
	}
	b.WriteString("Correct the spelling or remove the key.")
	return fmt.Errorf("%s", b.String())
}

// knownYAMLPaths derives, from the Config types themselves, the dotted paths
// yaml.v3 can decode: known is every path, containers are the ones with keys
// below them, and freeform are the prefixes under which any key is allowed (a
// map or an interface field — the config has none today).
//
// Derived by reflection rather than written out as a list on purpose: a
// hand-kept list goes stale the first time a field is added, the config file
// that uses the new field is then refused, and no test in this package would
// notice — they all load files the list already knows about. The name each
// field decodes from is computed exactly as gopkg.in/yaml.v3 computes it (tag,
// or the lower-cased field name; ",inline" flattens; a `yaml:"-"` field is not
// a key at all, which is what makes "warnings:" in a file unknown rather than
// accepted).
func knownYAMLPaths(t reflect.Type) (known, containers, freeform map[string]bool) {
	known, containers, freeform = map[string]bool{}, map[string]bool{}, map[string]bool{}

	yamlUnmarshaler := reflect.TypeOf((*yaml.Unmarshaler)(nil)).Elem()

	// Two cases this mirroring does NOT cover, because Config has no such field
	// today and inventing the handling blind would be worse than the note: a
	// `yaml:",inline"` MAP at the top level (yaml.v3 accepts one; here the
	// freeform prefix would be empty and every key in the file would come out
	// unknown), and `,inline` on a slice (yaml.v3 errors, this walks the element
	// type). Whoever adds a free-form section adds a test for it here.
	var expand func(t reflect.Type, prefix string, open map[reflect.Type]bool)
	expand = func(t reflect.Type, prefix string, open map[reflect.Type]bool) {
		for t.Kind() == reflect.Pointer || t.Kind() == reflect.Slice || t.Kind() == reflect.Array {
			t = t.Elem()
		}
		switch {
		case t.Kind() == reflect.Map, t.Kind() == reflect.Interface:
			// The keys below are the operator's to choose, so none of them can
			// be called unknown.
			if prefix != "" {
				freeform[prefix] = true
			}
			return
		case t.Kind() != reflect.Struct:
			return
		case reflect.PointerTo(t).Implements(yamlUnmarshaler):
			// The type parses itself; its Go fields say nothing about the keys
			// it accepts.
			if prefix != "" {
				freeform[prefix] = true
			}
			return
		case open[t]: // a self-referential type would recurse forever
			return
		}
		open[t] = true
		defer delete(open, t)

		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			if f.PkgPath != "" && !f.Anonymous {
				continue // unexported: yaml.v3 skips it too
			}
			name, inline, ok := yamlFieldName(f)
			if !ok {
				continue // yaml:"-"
			}
			if inline {
				expand(f.Type, prefix, open)
				continue
			}
			path := name
			if prefix != "" {
				path = prefix + "." + name
				containers[prefix] = true
			}
			known[path] = true
			expand(f.Type, path, open)
		}
	}
	expand(t, "", map[reflect.Type]bool{})
	return known, containers, freeform
}

// yamlFieldName reports the key a struct field decodes from, whether it is
// inlined, and whether it is a key at all. It mirrors getStructInfo in
// gopkg.in/yaml.v3: matching there is exact, so this must not guess.
func yamlFieldName(f reflect.StructField) (name string, inline, ok bool) {
	tag := f.Tag.Get("yaml")
	if tag == "" && !strings.Contains(string(f.Tag), ":") {
		tag = string(f.Tag)
	}
	if tag == "-" {
		return "", false, false
	}
	parts := strings.Split(tag, ",")
	for _, opt := range parts[1:] {
		if opt == "inline" {
			inline = true
		}
	}
	name = parts[0]
	if name == "" {
		name = strings.ToLower(f.Name)
	}
	return name, inline, true
}

// isFreeform reports whether path lies inside a section whose keys are not
// AlertLoop's to know.
func isFreeform(path string, freeform map[string]bool) bool {
	for p := range freeform {
		if path == p || strings.HasPrefix(path, p+".") {
			return true
		}
	}
	return false
}

// suggestKey finds the known key an unknown one was probably meant to be. It
// looks first among the keys valid in the same place (a misspelling), then
// anywhere in the file's shape (the right name in the wrong section, such as
// trusted_proxies written at the top level). An empty result means no guess is
// worth printing: a confident wrong guess costs more than no guess.
func suggestKey(path string, known map[string]bool) string {
	if best := suggestNearby(path, known); best != "" {
		return best
	}

	// The same name somewhere else: the operator knows the setting, not where
	// it belongs. A weak guess, offered as a question ("did you mean …?") and
	// never as an instruction the operator could follow without reading it.
	_, name := splitPath(path)
	for _, k := range sortedKeys(known) {
		if _, n := splitPath(k); n == name {
			return k
		}
	}
	return ""
}

// editBudget is how far a guess may reach, scaled to the length of the name.
// Two edits on a short key already land on an unrelated setting ("tls" is two
// edits from "to"), while a long one needs room: "trusted_proxy" is three edits
// from "trusted_proxies", and that is the misspelling this whole check was
// asked for.
func editBudget(name string) int {
	switch budget := len(name) / 4; {
	case budget < 1:
		return 1
	case budget > 3:
		return 3
	default:
		return budget
	}
}

// suggestNearby is the confident half of suggestKey: a key valid in the same
// place as the one written, within a small edit distance of it. This is the
// misspelling case, where the operator was aiming at a setting that belongs
// exactly where they wrote it.
func suggestNearby(path string, known map[string]bool) string {
	parent, name := splitPath(path)

	best, bestDist := "", editBudget(name)+1
	for _, k := range sortedKeys(known) {
		p, n := splitPath(k)
		if p != parent {
			continue
		}
		if d := editDistance(name, n); d < bestDist {
			best, bestDist = k, d
		}
	}
	return best
}

func splitPath(path string) (parent, name string) {
	if i := strings.LastIndexByte(path, '.'); i >= 0 {
		return path[:i], path[i+1:]
	}
	return "", path
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// editDistance is the optimal string alignment (Damerau-Levenshtein) distance
// between a and b, used only to decide whether one key is a plausible
// misspelling of another.
//
// Swapping two neighbours costs ONE edit, not two. Plain Levenshtein charges
// two for it, which is over the budget for a short key, and so "tsl" for "tls"
// and "ot" for "to" — the commonest typo there is — got no suggestion at all,
// while the message and the upgrade notes promise one.
func editDistance(a, b string) int {
	// Three rows: the transposition case needs the row before last.
	prev2 := make([]int, len(b)+1)
	prev := make([]int, len(b)+1)
	cur := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, min(cur[j-1]+1, prev[j-1]+cost))
			if i > 1 && j > 1 && a[i-1] == b[j-2] && a[i-2] == b[j-1] {
				cur[j] = min(cur[j], prev2[j-2]+1)
			}
		}
		prev2, prev, cur = prev, cur, prev2
	}
	return prev[len(b)]
}

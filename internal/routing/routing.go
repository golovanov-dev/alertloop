// Package routing decides which configured channels an event is delivered to.
//
// It is a plain top-to-bottom match of an event against a list of rules: the
// first matching rule wins and its channel list is used as-is. Channels from
// several rules are never merged, there are no regular expressions, and there
// is no plug-in policy mechanism — an operator reading the config top-down must
// be able to predict where an event goes, at three in the morning, without
// running it.
//
// When no routing section is configured the router is built in "all channels"
// mode, which reproduces the pre-0.2.0 behavior exactly. Ingestion therefore
// has no special case for unconfigured routing.
package routing

import (
	"fmt"
	"strings"

	"github.com/golovanov-dev/alertloop/internal/config"
	"github.com/golovanov-dev/alertloop/internal/domain"
)

// severityRank orders severities by how alarming they are, for min_severity.
// success deliberately shares info's rank: this is an order of alarm, not of
// importance, and a successful outcome is not more alarming than a notice.
var severityRank = map[string]int{
	"info":     10,
	"success":  10,
	"warning":  20,
	"error":    30,
	"critical": 40,
}

// Decision is the outcome of routing one event.
type Decision struct {
	// Rule is the name of the rule that matched; empty when none did.
	Rule string
	// Matched reports whether a rule matched. When false, Channels holds the
	// routing default — or every configured channel, when routing is not
	// configured at all.
	Matched bool
	// Channels are the delivery targets for this event. Empty means the event is
	// stored and delivered nowhere.
	Channels []domain.ChannelTarget
}

// Router matches events against the configured routing rules.
type Router struct {
	configured bool
	rules      []rule
	def        []domain.ChannelTarget
	defNames   []string
	views      []RuleView
	// known is every configured channel, kept for the startup diagnostics.
	known []domain.ChannelTarget
}

// rule is one compiled routing rule.
type rule struct {
	name     string
	match    matcher
	channels []domain.ChannelTarget
	names    []string
}

// NewAllChannels builds the router used when no routing section is configured:
// every event goes to every configured channel.
func NewAllChannels(targets []domain.ChannelTarget) *Router {
	return &Router{
		configured: false,
		def:        targets,
		defNames:   targetNames(targets),
		known:      targets,
	}
}

// New builds a router from a validated routing section. targets is the set of
// configured channels. An error here means the config never passed
// Config.Validate, which resolves channel names and rule conditions first.
func New(cfg config.Routing, targets []domain.ChannelTarget) (*Router, error) {
	byName := make(map[string]domain.ChannelTarget, len(targets))
	for _, t := range targets {
		byName[t.Name] = t
	}
	resolve := func(where string, names []string) ([]domain.ChannelTarget, []string, error) {
		out := make([]domain.ChannelTarget, 0, len(names))
		clean := make([]string, 0, len(names))
		for _, n := range names {
			name := strings.TrimSpace(n)
			t, ok := byName[name]
			if !ok {
				return nil, nil, fmt.Errorf("%s references unknown channel %q", where, n)
			}
			out = append(out, t)
			clean = append(clean, name)
		}
		return out, clean, nil
	}

	r := &Router{configured: true, known: targets}
	for _, rc := range cfg.Rules {
		name := strings.TrimSpace(rc.Name)
		chans, names, err := resolve(fmt.Sprintf("routing rule %q", name), rc.Channels)
		if err != nil {
			return nil, err
		}
		m, err := compile(rc.Match)
		if err != nil {
			return nil, fmt.Errorf("routing rule %q: %w", name, err)
		}
		r.rules = append(r.rules, rule{name: name, match: m, channels: chans, names: names})
		r.views = append(r.views, RuleView{
			Name:     name,
			Match:    matchView(rc.Match),
			Channels: names,
			CatchAll: m.catchAll(),
		})
	}
	def, defNames, err := resolve("routing default", cfg.Default)
	if err != nil {
		return nil, err
	}
	r.def, r.defNames = def, defNames
	return r, nil
}

// Route selects the channels for an event. Matching is linear in the number of
// rules and allocates nothing per rule.
func (r *Router) Route(e *domain.Event) Decision {
	v := eventValues{
		eventType: normalize(string(e.Type)),
		severity:  normalize(string(e.Severity)),
		source:    normalize(e.Source),
		category:  normalize(e.Category),
	}
	for i := range r.rules {
		if r.rules[i].match.matches(v) {
			return Decision{Rule: r.rules[i].name, Matched: true, Channels: r.rules[i].channels}
		}
	}
	return Decision{Channels: r.def}
}

// Configured reports whether a routing section was present. When false, every
// event is delivered to every configured channel.
func (r *Router) Configured() bool { return r.configured }

// Rules exposes the resolved routing table for the preview API and the startup
// log.
func (r *Router) Rules() []RuleView { return r.views }

// Default lists the channel names unmatched events are delivered to. With
// routing unconfigured this is every configured channel, which is where every
// event goes.
func (r *Router) Default() []string { return r.defNames }

// UnreachableRules reports the first catch-all rule that is not last, together
// with the rules below it that can therefore never match. Reported at startup:
// a rule below a catch-all is almost always an ordering mistake.
func (r *Router) UnreachableRules() (catchAll string, unreachable []string) {
	for i, rl := range r.rules {
		if !rl.match.catchAll() || i == len(r.rules)-1 {
			continue
		}
		for _, later := range r.rules[i+1:] {
			unreachable = append(unreachable, later.name)
		}
		return rl.name, unreachable
	}
	return "", nil
}

// UnusedChannels lists configured channels that no rule and no default can ever
// send to. A configured channel nothing routes to is nearly always a typo.
func (r *Router) UnusedChannels() []string {
	if !r.configured {
		return nil
	}
	used := make(map[string]bool, len(r.known))
	for _, n := range r.defNames {
		used[n] = true
	}
	for _, rl := range r.rules {
		for _, n := range rl.names {
			used[n] = true
		}
	}
	var out []string
	for _, t := range r.known {
		if !used[t.Name] {
			out = append(out, t.Name)
		}
	}
	return out
}

// RuleView describes one resolved rule for the routing preview API and the
// startup routing table.
type RuleView struct {
	Name     string    `json:"name"`
	Match    MatchView `json:"match"`
	Channels []string  `json:"channels"`
	// CatchAll reports that the rule has no conditions and matches everything.
	CatchAll bool `json:"catch_all"`
}

// MatchView is the rule's conditions as configured.
type MatchView struct {
	Type        []string `json:"type,omitempty"`
	Severity    []string `json:"severity,omitempty"`
	MinSeverity string   `json:"min_severity,omitempty"`
	Source      []string `json:"source,omitempty"`
	Category    []string `json:"category,omitempty"`
}

func matchView(m config.RoutingMatch) MatchView {
	return MatchView{
		Type:        trimAll(m.Type),
		Severity:    trimAll(m.Severity),
		MinSeverity: strings.TrimSpace(m.MinSeverity),
		Source:      trimAll(m.Source),
		Category:    trimAll(m.Category),
	}
}

// eventValues is an event reduced to its comparison form, computed once per
// event instead of once per rule.
type eventValues struct {
	eventType string
	severity  string
	source    string
	category  string
}

// matcher is a rule's compiled conditions. Values inside a field are OR-ed,
// fields are AND-ed, and an empty field constrains nothing.
type matcher struct {
	types       []string
	severities  []string
	minSeverity int
	sources     []pattern
	categories  []pattern
}

// pattern is a literal or trailing-wildcard match, compiled once at startup.
type pattern struct {
	value  string
	prefix bool
}

func compile(m config.RoutingMatch) (matcher, error) {
	out := matcher{
		types:      normalizeAll(m.Type),
		severities: normalizeAll(m.Severity),
	}
	if s := normalize(m.MinSeverity); s != "" {
		rank, ok := severityRank[s]
		if !ok {
			return matcher{}, fmt.Errorf("unknown min_severity %q", m.MinSeverity)
		}
		out.minSeverity = rank
	}
	var err error
	if out.sources, err = compilePatterns(m.Source); err != nil {
		return matcher{}, err
	}
	if out.categories, err = compilePatterns(m.Category); err != nil {
		return matcher{}, err
	}
	return out, nil
}

func compilePatterns(values []string) ([]pattern, error) {
	out := make([]pattern, 0, len(values))
	for _, v := range values {
		s := normalize(v)
		if i := strings.IndexByte(s, '*'); i >= 0 {
			if i != len(s)-1 {
				return nil, fmt.Errorf("only a trailing %q wildcard is supported, got %q", "*", v)
			}
			out = append(out, pattern{value: s[:len(s)-1], prefix: true})
			continue
		}
		out = append(out, pattern{value: s})
	}
	return out, nil
}

func (m matcher) matches(e eventValues) bool {
	if len(m.types) > 0 && !contains(m.types, e.eventType) {
		return false
	}
	if len(m.severities) > 0 && !contains(m.severities, e.severity) {
		return false
	}
	if m.minSeverity > 0 && severityRank[e.severity] < m.minSeverity {
		return false
	}
	if len(m.sources) > 0 && !matchesAny(m.sources, e.source) {
		return false
	}
	if len(m.categories) > 0 && !matchesAny(m.categories, e.category) {
		return false
	}
	return true
}

// catchAll reports that the rule has no conditions at all and therefore matches
// every event.
func (m matcher) catchAll() bool {
	return len(m.types) == 0 && len(m.severities) == 0 && m.minSeverity == 0 &&
		len(m.sources) == 0 && len(m.categories) == 0
}

func matchesAny(patterns []pattern, v string) bool {
	for _, p := range patterns {
		if p.prefix {
			if strings.HasPrefix(v, p.value) {
				return true
			}
			continue
		}
		if v == p.value {
			return true
		}
	}
	return false
}

func contains(values []string, v string) bool {
	for _, s := range values {
		if s == v {
			return true
		}
	}
	return false
}

// normalize is the comparison form of a value: trimmed and lower-cased, so
// " Order.Created " and "order.created" are the same value.
func normalize(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

func normalizeAll(values []string) []string {
	out := make([]string, 0, len(values))
	for _, v := range values {
		out = append(out, normalize(v))
	}
	return out
}

func trimAll(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	out := make([]string, 0, len(values))
	for _, v := range values {
		out = append(out, strings.TrimSpace(v))
	}
	return out
}

func targetNames(targets []domain.ChannelTarget) []string {
	out := make([]string, 0, len(targets))
	for _, t := range targets {
		out = append(out, t.Name)
	}
	return out
}

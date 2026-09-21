package routing

import (
	"strings"
	"testing"

	"github.com/golovanov-dev/alertloop/internal/config"
	"github.com/golovanov-dev/alertloop/internal/domain"
)

// testTargets is the channel set the rules in these tests refer to.
func testTargets() []domain.ChannelTarget {
	return []domain.ChannelTarget{
		{Type: domain.ChannelTelegram, Name: "dev-telegram"},
		{Type: domain.ChannelTelegram, Name: "customer-telegram"},
		{Type: domain.ChannelEmail, Name: "dev-email"},
	}
}

func newRouter(t *testing.T, cfg config.Routing) *Router {
	t.Helper()
	r, err := New(cfg, testTargets())
	if err != nil {
		t.Fatalf("build router: %v", err)
	}
	return r
}

func event(typ domain.EventType, sev domain.Severity, source, category string) *domain.Event {
	return &domain.Event{Type: typ, Severity: sev, Source: source, Category: category}
}

func names(targets []domain.ChannelTarget) []string {
	out := make([]string, 0, len(targets))
	for _, t := range targets {
		out = append(out, t.Name)
	}
	return out
}

func routed(t *testing.T, r *Router, e *domain.Event) (string, string) {
	t.Helper()
	d := r.Route(e)
	return d.Rule, strings.Join(names(d.Channels), ",")
}

// TestRouteMatchesEachFieldSeparately covers every supported condition on its
// own, then the AND between fields.
func TestRouteMatchesEachFieldSeparately(t *testing.T) {
	r := newRouter(t, config.Routing{
		Rules: []config.RoutingRule{
			{Name: "by-type", Match: config.RoutingMatch{Type: []string{"audit"}}, Channels: []string{"dev-email"}},
			{Name: "by-severity", Match: config.RoutingMatch{Severity: []string{"critical"}}, Channels: []string{"dev-telegram"}},
			{Name: "by-source", Match: config.RoutingMatch{Source: []string{"billing"}}, Channels: []string{"customer-telegram"}},
			{Name: "by-category", Match: config.RoutingMatch{Category: []string{"order.created"}}, Channels: []string{"customer-telegram"}},
			{Name: "type-and-category", Match: config.RoutingMatch{
				Type:     []string{"business_event"},
				Category: []string{"invoice.paid"},
			}, Channels: []string{"dev-email", "customer-telegram"}},
		},
		Default: []string{"dev-telegram"},
	})

	cases := []struct {
		name  string
		event *domain.Event
		rule  string
		chans string
	}{
		{"type", event(domain.EventAudit, domain.SeverityInfo, "s", ""), "by-type", "dev-email"},
		{"severity", event(domain.EventIncident, domain.SeverityCritical, "s", ""), "by-severity", "dev-telegram"},
		{"source", event(domain.EventIncident, domain.SeverityInfo, "billing", ""), "by-source", "customer-telegram"},
		{"category", event(domain.EventIncident, domain.SeverityInfo, "s", "order.created"), "by-category", "customer-telegram"},
		{"two fields", event(domain.EventBusiness, domain.SeverityInfo, "s", "invoice.paid"), "type-and-category", "dev-email,customer-telegram"},
		{"no match", event(domain.EventIncident, domain.SeverityInfo, "s", "other"), "", "dev-telegram"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rule, chans := routed(t, r, c.event)
			if rule != c.rule || chans != c.chans {
				t.Fatalf("routed to rule=%q channels=%q, want rule=%q channels=%q", rule, chans, c.rule, c.chans)
			}
		})
	}
}

// TestRouteOrWithinFieldAndBetweenFields is the core semantic of a rule: values
// inside one field are alternatives, fields are all required.
func TestRouteOrWithinFieldAndBetweenFields(t *testing.T) {
	r := newRouter(t, config.Routing{
		Rules: []config.RoutingRule{{
			Name: "either-type-and-warning-up",
			Match: config.RoutingMatch{
				Type:        []string{"incident", "audit"},
				MinSeverity: "warning",
			},
			Channels: []string{"dev-telegram"},
		}},
	})

	// OR inside `type`: both families match.
	for _, typ := range []domain.EventType{domain.EventIncident, domain.EventAudit} {
		if rule, _ := routed(t, r, event(typ, domain.SeverityError, "s", "")); rule == "" {
			t.Fatalf("type %q should have matched the rule", typ)
		}
	}
	// AND between fields: the right type but too low a severity does not match.
	if rule, _ := routed(t, r, event(domain.EventIncident, domain.SeverityInfo, "s", "")); rule != "" {
		t.Fatalf("info incident matched %q, want no match (min_severity is warning)", rule)
	}
	// AND between fields: high severity but the wrong family does not match.
	if rule, _ := routed(t, r, event(domain.EventBusiness, domain.SeverityCritical, "s", "")); rule != "" {
		t.Fatalf("business_event matched %q, want no match", rule)
	}
}

// TestMinSeverityRanks pins the documented alarm order, including the deliberate
// tie between info and success.
func TestMinSeverityRanks(t *testing.T) {
	cases := []struct {
		min   string
		sev   domain.Severity
		match bool
	}{
		{"info", domain.SeveritySuccess, true},
		{"success", domain.SeverityInfo, true}, // same rank: success is not "above" info
		{"warning", domain.SeverityInfo, false},
		{"warning", domain.SeverityWarning, true},
		{"error", domain.SeverityWarning, false},
		{"error", domain.SeverityCritical, true},
		{"critical", domain.SeverityError, false},
	}
	for _, c := range cases {
		r := newRouter(t, config.Routing{Rules: []config.RoutingRule{{
			Name:     "min",
			Match:    config.RoutingMatch{MinSeverity: c.min},
			Channels: []string{"dev-telegram"},
		}}})
		rule, _ := routed(t, r, event(domain.EventIncident, c.sev, "s", ""))
		if got := rule != ""; got != c.match {
			t.Errorf("min_severity=%s severity=%s matched=%v, want %v", c.min, c.sev, got, c.match)
		}
	}
}

func TestTrailingWildcardOnSourceAndCategory(t *testing.T) {
	r := newRouter(t, config.Routing{Rules: []config.RoutingRule{
		{Name: "orders", Match: config.RoutingMatch{Category: []string{"order.*"}}, Channels: []string{"customer-telegram"}},
		{Name: "workers", Match: config.RoutingMatch{Source: []string{"feeds_*"}}, Channels: []string{"dev-telegram"}},
	}})

	for _, c := range []struct{ category, rule string }{
		{"order.created", "orders"},
		{"order.", "orders"},
		{"orders.created", ""}, // the prefix is "order." — not a fuzzy match
		{"reorder.created", ""},
	} {
		if rule, _ := routed(t, r, event(domain.EventBusiness, domain.SeverityInfo, "s", c.category)); rule != c.rule {
			t.Errorf("category %q matched rule %q, want %q", c.category, rule, c.rule)
		}
	}
	if rule, _ := routed(t, r, event(domain.EventIncident, domain.SeverityInfo, "feeds_worker", "")); rule != "workers" {
		t.Errorf("source wildcard did not match, got rule %q", rule)
	}
}

// A "*" that is not the final character is a configuration error, not a literal.
func TestWildcardOnlyAllowedAtTheEnd(t *testing.T) {
	for _, bad := range []string{"*.created", "order.*.created", "or*der"} {
		_, err := New(config.Routing{Rules: []config.RoutingRule{{
			Name:     "bad",
			Match:    config.RoutingMatch{Category: []string{bad}},
			Channels: []string{"dev-telegram"},
		}}}, testTargets())
		if err == nil {
			t.Errorf("pattern %q was accepted, want a configuration error", bad)
		}
	}
}

func TestMatchingIgnoresCaseAndSurroundingSpace(t *testing.T) {
	r := newRouter(t, config.Routing{Rules: []config.RoutingRule{{
		Name:     "orders",
		Match:    config.RoutingMatch{Type: []string{" Business_Event "}, Category: []string{"Order.*"}},
		Channels: []string{"customer-telegram"},
	}}})
	if rule, _ := routed(t, r, event(domain.EventBusiness, domain.SeverityInfo, "s", " ORDER.created ")); rule != "orders" {
		t.Fatalf("case/space-insensitive matching failed, got rule %q", rule)
	}
}

// TestFirstMatchWins is the predictability guarantee: later rules are not
// consulted and channel lists are never merged.
func TestFirstMatchWins(t *testing.T) {
	r := newRouter(t, config.Routing{Rules: []config.RoutingRule{
		{Name: "first", Match: config.RoutingMatch{Type: []string{"incident"}}, Channels: []string{"dev-telegram"}},
		{Name: "second", Match: config.RoutingMatch{Severity: []string{"critical"}}, Channels: []string{"customer-telegram"}},
	}})
	rule, chans := routed(t, r, event(domain.EventIncident, domain.SeverityCritical, "s", ""))
	if rule != "first" || chans != "dev-telegram" {
		t.Fatalf("routed to rule=%q channels=%q, want the first matching rule only", rule, chans)
	}
}

func TestEmptyMatchIsCatchAllAndEmptyChannelsSuppress(t *testing.T) {
	r := newRouter(t, config.Routing{
		Rules: []config.RoutingRule{
			{Name: "silence-healthchecks", Match: config.RoutingMatch{Source: []string{"healthcheck"}}, Channels: nil},
			{Name: "everything-else", Channels: []string{"dev-telegram"}},
		},
		Default: []string{"customer-telegram"},
	})

	rule, chans := routed(t, r, event(domain.EventIncident, domain.SeverityError, "healthcheck", ""))
	if rule != "silence-healthchecks" || chans != "" {
		t.Fatalf("suppression rule gave rule=%q channels=%q, want the rule with no channels", rule, chans)
	}
	// The catch-all matches everything else, so the default is never reached.
	rule, chans = routed(t, r, event(domain.EventAudit, domain.SeverityInfo, "anything", "any"))
	if rule != "everything-else" || chans != "dev-telegram" {
		t.Fatalf("catch-all gave rule=%q channels=%q", rule, chans)
	}
}

func TestDefaultAppliesOnlyWithoutAMatch(t *testing.T) {
	r := newRouter(t, config.Routing{
		Rules: []config.RoutingRule{
			{Name: "incidents", Match: config.RoutingMatch{Type: []string{"incident"}}, Channels: []string{"dev-telegram"}},
		},
		Default: []string{"customer-telegram", "dev-email"},
	})
	if rule, chans := routed(t, r, event(domain.EventIncident, domain.SeverityInfo, "s", "")); chans != "dev-telegram" {
		t.Fatalf("matched event used the default: rule=%q channels=%q", rule, chans)
	}
	rule, chans := routed(t, r, event(domain.EventBusiness, domain.SeverityInfo, "s", ""))
	if rule != "" || chans != "customer-telegram,dev-email" {
		t.Fatalf("unmatched event gave rule=%q channels=%q, want the default", rule, chans)
	}
}

func TestNoRuleAndNoDefaultDeliversNowhere(t *testing.T) {
	r := newRouter(t, config.Routing{Rules: []config.RoutingRule{
		{Name: "incidents", Match: config.RoutingMatch{Type: []string{"incident"}}, Channels: []string{"dev-telegram"}},
	}})
	d := r.Route(event(domain.EventAudit, domain.SeverityInfo, "s", ""))
	if d.Matched || len(d.Channels) != 0 {
		t.Fatalf("expected no match and no channels, got %+v", d)
	}
}

// Without a routing section the router reproduces the pre-0.2.0 fan-out.
func TestUnconfiguredRouterSendsEverywhere(t *testing.T) {
	r := NewAllChannels(testTargets())
	if r.Configured() {
		t.Fatal("NewAllChannels must report routing as not configured")
	}
	d := r.Route(event(domain.EventIncident, domain.SeverityInfo, "s", ""))
	if got := strings.Join(names(d.Channels), ","); got != "dev-telegram,customer-telegram,dev-email" {
		t.Fatalf("unconfigured router routed to %q, want every channel", got)
	}
	if got := strings.Join(r.Default(), ","); got != "dev-telegram,customer-telegram,dev-email" {
		t.Fatalf("Default() = %q, want every channel (that is where every event goes)", got)
	}
}

func TestNewRejectsUnknownChannelNames(t *testing.T) {
	if _, err := New(config.Routing{Rules: []config.RoutingRule{
		{Name: "typo", Channels: []string{"dev-telgram"}},
	}}, testTargets()); err == nil {
		t.Fatal("expected an error for an unknown channel in a rule")
	}
	if _, err := New(config.Routing{Default: []string{"nope"}}, testTargets()); err == nil {
		t.Fatal("expected an error for an unknown channel in the default")
	}
}

func TestDiagnostics(t *testing.T) {
	r := newRouter(t, config.Routing{
		Rules: []config.RoutingRule{
			{Name: "catch-all", Channels: []string{"dev-telegram"}},
			{Name: "never-reached", Match: config.RoutingMatch{Type: []string{"audit"}}, Channels: []string{"dev-email"}},
		},
	})
	catchAll, unreachable := r.UnreachableRules()
	if catchAll != "catch-all" || strings.Join(unreachable, ",") != "never-reached" {
		t.Fatalf("UnreachableRules() = (%q, %v)", catchAll, unreachable)
	}
	// customer-telegram appears in no rule and no default.
	if got := strings.Join(r.UnusedChannels(), ","); got != "customer-telegram" {
		t.Fatalf("UnusedChannels() = %q, want customer-telegram", got)
	}

	// A catch-all in last position is normal and must not be reported.
	ok := newRouter(t, config.Routing{Rules: []config.RoutingRule{
		{Name: "audits", Match: config.RoutingMatch{Type: []string{"audit"}}, Channels: []string{"dev-email"}},
		{Name: "rest", Channels: []string{"dev-telegram", "customer-telegram"}},
	}})
	if catchAll, unreachable := ok.UnreachableRules(); catchAll != "" || unreachable != nil {
		t.Fatalf("a trailing catch-all was reported as a problem: (%q, %v)", catchAll, unreachable)
	}
	if unused := ok.UnusedChannels(); unused != nil {
		t.Fatalf("UnusedChannels() = %v, want none", unused)
	}
}

func TestRulesViewDescribesConditions(t *testing.T) {
	r := newRouter(t, config.Routing{Rules: []config.RoutingRule{
		{Name: "orders", Match: config.RoutingMatch{
			Type: []string{"business_event"}, Category: []string{"order.*"}, MinSeverity: "info",
		}, Channels: []string{"customer-telegram"}},
		{Name: "rest", Channels: []string{"dev-telegram"}},
	}})
	views := r.Rules()
	if len(views) != 2 {
		t.Fatalf("expected 2 rule views, got %d", len(views))
	}
	if views[0].Name != "orders" || views[0].CatchAll {
		t.Fatalf("unexpected first view: %+v", views[0])
	}
	if strings.Join(views[0].Match.Category, ",") != "order.*" || views[0].Match.MinSeverity != "info" {
		t.Fatalf("conditions not exposed: %+v", views[0].Match)
	}
	if !views[1].CatchAll {
		t.Fatal("a rule with no conditions must be reported as a catch-all")
	}
}

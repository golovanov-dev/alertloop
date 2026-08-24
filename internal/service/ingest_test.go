package service

import (
	"bytes"
	"context"
	"log/slog"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/golovanov-dev/alertloop/internal/config"
	"github.com/golovanov-dev/alertloop/internal/domain"
	"github.com/golovanov-dev/alertloop/internal/routing"
	"github.com/golovanov-dev/alertloop/internal/storage"
)

func newStore(t *testing.T) storage.Store {
	t.Helper()
	s, err := storage.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := s.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestIngestCreatesEventAndFansOut(t *testing.T) {
	s := newStore(t)
	targets := []domain.ChannelTarget{
		{Type: domain.ChannelWebhook, Name: "siem"},
		{Type: domain.ChannelEmail, Name: "ops"},
	}
	svc := NewIngestService(s, routing.NewAllChannels(targets), 5, nil, time.Now, nil)
	ctx := context.Background()

	ev, created, err := svc.ingestLegacy(ctx, EventInput{
		Type: domain.EventIncident, Severity: domain.SeverityCritical,
		Source: "feeds", Message: "failed", DedupeKey: "k1",
	})
	if err != nil || !created {
		t.Fatalf("ingest: created=%v err=%v", created, err)
	}
	if ev.State != domain.StateNew {
		t.Fatalf("expected new state, got %q", ev.State)
	}

	page, _ := s.ListDeliveryAttempts(ctx, storage.DeliveryFilter{EventID: ev.ID}, 50, "")
	if len(page.Items) != 2 {
		t.Fatalf("expected 2 delivery attempts (one per channel), got %d", len(page.Items))
	}
}

func TestIngestFansOutToMultipleChannelsOfSameType(t *testing.T) {
	s := newStore(t)
	// Two Telegram channels + one webhook: an event should create three
	// delivery attempts, each targeting a distinct named channel.
	targets := []domain.ChannelTarget{
		{Type: domain.ChannelTelegram, Name: "tg-ru"},
		{Type: domain.ChannelTelegram, Name: "tg-en"},
		{Type: domain.ChannelWebhook, Name: "siem"},
	}
	svc := NewIngestService(s, routing.NewAllChannels(targets), 5, nil, time.Now, nil)
	ctx := context.Background()

	ev, _, err := svc.ingestLegacy(ctx, EventInput{Type: domain.EventIncident, Source: "s", Message: "m"})
	if err != nil {
		t.Fatalf("ingest: %v", err)
	}

	page, _ := s.ListDeliveryAttempts(ctx, storage.DeliveryFilter{EventID: ev.ID}, 50, "")
	if len(page.Items) != 3 {
		t.Fatalf("expected 3 delivery attempts, got %d", len(page.Items))
	}
	names := map[string]domain.ChannelType{}
	for _, d := range page.Items {
		names[d.ChannelName] = d.Channel
	}
	if names["tg-ru"] != domain.ChannelTelegram || names["tg-en"] != domain.ChannelTelegram || names["siem"] != domain.ChannelWebhook {
		t.Fatalf("unexpected channel/name mapping: %+v", names)
	}

	// Filtering by a specific channel name returns just that one.
	only, _ := s.ListDeliveryAttempts(ctx, storage.DeliveryFilter{ChannelName: "tg-en"}, 50, "")
	if len(only.Items) != 1 || only.Items[0].ChannelName != "tg-en" {
		t.Fatalf("channel_name filter failed: %+v", only.Items)
	}
}

// routingTargets is the customer/developer split the routing tests below use.
func routingTargets() []domain.ChannelTarget {
	return []domain.ChannelTarget{
		{Type: domain.ChannelTelegram, Name: "customer-telegram"},
		{Type: domain.ChannelTelegram, Name: "dev-telegram"},
		{Type: domain.ChannelWebhook, Name: "siem"},
	}
}

// deliveredTo lists the channel names an event actually produced attempts for.
func deliveredTo(t *testing.T, s storage.Store, eventID string) []string {
	t.Helper()
	page, err := s.ListDeliveryAttempts(context.Background(), storage.DeliveryFilter{EventID: eventID}, 50, "")
	if err != nil {
		t.Fatalf("list deliveries: %v", err)
	}
	names := make([]string, 0, len(page.Items))
	for _, d := range page.Items {
		names = append(names, d.ChannelName)
	}
	sort.Strings(names)
	return names
}

// TestIngestRoutesEventsToDifferentAudiences is the acceptance scenario: on one
// instance, business events reach the customer and incidents reach the
// developer, and neither side sees the other's events.
func TestIngestRoutesEventsToDifferentAudiences(t *testing.T) {
	s := newStore(t)
	router, err := routing.New(config.Routing{
		Rules: []config.RoutingRule{
			// Suppression comes first: the first matching rule wins, so a rule
			// that silences a source has to sit above the rules it overrides.
			{Name: "silence-healthchecks", Match: config.RoutingMatch{Source: []string{"healthcheck"}}},
			{Name: "incidents-to-dev", Match: config.RoutingMatch{Type: []string{"incident"}},
				Channels: []string{"dev-telegram", "siem"}},
			{Name: "orders-to-customer", Match: config.RoutingMatch{
				Type: []string{"business_event"}, Category: []string{"order.*"},
			}, Channels: []string{"customer-telegram"}},
		},
		Default: []string{"dev-telegram"},
	}, routingTargets())
	if err != nil {
		t.Fatalf("build router: %v", err)
	}
	svc := NewIngestService(s, router, 5, nil, time.Now, nil)
	ctx := context.Background()

	cases := []struct {
		name  string
		in    EventInput
		wants []string
	}{
		{"incident goes to the developer", EventInput{
			Type: domain.EventIncident, Source: "feeds_worker", Message: "boom",
		}, []string{"dev-telegram", "siem"}},
		{"order goes to the customer", EventInput{
			Type: domain.EventBusiness, Source: "shop", Category: "order.created", Message: "new order",
		}, []string{"customer-telegram"}},
		{"suppressed source is delivered nowhere", EventInput{
			Type: domain.EventIncident, Source: "healthcheck", Message: "probe failed",
		}, []string{}},
		{"unmatched event falls back to the default", EventInput{
			Type: domain.EventAudit, Source: "admin", Message: "login",
		}, []string{"dev-telegram"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ev, created, err := svc.ingestLegacy(ctx, c.in)
			if err != nil || !created {
				t.Fatalf("ingest: created=%v err=%v", created, err)
			}
			// The event itself is always stored, including when it is suppressed.
			if _, err := s.GetEvent(ctx, ev.ID); err != nil {
				t.Fatalf("event was not stored: %v", err)
			}
			got := deliveredTo(t, s, ev.ID)
			if strings.Join(got, ",") != strings.Join(c.wants, ",") {
				t.Fatalf("delivered to %v, want %v", got, c.wants)
			}
		})
	}
}

// TestIngestWithoutRoutingDeliversToAllChannels is the 0.1.1 compatibility
// guard: with no routing section configured, every event still fans out to
// every configured channel.
func TestIngestWithoutRoutingDeliversToAllChannels(t *testing.T) {
	s := newStore(t)
	svc := NewIngestService(s, routing.NewAllChannels(routingTargets()), 5, nil, time.Now, nil)

	ev, _, err := svc.ingestLegacy(context.Background(), EventInput{
		Type: domain.EventBusiness, Source: "shop", Category: "order.created", Message: "m",
	})
	if err != nil {
		t.Fatalf("ingest: %v", err)
	}
	if got := deliveredTo(t, s, ev.ID); strings.Join(got, ",") != "customer-telegram,dev-telegram,siem" {
		t.Fatalf("delivered to %v, want every configured channel", got)
	}
}

// An event that matches nothing with no default configured is delivered
// nowhere. That is the one failure mode of routing that is otherwise invisible,
// so it must be loud — and it must not fire for events that were routed, or for
// duplicates, which create no deliveries by design.
func TestIngestWarnsWhenNothingMatchesAndNoDefault(t *testing.T) {
	s := newStore(t)
	router, err := routing.New(config.Routing{Rules: []config.RoutingRule{
		{Name: "incidents-to-dev", Match: config.RoutingMatch{Type: []string{"incident"}},
			Channels: []string{"dev-telegram"}},
	}}, routingTargets())
	if err != nil {
		t.Fatalf("build router: %v", err)
	}
	var logged bytes.Buffer
	svc := NewIngestService(s, router, 5, nil, time.Now,
		slog.New(slog.NewTextHandler(&logged, &slog.HandlerOptions{Level: slog.LevelDebug})))
	ctx := context.Background()

	// A routed event is not warned about.
	if _, _, err := svc.ingestLegacy(ctx, EventInput{Type: domain.EventIncident, Source: "s", Message: "m"}); err != nil {
		t.Fatalf("ingest incident: %v", err)
	}
	if strings.Contains(logged.String(), "level=WARN") {
		t.Fatalf("a routed event produced a warning:\n%s", logged.String())
	}

	ev, _, err := svc.ingestLegacy(ctx, EventInput{
		Type: domain.EventAudit, Severity: domain.SeverityWarning,
		Source: "admin", Category: "login", Message: "m", DedupeKey: "dup",
	})
	if err != nil {
		t.Fatalf("ingest audit: %v", err)
	}
	out := logged.String()
	if !strings.Contains(out, "level=WARN") || !strings.Contains(out, "event_id="+ev.ID) {
		t.Fatalf("expected a warning naming the event:\n%s", out)
	}
	for _, want := range []string{"type=audit", "severity=warning", "source=admin", "category=login"} {
		if !strings.Contains(out, want) {
			t.Errorf("warning is missing %q:\n%s", want, out)
		}
	}

	// A dedupe hit is not a new undelivered event and must not warn again.
	before := strings.Count(out, "level=WARN")
	if _, created, err := svc.ingestLegacy(ctx, EventInput{
		Type: domain.EventAudit, Source: "admin", Message: "m2", DedupeKey: "dup",
	}); err != nil || created {
		t.Fatalf("second ingest: created=%v err=%v", created, err)
	}
	if after := strings.Count(logged.String(), "level=WARN"); after != before {
		t.Fatalf("a dedupe hit produced another warning (%d -> %d)", before, after)
	}
}

// A rule with no channels suppresses delivery but must not suppress the event.
func TestIngestSuppressedEventIsStillStoredAndListed(t *testing.T) {
	s := newStore(t)
	router, err := routing.New(config.Routing{
		Rules: []config.RoutingRule{{Name: "silence-all"}}, // catch-all, no channels
	}, routingTargets())
	if err != nil {
		t.Fatalf("build router: %v", err)
	}
	svc := NewIngestService(s, router, 5, nil, time.Now, nil)
	ctx := context.Background()

	ev, created, err := svc.ingestLegacy(ctx, EventInput{Type: domain.EventIncident, Source: "s", Message: "m"})
	if err != nil || !created {
		t.Fatalf("ingest: created=%v err=%v", created, err)
	}
	page, _ := s.ListEvents(ctx, storage.EventFilter{}, 50, "")
	if len(page.Items) != 1 || page.Items[0].ID != ev.ID {
		t.Fatalf("suppressed event is missing from the event list: %+v", page.Items)
	}
	if got := deliveredTo(t, s, ev.ID); len(got) != 0 {
		t.Fatalf("suppression created deliveries: %v", got)
	}
}

func TestIngestDedupeCreatesNoNewDeliveries(t *testing.T) {
	s := newStore(t)
	svc := NewIngestService(s, routing.NewAllChannels([]domain.ChannelTarget{{Type: domain.ChannelWebhook, Name: "wh"}}), 5, nil, time.Now, nil)
	ctx := context.Background()

	first, _, _ := svc.ingestLegacy(ctx, EventInput{Type: domain.EventIncident, Source: "s", Message: "m", DedupeKey: "dup"})
	second, created, err := svc.ingestLegacy(ctx, EventInput{Type: domain.EventIncident, Source: "s", Message: "m2", DedupeKey: "dup"})
	if err != nil {
		t.Fatalf("second ingest: %v", err)
	}
	if created {
		t.Fatal("expected dedupe hit, not created")
	}
	if second.ID != first.ID {
		t.Fatal("expected same event id on dedupe")
	}
	page, _ := s.ListDeliveryAttempts(ctx, storage.DeliveryFilter{}, 50, "")
	if len(page.Items) != 1 {
		t.Fatalf("expected only 1 delivery attempt total, got %d", len(page.Items))
	}
}

func TestIngestValidation(t *testing.T) {
	s := newStore(t)
	svc := NewIngestService(s, nil, 5, nil, time.Now, nil)
	ctx := context.Background()

	cases := []EventInput{
		{Type: "bogus", Source: "s", Message: "m"},
		{Type: domain.EventIncident, Message: "m"}, // missing source
		{Type: domain.EventIncident, Source: "s"},  // missing message
		{Type: domain.EventIncident, Source: "s", Message: "m", Payload: []byte(`{bad json`)},
	}
	for i, c := range cases {
		if _, _, err := svc.ingestLegacy(ctx, c); err == nil {
			t.Errorf("case %d: expected validation error", i)
		}
	}
}

func TestIngestDefaultsSeverity(t *testing.T) {
	s := newStore(t)
	svc := NewIngestService(s, nil, 5, nil, time.Now, nil)
	ev, _, err := svc.ingestLegacy(context.Background(), EventInput{
		Type: domain.EventBusiness, Source: "s", Message: "m",
	})
	if err != nil {
		t.Fatalf("ingest: %v", err)
	}
	if ev.Severity != domain.SeverityInfo {
		t.Fatalf("expected default severity info, got %q", ev.Severity)
	}
}

// ingestLegacy is the pre-0.4.0 three-value shape of Ingest: event, created,
// error. The tests above were written against it, and keeping them on it is
// deliberate — they are now the regression suite proving that a client which
// sends no `status` still sees exactly the 0.3.x behaviour. Lifecycle tests use
// the real Ingest and read the outcome.
func (s *IngestService) ingestLegacy(ctx context.Context, in EventInput) (*domain.Event, bool, error) {
	res, err := s.Ingest(ctx, in)
	return res.Event, res.Created(), err
}

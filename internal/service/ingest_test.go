package service

import (
	"context"
	"testing"
	"time"

	"github.com/golovanov-dev/alertloop/internal/domain"
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
	svc := NewIngestService(s, targets, 5, time.Now)
	ctx := context.Background()

	ev, created, err := svc.Ingest(ctx, EventInput{
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
	svc := NewIngestService(s, targets, 5, time.Now)
	ctx := context.Background()

	ev, _, err := svc.Ingest(ctx, EventInput{Type: domain.EventIncident, Source: "s", Message: "m"})
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

func TestIngestDedupeCreatesNoNewDeliveries(t *testing.T) {
	s := newStore(t)
	svc := NewIngestService(s, []domain.ChannelTarget{{Type: domain.ChannelWebhook, Name: "wh"}}, 5, time.Now)
	ctx := context.Background()

	first, _, _ := svc.Ingest(ctx, EventInput{Type: domain.EventIncident, Source: "s", Message: "m", DedupeKey: "dup"})
	second, created, err := svc.Ingest(ctx, EventInput{Type: domain.EventIncident, Source: "s", Message: "m2", DedupeKey: "dup"})
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
	svc := NewIngestService(s, nil, 5, time.Now)
	ctx := context.Background()

	cases := []EventInput{
		{Type: "bogus", Source: "s", Message: "m"},
		{Type: domain.EventIncident, Message: "m"}, // missing source
		{Type: domain.EventIncident, Source: "s"},  // missing message
		{Type: domain.EventIncident, Source: "s", Message: "m", Payload: []byte(`{bad json`)},
	}
	for i, c := range cases {
		if _, _, err := svc.Ingest(ctx, c); err == nil {
			t.Errorf("case %d: expected validation error", i)
		}
	}
}

func TestIngestDefaultsSeverity(t *testing.T) {
	s := newStore(t)
	svc := NewIngestService(s, nil, 5, time.Now)
	ev, _, err := svc.Ingest(context.Background(), EventInput{
		Type: domain.EventBusiness, Source: "s", Message: "m",
	})
	if err != nil {
		t.Fatalf("ingest: %v", err)
	}
	if ev.Severity != domain.SeverityInfo {
		t.Fatalf("expected default severity info, got %q", ev.Severity)
	}
}

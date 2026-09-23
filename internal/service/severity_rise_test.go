package service

import (
	"context"
	"testing"
	"time"

	"github.com/golovanov-dev/alertloop/internal/config"
	"github.com/golovanov-dev/alertloop/internal/domain"
	"github.com/golovanov-dev/alertloop/internal/routing"
	"github.com/golovanov-dev/alertloop/internal/storage"
)

// onCallService routes critical incidents to on-call Telegram and email, and
// everything else to email only.
func onCallService(t *testing.T) (*IngestService, *EventService, storage.Store) {
	t.Helper()
	s := newStore(t)
	targets := []domain.ChannelTarget{
		{Type: domain.ChannelTelegram, Name: "oncall-telegram"},
		{Type: domain.ChannelEmail, Name: "ops-email"},
	}
	router, err := routing.New(config.Routing{
		Rules: []config.RoutingRule{{
			Name:     "critical",
			Match:    config.RoutingMatch{MinSeverity: "critical"},
			Channels: []string{"oncall-telegram", "ops-email"},
		}},
		Default: []string{"ops-email"},
	}, targets)
	if err != nil {
		t.Fatalf("router: %v", err)
	}
	rec := NewRecoveryNotifier(true, 5, quietLogger())
	clock := clockAt(time.Date(2026, 9, 22, 10, 0, 0, 0, time.UTC))
	return NewIngestService(s, router, 5, rec, clock, quietLogger()), NewEventService(s, rec, clock), s
}

// A warning went to email; the same incident turning critical must reach
// on-call, even after someone acknowledged the warning. Repeating or lowering
// the severity notifies nobody, and the recovery goes to everyone alerted.
func TestSeverityRiseAlertsChannelsNotAlertedYet(t *testing.T) {
	ingest, events, store := onCallService(t)
	ctx := context.Background()
	const key = "db-01:postgresql:availability"

	res, err := ingest.Ingest(ctx, firing(key, "slow", domain.SeverityWarning))
	if err != nil {
		t.Fatalf("warning: %v", err)
	}
	if _, err := events.Apply(ctx, res.Event.ID, domain.ActionAck); err != nil {
		t.Fatalf("ack: %v", err)
	}

	if _, err := ingest.Ingest(ctx, firing(key, "down", domain.SeverityCritical)); err != nil {
		t.Fatalf("critical: %v", err)
	}
	alerts := attemptsFor(t, store, domain.KindAlert)
	if _, ok := alerts["oncall-telegram"]; !ok || countDeliveries(t, store, domain.KindAlert) != 2 {
		t.Fatalf("alerts after the rise = %v, want ops-email once and oncall-telegram once", alerts)
	}

	for _, sev := range []domain.Severity{domain.SeverityCritical, domain.SeverityWarning} {
		if _, err := ingest.Ingest(ctx, firing(key, "still", sev)); err != nil {
			t.Fatalf("%s repeat: %v", sev, err)
		}
		if n := countDeliveries(t, store, domain.KindAlert); n != 2 {
			t.Fatalf("alert attempts after a %s repeat = %d, want 2", sev, n)
		}
	}

	if _, err := ingest.Ingest(ctx, EventInput{Status: domain.StatusResolved, DedupeKey: key}); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got := attemptsFor(t, store, domain.KindRecovery); len(got) != 2 {
		t.Fatalf("recoveries = %v, want one per alerted channel", got)
	}
}

// Mute means "stop telling me about this": a rise while muted alerts nobody.
func TestSeverityRiseOfAMutedIncidentAlertsNobody(t *testing.T) {
	ingest, events, store := onCallService(t)
	ctx := context.Background()
	const key = "db-01:disk:usage"

	res, err := ingest.Ingest(ctx, firing(key, "80%", domain.SeverityWarning))
	if err != nil {
		t.Fatalf("warning: %v", err)
	}
	if _, err := events.Apply(ctx, res.Event.ID, domain.ActionMute); err != nil {
		t.Fatalf("mute: %v", err)
	}
	if _, err := ingest.Ingest(ctx, firing(key, "99%", domain.SeverityCritical)); err != nil {
		t.Fatalf("critical: %v", err)
	}
	if _, ok := attemptsFor(t, store, domain.KindAlert)["oncall-telegram"]; ok {
		t.Fatal("a muted incident alerted on-call when its severity rose")
	}
}

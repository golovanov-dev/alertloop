package api

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/golovanov-dev/alertloop/internal/domain"
	"github.com/golovanov-dev/alertloop/internal/storage"
)

// seedAlertAndRecovery stores one incident with both an alert and a recovery
// delivery.
func seedAlertAndRecovery(t *testing.T, store storage.Store) string {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()

	e := &domain.Event{
		ID: "evt-1", Type: domain.EventIncident, Severity: domain.SeverityCritical,
		State: domain.StateResolved, Source: "monit", Message: "PostgreSQL down",
		DedupeKey: "server-01:postgresql:availability", Payload: []byte(`{}`),
		CreatedAt: now, UpdatedAt: now, LastSeenAt: now, ResolvedAt: &now,
	}
	if _, _, err := store.CreateEvent(ctx, e); err != nil {
		t.Fatalf("create event: %v", err)
	}
	for _, d := range []*domain.DeliveryAttempt{
		{
			ID: "att-alert", EventID: "evt-1", Channel: domain.ChannelTelegram, ChannelName: "tg",
			Kind: domain.KindAlert, State: domain.DeliverySent, MaxAttempts: 5,
			LastError: "", CreatedAt: now, UpdatedAt: now,
		},
		{
			ID: "att-recovery", EventID: "evt-1", Channel: domain.ChannelEmail, ChannelName: "mail",
			Kind: domain.KindRecovery, State: domain.DeliveryFailed, MaxAttempts: 5,
			LastError: "smtp: connection refused", CreatedAt: now, UpdatedAt: now,
		},
	} {
		if err := store.CreateDeliveryAttempt(ctx, d); err != nil {
			t.Fatalf("create attempt %s: %v", d.ID, err)
		}
	}
	return "evt-1"
}

// The delivery kind filter, which the admin console and any script use.
func TestDeliveryAttemptsAPIFiltersByKind(t *testing.T) {
	ts, store := newTestServer(t, nil)
	seedAlertAndRecovery(t, store)

	resp, body := doJSON(t, http.MethodGet, ts.URL+"/v1/delivery-attempts?kind=recovery", adminTok, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	items, _ := body["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("kind=recovery returned %d attempts, want 1", len(items))
	}
	first, _ := items[0].(map[string]any)
	if first["kind"] != string(domain.KindRecovery) {
		t.Fatalf("kind = %v, want recovery", first["kind"])
	}
}

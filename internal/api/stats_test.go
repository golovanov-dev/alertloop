package api

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/golovanov-dev/alertloop/internal/domain"
)

// The monitoring fields of /v1/stats are always present: a value that does
// not exist is null, not 0 or an empty time. The age is whole seconds since
// the oldest due attempt became due, and the tick is a time in whole seconds.
func TestStatsReportsMonitoringSignals(t *testing.T) {
	ts, store := newTestServer(t, nil)
	ctx := context.Background()

	resp, got := doJSON(t, "GET", ts.URL+"/v1/stats", adminTok, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("stats = %d", resp.StatusCode)
	}
	for key, want := range map[string]any{
		"open_incidents":                  float64(0),
		"oldest_due_delivery_age_seconds": nil,
		"dead_letter_last_24h":            float64(0),
		"worker_last_tick_at":             nil,
	} {
		v, present := got[key]
		if !present || v != want {
			t.Errorf("%s = %v (present %v) on an empty database, want %v", key, v, present, want)
		}
	}

	now := time.Now().UTC()
	e := &domain.Event{
		ID: "e1", Type: domain.EventIncident, Severity: domain.SeverityError, State: domain.StateNew,
		Source: "s", Message: "m", CreatedAt: now, UpdatedAt: now, LastSeenAt: now,
	}
	if _, _, err := store.CreateEvent(ctx, e); err != nil {
		t.Fatal(err)
	}
	queued := now.Add(-90 * time.Second)
	if err := store.CreateDeliveryAttempt(ctx, &domain.DeliveryAttempt{
		ID: "d1", EventID: "e1", Channel: domain.ChannelWebhook, ChannelName: "webhook", Kind: domain.KindAlert,
		State: domain.DeliveryPending, MaxAttempts: 5, CreatedAt: queued, UpdatedAt: queued,
	}); err != nil {
		t.Fatal(err)
	}
	tick := time.Date(2026, 9, 22, 12, 0, 7, 600_000_000, time.UTC)
	if err := store.RecordWorkerTick(ctx, tick); err != nil {
		t.Fatal(err)
	}

	_, got = doJSON(t, "GET", ts.URL+"/v1/stats", adminTok, "")
	if got["open_incidents"] != float64(1) {
		t.Errorf("open_incidents = %v, want 1", got["open_incidents"])
	}
	if age, _ := got["oldest_due_delivery_age_seconds"].(float64); age < 90 || age > 120 {
		t.Errorf("oldest_due_delivery_age_seconds = %v, want about 90", got["oldest_due_delivery_age_seconds"])
	}
	if got["worker_last_tick_at"] != "2026-09-22T12:00:07Z" {
		t.Errorf("worker_last_tick_at = %v, want 2026-09-22T12:00:07Z", got["worker_last_tick_at"])
	}
}

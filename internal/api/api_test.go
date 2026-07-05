package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/golovanov-dev/alertloop/internal/config"
	"github.com/golovanov-dev/alertloop/internal/domain"
	"github.com/golovanov-dev/alertloop/internal/service"
	"github.com/golovanov-dev/alertloop/internal/storage"
)

func newTestServer(t *testing.T, apiKeys map[string]string) (*httptest.Server, storage.Store) {
	t.Helper()
	store, err := storage.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := store.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	targets := []domain.ChannelTarget{{Type: domain.ChannelWebhook, Name: "webhook"}}
	srv := NewServer(Config{
		Store:      store,
		Ingest:     service.NewIngestService(store, targets, 5, time.Now),
		Events:     service.NewEventService(store, time.Now),
		Deliveries: service.NewDeliveryService(store, time.Now),
		APIKeys:    apiKeys,
		AdminToken: "admintok",
	})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts, store
}

func doJSON(t *testing.T, method, url, key, body string) (*http.Response, map[string]any) {
	t.Helper()
	req, _ := http.NewRequest(method, url, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if key != "" {
		req.Header.Set("X-API-Key", key)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	var m map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&m)
	resp.Body.Close()
	return resp, m
}

// adminTok is the admin token newTestServer configures; it also authenticates
// the JSON API (the admin console credential).
const adminTok = "admintok"

func TestIngestAndLifecycle(t *testing.T) {
	ts, _ := newTestServer(t, nil)

	// Ingest -> 201.
	resp, ev := doJSON(t, "POST", ts.URL+"/v1/events", adminTok,
		`{"type":"incident","severity":"critical","source":"api","message":"boom","dedupe_key":"k1"}`)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("ingest status = %d", resp.StatusCode)
	}
	id, _ := ev["id"].(string)
	if id == "" {
		t.Fatal("no id returned")
	}

	// Dedupe -> 200.
	resp, _ = doJSON(t, "POST", ts.URL+"/v1/events", adminTok,
		`{"type":"incident","severity":"critical","source":"api","message":"again","dedupe_key":"k1"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("dedupe status = %d, want 200", resp.StatusCode)
	}

	// Get -> 200.
	resp, got := doJSON(t, "GET", ts.URL+"/v1/events/"+id, adminTok, "")
	if resp.StatusCode != http.StatusOK || got["message"] != "boom" {
		t.Fatalf("get status=%d body=%v", resp.StatusCode, got)
	}

	// Ack -> acknowledged.
	resp, got = doJSON(t, "POST", ts.URL+"/v1/events/"+id+"/ack", adminTok, "")
	if resp.StatusCode != http.StatusOK || got["state"] != "acknowledged" {
		t.Fatalf("ack status=%d state=%v", resp.StatusCode, got["state"])
	}

	// Resolve, then ack should conflict (409).
	doJSON(t, "POST", ts.URL+"/v1/events/"+id+"/resolve", adminTok, "")
	resp, _ = doJSON(t, "POST", ts.URL+"/v1/events/"+id+"/ack", adminTok, "")
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("ack-after-resolve status = %d, want 409", resp.StatusCode)
	}

	// Unknown action -> 404.
	resp, _ = doJSON(t, "POST", ts.URL+"/v1/events/"+id+"/frobnicate", adminTok, "")
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown action status = %d, want 404", resp.StatusCode)
	}
}

func TestEmptyListsReturnArrayNotNull(t *testing.T) {
	ts, _ := newTestServer(t, nil)
	for _, path := range []string{"/v1/events", "/v1/delivery-attempts"} {
		req, _ := http.NewRequest("GET", ts.URL+path, nil)
		req.Header.Set("X-API-Key", adminTok)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		// Empty result must serialize as [] (not null) so strict clients don't break.
		if !strings.Contains(string(body), `"items":[]`) {
			t.Fatalf("%s empty list should be []; got %s", path, body)
		}
	}
}

func TestValidationRejected(t *testing.T) {
	ts, _ := newTestServer(t, nil)
	resp, _ := doJSON(t, "POST", ts.URL+"/v1/events", adminTok, `{"type":"incident"}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for missing fields, got %d", resp.StatusCode)
	}
}

func TestAdminTokenAuthorizesAPI(t *testing.T) {
	ts, _ := newTestServer(t, nil)
	// No credential -> 401 (admin token is configured, so the API is guarded).
	resp, _ := doJSON(t, "GET", ts.URL+"/v1/events", "", "")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no credential = %d, want 401", resp.StatusCode)
	}
	// Admin token -> 200.
	resp, _ = doJSON(t, "GET", ts.URL+"/v1/events", adminTok, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("admin token = %d, want 200", resp.StatusCode)
	}
}

func TestAPIKeyAuth(t *testing.T) {
	ts, _ := newTestServer(t, map[string]string{"secret-key": config.ScopeFull})

	// No key -> 401.
	resp, _ := doJSON(t, "GET", ts.URL+"/v1/events", "", "")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no key status = %d, want 401", resp.StatusCode)
	}
	// Wrong key -> 401.
	resp, _ = doJSON(t, "GET", ts.URL+"/v1/events", "wrong", "")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong key status = %d, want 401", resp.StatusCode)
	}
	// Right key -> 200.
	resp, _ = doJSON(t, "GET", ts.URL+"/v1/events", "secret-key", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("right key status = %d, want 200", resp.StatusCode)
	}
}

func TestAPIKeyScopes(t *testing.T) {
	ts, _ := newTestServer(t, map[string]string{
		"ingest-key": config.ScopeIngest,
		"read-key":   config.ScopeRead,
		"full-key":   config.ScopeFull,
	})
	ev := `{"type":"incident","severity":"error","source":"s","message":"m"}`

	// ingest key: can POST events, cannot read or act.
	if r, _ := doJSON(t, "POST", ts.URL+"/v1/events", "ingest-key", ev); r.StatusCode != http.StatusCreated {
		t.Fatalf("ingest POST = %d, want 201", r.StatusCode)
	}
	if r, _ := doJSON(t, "GET", ts.URL+"/v1/events", "ingest-key", ""); r.StatusCode != http.StatusForbidden {
		t.Fatalf("ingest GET events = %d, want 403", r.StatusCode)
	}

	// read key: can read, cannot POST events.
	if r, _ := doJSON(t, "GET", ts.URL+"/v1/events", "read-key", ""); r.StatusCode != http.StatusOK {
		t.Fatalf("read GET events = %d, want 200", r.StatusCode)
	}
	if r, _ := doJSON(t, "POST", ts.URL+"/v1/events", "read-key", ev); r.StatusCode != http.StatusForbidden {
		t.Fatalf("read POST events = %d, want 403", r.StatusCode)
	}

	// Create an event with the full key, then check action scope.
	_, created := doJSON(t, "POST", ts.URL+"/v1/events", "full-key", ev)
	id, _ := created["id"].(string)
	// read key cannot run a state action (needs full).
	if r, _ := doJSON(t, "POST", ts.URL+"/v1/events/"+id+"/ack", "read-key", ""); r.StatusCode != http.StatusForbidden {
		t.Fatalf("read ack = %d, want 403", r.StatusCode)
	}
	// full key can.
	if r, _ := doJSON(t, "POST", ts.URL+"/v1/events/"+id+"/ack", "full-key", ""); r.StatusCode != http.StatusOK {
		t.Fatalf("full ack = %d, want 200", r.StatusCode)
	}
}

func TestReplayEndpoint(t *testing.T) {
	ts, store := newTestServer(t, nil)
	ctx := context.Background()

	// Create an event and a dead-lettered attempt directly.
	ev, _, _ := store.CreateEvent(ctx, &domain.Event{
		ID: "e1", Type: domain.EventIncident, Severity: domain.SeverityError,
		State: domain.StateNew, Source: "s", Message: "m",
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	})
	att := &domain.DeliveryAttempt{
		ID: "d1", EventID: ev.ID, Channel: domain.ChannelWebhook,
		State: domain.DeliveryDeadLetter, Attempts: 5, MaxAttempts: 5,
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	if err := store.CreateDeliveryAttempt(ctx, att); err != nil {
		t.Fatalf("seed delivery: %v", err)
	}

	// Replay -> pending.
	resp, got := doJSON(t, "POST", ts.URL+"/v1/delivery-attempts/d1/replay", adminTok, "")
	if resp.StatusCode != http.StatusOK || got["state"] != "pending" {
		t.Fatalf("replay status=%d state=%v", resp.StatusCode, got["state"])
	}

	// Replaying a non-dead-letter attempt now -> 409.
	resp, _ = doJSON(t, "POST", ts.URL+"/v1/delivery-attempts/d1/replay", adminTok, "")
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("second replay status = %d, want 409", resp.StatusCode)
	}
}

func TestRateLimitPerIP(t *testing.T) {
	store, err := storage.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })

	srv := NewServer(Config{
		Store:      store,
		Ingest:     service.NewIngestService(store, nil, 5, time.Now),
		Events:     service.NewEventService(store, time.Now),
		Deliveries: service.NewDeliveryService(store, time.Now),
		// Tiny per-IP allowance to trip the limiter quickly.
		RateLimit: config.RateLimit{Enabled: true, PerIPPerSecond: 1, PerIPBurst: 3,
			IngestPerSecond: 1000, IngestBurst: 1000},
	})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	var got429 bool
	for i := 0; i < 10; i++ {
		resp, err := http.Get(ts.URL + "/health")
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode == http.StatusTooManyRequests {
			got429 = true
			break
		}
	}
	if !got429 {
		t.Fatal("expected a 429 after exceeding the per-IP burst")
	}
}

func TestAdminPageAuth(t *testing.T) {
	ts, _ := newTestServer(t, nil)

	resp, err := http.Get(ts.URL + "/events")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("events page without token = %d, want 401", resp.StatusCode)
	}

	resp, err = http.Get(ts.URL + "/events?token=admintok")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("events page with token = %d, want 200", resp.StatusCode)
	}
}

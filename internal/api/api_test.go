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
	"github.com/golovanov-dev/alertloop/internal/routing"
	"github.com/golovanov-dev/alertloop/internal/service"
	"github.com/golovanov-dev/alertloop/internal/storage"
)

func newTestServer(t *testing.T, apiKeys map[string]string) (*httptest.Server, storage.Store) {
	t.Helper()
	return newTestServerWithToken(t, apiKeys, adminTok)
}

func newTestServerWithToken(t *testing.T, apiKeys map[string]string, adminToken string) (*httptest.Server, storage.Store) {
	t.Helper()
	return newTestServerWith(t, func(c *Config) {
		c.APIKeys = apiKeys
		c.AdminToken = adminToken
	})
}

// newTestServerWith builds a test server on an in-memory store with one
// webhook channel; configure sets the credentials.
func newTestServerWith(t *testing.T, configure func(*Config)) (*httptest.Server, storage.Store) {
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
	c := Config{
		Store:      store,
		Ingest:     service.NewIngestService(store, routing.NewAllChannels(targets), 5, nil, time.Now, nil),
		Routing:    routing.NewAllChannels(targets),
		Events:     service.NewEventService(store, nil, time.Now),
		Deliveries: service.NewDeliveryService(store, time.Now),
	}
	configure(&c)
	ts := httptest.NewServer(NewServer(c).Handler())
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

// The public demo token works only straight from a private network: under
// Docker a request from the host arrives from the bridge gateway (172.x).
// Through a reverse proxy or from a public address it is refused; a token of
// the operator's own is not affected.
func TestDemoAdminTokenRefusedThroughProxyOrFromPublicAddress(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	cases := []struct {
		name, token, remote, header string
		want                        int
	}{
		{"demo from Docker bridge", config.DemoAdminToken, "172.17.0.1:40000", "", http.StatusOK},
		{"demo from IPv6 loopback", config.DemoAdminToken, "[::1]:40000", "", http.StatusOK},
		{"demo from IPv6 ULA", config.DemoAdminToken, "[fd00::5]:40000", "", http.StatusOK},
		{"demo from zoned link-local", config.DemoAdminToken, "[fe80::1%eth0]:40000", "", http.StatusOK},
		{"demo through a proxy (XFF)", config.DemoAdminToken, "127.0.0.1:40000", "X-Forwarded-For", http.StatusForbidden},
		{"demo through a proxy (Forwarded)", config.DemoAdminToken, "127.0.0.1:40000", "Forwarded", http.StatusForbidden},
		{"demo through a proxy (X-Real-IP)", config.DemoAdminToken, "127.0.0.1:40000", "X-Real-IP", http.StatusForbidden},
		{"demo from a public address", config.DemoAdminToken, "203.0.113.5:40000", "", http.StatusForbidden},
		{"own token through a proxy", adminTok, "127.0.0.1:40000", "X-Forwarded-For", http.StatusOK},
	}
	for _, c := range cases {
		r := req(c.remote, map[string]string{"X-API-Key": c.token})
		if c.header != "" {
			r.Header.Set(c.header, "203.0.113.5")
		}
		w := httptest.NewRecorder()
		apiKeyAuth(nil, nil, c.token, ok).ServeHTTP(w, r)
		if w.Code != c.want {
			t.Errorf("%s: status %d, want %d (%s)", c.name, w.Code, c.want, w.Body.String())
		}
	}
}

// With neither API keys nor an admin token there is no open mode: every /v1
// request is refused, whatever it presents. The process does not start in that
// state, and this keeps the API closed should it ever get this far.
func TestNoCredentialConfiguredRefusesEverything(t *testing.T) {
	ts, _ := newTestServerWithToken(t, nil, "")
	for _, key := range []string{"", "anything", adminTok} {
		for _, path := range []string{"/v1/events", "/v1/stats", "/v1/info"} {
			resp, _ := doJSON(t, "GET", ts.URL+path, key, "")
			if resp.StatusCode != http.StatusUnauthorized {
				t.Fatalf("GET %s with key %q = %d, want 401", path, key, resp.StatusCode)
			}
		}
	}
	resp, _ := doJSON(t, "POST", ts.URL+"/v1/events", "",
		`{"status":"firing","type":"incident","dedupe_key":"k","message":"m","severity":"error"}`)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("POST /v1/events without credential = %d, want 401", resp.StatusCode)
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

// The admin console tells a read or ingest key apart from other 403s by this
// text (web/admin/src/screens/Login.tsx): rewording requireScope must change
// the console too.
func TestScopeRefusalTextTheConsoleMatches(t *testing.T) {
	ts, _ := newTestServer(t, map[string]string{"read-key": config.ScopeRead})
	r, body := doJSON(t, "GET", ts.URL+"/v1/routing", "read-key", "")
	errObj, _ := body["error"].(map[string]any)
	msg, _ := errObj["message"].(string)
	if r.StatusCode != http.StatusForbidden || !strings.Contains(msg, "lacks the required scope") {
		t.Fatalf("read key on /v1/routing = %d %q, want 403 containing %q", r.StatusCode, msg, "lacks the required scope")
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
		Ingest:     service.NewIngestService(store, nil, 5, nil, time.Now, nil),
		Events:     service.NewEventService(store, nil, time.Now),
		Deliveries: service.NewDeliveryService(store, time.Now),
		// Tiny per-IP allowance to trip the limiter quickly.
		RateLimit: config.RateLimit{Enabled: true, PerIPPerSecond: 1, PerIPBurst: 3,
			IngestPerSecond: 1000, IngestBurst: 1000},
	})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	// An authenticated endpoint: this is what the limiter exists to protect,
	// and repeated unauthenticated hits on it are exactly the brute-force it
	// is supposed to bound.
	var got429 bool
	for i := 0; i < 10; i++ {
		resp, err := http.Get(ts.URL + "/v1/events")
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

	// Health probes are deliberately exempt. The Docker HEALTHCHECK, an
	// orchestrator probe, and an external uptime check all come from the same
	// address; throttling them would make "is it up?" stop answering during an
	// incident, which is when the question is asked.
	for i := 0; i < 10; i++ {
		for _, path := range []string{"/health", "/health/live", "/ready", "/health/ready"} {
			resp, err := http.Get(ts.URL + path)
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			if resp.StatusCode == http.StatusTooManyRequests {
				t.Fatalf("%s was rate limited; health probes must always answer", path)
			}
		}
	}
}

// The credential may come as `Authorization: Bearer <key>` instead of
// X-API-Key.
func TestBearerAuthorization(t *testing.T) {
	ts, _ := newTestServer(t, nil)
	req, _ := http.NewRequest("GET", ts.URL+"/v1/events", nil)
	req.Header.Set("Authorization", "Bearer "+adminTok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("Bearer admin token = %d, want 200", resp.StatusCode)
	}
}

// 404 and 500 answer with a fixed text: no id echoed, no database error.
func TestErrorsDoNotLeakDetails(t *testing.T) {
	ts, store := newTestServer(t, nil)

	resp, got := doJSON(t, "GET", ts.URL+"/v1/events/no-such-event", adminTok, "")
	if errBody, _ := got["error"].(map[string]any); resp.StatusCode != http.StatusNotFound || errBody["message"] != "resource not found" {
		t.Fatalf("missing event = %d %v, want 404 resource not found", resp.StatusCode, got)
	}

	store.Close() // every query now fails inside the driver
	resp, got = doJSON(t, "GET", ts.URL+"/v1/events", adminTok, "")
	if errBody, _ := got["error"].(map[string]any); resp.StatusCode != http.StatusInternalServerError || errBody["message"] != "internal server error" {
		t.Fatalf("failing store = %d %v, want 500 internal server error", resp.StatusCode, got)
	}
}

// Ingest has a process-wide limit on top of the per-IP one; other endpoints
// are not counted against it.
func TestGlobalIngestLimit(t *testing.T) {
	ts, _ := newTestServerWith(t, func(c *Config) {
		c.AdminToken = adminTok
		c.RateLimit = config.RateLimit{Enabled: true, PerIPPerSecond: 1000, PerIPBurst: 1000,
			IngestPerSecond: 0.001, IngestBurst: 2}
	})
	ev := `{"type":"incident","source":"s","message":"m"}`
	for i, want := range []int{http.StatusCreated, http.StatusCreated, http.StatusTooManyRequests} {
		if resp, _ := doJSON(t, "POST", ts.URL+"/v1/events", adminTok, ev); resp.StatusCode != want {
			t.Fatalf("ingest %d = %d, want %d", i+1, resp.StatusCode, want)
		}
	}
	if resp, _ := doJSON(t, "GET", ts.URL+"/v1/events", adminTok, ""); resp.StatusCode != http.StatusOK {
		t.Fatalf("listing after the ingest limit = %d, want 200", resp.StatusCode)
	}
}

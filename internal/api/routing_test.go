package api

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golovanov-dev/alertloop/internal/config"
	"github.com/golovanov-dev/alertloop/internal/domain"
	"github.com/golovanov-dev/alertloop/internal/routing"
	"github.com/golovanov-dev/alertloop/internal/service"
	"github.com/golovanov-dev/alertloop/internal/storage"
)

// newRoutedTestServer starts a server whose routing splits incidents from
// business events, the way the acceptance scenario does.
func newRoutedTestServer(t *testing.T, apiKeys map[string]string) (*httptest.Server, storage.Store) {
	t.Helper()
	store, err := storage.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := store.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	targets := []domain.ChannelTarget{
		{Type: domain.ChannelTelegram, Name: "dev-telegram"},
		{Type: domain.ChannelTelegram, Name: "customer-telegram"},
	}
	router, err := routing.New(config.Routing{
		Rules: []config.RoutingRule{
			{Name: "incidents-to-dev", Match: config.RoutingMatch{Type: []string{"incident"}},
				Channels: []string{"dev-telegram"}},
			{Name: "orders-to-customer", Match: config.RoutingMatch{
				Type: []string{"business_event"}, Category: []string{"order.*"},
			}, Channels: []string{"customer-telegram"}},
		},
		Default: []string{"dev-telegram"},
	}, targets)
	if err != nil {
		t.Fatalf("build router: %v", err)
	}

	srv := NewServer(Config{
		Store:      store,
		Ingest:     service.NewIngestService(store, router, 5, time.Now, nil),
		Events:     service.NewEventService(store, time.Now),
		Deliveries: service.NewDeliveryService(store, time.Now),
		Routing:    router,
		APIKeys:    apiKeys,
		AdminToken: adminTok,
	})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts, store
}

func TestRoutingTableEndpoint(t *testing.T) {
	ts, _ := newRoutedTestServer(t, nil)

	resp, body := doJSON(t, "GET", ts.URL+"/v1/routing", adminTok, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /v1/routing = %d", resp.StatusCode)
	}
	if body["configured"] != true {
		t.Fatalf("expected configured=true, got %v", body["configured"])
	}
	rules, _ := body["rules"].([]any)
	if len(rules) != 2 {
		t.Fatalf("expected 2 rules in the table, got %v", body["rules"])
	}
	first, _ := rules[0].(map[string]any)
	if first["name"] != "incidents-to-dev" {
		t.Fatalf("unexpected first rule: %v", first)
	}
	if def, _ := body["default"].([]any); len(def) != 1 || def[0] != "dev-telegram" {
		t.Fatalf("unexpected default: %v", body["default"])
	}
}

// With no routing configured the table says so, and reports the channels every
// event goes to.
func TestRoutingTableReportsUnconfiguredRouting(t *testing.T) {
	ts, _ := newTestServer(t, nil)
	_, body := doJSON(t, "GET", ts.URL+"/v1/routing", adminTok, "")
	if body["configured"] != false {
		t.Fatalf("expected configured=false, got %v", body["configured"])
	}
	if rules, _ := body["rules"].([]any); len(rules) != 0 {
		t.Fatalf("expected no rules, got %v", body["rules"])
	}
	if def, _ := body["default"].([]any); len(def) != 1 || def[0] != "webhook" {
		t.Fatalf("expected the configured channel as the effective destination, got %v", body["default"])
	}
}

func TestRoutingPreviewMatchesRulesWithoutCreatingEvents(t *testing.T) {
	ts, store := newRoutedTestServer(t, nil)

	cases := []struct {
		name  string
		body  string
		rule  string
		chans string
	}{
		{"incident", `{"type":"incident","severity":"critical","source":"feeds_worker"}`, "incidents-to-dev", "dev-telegram"},
		{"order", `{"type":"business_event","source":"shop","category":"order.created"}`, "orders-to-customer", "customer-telegram"},
		{"no match falls back to default", `{"type":"audit","source":"admin"}`, "", "dev-telegram"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			resp, body := doJSON(t, "POST", ts.URL+"/v1/routing/preview", adminTok, c.body)
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("preview status = %d (%v)", resp.StatusCode, body)
			}
			if body["matched_rule"] != c.rule {
				t.Fatalf("matched_rule = %v, want %q", body["matched_rule"], c.rule)
			}
			chans, _ := body["channels"].([]any)
			if len(chans) != 1 || chans[0] != c.chans {
				t.Fatalf("channels = %v, want [%s]", body["channels"], c.chans)
			}
		})
	}

	// The whole point of the preview: no events and no deliveries are created.
	events, _ := store.ListEvents(context.Background(), storage.EventFilter{}, 50, "")
	if len(events.Items) != 0 {
		t.Fatalf("preview created %d events", len(events.Items))
	}
	deliveries, _ := store.ListDeliveryAttempts(context.Background(), storage.DeliveryFilter{}, 50, "")
	if len(deliveries.Items) != 0 {
		t.Fatalf("preview created %d delivery attempts", len(deliveries.Items))
	}
}

func TestRoutingPreviewRejectsInvalidFields(t *testing.T) {
	ts, _ := newRoutedTestServer(t, nil)
	for _, body := range []string{`{"type":"incidents"}`, `{"severity":"fatal"}`, `{`} {
		if resp, _ := doJSON(t, "POST", ts.URL+"/v1/routing/preview", adminTok, body); resp.StatusCode != http.StatusBadRequest {
			t.Errorf("preview %s = %d, want 400", body, resp.StatusCode)
		}
	}
}

// Both endpoints are operator tools: read scope is not enough.
func TestRoutingEndpointsRequireFullScope(t *testing.T) {
	ts, _ := newRoutedTestServer(t, map[string]string{
		"read-key": config.ScopeRead,
		"full-key": config.ScopeFull,
	})
	for _, ep := range []struct{ method, path, body string }{
		{"GET", "/v1/routing", ""},
		{"POST", "/v1/routing/preview", `{"type":"incident"}`},
	} {
		if resp, _ := doJSON(t, ep.method, ts.URL+ep.path, "read-key", ep.body); resp.StatusCode != http.StatusForbidden {
			t.Errorf("%s %s with read scope = %d, want 403", ep.method, ep.path, resp.StatusCode)
		}
		if resp, _ := doJSON(t, ep.method, ts.URL+ep.path, "full-key", ep.body); resp.StatusCode != http.StatusOK {
			t.Errorf("%s %s with full scope = %d, want 200", ep.method, ep.path, resp.StatusCode)
		}
	}
}

// The OpenAPI contract must describe the endpoints that exist.
func TestOpenAPIDocumentsRoutingEndpoints(t *testing.T) {
	ts, _ := newRoutedTestServer(t, nil)
	resp, err := http.Get(ts.URL + "/openapi.yaml")
	if err != nil {
		t.Fatalf("fetch spec: %v", err)
	}
	defer resp.Body.Close()
	spec, _ := io.ReadAll(resp.Body)
	for _, want := range []string{"/v1/routing:", "/v1/routing/preview:"} {
		if !bytes.Contains(spec, []byte(want)) {
			t.Errorf("openapi.yaml does not document %s", want)
		}
	}
}

package api

import (
	"net/http"
	"strings"
	"testing"

	"github.com/golovanov-dev/alertloop/internal/config"
	"github.com/golovanov-dev/alertloop/internal/domain"
)

// An ingest key limited to web-01 reports web-01 and nothing else: it cannot
// report another source, nor refresh, close or read another source's incident
// by guessing its dedupe_key. It sees only id, state and outcome.
func TestIngestKeyIsLimitedToItsSources(t *testing.T) {
	ts, _ := newTestServerWith(t, func(c *Config) {
		c.APIKeys = map[string]string{"web-key": config.ScopeIngest}
		c.APIKeySources = map[string][]string{"web-key": {"web-01"}}
		c.AdminToken = adminTok
	})
	const dbKey = "db-01:postgresql:availability"
	url := ts.URL + "/v1/events"

	r, full := doJSON(t, "POST", url, adminTok,
		`{"status":"firing","type":"incident","severity":"critical","source":"db-01","message":"down","dedupe_key":"`+dbKey+`"}`)
	if r.StatusCode != http.StatusCreated || full["message"] != "down" || full["outcome"] != "created" {
		t.Fatalf("admin create = %d %v, want 201 with the event and outcome", r.StatusCode, full)
	}
	dbID, _ := full["id"].(string)

	r, own := doJSON(t, "POST", url, "web-key",
		`{"status":"firing","type":"incident","source":"web-01","message":"502","dedupe_key":"web-01:nginx:http"}`)
	if r.StatusCode != http.StatusCreated || len(own) != 3 || own["outcome"] != "created" || own["state"] != "new" || own["id"] == "" {
		t.Fatalf("own create = %d %v, want 201 with only id, state and outcome", r.StatusCode, own)
	}

	for name, body := range map[string]string{
		"another source":           `{"type":"incident","source":"db-01","message":"x"}`,
		"refresh of db-01's event": `{"status":"firing","type":"incident","severity":"info","source":"web-01","message":"fine","dedupe_key":"` + dbKey + `"}`,
		"resolve of db-01's event": `{"status":"resolved","dedupe_key":"` + dbKey + `"}`,
	} {
		r, got := doJSON(t, "POST", url, "web-key", body)
		errBody, _ := got["error"].(map[string]any)
		msg, _ := errBody["message"].(string)
		if r.StatusCode != http.StatusForbidden || errBody["code"] != "source_not_allowed" {
			t.Fatalf("%s = %d %v, want 403 source_not_allowed", name, r.StatusCode, got)
		}
		if name != "another source" && strings.Contains(msg, "db-01") {
			t.Fatalf("%s: the refusal names the other source: %q", name, msg)
		}
	}

	_, stored := doJSON(t, "GET", url+"/"+dbID, adminTok, "")
	if stored["state"] != string(domain.StateNew) || stored["severity"] != "critical" || stored["message"] != "down" {
		t.Fatalf("db-01's incident changed after refused requests: %v", stored)
	}

	if r, _ := doJSON(t, "POST", url, "web-key", `{"status":"resolved","dedupe_key":"never-seen"}`); r.StatusCode != http.StatusNoContent {
		t.Fatalf("resolve of an unknown key = %d, want 204", r.StatusCode)
	}
}

// The payload limit is exact, and a body over the read limit is refused as too
// large rather than cut and reported as broken JSON. Nothing refused is stored.
func TestIngestBodyLimits(t *testing.T) {
	ts, _ := newTestServer(t, nil)
	url := ts.URL + "/v1/events"
	withPayload := func(size int) string {
		payload := `{"p":"` + strings.Repeat("x", size-8) + `"}`
		return `{"type":"incident","source":"s","message":"m","payload":` + payload + `}`
	}

	if r, got := doJSON(t, "POST", url, adminTok, withPayload(domain.MaxPayloadBytes)); r.StatusCode != http.StatusCreated {
		t.Fatalf("payload of exactly %d bytes = %d %v, want 201", domain.MaxPayloadBytes, r.StatusCode, got)
	}
	if r, _ := doJSON(t, "POST", url, adminTok, withPayload(domain.MaxPayloadBytes+1)); r.StatusCode != http.StatusBadRequest {
		t.Fatalf("payload one byte over = %d, want 400", r.StatusCode)
	}
	r, got := doJSON(t, "POST", url, adminTok, withPayload(maxIngestBody+1))
	if errBody, _ := got["error"].(map[string]any); r.StatusCode != http.StatusRequestEntityTooLarge || errBody["code"] != "request_too_large" {
		t.Fatalf("body over %d bytes = %d %v, want 413 request_too_large", maxIngestBody, r.StatusCode, got)
	}

	_, list := doJSON(t, "GET", url, adminTok, "")
	if items, _ := list["items"].([]any); len(items) != 1 {
		t.Fatalf("stored events = %d, want only the accepted one", len(items))
	}
}

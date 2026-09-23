package api

import (
	"net/http"
	"strings"
	"testing"
)

// The HTTP contract of the incident lifecycle: which status code each outcome
// maps to. Callers — including the Monit adapter, which decides its own exit
// code from this — depend on these codes, not on the response body.
func TestIngestLifecycleStatusCodes(t *testing.T) {
	ts, _ := newTestServer(t, nil)
	const key = "server-01:postgresql:availability"
	firing := `{"status":"firing","type":"incident","severity":"critical","source":"monit",
		"message":"PostgreSQL does not respond on port 5432","dedupe_key":"` + key + `"}`

	resp, body := doJSON(t, http.MethodPost, ts.URL+"/v1/events", adminTok, firing)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("first firing: status = %d, want 201", resp.StatusCode)
	}
	firstID, _ := body["id"].(string)
	if firstID == "" {
		t.Fatal("first firing returned no event id")
	}

	// A repeat refreshes the open incident: 200, same event, not a second one.
	resp, body = doJSON(t, http.MethodPost, ts.URL+"/v1/events", adminTok, firing)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("repeated firing: status = %d, want 200", resp.StatusCode)
	}
	if id, _ := body["id"].(string); id != firstID {
		t.Fatalf("repeated firing returned event %q, want the open incident %q", id, firstID)
	}

	resolve := `{"status":"resolved","dedupe_key":"` + key + `"}`
	resp, body = doJSON(t, http.MethodPost, ts.URL+"/v1/events", adminTok, resolve)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("resolve: status = %d, want 200", resp.StatusCode)
	}
	if state, _ := body["state"].(string); state != "resolved" {
		t.Fatalf("state = %q, want \"resolved\"", state)
	}
	if _, ok := body["resolved_at"]; !ok {
		t.Fatal("the resolved event carries no resolved_at")
	}

	// A repeated recovery is a no-op, not an error.
	resp, _ = doJSON(t, http.MethodPost, ts.URL+"/v1/events", adminTok, resolve)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("repeated resolve: status = %d, want 200", resp.StatusCode)
	}

	// A recovery for a key nothing was ever stored under: accepted, nothing to
	// return. The adapter must read this as success, so it has to be a 2xx.
	resp, _ = doJSON(t, http.MethodPost, ts.URL+"/v1/events", adminTok,
		`{"status":"resolved","dedupe_key":"server-01:never:seen"}`)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("resolve of an unknown key: status = %d, want 204", resp.StatusCode)
	}

	// And the closed incident does not block the next outage.
	resp, body = doJSON(t, http.MethodPost, ts.URL+"/v1/events", adminTok, firing)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("recurrence: status = %d, want 201", resp.StatusCode)
	}
	if id, _ := body["id"].(string); id == firstID {
		t.Fatal("the recurrence reused the closed incident")
	}
}

func TestIngestRejectsUnknownStatus(t *testing.T) {
	ts, _ := newTestServer(t, nil)
	resp, _ := doJSON(t, http.MethodPost, ts.URL+"/v1/events", adminTok,
		`{"status":"flapping","type":"incident","source":"monit","message":"m","dedupe_key":"k"}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}

// Health checks are unauthenticated and exist under two names. Monitoring
// configurations in the wild are written against the sub-path form; the
// original paths are part of the compatibility contract and must keep working.
func TestHealthEndpointAliases(t *testing.T) {
	ts, _ := newTestServer(t, nil)
	for _, tc := range []struct{ path, want string }{
		{"/health", "ok"},
		{"/health/live", "ok"},
		{"/ready", "ready"},
		{"/health/ready", "ready"},
	} {
		t.Run(tc.path, func(t *testing.T) {
			resp, body := doJSON(t, http.MethodGet, ts.URL+tc.path, "", "")
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status = %d, want 200", resp.StatusCode)
			}
			if got, _ := body["status"].(string); got != tc.want {
				t.Fatalf("status field = %q, want %q", got, tc.want)
			}
		})
	}
}

// Actions apply to incidents only: an audit entry has nothing to acknowledge.
// The refusal is a 409 that says why.
func TestActionOnNonIncidentIsConflict(t *testing.T) {
	ts, _ := newTestServer(t, nil)
	resp, ev := doJSON(t, http.MethodPost, ts.URL+"/v1/events", adminTok,
		`{"type":"audit","source":"admin","message":"user logged in"}`)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("ingest status = %d", resp.StatusCode)
	}
	id, _ := ev["id"].(string)

	resp, body := doJSON(t, http.MethodPost, ts.URL+"/v1/events/"+id+"/ack", adminTok, "")
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("ack on audit: status = %d, want 409", resp.StatusCode)
	}
	errObj, _ := body["error"].(map[string]any)
	if msg, _ := errObj["message"].(string); !strings.Contains(msg, "incidents only") {
		t.Fatalf("message = %q, want it to say actions apply to incidents only", msg)
	}
}

package api

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// A filter value outside its enum, a limit that is not a positive number, a q
// over 200 characters and a q or source that is not UTF-8 are refused with 400 rather than answered with an
// empty list or the default. q is counted in characters, not bytes.
func TestListParametersAreValidated(t *testing.T) {
	ts, _ := newTestServer(t, nil)
	for _, c := range []struct {
		path string
		want int
	}{
		{"/v1/events?type=incidents", http.StatusBadRequest},
		{"/v1/delivery-attempts?state=done", http.StatusBadRequest},
		{"/v1/delivery-attempts?channel=ntfy", http.StatusOK}, // 0.8.0 channel types
		{"/v1/delivery-attempts?channel=sms", http.StatusBadRequest},
		{"/v1/events?limit=abc", http.StatusBadRequest},
		{"/v1/events?limit=1000", http.StatusOK}, // above the maximum: lowered, as before
		{"/v1/events?q=" + url.QueryEscape(strings.Repeat("я", 200)), http.StatusOK},
		{"/v1/events?q=" + strings.Repeat("x", 201), http.StatusBadRequest},
		{"/v1/events?q=%FF", http.StatusBadRequest},
		{"/v1/events?source=%FF", http.StatusBadRequest},
	} {
		resp, got := doJSON(t, "GET", ts.URL+c.path, adminTok, "")
		if resp.StatusCode != c.want {
			t.Errorf("GET %s = %d %v, want %d", c.path, resp.StatusCode, got, c.want)
		}
	}
}

// q reaches the store: only the event whose message contains it comes back.
func TestListEventsFiltersByQ(t *testing.T) {
	ts, _ := newTestServer(t, nil)
	for _, msg := range []string{"Диск /var заполнен", "PostgreSQL does not respond"} {
		doJSON(t, "POST", ts.URL+"/v1/events", adminTok,
			`{"type":"incident","source":"s","message":"`+msg+`"}`)
	}
	_, got := doJSON(t, "GET", ts.URL+"/v1/events?q="+url.QueryEscape("ДИСК"), adminTok, "")
	items, _ := got["items"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["message"] != "Диск /var заполнен" {
		t.Fatalf("q=ДИСК returned %v, want the disk event only", items)
	}
}

// The routing preview refuses a body over its limit with 413, as ingest does.
func TestRoutingPreviewBodyTooLarge(t *testing.T) {
	ts, _ := newTestServer(t, nil)
	body := `{"source":"` + strings.Repeat("x", maxPreviewBody) + `"}`
	resp, got := doJSON(t, "POST", ts.URL+"/v1/routing/preview", adminTok, body)
	if errBody, _ := got["error"].(map[string]any); resp.StatusCode != http.StatusRequestEntityTooLarge || errBody["code"] != "request_too_large" {
		t.Fatalf("preview over %d bytes = %d %v, want 413 request_too_large", maxPreviewBody, resp.StatusCode, got)
	}
}

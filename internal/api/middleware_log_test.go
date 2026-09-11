package api

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Probes are polled every few seconds — the Compose health check hits
// /health/ready every ten. Under both names they are logged at debug, so an
// info-level log is not a line every ten seconds; everything else stays at info.
func TestAccessLogKeepsProbesAtDebug(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	h := logging(log, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))

	for _, path := range []string{"/health", "/health/live", "/ready", "/health/ready"} {
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, path, nil))
	}
	if buf.Len() != 0 {
		t.Fatalf("probes were logged at info:\n%s", buf.String())
	}

	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/v1/events", nil))
	if !strings.Contains(buf.String(), "path=/v1/events") {
		t.Fatalf("an ordinary request was not logged at info: %q", buf.String())
	}
}

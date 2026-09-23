package api

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/golovanov-dev/alertloop/internal/config"
)

// Probes are polled every few seconds — the Compose health check hits
// /health/ready every ten. Under both names they are logged at debug, so an
// info-level log is not a line every ten seconds; everything else stays at info.
func TestAccessLogKeepsProbesAtDebug(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	h := logging(log, nil, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))

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

// The access log names who made a request: the client address the rate
// limiter counts, and the credential it was accepted with. An API key appears
// only as its key id; a request that was not authenticated has neither.
func TestAccessLogNamesClientAndCredential(t *testing.T) {
	var buf syncBuffer
	const key = "read-key-that-must-not-be-logged"
	ts, _ := newTestServerWith(t, func(c *Config) {
		c.APIKeys = map[string]string{key: config.ScopeRead}
		c.AdminToken = adminTok
		c.Logger = slog.New(slog.NewTextHandler(&buf, nil))
	})

	for _, c := range []struct {
		name, credential, want string
	}{
		{"api key", key, "client_ip=127.0.0.1 credential=" + keyID(key) + " scope=read"},
		{"admin token", adminTok, "client_ip=127.0.0.1 credential=admin scope=full"},
		{"no credential", "", "status=401 duration_ms="},
	} {
		buf.Reset()
		doJSON(t, "GET", ts.URL+"/v1/events", c.credential, "")
		// The line is written after the response has gone out.
		deadline := time.Now().Add(2 * time.Second)
		for !strings.Contains(buf.String(), "msg=http") && time.Now().Before(deadline) {
			time.Sleep(time.Millisecond)
		}
		line := buf.String()
		if !strings.Contains(line, c.want) {
			t.Errorf("%s: log line %q lacks %q", c.name, line, c.want)
		}
		if c.credential == "" && strings.Contains(line, "credential=") {
			t.Errorf("%s: unauthenticated request logged a credential: %q", c.name, line)
		}
		if strings.Contains(line, key) {
			t.Errorf("%s: the API key itself was logged: %q", c.name, line)
		}
	}
}

// syncBuffer is a bytes.Buffer the server goroutine and the test may share.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func (b *syncBuffer) Reset() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.buf.Reset()
}

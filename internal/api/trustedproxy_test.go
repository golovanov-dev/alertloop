package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func req(remote string, headers map[string]string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/v1/events", nil)
	r.RemoteAddr = remote
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	return r
}

// The header is attacker-controlled. Trusting it unconditionally would be worse
// than ignoring it: every request would arrive from a fresh "IP" and the per-IP
// limiter would never fire at all.
func TestClientIPIgnoresForwardedHeadersFromUntrustedPeers(t *testing.T) {
	none, err := NewTrustedProxies(nil)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	got := none.ClientIP(req("203.0.113.9:5555", map[string]string{
		"X-Forwarded-For": "1.2.3.4",
		"X-Real-IP":       "5.6.7.8",
	}))
	if got != "203.0.113.9" {
		t.Fatalf("client = %q, want the peer address: nothing is trusted", got)
	}

	// Configured, but this peer is not one of them.
	trusted, err := NewTrustedProxies([]string{"127.0.0.1", "10.0.0.0/8"})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	got = trusted.ClientIP(req("203.0.113.9:5555", map[string]string{"X-Forwarded-For": "1.2.3.4"}))
	if got != "203.0.113.9" {
		t.Fatalf("client = %q, want the peer: an untrusted peer cannot claim to be a proxy", got)
	}
}

func TestClientIPBelievesTrustedProxies(t *testing.T) {
	trusted, err := NewTrustedProxies([]string{"127.0.0.1", "10.0.0.0/8"})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	cases := []struct {
		name    string
		remote  string
		headers map[string]string
		want    string
	}{
		{
			name:   "single proxy hop",
			remote: "127.0.0.1:44444", want: "198.51.100.7",
			headers: map[string]string{"X-Forwarded-For": "198.51.100.7"},
		},
		{
			name:   "X-Real-IP when there is no XFF",
			remote: "127.0.0.1:44444", want: "198.51.100.7",
			headers: map[string]string{"X-Real-IP": "198.51.100.7"},
		},
		{
			name:   "chained proxies: the rightmost untrusted hop is the client",
			remote: "127.0.0.1:44444", want: "198.51.100.7",
			headers: map[string]string{"X-Forwarded-For": "198.51.100.7, 10.0.0.5"},
		},
		{
			// The forged part is on the LEFT, where anyone can put anything.
			// Walking from the right is what makes it harmless.
			name:   "a client-forged prefix is ignored",
			remote: "127.0.0.1:44444", want: "198.51.100.7",
			headers: map[string]string{"X-Forwarded-For": "1.2.3.4, 198.51.100.7, 10.0.0.5"},
		},
		{
			name:   "every hop trusted: count against the nearest proxy",
			remote: "127.0.0.1:44444", want: "127.0.0.1",
			headers: map[string]string{"X-Forwarded-For": "10.0.0.5"},
		},
		{
			name:   "trusted proxy sending no headers at all",
			remote: "127.0.0.1:44444", want: "127.0.0.1",
		},
		{
			name:   "garbage in the chain does not become a client id",
			remote: "127.0.0.1:44444", want: "127.0.0.1",
			headers: map[string]string{"X-Forwarded-For": "not-an-ip"},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := trusted.ClientIP(req(c.remote, c.headers)); got != c.want {
				t.Fatalf("client = %q, want %q", got, c.want)
			}
		})
	}
}

func TestTrustedProxiesRejectsGarbage(t *testing.T) {
	if _, err := NewTrustedProxies([]string{"nonsense"}); err == nil {
		t.Fatal("expected an error for an unparsable address")
	}
	// Blank entries are skipped rather than refused: a YAML list with an empty
	// line in it is a formatting accident, not a configuration error.
	tp, err := NewTrustedProxies([]string{"", "  ", "127.0.0.1"})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !tp.Configured() {
		t.Fatal("expected the real entry to survive")
	}
}

// Behind the documented nginx setup every request arrives from 127.0.0.1. If
// the limiter counts them all as one client, the whole internet shares a single
// bucket: brute-forcing the admin token becomes unbounded in practice, and one
// noisy client can exhaust the bucket and get everyone else a 429.
func TestPerIPLimitSeparatesClientsBehindAProxy(t *testing.T) {
	trusted, err := NewTrustedProxies([]string{"127.0.0.1"})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	limiter := newKeyedLimiter(1, 2)
	h := perIPLimit(limiter, trusted, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	send := func(clientIP string) int {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req("127.0.0.1:44444", map[string]string{"X-Forwarded-For": clientIP}))
		return rec.Code
	}

	// Burn one client's allowance.
	var blocked bool
	for i := 0; i < 10; i++ {
		if send("198.51.100.7") == http.StatusTooManyRequests {
			blocked = true
			break
		}
	}
	if !blocked {
		t.Fatal("a single client behind the proxy was never limited")
	}

	// A different client behind the same proxy must be unaffected.
	if code := send("198.51.100.8"); code != http.StatusOK {
		t.Fatalf("second client got %d; one noisy client must not lock out the rest", code)
	}
}

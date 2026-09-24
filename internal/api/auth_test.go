package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/golovanov-dev/alertloop/internal/auth"
	"github.com/golovanov-dev/alertloop/internal/routing"
	"github.com/golovanov-dev/alertloop/internal/service"
	"github.com/golovanov-dev/alertloop/internal/storage"
)

const testPassword = "a long enough password"

// authHandler is the full handler over an in-memory store, trusting the
// proxies listed.
func authHandler(t *testing.T, trusted ...string) (http.Handler, storage.Store) {
	t.Helper()
	s, store := authServer(t, trusted...)
	return s.Handler(), store
}

func authServer(t *testing.T, trusted ...string) (*Server, storage.Store) {
	t.Helper()
	store, err := storage.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	tp, err := NewTrustedProxies(trusted)
	if err != nil {
		t.Fatal(err)
	}
	return NewServer(Config{
		Store:          store,
		Ingest:         service.NewIngestService(store, routing.NewAllChannels(nil), 5, nil, time.Now, nil),
		Events:         service.NewEventService(store, nil, time.Now),
		Deliveries:     service.NewDeliveryService(store, time.Now),
		AdminToken:     adminTok,
		TrustedProxies: tp,
	}), store
}

// console is a browser on the console: a cookie jar and the console header.
type console struct {
	t    *testing.T
	base string
	c    *http.Client
}

func newConsole(t *testing.T, base string) *console {
	jar, _ := cookiejar.New(nil)
	return &console{t: t, base: base, c: &http.Client{Jar: jar}}
}

// do sends a request; header adds the console header.
func (b *console) do(method, path, body string, header bool) (int, map[string]any) {
	b.t.Helper()
	req, _ := http.NewRequest(method, b.base+path, strings.NewReader(body))
	if header {
		req.Header.Set(consoleHeader, "1")
	}
	resp, err := b.c.Do(req)
	if err != nil {
		b.t.Fatal(err)
	}
	defer resp.Body.Close()
	var m map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&m)
	return resp.StatusCode, m
}

func errCode(m map[string]any) string {
	e, _ := m["error"].(map[string]any)
	code, _ := e["code"].(string)
	return code
}

func addTestUser(t *testing.T, store storage.Store, login string) {
	t.Helper()
	u, err := auth.NewUser(login, testPassword)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CreateUser(context.Background(), u); err != nil {
		t.Fatal(err)
	}
}

// The first administrator: only with the admin token, only while there are no
// users, and it signs the browser in.
func TestConsoleSetupNeedsAdminTokenAndNoUsers(t *testing.T) {
	h, _ := authHandler(t)
	ts := httptest.NewServer(h)
	defer ts.Close()
	b := newConsole(t, ts.URL)

	if st, m := b.do("GET", "/admin/auth/me", "", false); st != 401 || errCode(m) != "setup_required" {
		t.Fatalf("me before setup: %d %v, want 401 setup_required", st, m)
	}
	if st, _ := b.do("POST", "/admin/auth/setup", `{"admin_token":"wrong","login":"alice","password":"`+testPassword+`"}`, true); st != 401 {
		t.Fatalf("setup with a wrong token: %d, want 401", st)
	}
	if st, m := b.do("POST", "/admin/auth/setup", `{"admin_token":"`+adminTok+`","login":"Alice","password":"`+testPassword+`"}`, true); st != 201 || m["login"] != "alice" {
		t.Fatalf("setup: %d %v, want 201 for alice", st, m)
	}
	if st, m := b.do("GET", "/admin/auth/me", "", false); st != 200 || m["login"] != "alice" {
		t.Fatalf("me after setup: %d %v", st, m)
	}
	other := newConsole(t, ts.URL)
	if st, _ := other.do("POST", "/admin/auth/setup", `{"admin_token":"`+adminTok+`","login":"mallory","password":"`+testPassword+`"}`, true); st != 409 {
		t.Fatalf("second setup: %d, want 409", st)
	}
}

// The session cookie opens /v1 with full scope; a state change made with it
// needs the console header and, when sent, a matching Origin.
func TestConsoleSessionOpensV1AndStateChangesNeedTheConsole(t *testing.T) {
	h, store := authHandler(t)
	ts := httptest.NewServer(h)
	defer ts.Close()
	addTestUser(t, store, "alice")
	b := newConsole(t, ts.URL)

	if st, m := b.do("POST", "/admin/auth/login", `{"login":"ALICE","password":"`+testPassword+`"}`, true); st != 200 {
		t.Fatalf("login: %d %v", st, m)
	}
	if st, _ := b.do("GET", "/v1/routing", "", false); st != 200 {
		t.Fatalf("GET /v1/routing (scope full) with the session: %d, want 200", st)
	}
	action := "/v1/delivery-attempts/00000000-0000-0000-0000-000000000000/replay"
	if st, _ := b.do("POST", action, "", false); st != 403 {
		t.Fatalf("POST without the console header: %d, want 403", st)
	}
	if st, _ := b.do("POST", action, "", true); st != 404 {
		t.Fatalf("POST with the console header: %d, want 404 (authenticated, no such attempt)", st)
	}
	req, _ := http.NewRequest("POST", ts.URL+action, nil)
	req.Header.Set(consoleHeader, "1")
	req.Header.Set("Origin", "https://evil.example")
	resp, err := b.c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 403 {
		t.Fatalf("POST from another origin: %d, want 403", resp.StatusCode)
	}
	if st, _ := b.do("POST", "/admin/auth/login", `{"login":"alice","password":"`+testPassword+`"}`, false); st != 403 {
		t.Fatalf("login without the console header: %d, want 403", st)
	}
}

// An unknown login and a wrong password get the same answer.
func TestSignInFailureDoesNotTellWhichPartWasWrong(t *testing.T) {
	h, store := authHandler(t)
	ts := httptest.NewServer(h)
	defer ts.Close()
	addTestUser(t, store, "alice")
	b := newConsole(t, ts.URL)

	st1, m1 := b.do("POST", "/admin/auth/login", `{"login":"alice","password":"not the password"}`, true)
	st2, m2 := b.do("POST", "/admin/auth/login", `{"login":"nobody","password":"not the password"}`, true)
	if st1 != 401 || st2 != 401 || m1["error"].(map[string]any)["message"] != m2["error"].(map[string]any)["message"] {
		t.Fatalf("wrong password: %d %v; unknown login: %d %v; want the same 401", st1, m1, st2, m2)
	}
}

// Sign-in over plain HTTP is accepted only from this machine or a private
// network reached directly (an SSH tunnel, the Compose gateway); behind a
// trusted proxy that reports HTTPS the cookie is Secure.
func TestSignInTransport(t *testing.T) {
	h, store := authHandler(t, "10.0.0.1")
	addTestUser(t, store, "alice")
	cases := []struct {
		name, peer, proto string
		want              int
		secure            bool
	}{
		{"loopback, plain HTTP", "127.0.0.1:5000", "", 200, false},
		{"private network (Compose gateway), plain HTTP", "172.18.0.1:5000", "", 200, false},
		{"public address, plain HTTP", "203.0.113.5:5000", "", 403, false},
		{"trusted proxy, HTTPS", "10.0.0.1:5000", "https", 200, true},
		{"untrusted peer claiming HTTPS", "203.0.113.5:5000", "https", 403, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := httptest.NewRequest("POST", "/admin/auth/login", strings.NewReader(`{"login":"alice","password":"`+testPassword+`"}`))
			r.RemoteAddr = c.peer
			r.Header.Set(consoleHeader, "1")
			if c.proto != "" {
				r.Header.Set("X-Forwarded-Proto", c.proto)
				r.Header.Set("X-Forwarded-For", "198.51.100.7")
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != c.want {
				t.Fatalf("status %d, want %d: %s", w.Code, c.want, w.Body)
			}
			if c.want != 200 {
				return
			}
			cookie := w.Result().Cookies()[0]
			if cookie.Name != sessionCookie || !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode || cookie.Secure != c.secure {
				t.Fatalf("cookie %+v, want HttpOnly, SameSite=Strict, Secure=%v", cookie, c.secure)
			}
		})
	}
}

// Disabling a user ends their session in the middle of it; so does a password
// reset by another administrator. Nobody disables themselves.
func TestDisablingAUserEndsTheirSession(t *testing.T) {
	h, store := authHandler(t)
	ts := httptest.NewServer(h)
	defer ts.Close()
	addTestUser(t, store, "alice")
	addTestUser(t, store, "bob")
	alice, bob := newConsole(t, ts.URL), newConsole(t, ts.URL)
	alice.do("POST", "/admin/auth/login", `{"login":"alice","password":"`+testPassword+`"}`, true)
	_, me := bob.do("POST", "/admin/auth/login", `{"login":"bob","password":"`+testPassword+`"}`, true)
	_, aliceMe := alice.do("GET", "/admin/auth/me", "", false)

	if st, _ := bob.do("GET", "/v1/events", "", false); st != 200 {
		t.Fatalf("bob before: %d", st)
	}
	if st, _ := alice.do("POST", "/admin/auth/users/"+me["id"].(string)+"/password", `{"password":"another long password"}`, true); st != 204 {
		t.Fatalf("reset bob's password: %d", st)
	}
	if st, _ := bob.do("GET", "/v1/events", "", false); st != 401 {
		t.Fatalf("bob after a password reset: %d, want 401", st)
	}
	bob.do("POST", "/admin/auth/login", `{"login":"bob","password":"another long password"}`, true)
	if st, _ := alice.do("POST", "/admin/auth/users/"+me["id"].(string)+"/disable", "", true); st != 200 {
		t.Fatalf("disable bob: %d", st)
	}
	if st, _ := bob.do("GET", "/v1/events", "", false); st != 401 {
		t.Fatalf("bob after being disabled: %d, want 401", st)
	}
	if st, _ := bob.do("POST", "/admin/auth/login", `{"login":"bob","password":"another long password"}`, true); st != 401 {
		t.Fatalf("disabled bob signs in: %d, want 401", st)
	}
	if st, _ := alice.do("POST", "/admin/auth/users/"+aliceMe["id"].(string)+"/disable", "", true); st != 409 {
		t.Fatalf("alice disables herself: %d, want 409", st)
	}
	if st, m := alice.do("POST", "/admin/auth/users/"+aliceMe["id"].(string)+"/password", `{"password":"another long password"}`, true); st != 409 || errCode(m) != "own_account" {
		t.Fatalf("alice resets her own password without the current one: %d %v, want 409 own_account", st, m)
	}
	if st, _ := alice.do("GET", "/v1/events", "", false); st != 200 {
		t.Fatalf("alice after the refused reset: %d, want 200", st)
	}

	// Enabling brings bob back with the password he had.
	if st, m := alice.do("POST", "/admin/auth/users/"+me["id"].(string)+"/enable", "", true); st != 200 || m["disabled_at"] != nil {
		t.Fatalf("enable bob: %d %v", st, m)
	}
	if st, _ := bob.do("POST", "/admin/auth/login", `{"login":"bob","password":"another long password"}`, true); st != 200 {
		t.Fatalf("enabled bob signs in: %d, want 200", st)
	}
	if st, _ := alice.do("POST", "/admin/auth/users/00000000-0000-0000-0000-000000000000/enable", "", true); st != 404 {
		t.Fatalf("enable an unknown user: %d, want 404", st)
	}
}

// Origin is checked against the host the browser reached: the Host header, or
// X-Forwarded-Host from a trusted proxy. A proxy that drops the port from Host
// does not break a console on a non-standard port; the refusal says what to
// fix in the proxy.
func TestOriginMatchesTheHostTheBrowserReached(t *testing.T) {
	h, store := authHandler(t, "10.0.0.1")
	addTestUser(t, store, "alice")
	cases := []struct {
		name, peer, host, fwdHost, origin string
		want                              int
	}{
		{"direct, same host and port", "127.0.0.1:5000", "127.0.0.1:8080", "", "http://127.0.0.1:8080", 200},
		{"direct, another port", "127.0.0.1:5000", "127.0.0.1:8080", "", "http://127.0.0.1:9090", 403},
		{"proxy keeps Host without the port (nginx $host)", "10.0.0.1:5000", "alerts.example.com", "", "https://alerts.example.com:8443", 200},
		{"proxy keeps Host with the default port", "10.0.0.1:5000", "alerts.example.com:443", "", "https://alerts.example.com", 200},
		{"proxy rewrites Host, trusted X-Forwarded-Host", "10.0.0.1:5000", "127.0.0.1:8080", "alerts.example.com:8443", "https://alerts.example.com:8443", 200},
		{"proxy rewrites Host, no X-Forwarded-Host", "10.0.0.1:5000", "127.0.0.1:8080", "", "https://alerts.example.com", 403},
		{"untrusted peer sends X-Forwarded-Host", "127.0.0.1:5000", "127.0.0.1:8080", "alerts.example.com", "https://alerts.example.com", 403},
		{"another site", "10.0.0.1:5000", "alerts.example.com", "", "https://evil.example", 403},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := httptest.NewRequest("POST", "/admin/auth/login", strings.NewReader(`{"login":"alice","password":"`+testPassword+`"}`))
			r.RemoteAddr = c.peer
			r.Host = c.host
			r.Header.Set(consoleHeader, "1")
			r.Header.Set("Origin", c.origin)
			if c.fwdHost != "" {
				r.Header.Set("X-Forwarded-Host", c.fwdHost)
			}
			if strings.HasPrefix(c.peer, "10.") {
				r.Header.Set("X-Forwarded-Proto", "https")
				r.Header.Set("X-Forwarded-For", "198.51.100.7")
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != c.want {
				t.Fatalf("status %d, want %d: %s", w.Code, c.want, w.Body)
			}
			if c.want == 403 && !strings.Contains(w.Body.String(), "proxy_set_header Host") {
				t.Fatalf("the refusal does not say how to fix the proxy: %s", w.Body)
			}
		})
	}
}

// When every password-check slot is busy, a sign-in is refused with 429 and
// Retry-After before any hashing, and does not count as a failed attempt.
func TestPasswordChecksAreBounded(t *testing.T) {
	s, store := authServer(t)
	h := s.Handler()
	addTestUser(t, store, "alice")
	login := func() *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/admin/auth/login", strings.NewReader(`{"login":"alice","password":"`+testPassword+`"}`))
		r.RemoteAddr = "127.0.0.1:5000"
		r.Header.Set(consoleHeader, "1")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	for range maxPasswordChecks {
		s.passwordChecks <- struct{}{}
	}
	if w := login(); w.Code != http.StatusTooManyRequests || w.Header().Get("Retry-After") == "" {
		t.Fatalf("all slots busy: %d, Retry-After %q; want 429 with Retry-After", w.Code, w.Header().Get("Retry-After"))
	}
	<-s.passwordChecks
	if w := login(); w.Code != 200 {
		t.Fatalf("a slot free: %d, want 200: %s", w.Code, w.Body)
	}
	if n := len(s.passwordChecks); n != maxPasswordChecks-1 {
		t.Fatalf("%d slots held after the check, want %d: the slot was not released", n, maxPasswordChecks-1)
	}
}

// The failed-sign-in delay counts an IPv6 client by its /64, as the
// per-address limiter does: another address in the same /64 is still held
// back, an address in another /64 is not.
func TestLoginDelayIsPerIPv6Slash64(t *testing.T) {
	s, store := authServer(t)
	h := s.Handler()
	addTestUser(t, store, "alice")
	login := func(peer, password string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/admin/auth/login", strings.NewReader(`{"login":"alice","password":"`+password+`"}`))
		r.RemoteAddr = peer
		r.Header.Set(consoleHeader, "1")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	for i := range 6 { // five are free, the sixth starts the delay
		if w := login(fmt.Sprintf("[fd00::%d]:5000", i+1), "wrong-password-123"); w.Code != http.StatusUnauthorized {
			t.Fatalf("failure %d: %d, want 401: %s", i+1, w.Code, w.Body)
		}
	}
	w := login("[fd00::99]:5000", testPassword)
	if w.Code != http.StatusTooManyRequests || !strings.Contains(w.Body.String(), "failed sign-ins") {
		t.Fatalf("same /64, new address: %d, want 429 from the sign-in delay: %s", w.Code, w.Body)
	}
	if w := login("[fd00:0:0:1::1]:5000", testPassword); w.Code != 200 {
		t.Fatalf("another /64: %d, want 200: %s", w.Code, w.Body)
	}
}

// Log out ends this session; "everywhere" ends the others too.
func TestLogoutEverywhere(t *testing.T) {
	h, store := authHandler(t)
	ts := httptest.NewServer(h)
	defer ts.Close()
	addTestUser(t, store, "alice")
	laptop, phone := newConsole(t, ts.URL), newConsole(t, ts.URL)
	for _, b := range []*console{laptop, phone} {
		b.do("POST", "/admin/auth/login", `{"login":"alice","password":"`+testPassword+`"}`, true)
	}
	if st, _ := laptop.do("POST", "/admin/auth/logout", `{"everywhere":true}`, true); st != 204 {
		t.Fatalf("logout: %d", st)
	}
	for name, b := range map[string]*console{"laptop": laptop, "phone": phone} {
		if st, _ := b.do("GET", "/v1/events", "", false); st != 401 {
			t.Errorf("%s after logout everywhere: %d, want 401", name, st)
		}
	}
}

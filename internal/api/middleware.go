package api

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/golovanov-dev/alertloop/internal/config"
)

// callerCtxKey carries the authenticated caller through the request.
type callerCtxKey struct{}

// caller is what a request's credential allows: its scope and, for an ingest
// key limited by `sources`, the sources it may report (nil: every source).
type caller struct {
	scope   string
	sources []string
}

// callerOf returns the caller apiKeyAuth stored on r.
func callerOf(r *http.Request) caller {
	c, _ := r.Context().Value(callerCtxKey{}).(caller)
	return c
}

// apiKeyAuth guards the JSON API. A request is accepted when it presents a known
// API key (via `Authorization: Bearer <key>` or the `X-API-Key` header) OR the
// admin token (the full-access admin console credential). The key's scope and
// sources are stored on the request context: requireScope enforces the scope
// per endpoint, ingestion enforces the sources. With no keys and no admin
// token every request is refused; the process does not start in that state
// (app.RequireCredential).
func apiKeyAuth(keyScopes map[string]string, keySources map[string][]string, adminToken string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		presented := extractAPIKey(r)
		if presented == "" {
			writeError(w, http.StatusUnauthorized, "unauthorized", "missing API key or admin token")
			return
		}
		if key, ok := lookupKey(keyScopes, presented); ok {
			noteCredential(r, keyID(key), keyScopes[key])
			next.ServeHTTP(w, withCaller(r, caller{scope: keyScopes[key], sources: keySources[key]}))
			return
		}
		// The admin token authenticates the API with full scope.
		if adminToken != "" && subtle.ConstantTimeCompare([]byte(presented), []byte(adminToken)) == 1 {
			if adminToken == config.DemoAdminToken && !demoTokenAllowed(r) {
				writeError(w, http.StatusForbidden, "forbidden",
					"the public demo admin token change-me-admin is refused through a reverse proxy "+
						"or from a public address; set your own admin token "+
						"(ALERTLOOP_ADMIN_TOKEN in .env, or admin_token in alertloop.yaml)")
				return
			}
			noteCredential(r, "admin", config.ScopeFull)
			next.ServeHTTP(w, withCaller(r, caller{scope: config.ScopeFull}))
			return
		}
		writeError(w, http.StatusUnauthorized, "unauthorized", "invalid API key or admin token")
	})
}

// demoTokenAllowed reports whether a request may use the public demo admin
// token: it came straight from a loopback or private address, not through a
// reverse proxy. Private ranges are allowed because under Docker a request from
// the host reaches the container from the bridge gateway (172.x).
func demoTokenAllowed(r *http.Request) bool {
	for _, h := range []string{"X-Forwarded-For", "Forwarded", "X-Real-IP"} {
		if r.Header.Get(h) != "" {
			return false
		}
	}
	ip := net.ParseIP(hostOnly(r.RemoteAddr))
	return ip != nil && (ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast())
}

// lookupKey finds the configured key equal to presented without leaking,
// through timing, how much of a key was guessed.
//
// A plain `map[string]string` lookup compares byte by byte and stops at the
// first difference. That is a far weaker signal than a prefix comparison on a
// raw string, and on its own it would be an acceptable risk - but the admin
// token three lines below is compared with subtle.ConstantTimeCompare, so the
// file contradicted itself, and the per-IP limiter that was supposed to bound
// guessing did not work behind a reverse proxy at all (fixed separately).
//
// Comparing SHA-256 digests removes the question: every candidate costs the
// same, and the loop deliberately does not break early.
func lookupKey(keyScopes map[string]string, presented string) (string, bool) {
	if len(keyScopes) == 0 || presented == "" {
		return "", false
	}
	sum := sha256.Sum256([]byte(presented))
	var (
		match string
		found bool
	)
	for key := range keyScopes {
		candidate := sha256.Sum256([]byte(key))
		if subtle.ConstantTimeCompare(sum[:], candidate[:]) == 1 {
			match, found = key, true
		}
	}
	return match, found
}

// keyID names an API key in the log without revealing it: the first 8 hex
// digits of its SHA-256.
func keyID(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:4])
}

func withCaller(r *http.Request, c caller) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), callerCtxKey{}, c))
}

// requireScope wraps a handler so it only runs when the caller's scope permits
// the operation. ScopeFull satisfies every requirement.
func requireScope(needed string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		have := callerOf(r).scope
		if have == config.ScopeFull || have == needed {
			next(w, r)
			return
		}
		writeError(w, http.StatusForbidden, "forbidden",
			"this API key lacks the required scope: "+needed)
	}
}

func extractAPIKey(r *http.Request) string {
	if h := r.Header.Get("Authorization"); h != "" {
		if after, ok := strings.CutPrefix(h, "Bearer "); ok {
			return strings.TrimSpace(after)
		}
	}
	return strings.TrimSpace(r.Header.Get("X-API-Key"))
}

// securityHeaders adds conservative security headers to every response.
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		next.ServeHTTP(w, r)
	})
}

// probePaths are the health and readiness endpoints under both of their names:
// logged at debug (see logging) and exempt from the per-IP limit (perIPLimit).
var probePaths = map[string]bool{
	"/health":       true,
	"/health/live":  true,
	"/ready":        true,
	"/health/ready": true,
}

// accessCtxKey carries the *accessCredential of a request.
type accessCtxKey struct{}

// accessCredential is the credential apiKeyAuth accepted, for the access log.
type accessCredential struct {
	id, scope string
}

// noteCredential tells the access log which credential r was accepted with.
func noteCredential(r *http.Request, id, scope string) {
	if c, ok := r.Context().Value(accessCtxKey{}).(*accessCredential); ok {
		c.id, c.scope = id, scope
	}
}

// logging is a minimal structured access log middleware. client_ip is the
// address the per-IP rate limiter counts; credential and scope are there when
// the request was authenticated. Health and readiness probes (hit every few
// seconds by Docker/orchestrators) are logged at debug so they do not drown
// the access log: at info the Compose health check alone adds a line every ten
// seconds.
func logging(log *slog.Logger, trusted *TrustedProxies, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		cred := &accessCredential{}
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(sw, r.WithContext(context.WithValue(r.Context(), accessCtxKey{}, cred)))
		level := slog.LevelInfo
		if probePaths[r.URL.Path] {
			level = slog.LevelDebug
		}
		attrs := []any{
			"method", r.Method,
			"path", r.URL.Path,
			"status", sw.status,
			"duration_ms", time.Since(start).Milliseconds(),
			"client_ip", trusted.ClientIP(r),
		}
		if cred.id != "" {
			attrs = append(attrs, "credential", cred.id, "scope", cred.scope)
		}
		log.Log(r.Context(), level, "http", attrs...)
	})
}

type statusWriter struct {
	http.ResponseWriter
	status int
	wrote  bool
}

func (w *statusWriter) WriteHeader(code int) {
	if !w.wrote {
		w.status = code
		w.wrote = true
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	w.wrote = true
	return w.ResponseWriter.Write(b)
}

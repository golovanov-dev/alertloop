package api

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/golovanov-dev/alertloop/internal/config"
)

// scopeCtxKey carries the authenticated caller's scope through the request.
type scopeCtxKey struct{}

// apiKeyAuth guards the JSON API. A request is accepted when it presents a known
// API key (via `Authorization: Bearer <key>` or the `X-API-Key` header) OR the
// admin token (the full-access admin console credential). The resolved scope is
// stored on the request context for per-endpoint enforcement by requireScope.
// With no keys and no admin token every request is refused; the process does
// not start in that state (app.RequireCredential).
func apiKeyAuth(keyScopes map[string]string, adminToken string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		presented := extractAPIKey(r)
		if presented == "" {
			writeError(w, http.StatusUnauthorized, "unauthorized", "missing API key or admin token")
			return
		}
		if scope, ok := lookupKey(keyScopes, presented); ok {
			next.ServeHTTP(w, withScope(r, scope))
			return
		}
		// The admin token authenticates the API with full scope.
		if adminToken != "" && subtle.ConstantTimeCompare([]byte(presented), []byte(adminToken)) == 1 {
			next.ServeHTTP(w, withScope(r, config.ScopeFull))
			return
		}
		writeError(w, http.StatusUnauthorized, "unauthorized", "invalid API key or admin token")
	})
}

// lookupKey resolves a presented API key to its scope without leaking, through
// timing, how much of a key was guessed.
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
		scope string
		found bool
	)
	for key, s := range keyScopes {
		candidate := sha256.Sum256([]byte(key))
		if subtle.ConstantTimeCompare(sum[:], candidate[:]) == 1 {
			scope, found = s, true
		}
	}
	return scope, found
}

func withScope(r *http.Request, scope string) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), scopeCtxKey{}, scope))
}

// requireScope wraps a handler so it only runs when the caller's scope permits
// the operation. ScopeFull satisfies every requirement.
func requireScope(needed string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		have, _ := r.Context().Value(scopeCtxKey{}).(string)
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

// probePaths are the health and readiness endpoints under both of their names.
// The Compose health check polls /health/ready every ten seconds; logged at
// info, that is a line every ten seconds in stdout and in the log file, around
// the lines anyone actually needs.
var probePaths = map[string]bool{
	"/health":       true,
	"/health/live":  true,
	"/ready":        true,
	"/health/ready": true,
}

// logging is a minimal structured access log middleware. Health and readiness
// probes (hit every few seconds by Docker/orchestrators) are logged at debug so
// they do not drown the access log.
func logging(log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(sw, r)
		level := slog.LevelInfo
		if probePaths[r.URL.Path] {
			level = slog.LevelDebug
		}
		log.Log(r.Context(), level, "http",
			"method", r.Method,
			"path", r.URL.Path,
			"status", sw.status,
			"duration_ms", time.Since(start).Milliseconds(),
		)
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

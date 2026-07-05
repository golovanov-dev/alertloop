package api

import (
	"context"
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
// When no keys AND no admin token are configured the API is open with full
// scope (useful for local demos); this is logged at startup by the server.
func apiKeyAuth(keyScopes map[string]string, adminToken string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if len(keyScopes) == 0 && adminToken == "" {
			next.ServeHTTP(w, withScope(r, config.ScopeFull))
			return
		}
		presented := extractAPIKey(r)
		if presented == "" {
			writeError(w, http.StatusUnauthorized, "unauthorized", "missing API key or admin token")
			return
		}
		if scope, ok := keyScopes[presented]; ok {
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

// adminTokenAuth guards the events web page and admin-only endpoints with a
// single shared token (no user accounts in Community). The token may be provided
// via `Authorization: Bearer`, the `X-Admin-Token` header, or a `token` query
// parameter (so the page is reachable from a browser).
func adminTokenAuth(token string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if token == "" {
			writeError(w, http.StatusServiceUnavailable, "admin_disabled",
				"admin token is not configured; set ALERTLOOP_ADMIN_TOKEN to enable the events page")
			return
		}
		presented := extractAdminToken(r)
		if subtle.ConstantTimeCompare([]byte(presented), []byte(token)) != 1 {
			writeError(w, http.StatusUnauthorized, "unauthorized", "invalid admin token")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func extractAdminToken(r *http.Request) string {
	if h := r.Header.Get("Authorization"); h != "" {
		if after, ok := strings.CutPrefix(h, "Bearer "); ok {
			return strings.TrimSpace(after)
		}
	}
	if h := r.Header.Get("X-Admin-Token"); h != "" {
		return strings.TrimSpace(h)
	}
	return strings.TrimSpace(r.URL.Query().Get("token"))
}

// cors enables cross-origin requests from the configured admin console
// origins. It reflects an allowed Origin, permits the admin/API headers, and
// answers preflight OPTIONS requests. Needed only when the admin UI is served
// from a different origin (standalone deployment); same-origin needs no CORS.
func cors(allowed []string, next http.Handler) http.Handler {
	allowAll := false
	set := map[string]bool{}
	for _, o := range allowed {
		if o == "*" {
			allowAll = true
		}
		set[o] = true
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin != "" && (allowAll || set[origin]) {
			h := w.Header()
			if allowAll {
				h.Set("Access-Control-Allow-Origin", "*")
			} else {
				h.Set("Access-Control-Allow-Origin", origin)
				h.Add("Vary", "Origin")
			}
			h.Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
			h.Set("Access-Control-Allow-Headers", "Authorization, X-API-Key, X-Admin-Token, Content-Type")
			h.Set("Access-Control-Max-Age", "600")
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// securityHeaders adds conservative security headers to every response.
// Referrer-Policy is the important one here: it stops the admin `?token=` in
// the events page URL from leaking to third parties via the Referer header.
// (A strict Content-Security-Policy is set per-page on the events pages, since a
// global one would break the Swagger UI assets.)
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		next.ServeHTTP(w, r)
	})
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
		if r.URL.Path == "/health" || r.URL.Path == "/ready" {
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

package api

import (
	"log/slog"
	"net/http"

	"github.com/golovanov-dev/alertloop/internal/adminui"
	"github.com/golovanov-dev/alertloop/internal/auth"
	"github.com/golovanov-dev/alertloop/internal/config"
	"github.com/golovanov-dev/alertloop/internal/routing"
	"github.com/golovanov-dev/alertloop/internal/service"
	"github.com/golovanov-dev/alertloop/internal/storage"
	"github.com/swaggest/swgui/v5emb"
)

// Server holds the HTTP handlers and their service dependencies.
type Server struct {
	store          storage.Store
	ingest         *service.IngestService
	events         *service.EventService
	deliveries     *service.DeliveryService
	routing        *routing.Router
	apiKeys        map[string]string   // key -> scope
	apiKeySources  map[string][]string // key -> allowed sources
	adminToken     string
	version        string
	trustedProxies *TrustedProxies
	log            *slog.Logger

	// Rate limiters (nil when disabled).
	ipLimiter     *keyedLimiter
	ingestLimiter *tokenBucket
	// loginLimiter bounds console sign-in attempts per client address; it is
	// on even when rate_limit is off. throttle backs off per login and
	// address. passwordChecks bounds the argon2id checks running at once.
	loginLimiter   *keyedLimiter
	throttle       *auth.Throttle
	passwordChecks chan struct{}
}

// Config wires a Server.
type Config struct {
	Store      storage.Store
	Ingest     *service.IngestService
	Events     *service.EventService
	Deliveries *service.DeliveryService
	// Routing backs the routing preview endpoints. Nil is treated as "routing
	// not configured".
	Routing *routing.Router
	APIKeys map[string]string // key -> scope (ingest|read|full)
	// APIKeySources limits ingest keys to their sources: key -> allowed
	// `source` values. A key absent from it may report every source.
	APIKeySources map[string][]string
	AdminToken    string
	Version       string
	RateLimit     config.RateLimit
	Logger        *slog.Logger
	// TrustedProxies decides whose X-Forwarded-For is believed when the per-IP
	// limiter identifies a client. Nil trusts nothing.
	TrustedProxies *TrustedProxies
}

// Console sign-in attempts per client address: a burst of 10, then one every
// 6 seconds.
const (
	loginPerSecond = 1.0 / 6
	loginBurst     = 10
)

// NewServer builds a Server from its dependencies.
func NewServer(c Config) *Server {
	log := c.Logger
	if log == nil {
		log = slog.Default()
	}
	router := c.Routing
	if router == nil {
		router = routing.NewAllChannels(nil)
	}
	s := &Server{
		store:          c.Store,
		ingest:         c.Ingest,
		events:         c.Events,
		deliveries:     c.Deliveries,
		routing:        router,
		apiKeys:        c.APIKeys,
		apiKeySources:  c.APIKeySources,
		adminToken:     c.AdminToken,
		version:        c.Version,
		trustedProxies: c.TrustedProxies,
		log:            log,
		loginLimiter:   newKeyedLimiter(loginPerSecond, loginBurst),
		throttle:       auth.NewThrottle(nil),
		passwordChecks: make(chan struct{}, maxPasswordChecks),
	}
	if c.RateLimit.Enabled {
		s.ipLimiter = newKeyedLimiter(c.RateLimit.PerIPPerSecond, c.RateLimit.PerIPBurst)
		s.ingestLimiter = newTokenBucket(c.RateLimit.IngestPerSecond, c.RateLimit.IngestBurst)
	}
	return s
}

// Handler builds the root HTTP handler with all routes and middleware.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	// JSON API. Each endpoint requires a scope: ingest to create events, read to
	// query, full for state actions and replay. The admin token has full scope.
	api := http.NewServeMux()
	// Ingest carries an extra global rate limit on top of the per-IP limit.
	api.Handle("POST /v1/events",
		requireScope(config.ScopeIngest, s.maybeIngestLimit(http.HandlerFunc(s.handleIngest)).ServeHTTP))
	api.HandleFunc("GET /v1/events", requireScope(config.ScopeRead, s.handleListEvents))
	api.HandleFunc("GET /v1/events/{id}", requireScope(config.ScopeRead, s.handleGetEvent))
	api.HandleFunc("POST /v1/events/{id}/{action}", requireScope(config.ScopeFull, s.handleEventAction))
	api.HandleFunc("GET /v1/delivery-attempts", requireScope(config.ScopeRead, s.handleListDeliveries))
	api.HandleFunc("POST /v1/delivery-attempts/{id}/replay", requireScope(config.ScopeFull, s.handleReplay))
	// Routing inspection requires full scope: the table names every channel and
	// the preview is an operator tool, not a dashboard read.
	api.HandleFunc("GET /v1/routing", requireScope(config.ScopeFull, s.handleRoutingTable))
	api.HandleFunc("POST /v1/routing/preview", requireScope(config.ScopeFull, s.handleRoutingPreview))
	api.HandleFunc("GET /v1/stats", requireScope(config.ScopeRead, s.handleStats))
	api.HandleFunc("GET /v1/info", requireScope(config.ScopeRead, s.handleInfo))
	mux.Handle("/v1/", s.sessionOrKey(apiKeyAuth(s.apiKeys, s.apiKeySources, s.adminToken, api), api))

	// Admin console SPA (static, unguarded assets) and its sign-in endpoints:
	// the console authenticates with a session cookie (auth.go).
	adminui.Register(mux)
	s.registerAuth(mux)

	// Health/readiness (unguarded).
	mux.HandleFunc("GET /health", s.handleHealth)
	mux.HandleFunc("GET /ready", s.handleReady)
	// Sub-path aliases. Monitoring tools and orchestrators are overwhelmingly
	// configured against /health/live and /health/ready, and two names for the
	// same check cost less than an integration guide explaining why ours differ.
	// The original paths are part of the compatibility contract and stay.
	mux.HandleFunc("GET /health/live", s.handleHealth)
	mux.HandleFunc("GET /health/ready", s.handleReady)

	// OpenAPI spec and Swagger UI (unguarded; the spec is public contract).
	mux.HandleFunc("GET /openapi.yaml", s.handleOpenAPISpec)
	mux.Handle("/swagger/", v5emb.New("AlertLoop API", "/openapi.yaml", "/swagger/"))
	mux.HandleFunc("GET /swagger", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/swagger/", http.StatusFound)
	})

	// Root redirect to Swagger for convenience.
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/swagger", http.StatusFound)
	})

	var h http.Handler = mux
	if s.ipLimiter != nil {
		h = perIPLimit(s.ipLimiter, s.trustedProxies, h)
	}
	return logging(s.log, s.trustedProxies, securityHeaders(h))
}

// maybeIngestLimit applies the global ingest token bucket when rate limiting is
// enabled, otherwise passes through.
func (s *Server) maybeIngestLimit(next http.Handler) http.Handler {
	if s.ingestLimiter == nil {
		return next
	}
	return globalLimit(s.ingestLimiter, next)
}

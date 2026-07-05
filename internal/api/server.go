package api

import (
	"log/slog"
	"net/http"

	"github.com/golovanov-dev/alertloop/internal/adminui"
	"github.com/golovanov-dev/alertloop/internal/config"
	"github.com/golovanov-dev/alertloop/internal/service"
	"github.com/golovanov-dev/alertloop/internal/storage"
	"github.com/swaggest/swgui/v5emb"
)

// Server holds the HTTP handlers and their service dependencies.
type Server struct {
	store       storage.Store
	ingest      *service.IngestService
	events      *service.EventService
	deliveries  *service.DeliveryService
	apiKeys     map[string]string // key -> scope
	adminToken  string
	version     string
	corsOrigins []string
	log         *slog.Logger

	// Rate limiters (nil when disabled).
	ipLimiter     *keyedLimiter
	ingestLimiter *tokenBucket
}

// Config wires a Server.
type Config struct {
	Store       storage.Store
	Ingest      *service.IngestService
	Events      *service.EventService
	Deliveries  *service.DeliveryService
	APIKeys     map[string]string // key -> scope (ingest|read|full)
	AdminToken  string
	Version     string
	RateLimit   config.RateLimit
	CORSOrigins []string
	Logger      *slog.Logger
}

// NewServer builds a Server from its dependencies.
func NewServer(c Config) *Server {
	log := c.Logger
	if log == nil {
		log = slog.Default()
	}
	s := &Server{
		store:       c.Store,
		ingest:      c.Ingest,
		events:      c.Events,
		deliveries:  c.Deliveries,
		apiKeys:     c.APIKeys,
		adminToken:  c.AdminToken,
		version:     c.Version,
		corsOrigins: c.CORSOrigins,
		log:         log,
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
	api.HandleFunc("GET /v1/stats", requireScope(config.ScopeRead, s.handleStats))
	api.HandleFunc("GET /v1/info", requireScope(config.ScopeRead, s.handleInfo))
	mux.Handle("/v1/", apiKeyAuth(s.apiKeys, s.adminToken, api))

	// Admin console SPA (static, unguarded assets — the app authenticates via
	// the API using the admin token).
	adminui.Register(mux)

	// Health/readiness (unguarded).
	mux.HandleFunc("GET /health", s.handleHealth)
	mux.HandleFunc("GET /ready", s.handleReady)

	// OpenAPI spec and Swagger UI (unguarded; the spec is public contract).
	mux.HandleFunc("GET /openapi.yaml", s.handleOpenAPISpec)
	mux.Handle("/swagger/", v5emb.New("AlertLoop API", "/openapi.yaml", "/swagger/"))
	mux.HandleFunc("GET /swagger", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/swagger/", http.StatusFound)
	})

	// Events web page (guarded by admin token).
	mux.Handle("GET /events", adminTokenAuth(s.adminToken, http.HandlerFunc(s.handleEventsPage)))
	mux.Handle("GET /events/{id}", adminTokenAuth(s.adminToken, http.HandlerFunc(s.handleEventDetailPage)))
	mux.Handle("GET /deliveries", adminTokenAuth(s.adminToken, http.HandlerFunc(s.handleDeliveriesPage)))

	// Root redirect to Swagger for convenience.
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/swagger", http.StatusFound)
	})

	var h http.Handler = mux
	if s.ipLimiter != nil {
		h = perIPLimit(s.ipLimiter, h)
	}
	if len(s.corsOrigins) > 0 {
		h = cors(s.corsOrigins, h)
	}
	return logging(s.log, securityHeaders(h))
}

// maybeIngestLimit applies the global ingest token bucket when rate limiting is
// enabled, otherwise passes through.
func (s *Server) maybeIngestLimit(next http.Handler) http.Handler {
	if s.ingestLimiter == nil {
		return next
	}
	return globalLimit(s.ingestLimiter, next)
}

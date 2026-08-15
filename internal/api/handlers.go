package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/golovanov-dev/alertloop/internal/domain"
	"github.com/golovanov-dev/alertloop/internal/routing"
	"github.com/golovanov-dev/alertloop/internal/service"
	"github.com/golovanov-dev/alertloop/internal/storage"
)

// handleIngest implements POST /v1/events.
func (s *Server) handleIngest(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, domain.MaxPayloadBytes*2))
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "could not read request body")
		return
	}
	var in service.EventInput
	if err := json.Unmarshal(body, &in); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON body")
		return
	}

	event, created, err := s.ingest.Ingest(r.Context(), in)
	if err != nil {
		// Ingestion is atomic: the event and its delivery jobs are stored
		// together or not at all, so any error means nothing was persisted.
		writeDomainError(w, err)
		return
	}

	status := http.StatusOK // dedupe hit: existing event returned
	if created {
		status = http.StatusCreated
	}
	writeJSON(w, status, event)
}

// handleListEvents implements GET /v1/events.
func (s *Server) handleListEvents(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := storage.EventFilter{
		Type:     domain.EventType(q.Get("type")),
		Severity: domain.Severity(q.Get("severity")),
		State:    domain.EventState(q.Get("state")),
		Source:   q.Get("source"),
	}
	page, err := s.events.List(r.Context(), f, parseLimit(q.Get("limit")), q.Get("cursor"))
	if err != nil {
		writeDomainError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, listResponse[domain.Event]{Items: orEmpty(page.Items), NextCursor: page.NextCursor})
}

// handleGetEvent implements GET /v1/events/{id}.
func (s *Server) handleGetEvent(w http.ResponseWriter, r *http.Request) {
	event, err := s.events.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		writeDomainError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, event)
}

// handleEventAction implements the POST /v1/events/{id}/{action} transitions.
func (s *Server) handleEventAction(w http.ResponseWriter, r *http.Request) {
	action, ok := domain.ParseAction(r.PathValue("action"))
	if !ok {
		writeError(w, http.StatusNotFound, "not_found", "unknown event action")
		return
	}
	event, err := s.events.Apply(r.Context(), r.PathValue("id"), action)
	if err != nil {
		writeDomainError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, event)
}

// handleListDeliveries implements GET /v1/delivery-attempts.
func (s *Server) handleListDeliveries(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := storage.DeliveryFilter{
		State:       domain.DeliveryState(q.Get("state")),
		Channel:     domain.ChannelType(q.Get("channel")),
		ChannelName: q.Get("channel_name"),
		EventID:     q.Get("event_id"),
	}
	page, err := s.deliveries.List(r.Context(), f, parseLimit(q.Get("limit")), q.Get("cursor"))
	if err != nil {
		writeDomainError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, listResponse[domain.DeliveryAttempt]{Items: orEmpty(page.Items), NextCursor: page.NextCursor})
}

// handleReplay implements POST /v1/delivery-attempts/{id}/replay.
func (s *Server) handleReplay(w http.ResponseWriter, r *http.Request) {
	att, err := s.deliveries.Replay(r.Context(), r.PathValue("id"))
	if err != nil {
		writeDomainError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, att)
}

// routingTableResponse is the resolved routing table as GET /v1/routing
// returns it.
type routingTableResponse struct {
	// Configured is false when no routing section is present; every event is
	// then delivered to every configured channel, which `default` lists.
	Configured bool               `json:"configured"`
	Rules      []routing.RuleView `json:"rules"`
	Default    []string           `json:"default"`
}

// handleRoutingTable implements GET /v1/routing.
func (s *Server) handleRoutingTable(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, routingTableResponse{
		Configured: s.routing.Configured(),
		Rules:      orEmpty(s.routing.Rules()),
		Default:    orEmpty(s.routing.Default()),
	})
}

// routingPreviewRequest is the event shape POST /v1/routing/preview matches.
// Only the fields routing rules can look at are accepted.
type routingPreviewRequest struct {
	Type     domain.EventType `json:"type"`
	Severity domain.Severity  `json:"severity"`
	Source   string           `json:"source"`
	Category string           `json:"category"`
}

// routingPreviewResponse reports where such an event would go.
type routingPreviewResponse struct {
	// MatchedRule is empty when no rule matched; Channels is then the routing
	// default.
	MatchedRule string   `json:"matched_rule"`
	Channels    []string `json:"channels"`
}

// handleRoutingPreview implements POST /v1/routing/preview. It answers "where
// would this event go" without creating or delivering anything, so rules can be
// checked on a live server without inventing false incidents.
func (s *Server) handleRoutingPreview(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 64*1024))
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "could not read request body")
		return
	}
	var in routingPreviewRequest
	if len(body) > 0 {
		if err := json.Unmarshal(body, &in); err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON body")
			return
		}
	}
	if in.Type != "" && !domain.ValidEventType(in.Type) {
		writeError(w, http.StatusBadRequest, "validation_failed", "invalid type")
		return
	}
	if in.Severity == "" {
		in.Severity = domain.SeverityInfo // same default ingestion applies
	}
	if !domain.ValidSeverity(in.Severity) {
		writeError(w, http.StatusBadRequest, "validation_failed", "invalid severity")
		return
	}

	decision := s.routing.Route(&domain.Event{
		Type:     in.Type,
		Severity: in.Severity,
		Source:   in.Source,
		Category: in.Category,
	})
	names := make([]string, 0, len(decision.Channels))
	for _, t := range decision.Channels {
		names = append(names, t.Name)
	}
	writeJSON(w, http.StatusOK, routingPreviewResponse{MatchedRule: decision.Rule, Channels: names})
}

// handleHealth implements GET /health (liveness).
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleReady implements GET /ready (readiness). It verifies DB connectivity.
func (s *Server) handleReady(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	if err := s.store.Ping(ctx); err != nil {
		writeError(w, http.StatusServiceUnavailable, "not_ready", "database unavailable")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

// statsResponse aggregates counts for the admin console overview.
type statsResponse struct {
	Events     map[string]int64 `json:"events"`
	Deliveries map[string]int64 `json:"deliveries"`
}

// handleStats implements GET /v1/stats — event and delivery counts by state.
func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	events, err := s.store.CountEventsByState(r.Context())
	if err != nil {
		writeDomainError(w, err)
		return
	}
	deliveries, err := s.store.CountDeliveriesByState(r.Context())
	if err != nil {
		writeDomainError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, statsResponse{Events: events, Deliveries: deliveries})
}

// handleInfo implements GET /v1/info — build/version and edition metadata.
func (s *Server) handleInfo(w http.ResponseWriter, r *http.Request) {
	version := s.version
	if version == "" {
		version = "dev"
	}
	writeJSON(w, http.StatusOK, map[string]string{
		"version": version,
		"edition": "community",
		"license": "AGPL-3.0-only",
	})
}

func parseLimit(s string) int {
	if s == "" {
		return 0
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0
	}
	return n
}

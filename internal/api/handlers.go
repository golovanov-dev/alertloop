package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"
	"unicode/utf8"

	apispec "github.com/golovanov-dev/alertloop/api"
	"github.com/golovanov-dev/alertloop/internal/config"
	"github.com/golovanov-dev/alertloop/internal/domain"
	"github.com/golovanov-dev/alertloop/internal/routing"
	"github.com/golovanov-dev/alertloop/internal/service"
	"github.com/golovanov-dev/alertloop/internal/storage"
)

// maxIngestBody bounds a POST /v1/events body: the largest payload plus room
// for the other fields.
const maxIngestBody = domain.MaxPayloadBytes * 2

// ingestResponse is what a caller that may read events gets back: the event
// and what the request did to it.
type ingestResponse struct {
	*domain.Event
	Outcome service.IngestOutcome `json:"outcome"`
}

// ingestSummary is what an ingest-scoped key gets back. It may not read
// events, so it sees only the event's id and state.
type ingestSummary struct {
	ID      string                `json:"id"`
	State   domain.EventState     `json:"state"`
	Outcome service.IngestOutcome `json:"outcome"`
}

// maxPreviewBody bounds a POST /v1/routing/preview body.
const maxPreviewBody = 64 * 1024

// maxSearchLen is the longest q GET /v1/events accepts, in characters.
const maxSearchLen = 200

// readBody reads a request body of at most limit bytes. When it cannot, it
// writes the error response, 413 for a body over the limit, and returns false.
func readBody(w http.ResponseWriter, r *http.Request, limit int64) ([]byte, bool) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, limit))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeError(w, http.StatusRequestEntityTooLarge, "request_too_large",
				fmt.Sprintf("request body exceeds %d bytes", limit))
			return nil, false
		}
		writeError(w, http.StatusBadRequest, "bad_request", "could not read request body")
		return nil, false
	}
	return body, true
}

// handleIngest implements POST /v1/events.
func (s *Server) handleIngest(w http.ResponseWriter, r *http.Request) {
	body, ok := readBody(w, r, maxIngestBody)
	if !ok {
		return
	}
	var in service.EventInput
	if err := json.Unmarshal(body, &in); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON body")
		return
	}

	who := callerOf(r)
	res, err := s.ingest.IngestFor(r.Context(), in, who.sources)
	if err != nil {
		// Ingestion is atomic: the event and its delivery jobs are stored
		// together or not at all, so any error means nothing was persisted.
		writeDomainError(w, err)
		return
	}

	// A `resolved` report for a dedupe_key nothing was ever stored under is a
	// success with nothing to return: the source may be reporting a recovery
	// after retention removed the incident, or after its own restart. 204 says
	// "accepted, no incident to show" without inventing an event.
	if res.Event == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	status := http.StatusOK // the incident already existed
	if res.Created() {
		status = http.StatusCreated
	}
	if who.scope == config.ScopeIngest {
		writeJSON(w, status, ingestSummary{ID: res.Event.ID, State: res.Event.State, Outcome: res.Outcome})
		return
	}
	writeJSON(w, status, ingestResponse{Event: res.Event, Outcome: res.Outcome})
}

// eventFilter reads the filters of GET /v1/events. A value outside its enum,
// or a q or source that is not UTF-8, is an error, not a filter that matches
// nothing (PostgreSQL rejects invalid UTF-8 in a query parameter).
func eventFilter(q url.Values) (storage.EventFilter, error) {
	f := storage.EventFilter{
		Type:     domain.EventType(q.Get("type")),
		Severity: domain.Severity(q.Get("severity")),
		State:    domain.EventState(q.Get("state")),
		Source:   q.Get("source"),
		Search:   q.Get("q"),
	}
	switch {
	case f.Type != "" && !domain.ValidEventType(f.Type):
		return f, invalidParam("type", string(f.Type))
	case f.Severity != "" && !domain.ValidSeverity(f.Severity):
		return f, invalidParam("severity", string(f.Severity))
	case f.State != "" && !domain.ValidEventState(f.State):
		return f, invalidParam("state", string(f.State))
	case !utf8.ValidString(f.Source):
		return f, fmt.Errorf("%w: source is not valid UTF-8", domain.ErrValidation)
	case !utf8.ValidString(f.Search):
		return f, fmt.Errorf("%w: q is not valid UTF-8", domain.ErrValidation)
	case utf8.RuneCountInString(f.Search) > maxSearchLen:
		return f, fmt.Errorf("%w: q is longer than %d characters", domain.ErrValidation, maxSearchLen)
	}
	return f, nil
}

// deliveryFilter reads the filters of GET /v1/delivery-attempts, as
// eventFilter does.
func deliveryFilter(q url.Values) (storage.DeliveryFilter, error) {
	f := storage.DeliveryFilter{
		State:       domain.DeliveryState(q.Get("state")),
		Channel:     domain.ChannelType(q.Get("channel")),
		ChannelName: q.Get("channel_name"),
		EventID:     q.Get("event_id"),
		Kind:        domain.DeliveryKind(q.Get("kind")),
	}
	switch {
	case f.State != "" && !domain.ValidDeliveryState(f.State):
		return f, invalidParam("state", string(f.State))
	case f.Channel != "" && !domain.ValidChannelType(f.Channel):
		return f, invalidParam("channel", string(f.Channel))
	case f.Kind != "" && !domain.ValidDeliveryKind(f.Kind):
		return f, invalidParam("kind", string(f.Kind))
	}
	return f, nil
}

func invalidParam(name, value string) error {
	return fmt.Errorf("%w: invalid %s %q", domain.ErrValidation, name, value)
}

// handleListEvents implements GET /v1/events.
func (s *Server) handleListEvents(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f, err := eventFilter(q)
	if err != nil {
		writeDomainError(w, err)
		return
	}
	limit, err := parseLimit(q.Get("limit"))
	if err != nil {
		writeDomainError(w, err)
		return
	}
	page, err := s.events.List(r.Context(), f, limit, q.Get("cursor"))
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
	f, err := deliveryFilter(q)
	if err != nil {
		writeDomainError(w, err)
		return
	}
	limit, err := parseLimit(q.Get("limit"))
	if err != nil {
		writeDomainError(w, err)
		return
	}
	page, err := s.deliveries.List(r.Context(), f, limit, q.Get("cursor"))
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
	body, ok := readBody(w, r, maxPreviewBody)
	if !ok {
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

// handleHealth implements GET /health and its /health/live alias (liveness).
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleReady implements GET /ready and its /health/ready alias (readiness).
// It verifies DB connectivity.
func (s *Server) handleReady(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	if err := s.store.Ping(ctx); err != nil {
		writeError(w, http.StatusServiceUnavailable, "not_ready", "database unavailable")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

// deadLetterWindow is the period dead_letter_last_24h counts over.
const deadLetterWindow = 24 * time.Hour

// statsResponse is what GET /v1/stats returns: counts for the admin console
// and absolute signals for monitoring AlertLoop itself. A signal with no value
// is null, never 0 or an empty time.
type statsResponse struct {
	Events                      map[string]int64 `json:"events"`
	Deliveries                  map[string]int64 `json:"deliveries"`
	OpenIncidents               int64            `json:"open_incidents"`
	OldestDueDeliveryAgeSeconds *int64           `json:"oldest_due_delivery_age_seconds"`
	DeadLetterLast24h           int64            `json:"dead_letter_last_24h"`
	WorkerLastTickAt            *time.Time       `json:"worker_last_tick_at"`
}

// handleStats implements GET /v1/stats.
func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	now := time.Now().UTC()
	m, err := s.store.Monitoring(r.Context(), now, now.Add(-deadLetterWindow))
	if err != nil {
		writeDomainError(w, err)
		return
	}
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
	resp := statsResponse{
		Events:            events,
		Deliveries:        deliveries,
		OpenIncidents:     m.OpenIncidents,
		DeadLetterLast24h: m.DeadLetters,
	}
	if m.OldestDueSince != nil {
		// Another process's clock may run a little ahead of this one.
		age := int64(max(0, now.Sub(*m.OldestDueSince)) / time.Second)
		resp.OldestDueDeliveryAgeSeconds = &age
	}
	if m.LastWorkerTick != nil {
		// Whole seconds: the tick is recorded every 15 s at most, and the
		// fromdateiso8601 of jq does not read fractions.
		tick := m.LastWorkerTick.UTC().Truncate(time.Second)
		resp.WorkerLastTickAt = &tick
	}
	writeJSON(w, http.StatusOK, resp)
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

// handleOpenAPISpec serves the embedded OpenAPI contract.
func (s *Server) handleOpenAPISpec(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/yaml; charset=utf-8")
	_, _ = w.Write(apispec.OpenAPIYAML)
}

// parseLimit reads the limit query parameter; 0 means the default. A value
// above the maximum is lowered to it.
func parseLimit(s string) (int, error) {
	if s == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(s)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("%w: limit must be a positive integer", domain.ErrValidation)
	}
	return n, nil
}

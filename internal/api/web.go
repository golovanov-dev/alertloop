package api

import (
	"html/template"
	"net/http"

	apispec "github.com/golovanov-dev/alertloop/api"
	"github.com/golovanov-dev/alertloop/internal/domain"
	"github.com/golovanov-dev/alertloop/internal/storage"
)

// setPageSecurityHeaders sets a strict Content-Security-Policy for the
// server-rendered events pages. They use only inline CSS (no scripts, no remote
// assets), so this locks the pages down to exactly that.
func setPageSecurityHeaders(w http.ResponseWriter) {
	w.Header().Set("Content-Security-Policy",
		"default-src 'none'; style-src 'unsafe-inline'; form-action 'self'; base-uri 'none'")
}

// handleOpenAPISpec serves the embedded OpenAPI contract.
func (s *Server) handleOpenAPISpec(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/yaml; charset=utf-8")
	_, _ = w.Write(apispec.OpenAPIYAML)
}

// handleEventsPage renders the admin events list. It is protected by the admin
// token (there are no user accounts in Community). The token is carried in the
// query string so in-page links keep working from a browser.
func (s *Server) handleEventsPage(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := storage.EventFilter{
		Type:     domain.EventType(q.Get("type")),
		Severity: domain.Severity(q.Get("severity")),
		State:    domain.EventState(q.Get("state")),
		Source:   q.Get("source"),
	}
	page, err := s.events.List(r.Context(), f, parseLimit(q.Get("limit")), q.Get("cursor"))
	if err != nil {
		http.Error(w, "failed to load events", http.StatusInternalServerError)
		return
	}

	data := eventsPageData{
		Token:      extractAdminToken(r),
		Events:     page.Items,
		NextCursor: page.NextCursor,
		Filter:     f,
	}
	setPageSecurityHeaders(w)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := eventsTemplate.Execute(w, data); err != nil {
		s.log.Error("render events page failed", "error", err)
	}
}

// handleEventDetailPage renders a single event with its delivery attempts.
func (s *Server) handleEventDetailPage(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	event, err := s.events.Get(r.Context(), id)
	if err != nil {
		http.Error(w, "event not found", http.StatusNotFound)
		return
	}
	deliveries, err := s.deliveries.List(r.Context(), storage.DeliveryFilter{EventID: id}, 200, "")
	if err != nil {
		http.Error(w, "failed to load deliveries", http.StatusInternalServerError)
		return
	}
	data := eventDetailPageData{
		Token:      extractAdminToken(r),
		Event:      event,
		Deliveries: deliveries.Items,
	}
	setPageSecurityHeaders(w)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := eventDetailTemplate.Execute(w, data); err != nil {
		s.log.Error("render event detail page failed", "error", err)
	}
}

// handleDeliveriesPage renders delivery attempts, defaulting to the dead-letter
// view — the operator's "what didn't get through" screen. Filter with ?state=.
func (s *Server) handleDeliveriesPage(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := storage.DeliveryFilter{
		State:       domain.DeliveryState(q.Get("state")),
		Channel:     domain.ChannelType(q.Get("channel")),
		ChannelName: q.Get("channel_name"),
	}
	page, err := s.deliveries.List(r.Context(), f, parseLimit(q.Get("limit")), q.Get("cursor"))
	if err != nil {
		http.Error(w, "failed to load delivery attempts", http.StatusInternalServerError)
		return
	}
	data := deliveriesPageData{
		Token:      extractAdminToken(r),
		State:      q.Get("state"),
		Deliveries: page.Items,
		NextCursor: page.NextCursor,
	}
	setPageSecurityHeaders(w)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := deliveriesTemplate.Execute(w, data); err != nil {
		s.log.Error("render deliveries page failed", "error", err)
	}
}

type eventsPageData struct {
	Token      string
	Events     []domain.Event
	NextCursor string
	Filter     storage.EventFilter
}

type deliveriesPageData struct {
	Token      string
	State      string
	Deliveries []domain.DeliveryAttempt
	NextCursor string
}

type eventDetailPageData struct {
	Token      string
	Event      *domain.Event
	Deliveries []domain.DeliveryAttempt
}

var templateFuncs = template.FuncMap{
	"shortID": func(id string) string {
		if len(id) > 8 {
			return id[:8]
		}
		return id
	},
}

var eventsTemplate = template.Must(template.New("events").Funcs(templateFuncs).Parse(eventsHTML))
var eventDetailTemplate = template.Must(template.New("detail").Funcs(templateFuncs).Parse(eventDetailHTML))
var deliveriesTemplate = template.Must(template.New("deliveries").Funcs(templateFuncs).Parse(deliveriesHTML))

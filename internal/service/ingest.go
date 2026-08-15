// Package service contains AlertLoop's application logic: event ingestion,
// event state transitions, and delivery-attempt operations. It sits between the
// HTTP API and the storage layer and is transport-agnostic.
package service

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/golovanov-dev/alertloop/internal/domain"
	"github.com/golovanov-dev/alertloop/internal/routing"
	"github.com/golovanov-dev/alertloop/internal/storage"
	"github.com/google/uuid"
)

// Clock returns the current time. Tests may substitute a fixed clock.
type Clock func() time.Time

// IngestService validates incoming events, stores them idempotently, and
// enqueues one delivery attempt per channel the router selects. With no routing
// section configured the router selects every configured channel, which is the
// historical fan-out behavior.
type IngestService struct {
	store       storage.Store
	router      *routing.Router
	maxAttempts int
	now         Clock
	log         *slog.Logger
}

// NewIngestService builds an IngestService. router decides which channels each
// event is delivered to; a nil logger falls back to slog.Default().
func NewIngestService(store storage.Store, router *routing.Router, maxAttempts int, now Clock, log *slog.Logger) *IngestService {
	if maxAttempts <= 0 {
		maxAttempts = domain.DefaultMaxAttempts
	}
	if now == nil {
		now = time.Now
	}
	if router == nil {
		router = routing.NewAllChannels(nil)
	}
	if log == nil {
		log = slog.Default()
	}
	return &IngestService{store: store, router: router, maxAttempts: maxAttempts, now: now, log: log}
}

// EventInput is the validated, transport-neutral shape of an ingestion request.
type EventInput struct {
	Type       domain.EventType `json:"type"`
	Severity   domain.Severity  `json:"severity"`
	Source     string           `json:"source"`
	Category   string           `json:"category"`
	Message    string           `json:"message"`
	EntityType string           `json:"entity_type"`
	EntityID   string           `json:"entity_id"`
	TraceID    string           `json:"trace_id"`
	DedupeKey  string           `json:"dedupe_key"`
	Payload    json.RawMessage  `json:"payload"`
}

// Ingest validates and stores an event. When the event is newly created it also
// enqueues delivery attempts. If a matching dedupe_key already exists, the
// existing event is returned and no new deliveries are created.
func (s *IngestService) Ingest(ctx context.Context, in EventInput) (event *domain.Event, created bool, err error) {
	if err := validateInput(&in); err != nil {
		return nil, false, err
	}

	now := s.now().UTC()
	e := &domain.Event{
		ID:         uuid.NewString(),
		Type:       in.Type,
		Severity:   in.Severity,
		State:      domain.StateNew,
		Source:     strings.TrimSpace(in.Source),
		Category:   strings.TrimSpace(in.Category),
		Message:    strings.TrimSpace(in.Message),
		EntityType: strings.TrimSpace(in.EntityType),
		EntityID:   strings.TrimSpace(in.EntityID),
		TraceID:    strings.TrimSpace(in.TraceID),
		DedupeKey:  strings.TrimSpace(in.DedupeKey),
		Payload:    normalizePayload(in.Payload),
		CreatedAt:  now,
		UpdatedAt:  now,
	}

	// Routing runs once, here at ingestion: a repeated dedupe_key returns the
	// stored event and creates no deliveries, so it is never routed twice.
	decision := s.router.Route(e)

	// Build the event and its delivery jobs, then persist them atomically so an
	// event is never stored without its deliveries.
	deliveries := s.buildDeliveries(e.ID, decision.Channels, now)
	stored, created, err := s.store.CreateEventWithDeliveries(ctx, e, deliveries)
	if err != nil {
		return nil, false, err
	}
	if created {
		s.logRouting(e, decision)
	}
	return stored, created, nil
}

// logRouting records where a newly stored event went. An event that matched
// nothing and has no routing default is delivered nowhere, which is the one
// failure mode of routing that is otherwise invisible — it is logged at warn
// level with everything needed to write the missing rule.
func (s *IngestService) logRouting(e *domain.Event, d routing.Decision) {
	if s.router.Configured() && !d.Matched && len(d.Channels) == 0 {
		s.log.Warn("event matched no routing rule and no routing default is configured; it is stored but will not be delivered",
			"event_id", e.ID, "type", e.Type, "severity", e.Severity,
			"source", e.Source, "category", e.Category)
		return
	}
	s.log.Debug("event routed", "event_id", e.ID, "rule", d.Rule, "channels", len(d.Channels))
}

func (s *IngestService) buildDeliveries(eventID string, targets []domain.ChannelTarget, now time.Time) []*domain.DeliveryAttempt {
	deliveries := make([]*domain.DeliveryAttempt, 0, len(targets))
	for _, t := range targets {
		deliveries = append(deliveries, &domain.DeliveryAttempt{
			ID:          uuid.NewString(),
			EventID:     eventID,
			Channel:     t.Type,
			ChannelName: t.Name,
			State:       domain.DeliveryPending,
			Attempts:    0,
			MaxAttempts: s.maxAttempts,
			CreatedAt:   now,
			UpdatedAt:   now,
		})
	}
	return deliveries
}

func validateInput(in *EventInput) error {
	if !domain.ValidEventType(in.Type) {
		return fmt.Errorf("%w: invalid or missing type", domain.ErrValidation)
	}
	if in.Severity == "" {
		in.Severity = domain.SeverityInfo
	}
	if !domain.ValidSeverity(in.Severity) {
		return fmt.Errorf("%w: invalid severity", domain.ErrValidation)
	}
	if strings.TrimSpace(in.Source) == "" {
		return fmt.Errorf("%w: source is required", domain.ErrValidation)
	}
	if strings.TrimSpace(in.Message) == "" {
		return fmt.Errorf("%w: message is required", domain.ErrValidation)
	}
	if len(in.Payload) > domain.MaxPayloadBytes {
		return fmt.Errorf("%w: payload exceeds %d bytes", domain.ErrValidation, domain.MaxPayloadBytes)
	}
	if len(in.Payload) > 0 && !json.Valid(in.Payload) {
		return fmt.Errorf("%w: payload is not valid JSON", domain.ErrValidation)
	}
	return nil
}

func normalizePayload(p json.RawMessage) json.RawMessage {
	if len(p) == 0 {
		return json.RawMessage(`{}`)
	}
	return p
}

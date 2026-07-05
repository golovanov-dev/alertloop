// Package service contains AlertLoop's application logic: event ingestion,
// event state transitions, and delivery-attempt operations. It sits between the
// HTTP API and the storage layer and is transport-agnostic.
package service

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/golovanov-dev/alertloop/internal/domain"
	"github.com/golovanov-dev/alertloop/internal/storage"
	"github.com/google/uuid"
)

// Clock returns the current time. Tests may substitute a fixed clock.
type Clock func() time.Time

// IngestService validates incoming events, stores them idempotently, and fans
// out one delivery attempt per configured channel (the Community single global
// channel configuration).
type IngestService struct {
	store       storage.Store
	targets     []domain.ChannelTarget
	maxAttempts int
	now         Clock
}

// NewIngestService builds an IngestService. targets is the set of configured
// channel instances every event is delivered to (Community fan-out).
func NewIngestService(store storage.Store, targets []domain.ChannelTarget, maxAttempts int, now Clock) *IngestService {
	if maxAttempts <= 0 {
		maxAttempts = domain.DefaultMaxAttempts
	}
	if now == nil {
		now = time.Now
	}
	return &IngestService{store: store, targets: targets, maxAttempts: maxAttempts, now: now}
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

	// Build the event and its delivery jobs, then persist them atomically so an
	// event is never stored without its deliveries.
	deliveries := s.buildDeliveries(e.ID, now)
	stored, created, err := s.store.CreateEventWithDeliveries(ctx, e, deliveries)
	if err != nil {
		return nil, false, err
	}
	return stored, created, nil
}

func (s *IngestService) buildDeliveries(eventID string, now time.Time) []*domain.DeliveryAttempt {
	deliveries := make([]*domain.DeliveryAttempt, 0, len(s.targets))
	for _, t := range s.targets {
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

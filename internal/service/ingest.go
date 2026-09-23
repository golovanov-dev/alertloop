// Package service contains AlertLoop's application logic: event ingestion,
// event state transitions, and delivery-attempt operations. It sits between the
// HTTP API and the storage layer and is transport-agnostic.
package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
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
	recovery    *RecoveryNotifier
	now         Clock
	log         *slog.Logger
}

// NewIngestService builds an IngestService. router decides which channels each
// event is delivered to; a nil logger falls back to slog.Default().
func NewIngestService(store storage.Store, router *routing.Router, maxAttempts int, recovery *RecoveryNotifier, now Clock, log *slog.Logger) *IngestService {
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
	return &IngestService{store: store, router: router, maxAttempts: maxAttempts, recovery: recovery, now: now, log: log}
}

// IngestOutcome describes what an ingestion request did to the incident it
// identifies. It exists so the HTTP layer can pick a status code without
// re-deriving the decision the service already made.
type IngestOutcome string

const (
	// OutcomeCreated: a new event was stored and its deliveries queued.
	OutcomeCreated IngestOutcome = "created"
	// OutcomeDeduplicated: an open incident with the same dedupe_key already
	// existed and was returned untouched. This is the pre-0.4.0 behaviour, kept
	// for requests that send no status.
	OutcomeDeduplicated IngestOutcome = "deduplicated"
	// OutcomeRefreshed: an open incident was updated with the newest report.
	OutcomeRefreshed IngestOutcome = "refreshed"
	// OutcomeResolved: this request closed the open incident.
	OutcomeResolved IngestOutcome = "resolved"
	// OutcomeAlreadyResolved: the incident was already closed; nothing changed.
	OutcomeAlreadyResolved IngestOutcome = "already_resolved"
	// OutcomeNothingToResolve: no event has ever carried this dedupe_key. Not an
	// error — a monitoring source may report a recovery after retention removed
	// the incident, or after being restarted.
	OutcomeNothingToResolve IngestOutcome = "nothing_to_resolve"
)

// IngestResult is the outcome of one ingestion request. Event is nil only for
// OutcomeNothingToResolve.
type IngestResult struct {
	Event   *domain.Event
	Outcome IngestOutcome
}

// Created reports whether a new event row was stored.
func (r IngestResult) Created() bool { return r.Outcome == OutcomeCreated }

// EventInput is the validated, transport-neutral shape of an ingestion request.
type EventInput struct {
	// Status selects the ingestion semantics and is the opt-in to the incident
	// lifecycle.
	//
	// Omitted, dedupe_key is an *idempotency* key and behaves exactly as it did
	// before 0.4.0: a repeat returns the stored event untouched. Present, it is
	// an *incident identity*: `firing` refreshes the open incident with the
	// newest report, `resolved` closes it. The same field has always served both
	// intents; making the intent explicit is what lets each keep its own
	// semantics.
	Status     domain.IngestStatus `json:"status"`
	Type       domain.EventType    `json:"type"`
	Severity   domain.Severity     `json:"severity"`
	Source     string              `json:"source"`
	Category   string              `json:"category"`
	Message    string              `json:"message"`
	EntityType string              `json:"entity_type"`
	EntityID   string              `json:"entity_id"`
	TraceID    string              `json:"trace_id"`
	DedupeKey  string              `json:"dedupe_key"`
	Payload    json.RawMessage     `json:"payload"`
}

// Ingest is IngestFor with no source restriction: the caller may report and
// change events of every source.
func (s *IngestService) Ingest(ctx context.Context, in EventInput) (IngestResult, error) {
	return s.IngestFor(ctx, in, nil)
}

// IngestFor validates and stores an event. When the event is newly created it
// also enqueues delivery attempts. What happens when the dedupe_key is already
// known depends on in.Status — see EventInput.Status and IngestOutcome.
//
// allowed lists the sources the caller may report; nil allows every source. A
// request from another source, or one whose dedupe_key finds an event of
// another source, fails with domain.ErrSourceNotAllowed and changes nothing.
func (s *IngestService) IngestFor(ctx context.Context, in EventInput, allowed []string) (IngestResult, error) {
	if err := validateInput(&in); err != nil {
		return IngestResult{}, err
	}
	src := strings.TrimSpace(in.Source)
	if allowed != nil && src != "" && !slices.Contains(allowed, src) {
		// Most often a misconfigured sender (a source left at its default), so
		// it is a warning like a refusal by the stored event, not only a 403 in
		// the access log.
		s.log.Warn("ingest refused: request source is not in this API key's sources",
			"request_source", src, "dedupe_key", strings.TrimSpace(in.DedupeKey))
		return IngestResult{}, fmt.Errorf("%w: this API key may not report events from source %q", domain.ErrSourceNotAllowed, src)
	}
	s.warnLifecycleOnNonIncident(in)

	now := s.now().UTC()

	if in.Status == domain.StatusResolved {
		return s.resolve(ctx, strings.TrimSpace(in.DedupeKey), allowed, src, now)
	}

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
		LastSeenAt: now,
	}

	// Routing runs here for a new event. A repeated dedupe_key is routed again
	// only for a `firing` that raises the severity (see refresh).
	decision := s.router.Route(e)

	// Build the event and its delivery jobs, then persist them atomically so an
	// event is never stored without its deliveries.
	deliveries := s.buildDeliveries(e.ID, decision.Channels, now)
	stored, created, err := s.store.CreateEventWithDeliveries(ctx, e, deliveries)
	if err != nil {
		return IngestResult{}, err
	}
	if created {
		s.logRouting(e, decision)
		return IngestResult{Event: stored, Outcome: OutcomeCreated}, nil
	}
	if err := s.checkOwner(stored, allowed, src); err != nil {
		return IngestResult{}, err
	}

	// The key matched an incident that is still open. Without an explicit
	// status the caller is using dedupe_key as an idempotency key, and a repeat
	// must stay a no-op.
	if in.Status != domain.StatusFiring {
		return IngestResult{Event: stored, Outcome: OutcomeDeduplicated}, nil
	}

	refreshed, err := s.refresh(ctx, stored, e, now)
	if err == nil {
		s.log.Debug("open incident refreshed", "event_id", refreshed.ID, "dedupe_key", refreshed.DedupeKey)
		return IngestResult{Event: refreshed, Outcome: OutcomeRefreshed}, nil
	}
	if !errors.Is(err, domain.ErrNotFound) {
		return IngestResult{}, err
	}

	// The incident was resolved between the lookup and the update, so its key is
	// free again. Retry the create: the source is telling us the problem is
	// STILL HAPPENING, and returning "deduplicated" against a closed incident
	// would drop that report on the floor - no update, no new incident, no
	// notification - until the next check cycle.
	//
	// Once, not in a loop. If a second resolve lands in the same instant, the
	// next report will open the incident; retrying forever to win a race
	// against a source that is flapping that fast would be worse.
	retry := *e
	retry.ID = uuid.NewString()
	stored, created, err = s.store.CreateEventWithDeliveries(ctx, &retry,
		s.buildDeliveries(retry.ID, decision.Channels, now))
	if err != nil {
		return IngestResult{}, err
	}
	if created {
		s.logRouting(&retry, decision)
		return IngestResult{Event: stored, Outcome: OutcomeCreated}, nil
	}
	if err := s.checkOwner(stored, allowed, src); err != nil {
		return IngestResult{}, err
	}
	return IngestResult{Event: stored, Outcome: OutcomeDeduplicated}, nil
}

// refresh applies a repeated `firing` to the open incident stored. A repeat
// creates no deliveries — a check that fails every minute must not notify
// anyone every minute — unless it raises the severity: then the channels the
// new severity routes to and that were not alerted yet get the alert. The
// store decides the rise against the severity it holds, in the same
// transaction as the update.
func (s *IngestService) refresh(ctx context.Context, stored, report *domain.Event, now time.Time) (*domain.Event, error) {
	u := storage.EventUpdate{
		Severity:   report.Severity,
		Message:    report.Message,
		Payload:    report.Payload,
		LastSeenAt: now,
	}
	if stored.Type == domain.EventIncident && domain.SeverityRank(report.Severity) > domain.SeverityRank(stored.Severity) {
		probe := *stored
		probe.Severity = report.Severity
		u.AlertsOnRise = s.buildDeliveries(stored.ID, s.router.Route(&probe).Channels, now)
		s.log.Info("incident severity rose; alerting channels not alerted yet",
			"event_id", stored.ID, "dedupe_key", stored.DedupeKey, "from", stored.Severity, "to", report.Severity)
	}
	return s.store.RefreshOpenEvent(ctx, stored.ID, u)
}

// checkOwner refuses a caller limited to allowed sources access to an event of
// another source. The error does not name that source; the log does, because a
// refusal here is either a misconfigured key or a key used against events it
// was not issued for.
func (s *IngestService) checkOwner(e *domain.Event, allowed []string, requestSource string) error {
	if allowed == nil || slices.Contains(allowed, e.Source) {
		return nil
	}
	s.log.Warn("ingest refused: dedupe_key belongs to an event of a source this API key may not report",
		"event_id", e.ID, "dedupe_key", e.DedupeKey, "event_source", e.Source, "request_source", requestSource)
	return fmt.Errorf("%w: dedupe_key belongs to an event of a source this API key may not report", domain.ErrSourceNotAllowed)
}

// resolve closes the open incident carrying key. It is deliberately forgiving:
// a monitoring source that reports a recovery twice, or reports one for an
// incident that retention already removed, has done nothing wrong and must not
// receive an error it would log as a delivery failure.
func (s *IngestService) resolve(ctx context.Context, key string, allowed []string, requestSource string, now time.Time) (IngestResult, error) {
	e, err := s.store.EventByDedupe(ctx, key)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			s.log.Debug("resolve for an unknown dedupe_key; nothing to close", "dedupe_key", key)
			return IngestResult{Outcome: OutcomeNothingToResolve}, nil
		}
		return IngestResult{}, err
	}
	if err := s.checkOwner(e, allowed, requestSource); err != nil {
		return IngestResult{}, err
	}
	// Already closed, before this request or by a request that won the race:
	// notifying again would tell the same people the same good news twice,
	// which is how a monitoring source that repeats itself becomes noise.
	event, closed, err := transition(ctx, s.store, s.recovery, e, now,
		func(domain.EventState) (domain.EventState, error) { return domain.StateResolved, nil })
	if errors.Is(err, domain.ErrNotFound) {
		// Retention removed the incident between the lookup and the close.
		return IngestResult{Outcome: OutcomeNothingToResolve}, nil
	}
	if err != nil {
		return IngestResult{}, err
	}
	if !closed {
		return IngestResult{Event: event, Outcome: OutcomeAlreadyResolved}, nil
	}
	s.log.Info("incident resolved by the reporting source", "event_id", event.ID, "dedupe_key", key)
	return IngestResult{Event: event, Outcome: OutcomeResolved}, nil
}

// warnLifecycleOnNonIncident flags `status: firing` on an event family that has
// no lifecycle: `business_event` and `audit`.
//
// It is not rejected — the request is valid and the event is stored — but it is
// almost always a misunderstanding with a silent consequence. The firing/
// resolved lifecycle is built for incidents: a stable dedupe_key means "this is
// the same problem, still happening", so every repeat REFRESHES the open event
// and creates no deliveries. Applied to a stream of business events (form
// submissions, orders) under one key such as "contact-form", the first report
// notifies and every later one quietly does not. Nothing fails, nothing is
// logged as an error, and the operator finds out weeks later that requests
// stopped arriving.
//
// `audit` is in the same position and for the same reason: an audit entry
// records something that already happened, so a stable key over a stream of
// them (one per login) loses every entry after the first in exactly this way.
//
// Warned at the moment the mistake is made rather than left to the reader of
// the documentation, because the symptom appears far from the cause. Such
// events are sent without `status`, or with a dedupe_key unique per report.
// Per request, not once per key: this is the same class of quiet loss that
// logRouting warns about on every event, and the repeat is where the loss
// actually happens.
func (s *IngestService) warnLifecycleOnNonIncident(in EventInput) {
	if in.Status != domain.StatusFiring || in.Type == domain.EventIncident {
		return
	}
	s.log.Warn("status=firing on a non-incident event type: repeats of this dedupe_key will refresh the open event and "+
		"deliver nothing, so only the first report notifies anyone. The firing/resolved lifecycle is for incidents — send "+
		"business_event and audit without status, or give each report its own dedupe_key",
		"type", in.Type, "source", strings.TrimSpace(in.Source),
		"category", strings.TrimSpace(in.Category), "dedupe_key", strings.TrimSpace(in.DedupeKey))
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
	if in.Status != "" && !domain.ValidIngestStatus(in.Status) {
		return fmt.Errorf("%w: invalid status, expected \"firing\" or \"resolved\"", domain.ErrValidation)
	}
	// A recovery report identifies an incident and asserts it is over; it does
	// not describe an event to store. Requiring a full event body for it would
	// force every caller to repeat fields that are already recorded on the
	// incident being closed.
	if in.Status == domain.StatusResolved {
		if strings.TrimSpace(in.DedupeKey) == "" {
			return fmt.Errorf("%w: dedupe_key is required with status=resolved; it is what identifies the incident to close", domain.ErrValidation)
		}
		return nil
	}
	if in.Status == domain.StatusFiring && strings.TrimSpace(in.DedupeKey) == "" {
		return fmt.Errorf("%w: dedupe_key is required with status=firing; without it every report would create a new incident", domain.ErrValidation)
	}
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

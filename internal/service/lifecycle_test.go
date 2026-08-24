package service

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/golovanov-dev/alertloop/internal/domain"
	"github.com/golovanov-dev/alertloop/internal/routing"
	"github.com/golovanov-dev/alertloop/internal/storage"
)

// clockAt returns a Clock that advances by a minute on every call, so a test
// can tell "the timestamp moved" from "the timestamp was rewritten with the
// same value".
func clockAt(start time.Time) Clock {
	n := 0
	return func() time.Time {
		t := start.Add(time.Duration(n) * time.Minute)
		n++
		return t
	}
}

// lifecycleService builds the service as a real deployment has it, recovery
// notices included: that is the default, and testing the lifecycle with them
// switched off would be testing a configuration almost nobody runs.
func lifecycleService(t *testing.T) (*IngestService, storage.Store) {
	t.Helper()
	s := newStore(t)
	svc := NewIngestService(s,
		routing.NewAllChannels([]domain.ChannelTarget{{Type: domain.ChannelWebhook, Name: "wh"}}),
		5, NewRecoveryNotifier(s, true, 5, quietLogger()),
		clockAt(time.Date(2026, 8, 22, 10, 0, 0, 0, time.UTC)), quietLogger())
	return svc, s
}

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// countDeliveries returns how many attempts of the given kind exist.
func countDeliveries(t *testing.T, s storage.Store, kind domain.DeliveryKind) int {
	t.Helper()
	page, err := s.ListDeliveryAttempts(context.Background(), storage.DeliveryFilter{}, 200, "")
	if err != nil {
		t.Fatalf("list deliveries: %v", err)
	}
	n := 0
	for _, d := range page.Items {
		if d.Kind == kind {
			n++
		}
	}
	return n
}

func firing(key, message string, sev domain.Severity) EventInput {
	return EventInput{
		Status:    domain.StatusFiring,
		Type:      domain.EventIncident,
		Severity:  sev,
		Source:    "monit",
		Message:   message,
		DedupeKey: key,
	}
}

// deliveryCount counts ALERT attempts only. Recovery notices are counted
// separately: an assertion about "how many times were we told about a problem"
// must not be satisfied by a message saying it is over.
func deliveryCount(t *testing.T, s storage.Store) int {
	t.Helper()
	return countDeliveries(t, s, domain.KindAlert)
}

// A repeated `firing` must update the open incident in place: the operator
// needs the newest severity and message, but must not be notified again for a
// problem they already know about.
func TestFiringRefreshesOpenIncident(t *testing.T) {
	svc, store := lifecycleService(t)
	ctx := context.Background()

	first, err := svc.Ingest(ctx, firing("host-1:pg:availability", "PostgreSQL not responding", domain.SeverityWarning))
	if err != nil {
		t.Fatalf("first firing: %v", err)
	}
	if first.Outcome != OutcomeCreated {
		t.Fatalf("first firing outcome = %q, want %q", first.Outcome, OutcomeCreated)
	}

	second, err := svc.Ingest(ctx, firing("host-1:pg:availability", "PostgreSQL still down after 5 cycles", domain.SeverityCritical))
	if err != nil {
		t.Fatalf("second firing: %v", err)
	}
	if second.Outcome != OutcomeRefreshed {
		t.Fatalf("second firing outcome = %q, want %q", second.Outcome, OutcomeRefreshed)
	}
	if second.Event.ID != first.Event.ID {
		t.Fatal("a repeated firing created a second incident instead of refreshing the open one")
	}
	if second.Event.Severity != domain.SeverityCritical {
		t.Fatalf("severity = %q, want the escalated %q", second.Event.Severity, domain.SeverityCritical)
	}
	if second.Event.Message != "PostgreSQL still down after 5 cycles" {
		t.Fatalf("message = %q, want the newest report", second.Event.Message)
	}
	if !second.Event.LastSeenAt.After(first.Event.LastSeenAt) {
		t.Fatalf("last_seen_at did not move: %s -> %s", first.Event.LastSeenAt, second.Event.LastSeenAt)
	}
	if !second.Event.CreatedAt.Equal(first.Event.CreatedAt) {
		t.Fatal("created_at moved; it must record when the incident began")
	}
	if second.Event.ResolvedAt != nil {
		t.Fatal("an incident that is still firing must not carry resolved_at")
	}
	if n := deliveryCount(t, store); n != 1 {
		t.Fatalf("delivery attempts = %d, want 1: refreshing an incident must not notify again", n)
	}
}

// An incident an operator has acknowledged stays acknowledged while the
// underlying problem persists. Resetting it to `new` on every repeat would undo
// the acknowledgement every minute.
func TestFiringDoesNotResetAcknowledgedState(t *testing.T) {
	svc, store := lifecycleService(t)
	ctx := context.Background()

	res, err := svc.Ingest(ctx, firing("host-1:disk:usage", "disk 91% full", domain.SeverityWarning))
	if err != nil {
		t.Fatalf("firing: %v", err)
	}
	if _, err := store.UpdateEventState(ctx, res.Event.ID, domain.StateAcknowledged, time.Now().UTC()); err != nil {
		t.Fatalf("acknowledge: %v", err)
	}

	again, err := svc.Ingest(ctx, firing("host-1:disk:usage", "disk 93% full", domain.SeverityWarning))
	if err != nil {
		t.Fatalf("repeat firing: %v", err)
	}
	if again.Event.State != domain.StateAcknowledged {
		t.Fatalf("state = %q, want it to stay %q", again.Event.State, domain.StateAcknowledged)
	}
	if again.Event.Message != "disk 93% full" {
		t.Fatalf("message = %q, want the newest report even while acknowledged", again.Event.Message)
	}
}

func TestResolveClosesOpenIncident(t *testing.T) {
	svc, _ := lifecycleService(t)
	ctx := context.Background()

	opened, err := svc.Ingest(ctx, firing("host-1:pg:availability", "PostgreSQL not responding", domain.SeverityCritical))
	if err != nil {
		t.Fatalf("firing: %v", err)
	}

	res, err := svc.Ingest(ctx, EventInput{Status: domain.StatusResolved, DedupeKey: "host-1:pg:availability"})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if res.Outcome != OutcomeResolved {
		t.Fatalf("outcome = %q, want %q", res.Outcome, OutcomeResolved)
	}
	if res.Event.ID != opened.Event.ID {
		t.Fatal("resolve closed a different event than the one that was open")
	}
	if res.Event.State != domain.StateResolved {
		t.Fatalf("state = %q, want %q", res.Event.State, domain.StateResolved)
	}
	if res.Event.ResolvedAt == nil {
		t.Fatal("resolved_at is nil; the recovery time was not recorded")
	}
}

// A monitoring source that reports the same recovery twice has done nothing
// wrong. The second report must not error, must not reopen the incident, and
// must not move the recorded recovery time.
func TestResolveIsIdempotent(t *testing.T) {
	svc, _ := lifecycleService(t)
	ctx := context.Background()

	if _, err := svc.Ingest(ctx, firing("host-1:worker:heartbeat", "heartbeat stale", domain.SeverityError)); err != nil {
		t.Fatalf("firing: %v", err)
	}
	first, err := svc.Ingest(ctx, EventInput{Status: domain.StatusResolved, DedupeKey: "host-1:worker:heartbeat"})
	if err != nil {
		t.Fatalf("first resolve: %v", err)
	}
	second, err := svc.Ingest(ctx, EventInput{Status: domain.StatusResolved, DedupeKey: "host-1:worker:heartbeat"})
	if err != nil {
		t.Fatalf("second resolve: %v", err)
	}
	if second.Outcome != OutcomeAlreadyResolved {
		t.Fatalf("outcome = %q, want %q", second.Outcome, OutcomeAlreadyResolved)
	}
	if second.Event.ID != first.Event.ID {
		t.Fatal("the repeated resolve returned a different event")
	}
	if !second.Event.ResolvedAt.Equal(*first.Event.ResolvedAt) {
		t.Fatalf("resolved_at moved on a repeat: %s -> %s", first.Event.ResolvedAt, second.Event.ResolvedAt)
	}
}

// A recovery can arrive for an incident retention already deleted, or after the
// monitoring source restarted and never saw the failure. That is a success with
// nothing to show, not an error the source would log as a failed delivery.
func TestResolveUnknownKeyIsNotAnError(t *testing.T) {
	svc, store := lifecycleService(t)
	ctx := context.Background()

	res, err := svc.Ingest(ctx, EventInput{Status: domain.StatusResolved, DedupeKey: "host-1:never:seen"})
	if err != nil {
		t.Fatalf("resolve of an unknown key returned an error: %v", err)
	}
	if res.Outcome != OutcomeNothingToResolve {
		t.Fatalf("outcome = %q, want %q", res.Outcome, OutcomeNothingToResolve)
	}
	if res.Event != nil {
		t.Fatal("resolving an unknown key invented an event")
	}
	page, _ := store.ListEvents(ctx, storage.EventFilter{}, 50, "")
	if len(page.Items) != 0 {
		t.Fatalf("stored %d events; a recovery must never create one", len(page.Items))
	}
}

// The defect this release fixes: before 0.4.0 dedupe_key was unique across every
// event in every state, so a service that failed, was fixed, and failed again
// never produced a second incident — the second outage was silently swallowed
// as a duplicate of the closed one.
func TestFailureRecursAfterResolve(t *testing.T) {
	svc, store := lifecycleService(t)
	ctx := context.Background()
	const key = "host-1:php-fpm:availability"

	first, err := svc.Ingest(ctx, firing(key, "php-fpm down", domain.SeverityCritical))
	if err != nil {
		t.Fatalf("first outage: %v", err)
	}
	if _, err := svc.Ingest(ctx, EventInput{Status: domain.StatusResolved, DedupeKey: key}); err != nil {
		t.Fatalf("recovery: %v", err)
	}

	second, err := svc.Ingest(ctx, firing(key, "php-fpm down again", domain.SeverityCritical))
	if err != nil {
		t.Fatalf("second outage: %v", err)
	}
	if second.Outcome != OutcomeCreated {
		t.Fatalf("outcome = %q, want %q: a resolved incident must not block its own recurrence",
			second.Outcome, OutcomeCreated)
	}
	if second.Event.ID == first.Event.ID {
		t.Fatal("the second outage reused the closed incident instead of opening a new one")
	}
	if n := deliveryCount(t, store); n != 2 {
		t.Fatalf("delivery attempts = %d, want 2: the second outage must notify", n)
	}
}

// The compatibility promise: a client that sends no `status` sees exactly the
// 0.3.x behaviour, where dedupe_key is an idempotency key and a repeat is a
// no-op. Only an explicit `firing` opts into refresh semantics.
func TestNoStatusKeepsPreLifecycleBehaviour(t *testing.T) {
	svc, store := lifecycleService(t)
	ctx := context.Background()

	first, err := svc.Ingest(ctx, EventInput{
		Type: domain.EventIncident, Source: "app", Message: "original", DedupeKey: "idem-1",
	})
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	second, err := svc.Ingest(ctx, EventInput{
		Type: domain.EventIncident, Source: "app", Message: "retried with different text", DedupeKey: "idem-1",
	})
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if second.Outcome != OutcomeDeduplicated {
		t.Fatalf("outcome = %q, want %q", second.Outcome, OutcomeDeduplicated)
	}
	if second.Event.Message != "original" {
		t.Fatalf("message = %q; a status-less repeat must leave the stored event untouched", second.Event.Message)
	}
	if !second.Event.UpdatedAt.Equal(first.Event.UpdatedAt) {
		t.Fatal("updated_at moved on a status-less repeat")
	}
	if n := deliveryCount(t, store); n != 1 {
		t.Fatalf("delivery attempts = %d, want 1", n)
	}
}

// An event ingested once has always been "seen" once, at creation. Nothing sets
// last_seen_at explicitly on that path, so this guards the default.
func TestLastSeenAtDefaultsToCreation(t *testing.T) {
	svc, _ := lifecycleService(t)
	res, err := svc.Ingest(context.Background(), EventInput{
		Type: domain.EventIncident, Source: "app", Message: "one-off",
	})
	if err != nil {
		t.Fatalf("ingest: %v", err)
	}
	if !res.Event.LastSeenAt.Equal(res.Event.CreatedAt) {
		t.Fatalf("last_seen_at = %s, want created_at %s", res.Event.LastSeenAt, res.Event.CreatedAt)
	}
}

func TestLifecycleValidation(t *testing.T) {
	svc, _ := lifecycleService(t)
	ctx := context.Background()

	cases := []struct {
		name string
		in   EventInput
	}{
		{"unknown status", EventInput{
			Status: "flapping", Type: domain.EventIncident, Source: "s", Message: "m", DedupeKey: "k",
		}},
		{"resolved without a dedupe_key", EventInput{Status: domain.StatusResolved}},
		{"resolved with a blank dedupe_key", EventInput{Status: domain.StatusResolved, DedupeKey: "   "}},
		{"firing without a dedupe_key", EventInput{
			Status: domain.StatusFiring, Type: domain.EventIncident, Source: "s", Message: "m",
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := svc.Ingest(ctx, c.in); err == nil {
				t.Fatal("expected a validation error, got none")
			}
		})
	}
}

// A recovery report identifies an incident; it does not describe a new event.
// Requiring type/source/message on it would force every caller to repeat fields
// already recorded on the incident being closed.
func TestResolveNeedsOnlyTheDedupeKey(t *testing.T) {
	svc, _ := lifecycleService(t)
	ctx := context.Background()

	if _, err := svc.Ingest(ctx, firing("host-1:cron:daily-import", "import did not run", domain.SeverityError)); err != nil {
		t.Fatalf("firing: %v", err)
	}
	res, err := svc.Ingest(ctx, EventInput{Status: domain.StatusResolved, DedupeKey: "host-1:cron:daily-import"})
	if err != nil {
		t.Fatalf("a resolve carrying only a dedupe_key was rejected: %v", err)
	}
	if res.Outcome != OutcomeResolved {
		t.Fatalf("outcome = %q, want %q", res.Outcome, OutcomeResolved)
	}
}

// Closing an incident by hand must stamp resolved_at exactly as an ingested
// recovery does; otherwise the two paths disagree about when the same incident
// ended, and one of them is silently wrong.
func TestManualResolveStampsResolvedAt(t *testing.T) {
	svc, store := lifecycleService(t)
	ctx := context.Background()
	events := NewEventService(store, nil, time.Now)

	res, err := svc.Ingest(ctx, firing("host-1:http:health", "health endpoint 500", domain.SeverityError))
	if err != nil {
		t.Fatalf("firing: %v", err)
	}
	closed, err := events.Apply(ctx, res.Event.ID, domain.ActionResolve)
	if err != nil {
		t.Fatalf("manual resolve: %v", err)
	}
	if closed.ResolvedAt == nil {
		t.Fatal("resolved_at is nil after the manual resolve action")
	}

	// And the key is free again, exactly as after an ingested recovery.
	again, err := svc.Ingest(ctx, firing("host-1:http:health", "health endpoint 500 again", domain.SeverityError))
	if err != nil {
		t.Fatalf("recurrence after a manual resolve: %v", err)
	}
	if again.Outcome != OutcomeCreated {
		t.Fatalf("outcome = %q, want %q", again.Outcome, OutcomeCreated)
	}
}

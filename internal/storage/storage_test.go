package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/golovanov-dev/alertloop/internal/domain"
)

func newTestStore(t *testing.T) Store {
	t.Helper()
	s, err := Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if err := s.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func sampleEvent(id, dedupe string, created time.Time) *domain.Event {
	return &domain.Event{
		ID:        id,
		Type:      domain.EventIncident,
		Severity:  domain.SeverityError,
		State:     domain.StateNew,
		Source:    "api",
		Message:   "boom",
		DedupeKey: dedupe,
		Payload:   []byte(`{"k":"v"}`),
		CreatedAt: created,
		UpdatedAt: created,
	}
}

func TestGetEventNotFound(t *testing.T) {
	s := newTestStore(t)
	_, err := s.GetEvent(context.Background(), "missing")
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestListEventsPaginationAndFilter(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	base := time.Now().UTC()
	for i := 0; i < 5; i++ {
		e := sampleEvent(fmt.Sprintf("e%d", i), "", base.Add(time.Duration(i)*time.Second))
		if i%2 == 0 {
			e.Type = domain.EventBusiness
		}
		if _, _, err := s.CreateEvent(ctx, e); err != nil {
			t.Fatalf("create %d: %v", i, err)
		}
	}

	// Page of 2, newest first.
	page, err := s.ListEvents(ctx, EventFilter{}, 2, "")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(page.Items) != 2 || page.NextCursor == "" {
		t.Fatalf("expected 2 items and a cursor, got %d items cursor=%q", len(page.Items), page.NextCursor)
	}
	if page.Items[0].ID != "e4" {
		t.Fatalf("expected newest first (e4), got %q", page.Items[0].ID)
	}

	// Follow the cursor.
	page2, err := s.ListEvents(ctx, EventFilter{}, 2, page.NextCursor)
	if err != nil {
		t.Fatalf("list page2: %v", err)
	}
	if page2.Items[0].ID != "e2" {
		t.Fatalf("expected e2 at start of page2, got %q", page2.Items[0].ID)
	}

	// Filter by type.
	filtered, err := s.ListEvents(ctx, EventFilter{Type: domain.EventBusiness}, 50, "")
	if err != nil {
		t.Fatalf("filter: %v", err)
	}
	if len(filtered.Items) != 3 {
		t.Fatalf("expected 3 business events, got %d", len(filtered.Items))
	}
}

func TestClaimAndMarkResult(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	e, _, _ := s.CreateEvent(ctx, sampleEvent("e1", "", time.Now()))

	d := &domain.DeliveryAttempt{
		ID:          "d1",
		EventID:     e.ID,
		Channel:     domain.ChannelWebhook,
		State:       domain.DeliveryPending,
		MaxAttempts: 5,
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}
	if err := s.CreateDeliveryAttempt(ctx, d); err != nil {
		t.Fatalf("create delivery: %v", err)
	}

	claimed, err := s.ClaimDue(ctx, time.Now(), 10)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if len(claimed) != 1 || claimed[0].State != domain.DeliverySending {
		t.Fatalf("expected 1 claimed in sending state, got %+v", claimed)
	}

	// A second claim finds nothing (already sending).
	again, err := s.ClaimDue(ctx, time.Now(), 10)
	if err != nil {
		t.Fatalf("claim again: %v", err)
	}
	if len(again) != 0 {
		t.Fatalf("expected no re-claim, got %d", len(again))
	}

	// Mark sent.
	d.State = domain.DeliverySent
	d.Attempts = 1
	if err := s.MarkResult(ctx, d, nil); err != nil {
		t.Fatalf("mark result: %v", err)
	}
	got, _ := s.GetDeliveryAttempt(ctx, "d1")
	if got.State != domain.DeliverySent || got.Attempts != 1 {
		t.Fatalf("unexpected final state: %+v", got)
	}
}

func TestClaimRespectsNextRetryAt(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	e, _, _ := s.CreateEvent(ctx, sampleEvent("e1", "", time.Now()))
	future := time.Now().Add(time.Hour)
	d := &domain.DeliveryAttempt{
		ID: "d1", EventID: e.ID, Channel: domain.ChannelWebhook,
		State: domain.DeliveryFailed, MaxAttempts: 5, NextRetryAt: &future,
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	if err := s.CreateDeliveryAttempt(ctx, d); err != nil {
		t.Fatalf("create: %v", err)
	}
	claimed, err := s.ClaimDue(ctx, time.Now(), 10)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if len(claimed) != 0 {
		t.Fatalf("expected nothing due yet, got %d", len(claimed))
	}
}

func TestRequeueStuckSending(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	e, _, _ := s.CreateEvent(ctx, sampleEvent("e1", "", time.Now()))

	// A stuck attempt: state=sending, updated long ago (worker died mid-send).
	old := time.Now().Add(-30 * time.Minute)
	stuck := &domain.DeliveryAttempt{
		ID: "d1", EventID: e.ID, Channel: domain.ChannelWebhook, ChannelName: "wh",
		State: domain.DeliverySending, MaxAttempts: 5, CreatedAt: old, UpdatedAt: old,
	}
	if err := s.CreateDeliveryAttempt(ctx, stuck); err != nil {
		t.Fatalf("create: %v", err)
	}
	// A fresh sending attempt should NOT be requeued.
	fresh := &domain.DeliveryAttempt{
		ID: "d2", EventID: e.ID, Channel: domain.ChannelWebhook, ChannelName: "wh",
		State: domain.DeliverySending, MaxAttempts: 5, CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	if err := s.CreateDeliveryAttempt(ctx, fresh); err != nil {
		t.Fatalf("create fresh: %v", err)
	}

	n, err := s.RequeueStuckSending(ctx, time.Now().Add(-5*time.Minute))
	if err != nil {
		t.Fatalf("requeue: %v", err)
	}
	if n != 1 {
		t.Fatalf("expected 1 requeued, got %d", n)
	}
	got, _ := s.GetDeliveryAttempt(ctx, "d1")
	if got.State != domain.DeliveryPending {
		t.Fatalf("stuck attempt not requeued: %s", got.State)
	}
	if f, _ := s.GetDeliveryAttempt(ctx, "d2"); f.State != domain.DeliverySending {
		t.Fatalf("fresh attempt should stay sending, got %s", f.State)
	}
}

// Payload text goes through the store as a raw JSON string, so the characters
// that break naive quoting have to survive it. This runs on SQLite; the same
// assertion against PostgreSQL, where payload is JSONB rather than TEXT, is in
// TestPostgresEventRoundTrip.
func TestPayloadSurvivesStorage(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Date(2026, 8, 22, 9, 0, 0, 0, time.UTC)

	e := sampleEvent("payload-1", "payload:key", now)
	e.Payload = []byte(`{"quote":"it's","dquote":"say \"hi\"","newline":"a\nb","unicode":"привет — ok","brace":"a}b","nested":{"n":1.5,"list":[1,"two",null]}}`)
	if _, _, err := s.CreateEvent(ctx, e); err != nil {
		t.Fatalf("create: %v", err)
	}

	got, err := s.GetEvent(ctx, "payload-1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	var back map[string]any
	if err := json.Unmarshal(got.Payload, &back); err != nil {
		t.Fatalf("payload is not valid JSON after the round trip: %v (%s)", err, got.Payload)
	}
	for k, want := range map[string]string{
		"quote":   "it's",
		"dquote":  `say "hi"`,
		"newline": "a\nb",
		"unicode": "привет — ok",
		"brace":   "a}b",
	} {
		if back[k] != want {
			t.Errorf("payload[%q] = %v, want %q", k, back[k], want)
		}
	}
}

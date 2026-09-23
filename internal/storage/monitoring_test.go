package storage

import (
	"context"
	"testing"
	"time"

	"github.com/golovanov-dev/alertloop/internal/domain"
)

func TestMonitoringSignals(t *testing.T) {
	checkMonitoring(t, newTestStore(t))
}

func TestPostgresMonitoringSignals(t *testing.T) {
	checkMonitoring(t, postgresStore(t))
}

func checkMonitoring(t *testing.T, s Store) {
	t.Helper()
	ctx := context.Background()
	now := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)
	since := now.Add(-24 * time.Hour)

	m, err := s.Monitoring(ctx, now, since)
	if err != nil {
		t.Fatalf("monitoring on an empty database: %v", err)
	}
	if m != (Monitoring{}) {
		t.Fatalf("empty database = %+v, want zero counts and no times", m)
	}

	// Open incident, resolved incident, business event: one is open.
	open := sampleEvent("open", "", now.Add(-2*time.Hour))
	closed := sampleEvent("closed", "", now.Add(-2*time.Hour))
	closed.State = domain.StateResolved
	business := sampleEvent("biz", "", now.Add(-2*time.Hour))
	business.Type = domain.EventBusiness
	for _, e := range []*domain.Event{open, closed, business} {
		if _, _, err := s.CreateEvent(ctx, e); err != nil {
			t.Fatalf("create %s: %v", e.ID, err)
		}
	}

	at := func(d time.Duration) *time.Time { v := now.Add(d); return &v }
	attempts := []*domain.DeliveryAttempt{
		// Due since 10 minutes ago: the oldest the worker could take.
		{ID: "due", ChannelName: "a", Kind: domain.KindAlert, State: domain.DeliveryPending, CreatedAt: now.Add(-10 * time.Minute)},
		// Created earlier, but its retry is not due yet.
		{ID: "later", ChannelName: "b", Kind: domain.KindAlert, State: domain.DeliveryFailed, NextRetryAt: at(time.Minute), CreatedAt: now.Add(-time.Hour)},
		// Older still, but held back until the alert on its channel is sent.
		{ID: "held", ChannelName: "b", Kind: domain.KindRecovery, State: domain.DeliveryPending, CreatedAt: now.Add(-50 * time.Minute)},
		// Dead-lettered an hour ago counts; 25 hours ago does not.
		{ID: "dl-recent", ChannelName: "c", Kind: domain.KindAlert, State: domain.DeliveryDeadLetter, CreatedAt: now.Add(-2 * time.Hour), UpdatedAt: now.Add(-time.Hour)},
		{ID: "dl-old", ChannelName: "d", Kind: domain.KindAlert, State: domain.DeliveryDeadLetter, CreatedAt: now.Add(-26 * time.Hour), UpdatedAt: now.Add(-25 * time.Hour)},
	}
	for _, a := range attempts {
		a.EventID, a.Channel, a.MaxAttempts = "open", domain.ChannelWebhook, 5
		if a.UpdatedAt.IsZero() {
			a.UpdatedAt = a.CreatedAt
		}
		if err := s.CreateDeliveryAttempt(ctx, a); err != nil {
			t.Fatalf("create attempt %s: %v", a.ID, err)
		}
	}

	// A late write from a second worker does not move the tick back.
	for _, tick := range []time.Time{now.Add(-5 * time.Second), now.Add(-20 * time.Second)} {
		if err := s.RecordWorkerTick(ctx, tick); err != nil {
			t.Fatalf("record tick: %v", err)
		}
	}

	m, err = s.Monitoring(ctx, now, since)
	if err != nil {
		t.Fatalf("monitoring: %v", err)
	}
	if m.OpenIncidents != 1 {
		t.Errorf("open incidents = %d, want 1", m.OpenIncidents)
	}
	if m.OldestDueSince == nil || !m.OldestDueSince.Equal(now.Add(-10*time.Minute)) {
		t.Errorf("oldest due since = %v, want %v", m.OldestDueSince, now.Add(-10*time.Minute))
	}
	if m.DeadLetters != 1 {
		t.Errorf("dead letters in 24h = %d, want 1", m.DeadLetters)
	}
	if m.LastWorkerTick == nil || !m.LastWorkerTick.Equal(now.Add(-5*time.Second)) {
		t.Errorf("last worker tick = %v, want %v", m.LastWorkerTick, now.Add(-5*time.Second))
	}
}

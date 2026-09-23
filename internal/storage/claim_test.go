package storage

import (
	"context"
	"sort"
	"testing"
	"time"

	"github.com/golovanov-dev/alertloop/internal/domain"
)

// A recovery waits for the alert of its own occurrence on its own channel:
// not delivered while that alert is failed or dead-lettered, delivered once it
// is sent. Another occurrence of the same incident, and another channel of the
// same one, do not hold it back.
func TestClaimHoldsRecoveryUntilItsAlertIsSent(t *testing.T) {
	checkRecoveryWaitsForAlert(t, newTestStore(t))
}

func TestPostgresClaimHoldsRecoveryUntilItsAlertIsSent(t *testing.T) {
	checkRecoveryWaitsForAlert(t, postgresStore(t))
}

func checkRecoveryWaitsForAlert(t *testing.T, s Store) {
	t.Helper()
	ctx := context.Background()
	now := time.Date(2026, 9, 21, 9, 0, 0, 0, time.UTC)
	later := now.Add(time.Hour)

	// First occurrence, already resolved; second occurrence of the same key.
	first := sampleEvent("occ-1", "svc:db", now)
	first.State, first.LastSeenAt = domain.StateResolved, now
	second := sampleEvent("occ-2", "svc:db", now.Add(time.Minute))
	second.LastSeenAt = now.Add(time.Minute)
	for _, e := range []*domain.Event{first, second} {
		if _, _, err := s.CreateEvent(ctx, e); err != nil {
			t.Fatalf("create event %s: %v", e.ID, err)
		}
	}

	add := func(id, eventID, channel string, kind domain.DeliveryKind, state domain.DeliveryState, retry *time.Time) {
		t.Helper()
		d := &domain.DeliveryAttempt{
			ID: id, EventID: eventID, Channel: domain.ChannelTelegram, ChannelName: channel,
			Kind: kind, State: state, MaxAttempts: 5, NextRetryAt: retry,
			CreatedAt: now, UpdatedAt: now,
		}
		if err := s.CreateDeliveryAttempt(ctx, d); err != nil {
			t.Fatalf("create attempt %s: %v", id, err)
		}
	}
	add("alert-1-tg", "occ-1", "tg", domain.KindAlert, domain.DeliveryFailed, &later)
	add("recovery-1-tg", "occ-1", "tg", domain.KindRecovery, domain.DeliveryPending, nil)
	add("alert-1-ops", "occ-1", "ops", domain.KindAlert, domain.DeliverySent, nil)
	add("recovery-1-ops", "occ-1", "ops", domain.KindRecovery, domain.DeliveryPending, nil)
	add("alert-2-tg", "occ-2", "tg", domain.KindAlert, domain.DeliverySent, nil)
	add("recovery-2-tg", "occ-2", "tg", domain.KindRecovery, domain.DeliveryPending, nil)

	claim := func(at time.Time, want ...string) {
		t.Helper()
		claimed, err := s.ClaimDue(ctx, at, 50)
		if err != nil {
			t.Fatalf("claim: %v", err)
		}
		var got []string
		for _, c := range claimed {
			got = append(got, c.ID)
		}
		sort.Strings(got)
		sort.Strings(want)
		if len(got) != len(want) {
			t.Fatalf("claimed %v, want %v", got, want)
		}
		for i := range got {
			if got[i] != want[i] {
				t.Fatalf("claimed %v, want %v", got, want)
			}
		}
	}
	mark := func(id string, state domain.DeliveryState) {
		t.Helper()
		d, err := s.GetDeliveryAttempt(ctx, id)
		if err != nil {
			t.Fatalf("get %s: %v", id, err)
		}
		d.State, d.NextRetryAt = state, nil
		if err := s.MarkResult(ctx, d); err != nil {
			t.Fatalf("mark %s: %v", id, err)
		}
	}

	// The telegram alert of the first occurrence is waiting for a retry.
	claim(now, "recovery-1-ops", "recovery-2-tg")

	// Its retry comes due and it goes out alone; the last try fails, and the
	// recovery keeps waiting behind the dead-lettered alert.
	claim(later, "alert-1-tg")
	mark("alert-1-tg", domain.DeliveryDeadLetter)
	claim(later)

	// Replayed: the alert goes first, the recovery only after it is sent.
	if _, err := s.Replay(ctx, "alert-1-tg", later); err != nil {
		t.Fatalf("replay: %v", err)
	}
	claim(later, "alert-1-tg")
	mark("alert-1-tg", domain.DeliverySent)
	claim(later, "recovery-1-tg")
}

// The worker passes the channels whose share of its slots is used up; their
// attempts stay queued and the next channel's are claimed instead.
func TestClaimSkipsTheGivenChannels(t *testing.T) {
	checkClaimSkipsChannels(t, newTestStore(t))
}

func TestPostgresClaimSkipsTheGivenChannels(t *testing.T) {
	checkClaimSkipsChannels(t, postgresStore(t))
}

func checkClaimSkipsChannels(t *testing.T, s Store) {
	t.Helper()
	ctx := context.Background()
	now := time.Date(2026, 9, 22, 9, 0, 0, 0, time.UTC)
	if _, _, err := s.CreateEvent(ctx, sampleEvent("ev", "", now)); err != nil {
		t.Fatalf("create event: %v", err)
	}
	for i, channel := range []string{"mail", "hook", "tg"} {
		d := &domain.DeliveryAttempt{
			ID: channel, EventID: "ev", Channel: domain.ChannelWebhook, ChannelName: channel,
			State: domain.DeliveryPending, MaxAttempts: 5,
			CreatedAt: now.Add(time.Duration(i) * time.Second), UpdatedAt: now,
		}
		if err := s.CreateDeliveryAttempt(ctx, d); err != nil {
			t.Fatalf("create attempt %s: %v", channel, err)
		}
	}
	claimed, err := s.ClaimDue(ctx, now.Add(time.Hour), 10, "mail", "hook")
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if len(claimed) != 1 || claimed[0].ID != "tg" {
		t.Fatalf("claimed %v, want only tg", claimed)
	}
}

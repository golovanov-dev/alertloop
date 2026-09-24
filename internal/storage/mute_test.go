package storage

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/golovanov-dev/alertloop/internal/domain"
)

// Mute cancels the alerts not sent yet, and one that was `sending` at the
// moment of mute is not sent yet either: whatever hands it back without a
// successful send (a failure, dead-lettering, the worker stopping, the reaper)
// stores it as `cancelled`. A sent alert stays sent, a recovery and the alerts
// of an event that is not muted are handed back as before, and a dead-lettered
// alert is left for the operator to replay.
func TestMuteCancelsAlertsHandedBackAfterMute(t *testing.T) {
	checkMuteCancelsAlertsHandedBackAfterMute(t, newTestStore(t))
}

func TestPostgresMuteCancelsAlertsHandedBackAfterMute(t *testing.T) {
	checkMuteCancelsAlertsHandedBackAfterMute(t, postgresStore(t))
}

func checkMuteCancelsAlertsHandedBackAfterMute(t *testing.T, s Store) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	stale := now.Add(-time.Hour)

	for _, id := range []string{"muted", "open"} {
		if _, _, err := s.CreateEvent(ctx, sampleEvent(id, "svc:"+id, now)); err != nil {
			t.Fatalf("create event %s: %v", id, err)
		}
	}
	queue := func(id, event string, kind domain.DeliveryKind, state domain.DeliveryState, updated time.Time) {
		t.Helper()
		d := &domain.DeliveryAttempt{
			ID: id, EventID: event, Channel: domain.ChannelWebhook, ChannelName: "wh-" + id,
			Kind: kind, State: state, Attempts: 1, MaxAttempts: 2, CreatedAt: updated, UpdatedAt: updated,
		}
		if err := s.CreateDeliveryAttempt(ctx, d); err != nil {
			t.Fatalf("create %s: %v", id, err)
		}
	}
	for _, id := range []string{"fails", "dead", "back", "sent"} {
		queue(id, "muted", domain.KindAlert, domain.DeliverySending, now)
		queue("open-"+id, "open", domain.KindAlert, domain.DeliverySending, now)
	}
	queue("stuck", "muted", domain.KindAlert, domain.DeliverySending, stale)
	queue("stuck-recovery", "muted", domain.KindRecovery, domain.DeliverySending, stale)
	queue("open-stuck", "open", domain.KindAlert, domain.DeliverySending, stale)
	queue("dead-letter", "muted", domain.KindAlert, domain.DeliveryDeadLetter, now)

	if _, err := s.TransitionEvent(ctx, "muted", StateChange{
		From: domain.StateNew, To: domain.StateMuted, At: now, CancelAlerts: true,
	}); err != nil {
		t.Fatalf("mute: %v", err)
	}

	retry := now.Add(time.Minute)
	outcomes := map[string]domain.DeliveryAttempt{
		"fails": {State: domain.DeliveryFailed, Attempts: 2, NextRetryAt: &retry, LastError: "503"},
		"dead":  {State: domain.DeliveryDeadLetter, Attempts: 2, LastError: "503"},
		"back":  {State: domain.DeliveryPending, Attempts: 1},
		"sent":  {State: domain.DeliverySent, Attempts: 2},
	}
	for id, out := range outcomes {
		for _, prefix := range []string{"", "open-"} {
			d := out
			d.ID, d.UpdatedAt = prefix+id, now
			if err := s.MarkResult(ctx, &d, nil); err != nil {
				t.Fatalf("mark %s: %v", d.ID, err)
			}
			want := out.State
			if prefix == "" && out.State != domain.DeliverySent {
				want = domain.DeliveryCancelled
			}
			if d.State != want {
				t.Fatalf("MarkResult reported %s as %q, want %q", d.ID, d.State, want)
			}
		}
	}
	if _, err := s.RequeueStuckSending(ctx, now.Add(-time.Minute)); err != nil {
		t.Fatalf("requeue: %v", err)
	}

	want := map[string]domain.DeliveryState{
		"fails":          domain.DeliveryCancelled,
		"dead":           domain.DeliveryCancelled,
		"back":           domain.DeliveryCancelled,
		"sent":           domain.DeliverySent,
		"stuck":          domain.DeliveryCancelled,
		"stuck-recovery": domain.DeliveryPending,
		"dead-letter":    domain.DeliveryDeadLetter,
		"open-fails":     domain.DeliveryFailed,
		"open-dead":      domain.DeliveryDeadLetter,
		"open-back":      domain.DeliveryPending,
		"open-sent":      domain.DeliverySent,
		"open-stuck":     domain.DeliveryPending,
	}
	for id, state := range want {
		got, err := s.GetDeliveryAttempt(ctx, id)
		if err != nil {
			t.Fatalf("get %s: %v", id, err)
		}
		if got.State != state {
			t.Fatalf("%s is %q, want %q", id, got.State, state)
		}
		if state == domain.DeliveryCancelled && got.NextRetryAt != nil {
			t.Fatalf("cancelled %s keeps next_retry_at %v", id, got.NextRetryAt)
		}
	}
	if got, _ := s.GetDeliveryAttempt(ctx, "open-fails"); got.NextRetryAt == nil {
		t.Fatal("failed alert of an event that is not muted lost its next_retry_at")
	}

	// The worker never takes a cancelled attempt back.
	claimed, err := s.ClaimDue(ctx, now.Add(time.Hour), 50)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	for _, d := range claimed {
		if d.EventID == "muted" && d.Kind == domain.KindAlert {
			t.Fatalf("claimed alert %s of a muted incident", d.ID)
		}
	}

	// Replay is the operator's explicit decision and still works.
	replayed, err := s.Replay(ctx, "dead-letter", now)
	if err != nil {
		t.Fatalf("replay dead-lettered alert of a muted incident: %v", err)
	}
	if replayed.State != domain.DeliveryPending {
		t.Fatalf("replayed alert is %q, want pending", replayed.State)
	}
}

// A failure recorded at the same moment as a mute ends `cancelled` whichever
// lands first: recorded first, it is `failed` and the mute cancels it;
// recorded second, it sees the mute. On PostgreSQL this is the share lock in
// mutedAlertCond at work: without it the result could read the event from
// before the mute while the mute skips the row that is still `sending`.
func TestMuteRacingAFailureCancelsTheAlert(t *testing.T) {
	checkMuteRacingAFailureCancelsTheAlert(t, newTestStore(t))
}

func TestPostgresMuteRacingAFailureCancelsTheAlert(t *testing.T) {
	checkMuteRacingAFailureCancelsTheAlert(t, postgresStore(t))
}

func checkMuteRacingAFailureCancelsTheAlert(t *testing.T, s Store) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	const rounds = 30
	for i := 0; i < rounds; i++ {
		id := fmt.Sprintf("race-%d", i)
		if _, _, err := s.CreateEvent(ctx, sampleEvent(id, "svc:"+id, now)); err != nil {
			t.Fatalf("create event: %v", err)
		}
		d := &domain.DeliveryAttempt{
			ID: "alert-" + id, EventID: id, Channel: domain.ChannelWebhook, ChannelName: "wh",
			Kind: domain.KindAlert, State: domain.DeliverySending, MaxAttempts: 5, CreatedAt: now, UpdatedAt: now,
		}
		if err := s.CreateDeliveryAttempt(ctx, d); err != nil {
			t.Fatalf("create alert: %v", err)
		}

		var muteErr, markErr error
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			_, muteErr = s.TransitionEvent(ctx, id, StateChange{
				From: domain.StateNew, To: domain.StateMuted, At: now, CancelAlerts: true,
			})
		}()
		go func() {
			defer wg.Done()
			retry := now.Add(time.Minute)
			markErr = s.MarkResult(ctx, &domain.DeliveryAttempt{
				ID: d.ID, State: domain.DeliveryFailed, Attempts: 1, NextRetryAt: &retry,
				LastError: "503", UpdatedAt: now,
			}, nil)
		}()
		wg.Wait()
		if muteErr != nil || markErr != nil {
			t.Fatalf("round %d: mute %v, mark %v", i, muteErr, markErr)
		}
		got, err := s.GetDeliveryAttempt(ctx, d.ID)
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		if got.State != domain.DeliveryCancelled {
			t.Fatalf("round %d: alert is %q after a mute racing its failure, want cancelled", i, got.State)
		}
	}
}

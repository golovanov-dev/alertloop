package storage

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/golovanov-dev/alertloop/internal/domain"
)

// Two requests closing the same incident at once: the conditional update lets
// exactly one through, and only that one queues recovery attempts. On
// PostgreSQL the second transaction waits on the row lock and then finds the
// state already changed; SQLite serialises them on its single connection.
func TestConcurrentCloseQueuesOneRecovery(t *testing.T) {
	checkConcurrentCloseQueuesOneRecovery(t, newTestStore(t))
}

func TestPostgresConcurrentCloseQueuesOneRecovery(t *testing.T) {
	checkConcurrentCloseQueuesOneRecovery(t, postgresStore(t))
}

func checkConcurrentCloseQueuesOneRecovery(t *testing.T, s Store) {
	t.Helper()
	ctx := context.Background()
	now := time.Date(2026, 9, 22, 9, 0, 0, 0, time.UTC)

	e := sampleEvent("race-1", "svc:race", now)
	e.LastSeenAt = now
	if _, _, err := s.CreateEvent(ctx, e); err != nil {
		t.Fatalf("create event: %v", err)
	}
	for _, ch := range []string{"tg", "mail"} {
		d := &domain.DeliveryAttempt{
			ID: "alert-" + ch, EventID: e.ID, Channel: domain.ChannelTelegram, ChannelName: ch,
			Kind: domain.KindAlert, State: domain.DeliverySent, MaxAttempts: 5, CreatedAt: now, UpdatedAt: now,
		}
		if err := s.CreateDeliveryAttempt(ctx, d); err != nil {
			t.Fatalf("create alert %s: %v", ch, err)
		}
	}

	errs := make([]error, 2)
	var wg sync.WaitGroup
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = s.TransitionEvent(ctx, e.ID, StateChange{
				From: domain.StateNew, To: domain.StateResolved, At: now.Add(time.Minute), RecoveryMaxAttempts: 5,
			})
		}(i)
	}
	wg.Wait()

	won := 0
	for _, err := range errs {
		switch {
		case err == nil:
			won++
		case !errors.Is(err, ErrStateChanged):
			t.Fatalf("close: %v", err)
		}
	}
	if won != 1 {
		t.Fatalf("%d concurrent closes succeeded, want exactly 1", won)
	}
	page, err := s.ListDeliveryAttempts(ctx, DeliveryFilter{Kind: domain.KindRecovery}, 50, "")
	if err != nil {
		t.Fatalf("list recoveries: %v", err)
	}
	if len(page.Items) != 2 {
		t.Fatalf("recovery attempts = %d, want 2 (one per alerted channel)", len(page.Items))
	}
}

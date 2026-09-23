package storage

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/golovanov-dev/alertloop/internal/domain"
)

// The same open dedupe_key inserted twice fails with the driver's
// unique-violation code, which is what the create path recognises.
func TestDuplicateOpenKeyIsAUniqueViolation(t *testing.T) {
	checkDuplicateOpenKeyIsAUniqueViolation(t, newTestStore(t))
}

func TestPostgresDuplicateOpenKeyIsAUniqueViolation(t *testing.T) {
	checkDuplicateOpenKeyIsAUniqueViolation(t, postgresStore(t))
}

func checkDuplicateOpenKeyIsAUniqueViolation(t *testing.T, s Store) {
	t.Helper()
	ctx := context.Background()
	now := time.Date(2026, 9, 22, 9, 0, 0, 0, time.UTC)
	st := s.(*sqlStore)
	if err := st.insertEvent(ctx, st.db, sampleEvent("dup-1", "svc:dup", now)); err != nil {
		t.Fatalf("first insert: %v", err)
	}
	err := st.insertEvent(ctx, st.db, sampleEvent("dup-2", "svc:dup", now))
	if !isUniqueViolation(err) {
		t.Fatalf("second insert error = %v, want a recognised unique violation", err)
	}
}

// Several reports of one new incident at once: one creates it, the others get
// that same event back without an error, and only one set of deliveries exists.
func TestConcurrentCreateWithOneDedupeKey(t *testing.T) {
	checkConcurrentCreateWithOneDedupeKey(t, newTestStore(t))
}

func TestPostgresConcurrentCreateWithOneDedupeKey(t *testing.T) {
	checkConcurrentCreateWithOneDedupeKey(t, postgresStore(t))
}

func checkConcurrentCreateWithOneDedupeKey(t *testing.T, s Store) {
	t.Helper()
	ctx := context.Background()
	now := time.Date(2026, 9, 22, 9, 0, 0, 0, time.UTC)

	const n = 8
	ids := make([]string, n)
	created := make([]bool, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			e := sampleEvent(fmt.Sprintf("race-%d", i), "svc:same-key", now)
			d := &domain.DeliveryAttempt{
				ID: fmt.Sprintf("alert-%d", i), EventID: e.ID, Channel: domain.ChannelWebhook, ChannelName: "wh",
				State: domain.DeliveryPending, MaxAttempts: 5, CreatedAt: now, UpdatedAt: now,
			}
			stored, ok, err := s.CreateEventWithDeliveries(ctx, e, []*domain.DeliveryAttempt{d})
			errs[i], created[i] = err, ok
			if stored != nil {
				ids[i] = stored.ID
			}
		}(i)
	}
	wg.Wait()

	won := 0
	for i := range n {
		if errs[i] != nil {
			t.Fatalf("report %d: %v", i, errs[i])
		}
		if created[i] {
			won++
		}
		if ids[i] != ids[0] {
			t.Fatalf("report %d got event %s, report 0 got %s: want the same incident", i, ids[i], ids[0])
		}
	}
	if won != 1 {
		t.Fatalf("%d reports created the incident, want exactly 1", won)
	}
	page, err := s.ListDeliveryAttempts(ctx, DeliveryFilter{}, 50, "")
	if err != nil {
		t.Fatalf("list deliveries: %v", err)
	}
	if len(page.Items) != 1 {
		t.Fatalf("delivery attempts = %d, want 1", len(page.Items))
	}
}

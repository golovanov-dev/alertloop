package storage

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/golovanov-dev/alertloop/internal/domain"
)

func TestFallbackIsQueuedOnceWithTheDeadLetter(t *testing.T) {
	checkFallbackIsQueuedOnceWithTheDeadLetter(t, newTestStore(t))
}

func TestPostgresFallbackIsQueuedOnceWithTheDeadLetter(t *testing.T) {
	checkFallbackIsQueuedOnceWithTheDeadLetter(t, postgresStore(t))
}

func TestFallbackCopiesToAChannelThatHasTheAlert(t *testing.T) {
	checkFallbackCopiesToAChannelThatHasTheAlert(t, newTestStore(t))
}

func TestPostgresFallbackCopiesToAChannelThatHasTheAlert(t *testing.T) {
	checkFallbackCopiesToAChannelThatHasTheAlert(t, postgresStore(t))
}

func TestFallbackCopyAfterRecoveryGetsItsOwn(t *testing.T) {
	checkFallbackCopyAfterRecoveryGetsItsOwn(t, newTestStore(t))
}

func TestPostgresFallbackCopyAfterRecoveryGetsItsOwn(t *testing.T) {
	checkFallbackCopyAfterRecoveryGetsItsOwn(t, postgresStore(t))
}

func TestRecoveryFollowsItsOwnAlert(t *testing.T) {
	checkRecoveryFollowsItsOwnAlert(t, newTestStore(t))
}

func TestPostgresRecoveryFollowsItsOwnAlert(t *testing.T) {
	checkRecoveryFollowsItsOwnAlert(t, postgresStore(t))
}

func TestCancelledAlertHoldsNoRecovery(t *testing.T) {
	checkCancelledAlertHoldsNoRecovery(t, newTestStore(t))
}

func TestPostgresCancelledAlertHoldsNoRecovery(t *testing.T) {
	checkCancelledAlertHoldsNoRecovery(t, postgresStore(t))
}

func TestFallbackOfAResolvedIncidentGetsItsRecovery(t *testing.T) {
	checkFallbackOfAResolvedIncidentGetsItsRecovery(t, newTestStore(t))
}

func TestPostgresFallbackOfAResolvedIncidentGetsItsRecovery(t *testing.T) {
	checkFallbackOfAResolvedIncidentGetsItsRecovery(t, postgresStore(t))
}

// sendingAlert creates event id (of type typ) with one alert to "tg" in
// `sending`, as ClaimDue leaves it.
func sendingAlert(t *testing.T, s Store, id string, typ domain.EventType, at time.Time) *domain.DeliveryAttempt {
	t.Helper()
	ctx := context.Background()
	e := sampleEvent(id, "", at)
	e.Type = typ
	if _, _, err := s.CreateEvent(ctx, e); err != nil {
		t.Fatalf("create event %s: %v", id, err)
	}
	d := &domain.DeliveryAttempt{
		ID: id + "-tg", EventID: id, Channel: domain.ChannelTelegram, ChannelName: "tg",
		Kind: domain.KindAlert, State: domain.DeliverySending, MaxAttempts: 1, CreatedAt: at, UpdatedAt: at,
	}
	if err := s.CreateDeliveryAttempt(ctx, d); err != nil {
		t.Fatalf("create attempt: %v", err)
	}
	return d
}

// deadLetter records d as dead-lettered with a fallback to "mail".
func deadLetter(t *testing.T, s Store, d *domain.DeliveryAttempt, fallbackID string, at time.Time) {
	t.Helper()
	d.State, d.Attempts, d.LastError, d.UpdatedAt = domain.DeliveryDeadLetter, 1, "telegram send failed (status 502)", at
	fb := &domain.DeliveryAttempt{
		ID: fallbackID, EventID: d.EventID, Channel: domain.ChannelEmail, ChannelName: "mail",
		Kind: domain.KindAlert, State: domain.DeliveryPending, MaxAttempts: 3, CreatedAt: at, UpdatedAt: at,
	}
	if err := s.MarkResult(context.Background(), d, fb); err != nil {
		t.Fatalf("mark %s: %v", d.ID, err)
	}
}

// claimAndSend claims what is due, marks it sent and reports each as
// "<channel> <kind>". It fails the test if a recovery is claimed before the
// alert it follows is sent.
func claimAndSend(t *testing.T, s Store, at time.Time) []string {
	t.Helper()
	ctx := context.Background()
	claimed, err := s.ClaimDue(ctx, at, 10)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	var got []string
	for i := range claimed {
		c := &claimed[i]
		if c.Kind == domain.KindRecovery {
			if c.RecoveryFor == nil {
				t.Fatalf("recovery %s has no recovery_for", c.ID)
			}
			a, err := s.GetDeliveryAttempt(ctx, c.RecoveryFor.ID)
			if err != nil {
				t.Fatalf("get alert of recovery %s: %v", c.ID, err)
			}
			if a.State != domain.DeliverySent {
				t.Fatalf("recovery %s claimed while its alert %s is %s", c.ID, a.ID, a.State)
			}
		}
		c.State, c.Attempts, c.UpdatedAt = domain.DeliverySent, 1, at
		if err := s.MarkResult(ctx, c, nil); err != nil {
			t.Fatalf("mark %s sent: %v", c.ID, err)
		}
		got = append(got, c.ChannelName+" "+string(c.Kind))
	}
	slices.Sort(got)
	return got
}

// recoveryFor returns the recovery that follows alert id.
func recoveryFor(t *testing.T, s Store, event, id string) *domain.DeliveryAttempt {
	t.Helper()
	var found *domain.DeliveryAttempt
	for _, r := range attemptsOf(t, s, event, domain.KindRecovery) {
		if r.RecoveryFor != nil && r.RecoveryFor.ID == id {
			if found != nil {
				t.Fatalf("alert %s has two recoveries: %s and %s", id, found.ID, r.ID)
			}
			found = &r
		}
	}
	return found
}

func attemptsOf(t *testing.T, s Store, event string, kind domain.DeliveryKind) []domain.DeliveryAttempt {
	t.Helper()
	page, err := s.ListDeliveryAttempts(context.Background(), DeliveryFilter{EventID: event, Kind: kind}, 50, "")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	return page.Items
}

func checkFallbackIsQueuedOnceWithTheDeadLetter(t *testing.T, s Store) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	// A business event: a fallback is about delivery, not about incidents.
	d := sendingAlert(t, s, "b", domain.EventBusiness, now)
	deadLetter(t, s, d, "fb1", now)

	got, err := s.GetDeliveryAttempt(ctx, "fb1")
	if err != nil {
		t.Fatalf("the fallback was not queued: %v", err)
	}
	if got.ChannelName != "mail" || got.State != domain.DeliveryPending ||
		got.FallbackOf == nil || got.FallbackOf.ID != d.ID || got.FallbackOf.ChannelName != "tg" {
		t.Fatalf("fallback = %+v, fallback_of = %+v", got, got.FallbackOf)
	}
	src, err := s.GetDeliveryAttempt(ctx, d.ID)
	if err != nil {
		t.Fatal(err)
	}
	if src.FallbackTo == nil || src.FallbackTo.ID != "fb1" || src.FallbackTo.ChannelName != "mail" {
		t.Fatalf("source fallback_to = %+v", src.FallbackTo)
	}

	// Replayed and dead-lettered again: still one fallback for this alert.
	if _, err := s.Replay(ctx, d.ID, now); err != nil {
		t.Fatalf("replay: %v", err)
	}
	if _, err := s.ClaimDue(ctx, now.Add(time.Second), 10); err != nil {
		t.Fatalf("claim: %v", err)
	}
	deadLetter(t, s, d, "fb2", now.Add(time.Second))
	if n := len(attemptsOf(t, s, "b", domain.KindAlert)); n != 2 {
		t.Fatalf("alerts after a second dead letter = %d, want 2 (the alert and one fallback)", n)
	}

	// A muted incident: the alert is cancelled, nothing is redirected.
	m := sendingAlert(t, s, "m", domain.EventIncident, now)
	if _, err := s.TransitionEvent(ctx, "m", StateChange{From: domain.StateNew, To: domain.StateMuted, At: now}); err != nil {
		t.Fatalf("mute: %v", err)
	}
	deadLetter(t, s, m, "fb-m", now)
	if m.State != domain.DeliveryCancelled {
		t.Fatalf("alert of a muted incident stored as %s, want cancelled", m.State)
	}
	if n := len(attemptsOf(t, s, "m", domain.KindAlert)); n != 1 {
		t.Fatalf("alerts of a muted incident = %d, want 1 (no fallback)", n)
	}
}

// An alert that dead-letters after its incident was resolved is redirected all
// the same; the fallback channel must then also hear that it is over, since the
// recoveries were queued before it was told anything.
func checkFallbackOfAResolvedIncidentGetsItsRecovery(t *testing.T, s Store) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	for _, tc := range []struct {
		event      string
		recoveries int // RecoveryMaxAttempts at resolve; 0 is notify_on_resolve off
		want       []string
	}{
		{"r", 3, []string{"mail", "tg"}},
		{"q", 0, nil},
	} {
		d := sendingAlert(t, s, tc.event, domain.EventIncident, now)
		if _, err := s.TransitionEvent(ctx, tc.event, StateChange{
			From: domain.StateNew, To: domain.StateResolved, At: now, RecoveryMaxAttempts: tc.recoveries,
		}); err != nil {
			t.Fatalf("resolve: %v", err)
		}
		deadLetter(t, s, d, "fb-"+tc.event, now)

		var got []string
		for _, r := range attemptsOf(t, s, tc.event, domain.KindRecovery) {
			got = append(got, r.ChannelName)
		}
		slices.Sort(got)
		if !slices.Equal(got, tc.want) {
			t.Fatalf("event %s: recoveries to %v, want %v", tc.event, got, tc.want)
		}
	}
}

// Without routing the fallback channel gets the alert directly too; the copy
// still goes there, since it is what names the broken channel. A direct alert
// that dead-lettered is no exception: that channel may be back up by now.
func checkFallbackCopiesToAChannelThatHasTheAlert(t *testing.T, s Store) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	for _, direct := range []domain.DeliveryState{domain.DeliverySent, domain.DeliveryDeadLetter} {
		event := "direct-" + string(direct)
		d := sendingAlert(t, s, event, domain.EventIncident, now)
		if err := s.CreateDeliveryAttempt(ctx, &domain.DeliveryAttempt{
			ID: event + "-mail", EventID: event, Channel: domain.ChannelEmail, ChannelName: "mail",
			Kind: domain.KindAlert, State: direct, MaxAttempts: 3, CreatedAt: now, UpdatedAt: now,
		}); err != nil {
			t.Fatalf("create mail alert: %v", err)
		}
		deadLetter(t, s, d, "fb-"+event, now)

		src, err := s.GetDeliveryAttempt(ctx, d.ID)
		if err != nil {
			t.Fatalf("get source: %v", err)
		}
		// The console tells a redirected alert that got through from one that
		// did not by the state of the attempt it links to.
		if to := src.FallbackTo; to == nil || to.ID != "fb-"+event || to.State != domain.DeliveryPending {
			t.Errorf("mail alert %s: fallback_to = %+v, want the queued copy in pending", direct, to)
		}
	}
}

// The fallback channel got the alert directly and then its recovery; the copy
// that follows gets a recovery of its own, sent only after the copy, so the
// channel does not end on "down".
func checkFallbackCopyAfterRecoveryGetsItsOwn(t *testing.T, s Store) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	d := sendingAlert(t, s, "e", domain.EventIncident, now)
	if err := s.CreateDeliveryAttempt(ctx, &domain.DeliveryAttempt{
		ID: "e-mail", EventID: "e", Channel: domain.ChannelEmail, ChannelName: "mail",
		Kind: domain.KindAlert, State: domain.DeliverySent, MaxAttempts: 3, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("create mail alert: %v", err)
	}
	if _, err := s.TransitionEvent(ctx, "e", StateChange{
		From: domain.StateNew, To: domain.StateResolved, At: now, RecoveryMaxAttempts: 3,
	}); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	claimAndSend := func(at time.Time) []string { return claimAndSend(t, s, at) }
	// tg is still sending its alert, so only the recovery to mail is due.
	if got := claimAndSend(now); !slices.Equal(got, []string{"mail recovery"}) {
		t.Fatalf("due before the dead letter = %v, want [mail recovery]", got)
	}

	deadLetter(t, s, d, "fb", now.Add(time.Second))
	// The copy's recovery waits for the copy; tg's waits for a replay of tg.
	if got := claimAndSend(now.Add(2 * time.Second)); !slices.Equal(got, []string{"mail alert"}) {
		t.Fatalf("due after the dead letter = %v, want [mail alert] (the copy)", got)
	}
	if got := claimAndSend(now.Add(3 * time.Second)); !slices.Equal(got, []string{"mail recovery"}) {
		t.Fatalf("due after the copy was sent = %v, want [mail recovery]", got)
	}
}

// tg falls back to mail; mail also had the alert directly, and that one
// dead-lettered. The copy gets through, and its recovery follows it; the
// recoveries of the dead-lettered alerts wait for their replay, each for its
// own alert. The incident is closed before tg dead-letters in one run and after
// it in the other: the copy gets exactly one recovery either way.
func checkRecoveryFollowsItsOwnAlert(t *testing.T, s Store) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	for _, resolveFirst := range []bool{true, false} {
		event := "late"
		if resolveFirst {
			event = "early"
		}
		d := sendingAlert(t, s, event, domain.EventIncident, now)
		mailID := event + "-mail"
		if err := s.CreateDeliveryAttempt(ctx, &domain.DeliveryAttempt{
			ID: mailID, EventID: event, Channel: domain.ChannelEmail, ChannelName: "mail",
			Kind: domain.KindAlert, State: domain.DeliveryDeadLetter, Attempts: 3, MaxAttempts: 3,
			CreatedAt: now, UpdatedAt: now,
		}); err != nil {
			t.Fatalf("create mail alert: %v", err)
		}
		resolve := func() {
			t.Helper()
			if _, err := s.TransitionEvent(ctx, event, StateChange{
				From: domain.StateNew, To: domain.StateResolved, At: now, RecoveryMaxAttempts: 3,
			}); err != nil {
				t.Fatalf("resolve: %v", err)
			}
		}
		if resolveFirst {
			resolve()
		}
		copyID := "fb-" + event
		deadLetter(t, s, d, copyID, now.Add(time.Second))
		if !resolveFirst {
			resolve()
		}

		for _, id := range []string{d.ID, mailID, copyID} {
			r := recoveryFor(t, s, event, id)
			if r == nil {
				t.Fatalf("%s: alert %s has no recovery", event, id)
			}
			if id == copyID && (r.RecoveryFor.ChannelName != "mail" || r.RecoveryFor.State != domain.DeliveryPending) {
				t.Errorf("%s: recovery_for of the copy's recovery = %+v, want mail in pending", event, r.RecoveryFor)
			}
		}
		if n := len(attemptsOf(t, s, event, domain.KindRecovery)); n != 3 {
			t.Fatalf("%s: %d recoveries, want 3 (one per alert)", event, n)
		}

		at := now.Add(2 * time.Second)
		step := func(want ...string) {
			t.Helper()
			at = at.Add(time.Second)
			if got := claimAndSend(t, s, at); !slices.Equal(got, want) {
				t.Fatalf("%s: due = %v, want %v", event, got, want)
			}
		}
		step("mail alert")    // the copy
		step("mail recovery") // the copy's recovery, not held by the dead direct alert
		step()                // the other two wait for their alerts
		if _, err := s.Replay(ctx, mailID, at); err != nil {
			t.Fatalf("replay mail: %v", err)
		}
		step("mail alert")
		step("mail recovery")
		if _, err := s.Replay(ctx, d.ID, at); err != nil {
			t.Fatalf("replay tg: %v", err)
		}
		step("tg alert")
		step("tg recovery")
		step()
	}
}

// A direct alert cancelled by a mute (and the incident unmuted) gets no
// recovery and holds none: the channel still hears "resolved" after the
// fallback copy it did get.
func checkCancelledAlertHoldsNoRecovery(t *testing.T, s Store) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	d := sendingAlert(t, s, "c", domain.EventIncident, now)
	if err := s.CreateDeliveryAttempt(ctx, &domain.DeliveryAttempt{
		ID: "c-mail", EventID: "c", Channel: domain.ChannelEmail, ChannelName: "mail",
		Kind: domain.KindAlert, State: domain.DeliveryCancelled, MaxAttempts: 3, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("create mail alert: %v", err)
	}
	deadLetter(t, s, d, "fb-c", now.Add(time.Second))
	if _, err := s.TransitionEvent(ctx, "c", StateChange{
		From: domain.StateNew, To: domain.StateResolved, At: now, RecoveryMaxAttempts: 3,
	}); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if r := recoveryFor(t, s, "c", "c-mail"); r != nil {
		t.Fatalf("the cancelled alert got recovery %s", r.ID)
	}
	if n := len(attemptsOf(t, s, "c", domain.KindRecovery)); n != 2 {
		t.Fatalf("%d recoveries, want 2 (tg and the copy)", n)
	}
	at := now.Add(2 * time.Second)
	for _, want := range [][]string{{"mail alert"}, {"mail recovery"}, nil} {
		at = at.Add(time.Second)
		if got := claimAndSend(t, s, at); !slices.Equal(got, want) {
			t.Fatalf("due = %v, want %v", got, want)
		}
	}
}

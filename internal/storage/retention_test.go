package storage

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/golovanov-dev/alertloop/internal/domain"
)

func eventAt(id, key string, state domain.EventState, created, lastSeen time.Time, resolved *time.Time) *domain.Event {
	return &domain.Event{
		ID: id, Type: domain.EventIncident, Severity: domain.SeverityError,
		State: state, Source: "monit", Message: "m", DedupeKey: key,
		Payload:   []byte(`{}`),
		CreatedAt: created, UpdatedAt: created, LastSeenAt: lastSeen, ResolvedAt: resolved,
	}
}

// The defect this guards: retention aged an event by created_at, so the
// incident that had been burning LONGEST was the first one deleted - while it
// was still burning. Everything downstream then went wrong quietly: the
// incident vanished from the API, the eventual `resolved` found nothing to
// close and notified nobody, and the next report opened a fresh incident and
// woke everyone again.
func TestRetentionKeepsIncidentsThatAreStillFiring(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	cutoff := now.AddDate(0, 0, -30)

	old := now.AddDate(0, 0, -40)
	recent := now.Add(-time.Minute)
	resolvedLongAgo := now.AddDate(0, 0, -35)
	resolvedYesterday := now.AddDate(0, 0, -1)

	cases := []struct {
		id       string
		state    domain.EventState
		created  time.Time
		lastSeen time.Time
		resolved *time.Time
		survives bool
		why      string
	}{
		{
			id: "open-old-but-live", state: domain.StateNew,
			created: old, lastSeen: recent, resolved: nil, survives: true,
			why: "open 40 days, reported a minute ago - this is current state, not history",
		},
		{
			id: "acked-old-but-live", state: domain.StateAcknowledged,
			created: old, lastSeen: recent, resolved: nil, survives: true,
			why: "an acknowledged incident is still open",
		},
		{
			id: "open-abandoned", state: domain.StateNew,
			created: old, lastSeen: old, resolved: nil, survives: false,
			why: "open but not reported for 40 days - the source is gone",
		},
		{
			id: "closed-long-ago", state: domain.StateResolved,
			created: old, lastSeen: old, resolved: &resolvedLongAgo, survives: false,
			why: "resolved 35 days ago - history, past the window",
		},
		{
			id: "born-old-closed-yesterday", state: domain.StateResolved,
			created: old, lastSeen: old, resolved: &resolvedYesterday, survives: true,
			why: "an incident ages from when it ended, not when it began",
		},
		{
			id: "recent", state: domain.StateNew,
			created: recent, lastSeen: recent, resolved: nil, survives: true,
			why: "obviously",
		},
	}

	for _, c := range cases {
		e := eventAt(c.id, c.id+":key", c.state, c.created, c.lastSeen, c.resolved)
		if _, _, err := s.CreateEvent(ctx, e); err != nil {
			t.Fatalf("create %s: %v", c.id, err)
		}
	}

	if _, err := s.DeleteEventsBefore(ctx, cutoff); err != nil {
		t.Fatalf("retention: %v", err)
	}

	for _, c := range cases {
		_, err := s.GetEvent(ctx, c.id)
		found := err == nil
		if !found && !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("get %s: %v", c.id, err)
		}
		if found != c.survives {
			verb := "was deleted"
			if found {
				verb = "survived"
			}
			t.Errorf("%s %s, want the opposite: %s", c.id, verb, c.why)
		}
	}
}

// A non-lifecycle client - anything written before 0.4.0, or any source that
// sends no `status` - creates events whose last_seen_at equals created_at.
// Retention must behave for them exactly as it did in 0.3.x.
func TestRetentionUnchangedForOneOffEvents(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	cutoff := now.AddDate(0, 0, -30)

	for i, created := range []time.Time{
		now.AddDate(0, 0, -60),
		now.AddDate(0, 0, -31),
		now.AddDate(0, 0, -29),
		now,
	} {
		// LastSeenAt left zero on purpose: the store fills it from CreatedAt,
		// which is what the 0003 migration did to every pre-existing row.
		e := eventAt(fmt.Sprintf("one-off-%d", i), "", domain.StateNew, created, time.Time{}, nil)
		if _, _, err := s.CreateEvent(ctx, e); err != nil {
			t.Fatalf("create: %v", err)
		}
	}

	n, err := s.DeleteEventsBefore(ctx, cutoff)
	if err != nil {
		t.Fatalf("retention: %v", err)
	}
	if n != 2 {
		t.Fatalf("deleted %d events, want the 2 older than the window", n)
	}
}

// Deleting an event takes its delivery attempts with it, and the standalone
// sweep must not touch attempts belonging to an event that is still retained -
// the delivery history of a live incident is exactly what an operator opens
// when asking why a notification never arrived.
func TestRetentionKeepsDeliveriesOfRetainedEvents(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	cutoff := now.AddDate(0, 0, -30)
	old := now.AddDate(0, 0, -40)

	live := eventAt("live", "live:key", domain.StateNew, old, now.Add(-time.Minute), nil)
	if _, _, err := s.CreateEvent(ctx, live); err != nil {
		t.Fatalf("create: %v", err)
	}
	att := &domain.DeliveryAttempt{
		ID: "att-old", EventID: "live", Channel: domain.ChannelWebhook, ChannelName: "wh",
		Kind: domain.KindAlert, State: domain.DeliverySent, MaxAttempts: 5,
		CreatedAt: old, UpdatedAt: old,
	}
	if err := s.CreateDeliveryAttempt(ctx, att); err != nil {
		t.Fatalf("create attempt: %v", err)
	}

	if _, err := s.DeleteEventsBefore(ctx, cutoff); err != nil {
		t.Fatalf("retention events: %v", err)
	}
	if _, err := s.DeleteDeliveryAttemptsBefore(ctx, cutoff); err != nil {
		t.Fatalf("retention deliveries: %v", err)
	}

	if _, err := s.GetDeliveryAttempt(ctx, "att-old"); err != nil {
		t.Fatalf("the alert history of a live incident was swept: %v", err)
	}
}

// MarkResult writes only to a row that is still `sending`. Otherwise a result
// arriving late - after the reaper decided the attempt was stuck and requeued
// it - would stamp a stale outcome over a job already queued for another try.
func TestMarkResultOnlyWritesToAClaimedAttempt(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now()

	e, _, err := s.CreateEvent(ctx, eventAt("e1", "", domain.StateNew, now, now, nil))
	if err != nil {
		t.Fatalf("create event: %v", err)
	}
	att := &domain.DeliveryAttempt{
		ID: "d1", EventID: e.ID, Channel: domain.ChannelWebhook, ChannelName: "wh",
		State: domain.DeliveryPending, MaxAttempts: 5, CreatedAt: now, UpdatedAt: now,
	}
	if err := s.CreateDeliveryAttempt(ctx, att); err != nil {
		t.Fatalf("create attempt: %v", err)
	}

	// Not claimed yet: a result must not apply.
	att.State = domain.DeliverySent
	if err := s.MarkResult(ctx, att, nil); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("MarkResult on a pending attempt: err = %v, want ErrNotFound", err)
	}
	stored, _ := s.GetDeliveryAttempt(ctx, "d1")
	if stored.State != domain.DeliveryPending {
		t.Fatalf("state = %q, want it left pending", stored.State)
	}

	// Claimed: it applies.
	if _, err := s.ClaimDue(ctx, now.Add(time.Hour), 10); err != nil {
		t.Fatalf("claim: %v", err)
	}
	att.Attempts = 1
	if err := s.MarkResult(ctx, att, nil); err != nil {
		t.Fatalf("MarkResult on a claimed attempt: %v", err)
	}
	stored, _ = s.GetDeliveryAttempt(ctx, "d1")
	if stored.State != domain.DeliverySent {
		t.Fatalf("state = %q, want sent", stored.State)
	}

	// And a second, late result finds nothing to write to.
	if err := s.MarkResult(ctx, att, nil); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("a repeated MarkResult: err = %v, want ErrNotFound", err)
	}
}

// A delivery error carrying non-Latin text used to be cut mid-character. SQLite
// stored the broken bytes; PostgreSQL rejects them outright, which left the
// attempt stuck in `sending` and requeued by the reaper every five minutes,
// forever. Storing it must produce valid UTF-8 on both engines.
func TestLastErrorIsTruncatedByRunes(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now()

	if _, _, err := s.CreateEvent(ctx, eventAt("e1", "", domain.StateNew, now, now, nil)); err != nil {
		t.Fatalf("create event: %v", err)
	}
	att := &domain.DeliveryAttempt{
		ID: "d1", EventID: "e1", Channel: domain.ChannelTelegram, ChannelName: "tg",
		State: domain.DeliveryPending, MaxAttempts: 5, CreatedAt: now, UpdatedAt: now,
	}
	if err := s.CreateDeliveryAttempt(ctx, att); err != nil {
		t.Fatalf("create attempt: %v", err)
	}
	if _, err := s.ClaimDue(ctx, now.Add(time.Hour), 10); err != nil {
		t.Fatalf("claim: %v", err)
	}

	// Cyrillic is 2 bytes per rune, so a 1000-BYTE cut lands mid-character.
	att.State = domain.DeliveryFailed
	att.Attempts = 1
	att.LastError = "telegram: " + strings.Repeat("Ошибка сервера ", 200)
	if err := s.MarkResult(ctx, att, nil); err != nil {
		t.Fatalf("mark result: %v", err)
	}

	stored, err := s.GetDeliveryAttempt(ctx, "d1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !utf8.ValidString(stored.LastError) {
		t.Fatalf("stored last_error is not valid UTF-8: %q", stored.LastError[len(stored.LastError)-8:])
	}
	if n := len([]rune(stored.LastError)); n > domain.MaxLastErrorRunes {
		t.Fatalf("stored %d runes, want at most %d", n, domain.MaxLastErrorRunes)
	}
	if !strings.HasPrefix(stored.LastError, "telegram: ") {
		t.Fatalf("the beginning of the error was lost: %q", stored.LastError[:20])
	}
}

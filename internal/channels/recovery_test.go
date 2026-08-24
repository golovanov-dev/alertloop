package channels

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/golovanov-dev/alertloop/internal/domain"
)

func resolvedEvent() *domain.Event {
	started := time.Date(2026, 8, 22, 10, 0, 0, 0, time.UTC)
	ended := started.Add(12*time.Minute + 30*time.Second)
	e := sampleEvent()
	e.Message = "PostgreSQL does not respond on port 5432"
	e.DedupeKey = "server-01:postgresql:availability"
	e.State = domain.StateResolved
	e.CreatedAt = started
	e.LastSeenAt = ended
	e.ResolvedAt = &ended
	return e
}

// A recovery must be readable as good news in the first two words. Repeating
// "[CRITICAL]" on the message that says the problem is over is how a recovery
// gets read as another failure — on a phone, that is all the reader sees.
func TestRecoverySubjectLeadsWithResolved(t *testing.T) {
	subject := subjectLine(domain.Recovery(resolvedEvent()))
	if !strings.HasPrefix(subject, "[RESOLVED] ") {
		t.Fatalf("recovery subject = %q, want it to lead with [RESOLVED]", subject)
	}
	if strings.Contains(subject, "CRITICAL") {
		t.Fatalf("recovery subject still shouts the severity: %q", subject)
	}
	if !strings.Contains(subject, "PostgreSQL does not respond") {
		t.Fatalf("recovery subject does not identify what recovered: %q", subject)
	}

	alert := subjectLine(domain.Alert(sampleEvent()))
	if !strings.HasPrefix(alert, "[CRITICAL/incident] ") {
		t.Fatalf("alert subject changed: %q", alert)
	}
}

func TestRecoveryBodyCarriesTheOutageWindow(t *testing.T) {
	body := plainBody(domain.Recovery(resolvedEvent()))

	for _, want := range []string{
		"The incident below is resolved.",
		"Key:      server-01:postgresql:availability",
		"Started:  2026-08-22 10:00:00 UTC",
		"Resolved: 2026-08-22 10:12:30 UTC",
		"Duration: 12m30s",
		"State:    resolved",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("recovery body missing %q:\n%s", want, body)
		}
	}
}

// An alert must not claim to be resolved, and must not print a recovery time it
// does not have.
func TestAlertBodyHasNoRecoveryFields(t *testing.T) {
	body := plainBody(domain.Alert(sampleEvent()))
	for _, unwanted := range []string{"resolved", "Resolved:", "Duration:"} {
		if strings.Contains(body, unwanted) {
			t.Fatalf("alert body contains %q:\n%s", unwanted, body)
		}
	}
}

// An incident that has been firing for a while shows when it was last seen, so
// the reader can tell a live problem from a stale one. For an event reported
// once, the two timestamps are the same and the line is noise.
func TestAlertBodyShowsLastSeenOnlyWhenItMoved(t *testing.T) {
	e := sampleEvent()
	e.CreatedAt = time.Date(2026, 8, 22, 10, 0, 0, 0, time.UTC)
	e.LastSeenAt = e.CreatedAt
	if body := plainBody(domain.Alert(e)); strings.Contains(body, "Last seen:") {
		t.Fatalf("a once-reported event should not print Last seen:\n%s", body)
	}

	e.LastSeenAt = e.CreatedAt.Add(9 * time.Minute)
	body := plainBody(domain.Alert(e))
	if !strings.Contains(body, "Last seen: 2026-08-22 10:09:00 UTC") {
		t.Fatalf("a still-firing incident should print Last seen:\n%s", body)
	}
}

func TestHumanDuration(t *testing.T) {
	cases := []struct {
		in   time.Duration
		want string
	}{
		{0, "0s"},
		{45 * time.Second, "45s"},
		{90 * time.Second, "1m30s"},
		{12*time.Minute + 30*time.Second, "12m30s"},
		{59*time.Minute + 59*time.Second, "59m59s"},
		{time.Hour, "1h0m"},
		{11 * time.Hour, "11h0m"},
		{2*time.Hour + 5*time.Minute, "2h5m"},
		{27 * time.Hour, "1d3h"},
		{-5 * time.Second, "0s"},
	}
	for _, c := range cases {
		if got := humanDuration(c.in); got != c.want {
			t.Errorf("humanDuration(%s) = %q, want %q", c.in, got, c.want)
		}
	}
}

// A webhook receiver has to be able to close its own ticket instead of opening
// a second one, which means the body must say which of the two this is.
func TestWebhookPayloadCarriesTheKind(t *testing.T) {
	var bodies []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var m map[string]any
		_ = json.NewDecoder(r.Body).Decode(&m)
		bodies = append(bodies, m)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	wh := NewWebhook("hook", srv.URL, "", time.Second)
	if err := wh.Send(context.Background(), domain.Alert(sampleEvent())); err != nil {
		t.Fatalf("send alert: %v", err)
	}
	if err := wh.Send(context.Background(), domain.Recovery(resolvedEvent())); err != nil {
		t.Fatalf("send recovery: %v", err)
	}

	if len(bodies) != 2 {
		t.Fatalf("received %d webhook calls, want 2", len(bodies))
	}
	if bodies[0]["kind"] != "alert" {
		t.Fatalf("alert payload kind = %v, want \"alert\"", bodies[0]["kind"])
	}
	if bodies[1]["kind"] != "recovery" {
		t.Fatalf("recovery payload kind = %v, want \"recovery\"", bodies[1]["kind"])
	}
	// The event stays where receivers already look for it.
	if _, ok := bodies[1]["event"].(map[string]any); !ok {
		t.Fatalf("recovery payload has no event object: %v", bodies[1])
	}
}

// A notification built without a kind (an older row, a caller that forgot) must
// render as an alert rather than as an empty string in the payload.
func TestZeroKindRendersAsAlert(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	wh := NewWebhook("hook", srv.URL, "", time.Second)
	if err := wh.Send(context.Background(), domain.Notification{Event: sampleEvent()}); err != nil {
		t.Fatalf("send: %v", err)
	}
	if got["kind"] != "alert" {
		t.Fatalf("kind = %v, want \"alert\"", got["kind"])
	}
}

// Gmail and Microsoft 365 both treat a missing Date or Message-ID as a spam
// signal. For a product whose whole job is putting a notification in front of a
// person, the spam folder is total failure - and it would look like "email does
// not work" rather than like a missing header.
func TestEmailCarriesTheHeadersThatKeepItOutOfSpam(t *testing.T) {
	fm := &fakeMailer{}
	e := newEmailWithSender("ops", "alerts@example.com", []string{"ops@example.com"}, fm)
	if err := e.Send(context.Background(), domain.Alert(sampleEvent())); err != nil {
		t.Fatalf("send: %v", err)
	}
	msg := string(fm.msg)

	for _, want := range []string{"Date: ", "Message-ID: <", "Auto-Submitted: auto-generated"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("message is missing %q:\n%s", want, msg)
		}
	}
	if !strings.Contains(msg, "@example.com>") {
		t.Fatalf("Message-ID should use the sender's domain:\n%s", msg)
	}
}

// An alert and its recovery are two messages about one incident. Sharing a
// Message-ID would let a client threading by it treat the recovery as a
// duplicate of the alert and hide it - which is the one message the reader is
// waiting for.
func TestAlertAndRecoveryHaveDifferentMessageIDs(t *testing.T) {
	alertID := messageID(domain.Alert(sampleEvent()), "alerts@example.com")
	recoveryID := messageID(domain.Recovery(resolvedEvent()), "alerts@example.com")

	if alertID == recoveryID {
		t.Fatalf("alert and recovery share a Message-ID: %s", alertID)
	}
	// A retry of the SAME notification must reuse its id, or a delivery that
	// succeeds on the second attempt arrives twice.
	if again := messageID(domain.Alert(sampleEvent()), "alerts@example.com"); again != alertID {
		t.Fatalf("Message-ID is not stable across retries: %s vs %s", alertID, again)
	}
	// It must be a syntactically valid msg-id.
	for _, id := range []string{alertID, recoveryID} {
		if !strings.HasPrefix(id, "<") || !strings.HasSuffix(id, ">") || !strings.Contains(id, "@") {
			t.Fatalf("malformed Message-ID: %q", id)
		}
	}
}

// A subject in a non-Latin script must not be cut through a character: the
// broken tail gets Q-encoded and the reader sees a replacement glyph in the one
// line of the message they are most likely to read.
func TestSubjectTruncationDoesNotBreakCharacters(t *testing.T) {
	long := strings.Repeat("Проверка ", 60) // far past the 200-rune cap
	got := sanitizeHeader(long)
	if !utf8.ValidString(got) {
		t.Fatalf("sanitised header is not valid UTF-8: %q", got)
	}
	if n := len([]rune(got)); n > domain.MaxHeaderRunes {
		t.Fatalf("header is %d runes, want at most %d", n, domain.MaxHeaderRunes)
	}
	if !strings.HasPrefix(got, "Проверка") {
		t.Fatalf("header lost its beginning: %q", got)
	}
}

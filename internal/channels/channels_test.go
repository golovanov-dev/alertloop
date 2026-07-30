package channels

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golovanov-dev/alertloop/internal/domain"
)

func sampleEvent() *domain.Event {
	return &domain.Event{
		ID:        "e1",
		Type:      domain.EventIncident,
		Severity:  domain.SeverityCritical,
		State:     domain.StateNew,
		Source:    "feeds_worker",
		Message:   "Feed processing failed",
		Payload:   json.RawMessage(`{"developer_id":15}`),
		CreatedAt: time.Now(),
	}
}

func TestWebhookSignsAndPosts(t *testing.T) {
	var gotBody []byte
	var gotSig string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		gotSig = r.Header.Get(SignatureHeader)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	wh := NewWebhook("siem", srv.URL, "topsecret", time.Second)
	if wh.Name() != "siem" || wh.Type() != domain.ChannelWebhook {
		t.Fatalf("unexpected name/type: %s/%s", wh.Name(), wh.Type())
	}
	if err := wh.Send(context.Background(), sampleEvent()); err != nil {
		t.Fatalf("send: %v", err)
	}
	if !Verify("topsecret", gotBody, gotSig) {
		t.Fatal("signature did not verify")
	}
	if Verify("wrongsecret", gotBody, gotSig) {
		t.Fatal("signature verified under wrong secret")
	}
}

func TestWebhookNon2xxIsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	wh := NewWebhook("wh", srv.URL, "s", time.Second)
	if err := wh.Send(context.Background(), sampleEvent()); err == nil {
		t.Fatal("expected error on 500 response")
	}
}

func TestTelegramSend(t *testing.T) {
	var gotPath string
	var req telegramRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &req)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	tg := NewTelegram("tg-main", "BOT123", "-100999", srv.URL, time.Second)
	if err := tg.Send(context.Background(), sampleEvent()); err != nil {
		t.Fatalf("send: %v", err)
	}
	if gotPath != "/botBOT123/sendMessage" {
		t.Fatalf("unexpected path %q", gotPath)
	}
	if req.ChatID != "-100999" || req.Text == "" {
		t.Fatalf("unexpected request %+v", req)
	}
	// A Telegram message has no separate subject: the header line carries the
	// event message, so the body must not repeat it. Regression guard for the
	// text being printed twice in a row.
	if n := countOccurrences(req.Text, "Feed processing failed"); n != 1 {
		t.Fatalf("event message appears %d times in telegram text, want 1:\n%s", n, req.Text)
	}
	if !contains(req.Text, "[CRITICAL/incident] Feed processing failed\n\nType:     incident") {
		t.Fatalf("unexpected telegram text layout:\n%s", req.Text)
	}
}

func TestTelegramAPIErrorIsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"ok":false,"error_code":400,"description":"chat not found"}`))
	}))
	defer srv.Close()
	tg := NewTelegram("tg", "t", "c", srv.URL, time.Second)
	if err := tg.Send(context.Background(), sampleEvent()); err == nil {
		t.Fatal("expected error from telegram API failure")
	}
}

// fakeMailer records the message instead of dialing SMTP.
type fakeMailer struct {
	from string
	to   []string
	msg  []byte
	err  error
}

func (f *fakeMailer) send(_ context.Context, from string, to []string, msg []byte) error {
	f.from, f.to, f.msg = from, to, msg
	return f.err
}

func TestEmailBuildsMessage(t *testing.T) {
	fm := &fakeMailer{}
	e := newEmailWithSender("ops", "alerts@example.com", []string{"ops@example.com"}, fm)
	if err := e.Send(context.Background(), sampleEvent()); err != nil {
		t.Fatalf("send: %v", err)
	}
	msg := string(fm.msg)
	if fm.from != "alerts@example.com" || len(fm.to) != 1 {
		t.Fatalf("unexpected envelope from=%q to=%v", fm.from, fm.to)
	}
	for _, want := range []string{"Subject: [CRITICAL/incident] Feed processing failed", "Source:   feeds_worker", "Event ID: e1"} {
		if !contains(msg, want) {
			t.Fatalf("message missing %q:\n%s", want, msg)
		}
	}
	// The subject already carries the event message; the body must not repeat
	// it. Regression guard for subject and first body line being identical.
	body := msg
	if i := indexOf(msg, "\r\n\r\n"); i >= 0 {
		body = msg[i+4:]
	}
	if contains(body, "Feed processing failed") {
		t.Fatalf("event message repeated in the email body:\n%s", body)
	}
	if !hasPrefix(body, "Type:     incident\r\n") {
		t.Fatalf("email body should start with the field block, got:\n%s", body)
	}
}

func TestPlainBodyOmitsMessage(t *testing.T) {
	e := sampleEvent()
	body := plainBody(e)
	if contains(body, e.Message) {
		t.Fatalf("plainBody must not repeat the event message (it is in subjectLine):\n%s", body)
	}
	if !hasPrefix(body, "Type:     incident\n") {
		t.Fatalf("plainBody should start with the field block, got:\n%s", body)
	}
	// The body stays self-contained without the message: it still identifies the
	// event so it can be opened in the console.
	for _, want := range []string{"Severity: critical", "State:    new", "Event ID: e1", "Payload:"} {
		if !contains(body, want) {
			t.Fatalf("plainBody missing %q:\n%s", want, body)
		}
	}
}

func TestEmailHeaderInjectionSanitized(t *testing.T) {
	fm := &fakeMailer{}
	e := newEmailWithSender("ops", "alerts@example.com", []string{"ops@example.com"}, fm)
	ev := sampleEvent()
	// Attempt to inject an extra header via the message (CRLF).
	ev.Message = "pwned\r\nBcc: attacker@evil.com"
	if err := e.Send(context.Background(), ev); err != nil {
		t.Fatalf("send: %v", err)
	}
	msg := string(fm.msg)
	// Inspect only the header section (everything before the blank line).
	headerPart := msg
	if i := indexOf(msg, "\r\n\r\n"); i >= 0 {
		headerPart = msg[:i]
	}
	// A successful injection would appear as a new "\r\nBcc:" header line.
	if contains(headerPart, "\r\nBcc:") {
		t.Fatalf("SMTP header injection not sanitized:\n%q", headerPart)
	}
}

func TestTelegramRedactsTokenOnNetworkError(t *testing.T) {
	// Point at a closed local port so client.Do fails with a *url.Error whose
	// text embeds the request URL (which contains the bot token).
	tg := NewTelegram("tg", "SUPERSECRETTOKEN", "chat", "http://127.0.0.1:1", 500*time.Millisecond)
	err := tg.Send(context.Background(), sampleEvent())
	if err == nil {
		t.Fatal("expected a network error")
	}
	if contains(err.Error(), "SUPERSECRETTOKEN") {
		t.Fatalf("bot token leaked in error: %v", err)
	}
	if !contains(err.Error(), "***") {
		t.Fatalf("expected redaction marker in error: %v", err)
	}
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && (indexOf(haystack, needle) >= 0)
}

func hasPrefix(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}

func countOccurrences(s, sub string) int {
	if sub == "" {
		return 0
	}
	n := 0
	for i := 0; i+len(sub) <= len(s); {
		if s[i:i+len(sub)] == sub {
			n++
			i += len(sub)
			continue
		}
		i++
	}
	return n
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

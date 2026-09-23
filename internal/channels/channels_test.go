package channels

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
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
	var gotSig, gotVersion string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		gotSig = r.Header.Get(SignatureHeader)
		gotVersion = r.Header.Get(SignatureVersionHeader)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	wh := NewWebhook("siem", srv.URL, "topsecret", time.Second)
	if wh.Name() != "siem" || wh.Type() != domain.ChannelWebhook {
		t.Fatalf("unexpected name/type: %s/%s", wh.Name(), wh.Type())
	}
	if err := wh.Send(context.Background(), domain.Alert(sampleEvent())); err != nil {
		t.Fatalf("send: %v", err)
	}
	// Sign itself is pinned by TestSignKnownAnswer; here: the header carries
	// the signature of exactly the bytes that were posted.
	if gotSig != Sign("topsecret", gotBody) {
		t.Fatalf("%s = %q does not sign the posted body", SignatureHeader, gotSig)
	}
	if gotVersion != "v1" {
		t.Fatalf("%s = %q, want v1", SignatureVersionHeader, gotVersion)
	}
}

// The signature format is a contract with receivers: bare lowercase hex of
// HMAC-SHA256(secret, raw body). The expected value was computed outside Go
// (openssl dgst -sha256 -hmac topsecret).
func TestSignKnownAnswer(t *testing.T) {
	body := []byte(`{"event":{"id":"e1"},"kind":"alert","timestamp":"2026-09-22T10:00:00Z"}`)
	const want = "ab6668b770b65d2612787f469b863ca9cd0b9716a63287bc59dfb45fb2023970"
	if got := Sign("topsecret", body); got != want {
		t.Fatalf("Sign = %s, want %s", got, want)
	}
}

// A Slack or Discord webhook URL carries its secret in the path; the delivery
// error lands in last_error, the read API, the console and the log.
func TestWebhookRedactsURLOnNetworkError(t *testing.T) {
	wh := NewWebhook("wh", "http://127.0.0.1:1/services/SECRETPATH?token=SECRETQUERY", "", 500*time.Millisecond)
	err := wh.Send(context.Background(), domain.Alert(sampleEvent()))
	if err == nil {
		t.Fatal("expected a network error")
	}
	if strings.Contains(err.Error(), "SECRETPATH") || strings.Contains(err.Error(), "SECRETQUERY") {
		t.Fatalf("webhook URL leaked in error: %v", err)
	}
	if !strings.Contains(err.Error(), "http://127.0.0.1:1") {
		t.Fatalf("error should still name the endpoint origin: %v", err)
	}
}

// A URL the request cannot even be built from is redacted too: its parse error
// quotes it whole.
func TestWebhookRedactsUnparsableURL(t *testing.T) {
	wh := NewWebhook("wh", "https://hooks.example/services/SECRETPATH\x7f", "", time.Second)
	err := wh.Send(context.Background(), domain.Alert(sampleEvent()))
	if err == nil || strings.Contains(err.Error(), "SECRETPATH") {
		t.Fatalf("error = %v, want one without the URL", err)
	}
}

func TestWebhookNon2xxIsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	wh := NewWebhook("wh", srv.URL, "s", time.Second)
	if err := wh.Send(context.Background(), domain.Alert(sampleEvent())); err == nil {
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

	tg := NewTelegram(TelegramConfig{Name: "tg-main", BotToken: "BOT123", ChatID: "-100999", APIBase: srv.URL, Timeout: time.Second})
	if err := tg.Send(context.Background(), domain.Alert(sampleEvent())); err != nil {
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
	if n := strings.Count(req.Text, "Feed processing failed"); n != 1 {
		t.Fatalf("event message appears %d times in telegram text, want 1:\n%s", n, req.Text)
	}
	if !strings.Contains(req.Text, "[CRITICAL/incident] Feed processing failed\n\nType:     incident") {
		t.Fatalf("unexpected telegram text layout:\n%s", req.Text)
	}
}

func TestTelegramAPIErrorIsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"ok":false,"error_code":400,"description":"chat not found"}`))
	}))
	defer srv.Close()
	tg := NewTelegram(TelegramConfig{Name: "tg", BotToken: "t", ChatID: "c", APIBase: srv.URL, Timeout: time.Second})
	if err := tg.Send(context.Background(), domain.Alert(sampleEvent())); err == nil {
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
	if err := e.Send(context.Background(), domain.Alert(sampleEvent())); err != nil {
		t.Fatalf("send: %v", err)
	}
	msg := string(fm.msg)
	if fm.from != "alerts@example.com" || len(fm.to) != 1 {
		t.Fatalf("unexpected envelope from=%q to=%v", fm.from, fm.to)
	}
	for _, want := range []string{"Subject: [CRITICAL/incident] Feed processing failed", "Source:   feeds_worker", "Event ID: e1"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("message missing %q:\n%s", want, msg)
		}
	}
	// The subject already carries the event message; the body must not repeat
	// it. Regression guard for subject and first body line being identical.
	body := msg
	if i := strings.Index(msg, "\r\n\r\n"); i >= 0 {
		body = msg[i+4:]
	}
	if strings.Contains(body, "Feed processing failed") {
		t.Fatalf("event message repeated in the email body:\n%s", body)
	}
	if !strings.HasPrefix(body, "Type:     incident\r\n") {
		t.Fatalf("email body should start with the field block, got:\n%s", body)
	}
}

func TestPlainBodyOmitsMessage(t *testing.T) {
	e := sampleEvent()
	body := plainBody(domain.Alert(e))
	if strings.Contains(body, e.Message) {
		t.Fatalf("plainBody must not repeat the event message (it is in subjectLine):\n%s", body)
	}
	if !strings.HasPrefix(body, "Type:     incident\n") {
		t.Fatalf("plainBody should start with the field block, got:\n%s", body)
	}
	// The body stays self-contained without the message: it still identifies the
	// event so it can be opened in the console.
	for _, want := range []string{"Severity: critical", "State:    new", "Event ID: e1", "Payload:"} {
		if !strings.Contains(body, want) {
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
	if err := e.Send(context.Background(), domain.Alert(ev)); err != nil {
		t.Fatalf("send: %v", err)
	}
	msg := string(fm.msg)
	// Inspect only the header section (everything before the blank line).
	headerPart := msg
	if i := strings.Index(msg, "\r\n\r\n"); i >= 0 {
		headerPart = msg[:i]
	}
	// A successful injection would appear as a new "\r\nBcc:" header line.
	if strings.Contains(headerPart, "\r\nBcc:") {
		t.Fatalf("SMTP header injection not sanitized:\n%q", headerPart)
	}
}

func TestTelegramRedactsTokenOnNetworkError(t *testing.T) {
	// Point at a closed local port so client.Do fails with a *url.Error whose
	// text embeds the request URL (which contains the bot token).
	tg := NewTelegram(TelegramConfig{Name: "tg", BotToken: "SUPERSECRETTOKEN", ChatID: "chat", APIBase: "http://127.0.0.1:1", Timeout: 500 * time.Millisecond})
	err := tg.Send(context.Background(), domain.Alert(sampleEvent()))
	if err == nil {
		t.Fatal("expected a network error")
	}
	if strings.Contains(err.Error(), "SUPERSECRETTOKEN") {
		t.Fatalf("bot token leaked in error: %v", err)
	}
	if !strings.Contains(err.Error(), "***") {
		t.Fatalf("expected redaction marker in error: %v", err)
	}
}

// The worker returns a send cut short at shutdown to the queue uncounted only
// when the channel's error says it was cancelled. Telegram redacts its error
// text (the URL carries the bot token), so it must still carry the
// cancellation; a webhook wraps the transport error directly.
func TestHTTPChannelsReportCancellation(t *testing.T) {
	block := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-block:
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	defer close(block)

	chans := map[string]Channel{
		"telegram": NewTelegram(TelegramConfig{Name: "tg", BotToken: "SECRET", ChatID: "c", APIBase: srv.URL, Timeout: time.Minute}),
		"webhook":  NewWebhook("wh", srv.URL+"/hook", "", time.Minute),
	}
	for name, ch := range chans {
		ctx, cancel := context.WithCancel(context.Background())
		time.AfterFunc(100*time.Millisecond, cancel)
		err := ch.Send(ctx, domain.Alert(sampleEvent()))
		cancel()
		if err == nil {
			t.Fatalf("%s: send to a server that never answers succeeded", name)
		}
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("%s: error does not say the send was cancelled: %v", name, err)
		}
		if strings.Contains(err.Error(), "SECRET") {
			t.Fatalf("%s: error leaks the bot token: %v", name, err)
		}
	}
}

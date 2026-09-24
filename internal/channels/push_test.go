package channels

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/golovanov-dev/alertloop/internal/domain"
)

func TestNtfyPublish(t *testing.T) {
	type request struct {
		path, auth string
		msg        ntfyMessage
	}
	var got []request
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var m ntfyMessage
		if err := json.NewDecoder(r.Body).Decode(&m); err != nil {
			t.Errorf("body: %v", err)
		}
		got = append(got, request{r.URL.Path, r.Header.Get("Authorization"), m})
	}))
	defer srv.Close()

	ch := NewNtfy(NtfyConfig{Name: "phone", Server: srv.URL + "/", Topic: "ops", Token: "tk_1", Timeout: time.Second})
	if err := ch.Send(context.Background(), cyrillicAlert()); err != nil {
		t.Fatalf("send alert: %v", err)
	}
	if err := ch.Send(context.Background(), domain.Recovery(resolvedEvent())); err != nil {
		t.Fatalf("send recovery: %v", err)
	}
	login := NewNtfy(NtfyConfig{Name: "phone", Server: srv.URL, Topic: "ops", Username: "u", Password: "p", Timeout: time.Second})
	if err := login.Send(context.Background(), domain.Alert(sampleEvent())); err != nil {
		t.Fatalf("send with login: %v", err)
	}

	alert, rec := got[0], got[1]
	if alert.path != "/" || alert.auth != "Bearer tk_1" || got[2].auth != "Basic dTpw" {
		t.Fatalf("path %q, auth %q / %q", alert.path, alert.auth, got[2].auth)
	}
	m := alert.msg
	if m.Topic != "ops" || m.Priority != 5 || m.Tags[0] != "rotating_light" || m.Click != testEventURL {
		t.Fatalf("alert = %+v", m)
	}
	if !strings.HasPrefix(m.Title, "[CRITICAL/incident] Сбой оплаты") || !strings.HasPrefix(m.Message, "Redirected:") {
		t.Fatalf("title %q, message %.60q", m.Title, m.Message)
	}
	if len(m.Message) > ntfyMessageMax || !utf8.ValidString(m.Message) || !strings.HasSuffix(m.Message, truncatedMark) {
		t.Fatalf("message is %d bytes, valid UTF-8 %v, ends %q", len(m.Message), utf8.ValidString(m.Message), m.Message[len(m.Message)-20:])
	}
	if strings.Contains(m.Message, "Link:") {
		t.Fatal("the message repeats the link the click action already carries")
	}
	if r := rec.msg; r.Priority != 2 || r.Tags[0] != "white_check_mark" || !strings.HasPrefix(r.Title, "[RESOLVED]") || r.Click != "" {
		t.Fatalf("recovery = %+v", r)
	}
}

func TestPushoverMessage(t *testing.T) {
	var got []url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/1/messages.json" {
			t.Errorf("path = %q", r.URL.Path)
		}
		if err := r.ParseForm(); err != nil {
			t.Errorf("form: %v", err)
		}
		got = append(got, r.PostForm)
		_, _ = w.Write([]byte(`{"status":1,"request":"r"}`))
	}))
	defer srv.Close()

	ch := NewPushover(PushoverConfig{Name: "po", Token: "app", UserKey: "usr", APIBase: srv.URL, Timeout: time.Second})
	if err := ch.Send(context.Background(), cyrillicAlert()); err != nil {
		t.Fatalf("send alert: %v", err)
	}
	if err := ch.Send(context.Background(), domain.Recovery(resolvedEvent())); err != nil {
		t.Fatalf("send recovery: %v", err)
	}

	a := got[0]
	if a.Get("token") != "app" || a.Get("user") != "usr" || a.Get("priority") != "1" ||
		a.Get("url") != testEventURL || a.Get("url_title") != "Open in AlertLoop" {
		t.Fatalf("alert form = %v", a)
	}
	msg := a.Get("message")
	if n := utf8.RuneCountInString(msg); n > pushoverMessageMax || !strings.HasSuffix(msg, truncatedMark) || !strings.HasPrefix(msg, "Redirected:") {
		t.Fatalf("message: %d runes, %.60q", n, msg)
	}
	if !strings.Contains(a.Get("title"), "Сбой оплаты") {
		t.Fatalf("title = %q", a.Get("title"))
	}
	r := got[1]
	if r.Get("priority") != "-1" || !strings.HasPrefix(r.Get("title"), "[RESOLVED]") || r.Has("url") {
		t.Fatalf("recovery form = %v", r)
	}
}

// ntfy limits the JSON body, not only the message: the body is measured as
// sent, and "<", ">", "&" are not escaped to six bytes each.
func TestNtfyBodyFitsTheLimit(t *testing.T) {
	var body []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ = io.ReadAll(r.Body)
	}))
	defer srv.Close()
	n := domain.Alert(sampleEvent())
	n.Event.Message = strings.Repeat(`"`, 300)
	n.Event.Payload = json.RawMessage(`{"html":"<b>&</b>` + strings.Repeat(`\"`, 3000) + `"}`)
	ch := NewNtfy(NtfyConfig{Name: "n", Server: srv.URL, Topic: "ops", Timeout: time.Second})
	if err := ch.Send(context.Background(), n); err != nil {
		t.Fatal(err)
	}
	var m ntfyMessage
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatalf("body: %v", err)
	}
	if len(body) > ntfyBodyMax || !strings.Contains(string(body), "<b>&</b>") || !strings.HasSuffix(m.Message, truncatedMark) {
		t.Fatalf("body is %d bytes (max %d), starts %.80q", len(body), ntfyBodyMax, body)
	}
}

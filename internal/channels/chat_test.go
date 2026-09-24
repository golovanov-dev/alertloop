package channels

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/golovanov-dev/alertloop/internal/domain"
)

const testEventURL = "https://alerts.example.com/admin/#/events/e1"

// capture starts a server that records every request body and answers with
// status and reply.
func capture(t *testing.T, status int, reply string) (*httptest.Server, *[]map[string]any) {
	t.Helper()
	var bodies []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var m map[string]any
		if err := json.NewDecoder(r.Body).Decode(&m); err != nil {
			t.Errorf("request body is not JSON: %v", err)
		}
		bodies = append(bodies, m)
		w.WriteHeader(status)
		_, _ = w.Write([]byte(reply))
	}))
	t.Cleanup(srv.Close)
	return srv, &bodies
}

// cyrillicAlert is an alert redirected from a broken channel, with a console
// link and a payload far past every chat limit.
func cyrillicAlert() domain.Notification {
	e := sampleEvent()
	e.Message = "Сбой оплаты <!channel> @everyone"
	e.DedupeKey = "shop:payments"
	e.Payload = json.RawMessage(`{"log":"` + strings.Repeat("ошибка ", 1000) + `"}`)
	n := domain.Alert(e)
	n.Fallback = &domain.FallbackOrigin{Channel: "ops-telegram", Error: "telegram send failed (status 401)"}
	n.EventURL = testEventURL
	return n
}

// get walks a decoded JSON body: get(m, "attachments", 0, "color").
func get(v any, path ...any) any {
	for _, p := range path {
		switch k := p.(type) {
		case string:
			m, _ := v.(map[string]any)
			v = m[k]
		case int:
			a, _ := v.([]any)
			if k >= len(a) {
				return nil
			}
			v = a[k]
		}
	}
	return v
}

func str(v any, path ...any) string { s, _ := get(v, path...).(string); return s }

func TestSlackMessage(t *testing.T) {
	srv, bodies := capture(t, http.StatusOK, "ok")
	ch := NewSlack("ops-slack", srv.URL+"/services/T0/B0/SECRET", time.Second)
	if err := ch.Send(context.Background(), cyrillicAlert()); err != nil {
		t.Fatalf("send alert: %v", err)
	}
	if err := ch.Send(context.Background(), domain.Recovery(resolvedEvent())); err != nil {
		t.Fatalf("send recovery: %v", err)
	}
	alert, rec := (*bodies)[0], (*bodies)[1]

	if got := str(alert, "text"); got != "🔴 [CRITICAL/incident] Сбой оплаты &lt;!channel&gt; @\u200beveryone" {
		t.Fatalf("alert text = %q", got)
	}
	att := get(alert, "attachments", 0)
	if str(att, "color") != "#D0021B" || str(att, "title_link") != testEventURL {
		t.Fatalf("alert attachment color/link = %q %q", str(att, "color"), str(att, "title_link"))
	}
	text := str(att, "text")
	if !strings.HasPrefix(text, `Redirected: channel "ops-telegram"`) || !strings.Contains(text, truncatedMark) {
		t.Fatalf("attachment text lacks the redirect or the truncation mark:\n%.300s", text)
	}
	if !strings.Contains(string(mustJSON(t, att)), `"title":"Key","value":"shop:payments"`) {
		t.Fatalf("fields lack the dedupe key: %v", get(att, "fields"))
	}

	if got := str(rec, "text"); !strings.HasPrefix(got, "✅ [RESOLVED] PostgreSQL does not respond") {
		t.Fatalf("recovery text = %q", got)
	}
	if str(rec, "attachments", 0, "color") != "#2EB67D" || !strings.Contains(str(rec, "attachments", 0, "text"), "resolved") {
		t.Fatalf("recovery attachment = %v", get(rec, "attachments", 0))
	}
	if _, ok := get(rec, "attachments", 0).(map[string]any)["title_link"]; ok {
		t.Fatal("a notification without public_url carries a link")
	}
}

func TestTeamsCard(t *testing.T) {
	srv, bodies := capture(t, http.StatusAccepted, "")
	ch := NewTeams("ops-teams", srv.URL+"/workflows/x/triggers/manual/paths/invoke?sig=SECRET", time.Second)
	if err := ch.Send(context.Background(), cyrillicAlert()); err != nil {
		t.Fatalf("send alert: %v", err)
	}
	if err := ch.Send(context.Background(), domain.Recovery(resolvedEvent())); err != nil {
		t.Fatalf("send recovery: %v", err)
	}
	alert, rec := (*bodies)[0], (*bodies)[1]

	if str(alert, "type") != "message" || str(alert, "attachments", 0, "contentType") != "application/vnd.microsoft.card.adaptive" {
		t.Fatalf("envelope = %v", alert)
	}
	card := get(alert, "attachments", 0, "content")
	if str(card, "type") != "AdaptiveCard" || str(card, "body", 0, "color") != "attention" ||
		!strings.Contains(str(card, "body", 0, "text"), "Сбой оплаты") {
		t.Fatalf("card head = %v", get(card, "body", 0))
	}
	if !strings.HasPrefix(str(card, "body", 1, "text"), "Redirected:") {
		t.Fatalf("card does not lead with the redirect: %v", get(card, "body", 1))
	}
	if str(card, "actions", 0, "url") != testEventURL {
		t.Fatalf("card action = %v", get(card, "actions"))
	}
	if n := utf8.RuneCountInString(string(mustJSON(t, card))); n > 12000 {
		t.Fatalf("card is %d characters; the payload was not cut", n)
	}

	rcard := get(rec, "attachments", 0, "content")
	if str(rcard, "body", 0, "color") != "good" || str(rcard, "body", 1, "text") != "The incident below is resolved." {
		t.Fatalf("recovery card = %v", get(rcard, "body"))
	}
	if get(rcard, "actions") != nil {
		t.Fatal("a notification without public_url carries a link")
	}
}

func TestDiscordMessage(t *testing.T) {
	srv, bodies := capture(t, http.StatusNoContent, "")
	ch := NewDiscord("ops-discord", srv.URL+"/api/webhooks/1/SECRET", time.Second)
	n := cyrillicAlert()
	n.Event.Message = strings.Repeat("Длинное сообщение ", 200)
	n.Event.Source = "" // Discord refuses an empty field value
	if err := ch.Send(context.Background(), n); err != nil {
		t.Fatalf("send: %v", err)
	}
	msg := (*bodies)[0]
	content := str(msg, "content")
	if utf8.RuneCountInString(content) > 2000 || !strings.HasSuffix(content, truncatedMark) || !strings.HasPrefix(content, "🔴 ") {
		t.Fatalf("content: %d runes, %.40q…", utf8.RuneCountInString(content), content)
	}
	if parse, ok := get(msg, "allowed_mentions", "parse").([]any); !ok || len(parse) != 0 {
		t.Fatalf("allowed_mentions = %v; @everyone in a message would ping the server", get(msg, "allowed_mentions"))
	}
	embed := get(msg, "embeds", 0)
	if get(embed, "color") != float64(0xD0021B) || str(embed, "url") != testEventURL {
		t.Fatalf("embed color/url = %v %v", get(embed, "color"), get(embed, "url"))
	}
	if d := str(embed, "description"); utf8.RuneCountInString(d) > 4096 || !strings.Contains(d, truncatedMark) {
		t.Fatalf("description is %d runes or was not cut", utf8.RuneCountInString(d))
	}
	for _, f := range get(embed, "fields").([]any) {
		if str(f, "value") == "" {
			t.Fatalf("empty field value: %v", f)
		}
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// Every HTTP channel shares one classification; Slack stands in for all.
func TestHTTPChannelsClassifyResponses(t *testing.T) {
	cases := []struct {
		status int
		reply  string
		want   string // "" = delivered
	}{
		{http.StatusOK, "ok", ""},
		{http.StatusTooManyRequests, `{"message":"You are being rate limited."}`, "slack rate limited the message (status 429): You are being rate limited."},
		{http.StatusBadGateway, "<html>\n bad gateway </html>", "slack server error (status 502): <html> bad gateway </html>"},
		{http.StatusForbidden, "invalid_token", "slack rejected the message (status 403): invalid_token"},
		{http.StatusBadRequest, `{"errors":["user key is invalid"],"status":0}`, "slack rejected the message (status 400): user key is invalid"},
	}
	for _, c := range cases {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(c.status)
			_, _ = w.Write([]byte(c.reply))
		}))
		err := NewSlack("s", srv.URL+"/hook", time.Second).Send(context.Background(), domain.Alert(sampleEvent()))
		srv.Close()
		got := ""
		if err != nil {
			got = err.Error()
		}
		if got != c.want {
			t.Errorf("status %d: error = %q, want %q", c.status, got, c.want)
		}
	}
}

// The webhook URLs, tokens and keys reach no error text: errors are stored in
// last_error and shown by the API, the console and the log.
func TestHTTPChannelsKeepSecretsOutOfErrors(t *testing.T) {
	// A server that quotes back everything it got, as some error pages do.
	echo := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(r.URL.String() + " " + r.Header.Get("Authorization") + " " + string(body)))
	}))
	defer echo.Close()
	dead := "http://127.0.0.1:1"

	for _, base := range []string{echo.URL, dead} {
		chans := map[string]Channel{
			"slack":        NewSlack("s", base+"/services/T0/B0/SECRET1", time.Second),
			"teams":        NewTeams("t", base+"/invoke?sig=SECRET1", time.Second),
			"discord":      NewDiscord("d", base+"/api/webhooks/1/SECRET1", time.Second),
			"ntfy token":   NewNtfy(NtfyConfig{Name: "n", Server: base, Topic: "SECRET3topic", Token: "tk_SECRET1", Timeout: time.Second}),
			"ntfy login":   NewNtfy(NtfyConfig{Name: "n", Server: base, Topic: "SECRET3topic", Username: "u", Password: "SECRET1", Timeout: time.Second}),
			"ntfy open":    NewNtfy(NtfyConfig{Name: "n", Server: base, Topic: "SECRET3topic", Timeout: time.Second}),
			"pushover app": NewPushover(PushoverConfig{Name: "p", Token: "SECRET1", UserKey: "SECRET2", APIBase: base, Timeout: time.Second}),
		}
		for name, ch := range chans {
			err := ch.Send(context.Background(), domain.Alert(sampleEvent()))
			if err == nil {
				t.Fatalf("%s via %s: expected an error", name, base)
			}
			if strings.Contains(err.Error(), "SECRET") || strings.Contains(err.Error(), "dTpTRUNSRVQx") {
				t.Errorf("%s via %s: secret in error: %v", name, base, err)
			}
		}
	}
}

// The console link: a Link line in email and Telegram text, before the payload
// so a length limit cuts the payload first; event_url in the webhook payload.
func TestEventLinkInTextAndWebhook(t *testing.T) {
	n := domain.Alert(sampleEvent())
	n.EventURL = testEventURL
	body := plainBody(n)
	link := strings.Index(body, "Link:     "+testEventURL+"\n")
	if link < 0 || link > strings.Index(body, "Payload:") {
		t.Fatalf("link missing or after the payload:\n%s", body)
	}

	srv, bodies := capture(t, http.StatusOK, "")
	wh := NewWebhook("hook", srv.URL, "", time.Second)
	for _, n := range []domain.Notification{n, domain.Alert(sampleEvent())} {
		if err := wh.Send(context.Background(), n); err != nil {
			t.Fatalf("send: %v", err)
		}
	}
	if (*bodies)[0]["event_url"] != testEventURL {
		t.Fatalf("event_url = %v", (*bodies)[0]["event_url"])
	}
	if _, ok := (*bodies)[1]["event_url"]; ok {
		t.Fatal("event_url present without public_url")
	}
}

// A secret the service quotes right where the error text is cut: redacted
// first, its start does not survive the cut.
func TestHTTPChannelsRedactBeforeCutting(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(strings.Repeat("x", 280) + "tk_SECRET1 is not valid"))
	}))
	defer srv.Close()
	ch := NewNtfy(NtfyConfig{Name: "n", Server: srv.URL, Topic: "ops", Token: "tk_SECRET1", Timeout: time.Second})
	err := ch.Send(context.Background(), domain.Alert(sampleEvent()))
	if err == nil || strings.Contains(err.Error(), "tk_") || !strings.Contains(err.Error(), "***") {
		t.Fatalf("error = %v, want the token redacted, not cut", err)
	}
}

// A 3xx is not followed: Go would repeat the POST as a GET without the body,
// and a 2xx to that GET would mark a lost message as sent.
func TestHTTPChannelsDoNotFollowRedirects(t *testing.T) {
	var methods []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		methods = append(methods, r.Method)
		if r.Method == http.MethodPost {
			// To itself: the GET that follows it would get a 200.
			http.Redirect(w, r, "/moved/SECRET9", http.StatusMovedPermanently)
			return
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	chans := []Channel{
		NewSlack("s", srv.URL+"/services/T0/B0/SECRET1", time.Second),
		NewTeams("t", srv.URL+"/invoke?sig=SECRET1", time.Second),
		NewDiscord("d", srv.URL+"/api/webhooks/1/SECRET1", time.Second),
		NewNtfy(NtfyConfig{Name: "n", Server: srv.URL, Topic: "ops", Timeout: time.Second}),
		NewPushover(PushoverConfig{Name: "p", Token: "SECRET1", UserKey: "SECRET2", APIBase: srv.URL, Timeout: time.Second}),
		NewWebhook("w", srv.URL+"/hook/SECRET1", "", time.Second),
		NewTelegram(TelegramConfig{Name: "tg", BotToken: "1:SECRET1", ChatID: "1", APIBase: srv.URL, Timeout: time.Second}),
	}
	for _, ch := range chans {
		methods = nil
		err := ch.Send(context.Background(), domain.Alert(sampleEvent()))
		want := "answered status 301: redirect to " + srv.URL + " not followed"
		if err == nil || !strings.Contains(err.Error(), want) || strings.Contains(err.Error(), "SECRET") {
			t.Errorf("%s: error = %v, want it to contain %q and no secret", ch.Type(), err, want)
		}
		if len(methods) != 1 || methods[0] != http.MethodPost {
			t.Errorf("%s: requests %v, want one POST", ch.Type(), methods)
		}
	}
}

// Mattermost and Rocket.Chat act on a plain @channel; Slack on <!channel>.
func TestSlackNeutralisesGroupMentions(t *testing.T) {
	srv, bodies := capture(t, http.StatusOK, "ok")
	n := domain.Alert(sampleEvent())
	n.Event.Message = "@channel @HERE @all @everyone <!here> mail@example.com"
	if err := NewSlack("s", srv.URL+"/hook", time.Second).Send(context.Background(), n); err != nil {
		t.Fatal(err)
	}
	got := str((*bodies)[0], "text")
	want := "@\u200bchannel @\u200bHERE @\u200ball @\u200beveryone &lt;!here&gt; mail@example.com"
	if !strings.HasSuffix(got, want) {
		t.Fatalf("text = %q, want it to end with %q", got, want)
	}
}

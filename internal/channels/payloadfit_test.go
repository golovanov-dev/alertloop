package channels

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/golovanov-dev/alertloop/internal/domain"
)

// digestPayload is shaped like a daily PHP warnings digest: a list of places
// that, indented, is far over a Telegram message.
func digestPayload(places int) json.RawMessage {
	// Written by hand, not marshalled from a map: the keys are deliberately
	// out of alphabetical order, as a client sends them.
	top := make([]string, places)
	for i := range top {
		top[i] = fmt.Sprintf(`{"where":"site/theme/app/choice-flats.php:%d","count":%d,"level":"notice","message":"Undefined index: class","service":"site"}`, 100+i, 200-i)
	}
	return json.RawMessage(`{"total":1045,"date":"2026-11-12","top":[` + strings.Join(top, ",") + `]}`)
}

func TestFitPayloadKeepsSmallPayloadAsIs(t *testing.T) {
	raw := json.RawMessage(`{"job":"s3_sync_db","exit_code":1}`)
	if got := fitPayload(raw, 1000); got != prettyJSON(raw) {
		t.Fatalf("a payload that fits changed:\n%s", got)
	}
}

func TestFitPayloadShrinksArraysToValidJSON(t *testing.T) {
	raw := digestPayload(30)
	got := fitPayload(raw, 1500)
	if n := utf8.RuneCountInString(got); n > 1500 {
		t.Fatalf("fitted payload is %d runes, limit 1500", n)
	}
	var v struct {
		Date  string `json:"date"`
		Total int    `json:"total"`
		Top   []any  `json:"top"`
	}
	if err := json.Unmarshal([]byte(got), &v); err != nil {
		t.Fatalf("fitted payload is not valid JSON: %v\n%s", err, got)
	}
	if v.Date != "2026-11-12" || v.Total != 1045 || len(v.Top) < 2 {
		t.Fatalf("lost the scalars or every item: %+v", v)
	}
	last, ok := v.Top[len(v.Top)-1].(string)
	if !ok || last != fmt.Sprintf("… %d more", 30-(len(v.Top)-1)) {
		t.Fatalf("the cut array should end with how many items were left out, got %v", v.Top[len(v.Top)-1])
	}
	if first, _ := v.Top[0].(map[string]any); first["where"] != "site/theme/app/choice-flats.php:100" {
		t.Fatalf("the first items must be kept, got %v", v.Top[0])
	}
	// Keys keep the payload's own order rather than a sorted one.
	if !strings.HasPrefix(got, "{\n  \"total\": 1045,\n  \"date\"") ||
		!strings.Contains(got, "\"where\": \"site/theme/app/choice-flats.php:100\",\n      \"count\": 200,") {
		t.Fatalf("key order changed:\n%s", got)
	}
}

func TestFitPayloadShortensLongStrings(t *testing.T) {
	raw, _ := json.Marshal(map[string]string{"trace": strings.Repeat("frame <a> -> b\n", 500)})
	got := fitPayload(raw, 400)
	var v map[string]string
	if err := json.Unmarshal([]byte(got), &v); err != nil || utf8.RuneCountInString(got) > 400 {
		t.Fatalf("got %d runes, err %v:\n%s", utf8.RuneCountInString(got), err, got)
	}
	if !strings.HasSuffix(v["trace"], truncatedMark) || !strings.Contains(got, "<a> ->") {
		t.Fatalf("the string should be clipped with a mark and not HTML-escaped:\n%s", got)
	}
}

func TestFitPayloadCutsNonJSONAsText(t *testing.T) {
	got := fitPayload(json.RawMessage(strings.Repeat("x", 500)), 100)
	if utf8.RuneCountInString(got) != 100 || !strings.HasSuffix(got, truncatedMark) {
		t.Fatalf("got %q", got)
	}
}

func TestTelegramKeepsLongPayloadValidJSON(t *testing.T) {
	var req telegramRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &req)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	e := sampleEvent()
	e.Payload = digestPayload(30)
	tg := NewTelegram(TelegramConfig{Name: "tg", BotToken: "B", ChatID: "1", APIBase: srv.URL, Timeout: time.Second})
	if err := tg.Send(context.Background(), domain.Alert(e)); err != nil {
		t.Fatalf("send: %v", err)
	}
	if n := utf8.RuneCountInString(req.Text); n > telegramMaxText {
		t.Fatalf("text is %d runes, Telegram allows %d", n, telegramMaxText)
	}
	_, payload, ok := strings.Cut(req.Text, payloadLabel)
	if !ok {
		t.Fatalf("no payload in:\n%s", req.Text)
	}
	if !json.Valid([]byte(payload)) {
		t.Fatalf("payload in the message is not valid JSON:\n%s", payload)
	}
	if !strings.Contains(payload, " more\"") {
		t.Fatalf("payload should say how many items were left out:\n%s", payload)
	}
}

// assertFittedPayload checks that the payload part of a text body that was
// over its limit is still valid JSON and says it was shortened.
func assertFittedPayload(t *testing.T, text string) {
	t.Helper()
	_, payload, ok := strings.Cut(text, payloadLabel)
	if !ok {
		t.Fatalf("no payload in:\n%.300s", text)
	}
	if !json.Valid([]byte(payload)) || !strings.Contains(payload, truncatedMark) {
		t.Fatalf("payload should be valid JSON with a shortened string:\n%s", payload)
	}
}

func TestFitPayloadGivesLongStringTheRoomLeft(t *testing.T) {
	raw, _ := json.Marshal(map[string]string{"trace": strings.Repeat("x", 10000)})
	got := fitPayload(raw, 3000)
	if n := utf8.RuneCountInString(got); n > 3000 || n < 2900 {
		t.Fatalf("fitted payload is %d runes; a single long string should fill the 3000 available", n)
	}
}

func TestFitPayloadCompactStaysOnOneLine(t *testing.T) {
	got := fitPayloadCompact(digestPayload(30), 1000)
	if utf8.RuneCountInString(got) > 1000 || strings.Contains(got, "\n") || !json.Valid([]byte(got)) || !strings.Contains(got, " more\"") {
		t.Fatalf("got %d runes:\n%s", utf8.RuneCountInString(got), got)
	}
}

func TestFitPayloadTooSmallBudgetIsEmpty(t *testing.T) {
	if got := fitPayload(digestPayload(30), utf8.RuneCountInString(truncatedMark)-1); got != "" {
		t.Fatalf("a budget below the truncation mark should give nothing, got %q", got)
	}
}

// A payload nested thousands of levels deep must not cost seconds per send:
// shrinking stops at shrinkDepthMax and the rest is cut as text.
func TestFitPayloadDeepNestingIsBounded(t *testing.T) {
	const depth = 9000
	raw := json.RawMessage(`{"a":` + strings.Repeat("[", depth) + strings.Repeat("]", depth) + `,"s":"` + strings.Repeat("x", 200*1024) + `"}`)
	start := time.Now()
	got := fitPayload(raw, 3500)
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("fitting took %v", d)
	}
	if utf8.RuneCountInString(got) > 3500 {
		t.Fatalf("fitted payload is %d runes, limit 3500", utf8.RuneCountInString(got))
	}
}

func TestJSONDepthIgnoresBracketsInStrings(t *testing.T) {
	for raw, want := range map[string]int{
		`{}`:                     1,
		`{"a":[1,{"b":[]}]}`:     4,
		`{"s":"[[[{{{\"]]]"}`:    1,
		`{"s":"\\","a":[[]]}`:    3,
		`["{\"x\":[1]}", [[0]]]`: 3,
	} {
		if got := jsonDepth(json.RawMessage(raw)); got != want {
			t.Errorf("jsonDepth(%s) = %d, want %d", raw, got, want)
		}
	}
}

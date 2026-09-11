package api

import (
	"context"
	"io"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/golovanov-dev/alertloop/internal/domain"
	"github.com/golovanov-dev/alertloop/internal/storage"
)

var (
	rowRe  = regexp.MustCompile(`(?s)<tr>(.*?)</tr>`)
	cellRe = regexp.MustCompile(`(?s)<t[hd][^>]*>(.*?)</t[hd]>`)
	tagRe  = regexp.MustCompile(`<[^>]*>`)
)

// cells strips markup from one table row and returns its text cells.
func cells(row string) []string {
	var out []string
	for _, m := range cellRe.FindAllStringSubmatch(row, -1) {
		out = append(out, strings.TrimSpace(tagRe.ReplaceAllString(m[1], "")))
	}
	return out
}

// columnUnder returns the value of the cell sitting under the named header.
// This is the point: a header and a cell can both be present and still be in
// different columns, which is exactly how "Last error" ended up printing under
// "Kind" and nobody noticed until an audit read the template.
func columnUnder(t *testing.T, html, header string) (string, bool) {
	t.Helper()
	rows := rowRe.FindAllStringSubmatch(html, -1)
	if len(rows) < 2 {
		return "", false
	}
	headers := cells(rows[0][1])
	idx := -1
	for i, h := range headers {
		if strings.EqualFold(h, header) {
			idx = i
			break
		}
	}
	if idx < 0 {
		t.Fatalf("no %q column. Headers: %v", header, headers)
	}
	// The first body row is enough: every row comes from the same template.
	first := cells(rows[1][1])
	if len(first) != len(headers) {
		t.Fatalf("row has %d cells but there are %d headers - the table is misaligned\nheaders: %v\ncells:   %v",
			len(first), len(headers), headers, first)
	}
	return first[idx], true
}

// seedAlertAndRecovery stores one incident with both an alert and a recovery
// delivery, each carrying a distinctive error string so a swapped column is
// unmistakable.
func seedAlertAndRecovery(t *testing.T, store storage.Store) string {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()

	e := &domain.Event{
		ID: "evt-1", Type: domain.EventIncident, Severity: domain.SeverityCritical,
		State: domain.StateResolved, Source: "monit", Message: "PostgreSQL down",
		DedupeKey: "server-01:postgresql:availability", Payload: []byte(`{}`),
		CreatedAt: now, UpdatedAt: now, LastSeenAt: now, ResolvedAt: &now,
	}
	if _, _, err := store.CreateEvent(ctx, e); err != nil {
		t.Fatalf("create event: %v", err)
	}
	for _, d := range []*domain.DeliveryAttempt{
		{
			ID: "att-alert", EventID: "evt-1", Channel: domain.ChannelTelegram, ChannelName: "tg",
			Kind: domain.KindAlert, State: domain.DeliverySent, MaxAttempts: 5,
			LastError: "", CreatedAt: now, UpdatedAt: now,
		},
		{
			ID: "att-recovery", EventID: "evt-1", Channel: domain.ChannelEmail, ChannelName: "mail",
			Kind: domain.KindRecovery, State: domain.DeliveryFailed, MaxAttempts: 5,
			LastError: "smtp: connection refused", CreatedAt: now, UpdatedAt: now,
		},
	} {
		if err := store.CreateDeliveryAttempt(ctx, d); err != nil {
			t.Fatalf("create attempt %s: %v", d.ID, err)
		}
	}
	return "evt-1"
}

func getPage(t *testing.T, url string) string {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s: status %d", url, resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read %s: %v", url, err)
	}
	return string(body)
}

// The release notes claim `kind` is shown on the built-in pages. It was not:
// /deliveries had no such column, and on the event page the header row and the
// cells were in different orders, so the error text printed under "Kind".
func TestBuiltInPagesShowDeliveryKindInTheRightColumn(t *testing.T) {
	ts, store := newTestServer(t, nil)
	id := seedAlertAndRecovery(t, store)

	t.Run("event detail", func(t *testing.T) {
		html := getPage(t, ts.URL+"/events/"+id+"?token="+adminTok)
		kind, ok := columnUnder(t, html, "Kind")
		if !ok {
			t.Fatal("no delivery rows rendered")
		}
		if kind != string(domain.KindAlert) && kind != string(domain.KindRecovery) {
			t.Fatalf("the Kind column contains %q; header and cells are misaligned", kind)
		}
		errCol, _ := columnUnder(t, html, "Last error")
		if errCol == string(domain.KindAlert) || errCol == string(domain.KindRecovery) {
			t.Fatalf("the Last error column contains a delivery kind (%q)", errCol)
		}
	})

	t.Run("deliveries page", func(t *testing.T) {
		html := getPage(t, ts.URL+"/deliveries?token="+adminTok)
		kind, ok := columnUnder(t, html, "Kind")
		if !ok {
			t.Fatal("no delivery rows rendered")
		}
		if kind != string(domain.KindAlert) && kind != string(domain.KindRecovery) {
			t.Fatalf("the Kind column contains %q", kind)
		}
		if !strings.Contains(html, "recovery") {
			t.Fatal("the recovery attempt is not visible on /deliveries")
		}
	})
}

// The page advertises a kind filter; it has to actually filter.
func TestDeliveriesPageFiltersByKind(t *testing.T) {
	ts, store := newTestServer(t, nil)
	seedAlertAndRecovery(t, store)

	recoveries := getPage(t, ts.URL+"/deliveries?token="+adminTok+"&kind=recovery")
	if !strings.Contains(recoveries, "att-recovery"[:3]) || !strings.Contains(recoveries, "mail") {
		t.Fatalf("the recovery attempt is missing from a kind=recovery listing")
	}
	if strings.Contains(recoveries, ">tg<") {
		t.Fatal("kind=recovery also listed the alert attempt")
	}

	alerts := getPage(t, ts.URL+"/deliveries?token="+adminTok+"&kind=alert")
	if !strings.Contains(alerts, "tg") {
		t.Fatal("the alert attempt is missing from a kind=alert listing")
	}
	if strings.Contains(alerts, "smtp: connection refused") {
		t.Fatal("kind=alert also listed the recovery attempt")
	}
}

// The JSON API filter, which the admin console and any script use.
func TestDeliveryAttemptsAPIFiltersByKind(t *testing.T) {
	ts, store := newTestServer(t, nil)
	seedAlertAndRecovery(t, store)

	resp, body := doJSON(t, http.MethodGet, ts.URL+"/v1/delivery-attempts?kind=recovery", adminTok, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	items, _ := body["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("kind=recovery returned %d attempts, want 1", len(items))
	}
	first, _ := items[0].(map[string]any)
	if first["kind"] != string(domain.KindRecovery) {
		t.Fatalf("kind = %v, want recovery", first["kind"])
	}
}

// The delivery table on /events/{id} used to print a bare "15:04:05". With up
// to retention_days of history, and a retry that can be
// scheduled for the next day, a time without a date does not say when. Both
// columns carry the date now, in UTC as their headers say, in the same format
// as the /events list.
func TestEventPageDeliveryTableShowsTheDate(t *testing.T) {
	ts, store := newTestServer(t, nil)
	ctx := context.Background()
	created := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
	updated := time.Date(2026, 3, 4, 23, 50, 1, 0, time.UTC)
	retry := time.Date(2026, 3, 5, 0, 20, 1, 0, time.UTC) // tomorrow, from the row's point of view

	e := &domain.Event{
		ID: "evt-date", Type: domain.EventIncident, Severity: domain.SeverityCritical,
		State: domain.StateNew, Source: "monit", Message: "disk full", Payload: []byte(`{}`),
		CreatedAt: created, UpdatedAt: created, LastSeenAt: created,
	}
	if _, _, err := store.CreateEvent(ctx, e); err != nil {
		t.Fatalf("create event: %v", err)
	}
	if err := store.CreateDeliveryAttempt(ctx, &domain.DeliveryAttempt{
		ID: "att-date", EventID: "evt-date", Channel: domain.ChannelTelegram, ChannelName: "tg",
		Kind: domain.KindAlert, State: domain.DeliveryFailed, Attempts: 2, MaxAttempts: 5,
		NextRetryAt: &retry, LastError: "timeout", CreatedAt: created, UpdatedAt: updated,
	}); err != nil {
		t.Fatalf("create attempt: %v", err)
	}

	html := getPage(t, ts.URL+"/events/evt-date?token="+adminTok)
	for header, want := range map[string]string{
		"Updated (UTC)":    "2026-03-04 23:50:01",
		"Next retry (UTC)": "2026-03-05 00:20:01",
	} {
		got, ok := columnUnder(t, html, header)
		if !ok {
			t.Fatal("no delivery rows rendered")
		}
		if got != want {
			t.Errorf("%s = %q, want %q", header, got, want)
		}
	}
}

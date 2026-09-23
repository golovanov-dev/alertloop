package storage

import (
	"context"
	"testing"
	"time"

	"github.com/golovanov-dev/alertloop/internal/domain"
)

// Search matches a substring of the message whatever its case, Cyrillic
// included, on both engines; `%` in it is literal; and it pages with the
// cursor like any other filter.
func TestListEventsSearchesMessage(t *testing.T) {
	checkSearch(t, newTestStore(t))
}

func TestPostgresListEventsSearchesMessage(t *testing.T) {
	checkSearch(t, postgresStore(t))
}

func checkSearch(t *testing.T, s Store) {
	t.Helper()
	ctx := context.Background()
	base := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)
	for i, msg := range []string{
		"ДИСК /var заполнен на 100%",
		"Диск /data заполнен на 1000 МБ",
		"PostgreSQL does not respond",
	} {
		e := sampleEvent(string(rune('a'+i)), "", base.Add(time.Duration(i)*time.Minute))
		e.Message = msg
		if _, _, err := s.CreateEvent(ctx, e); err != nil {
			t.Fatalf("create: %v", err)
		}
	}

	var got []string
	cursor := ""
	for {
		page, err := s.ListEvents(ctx, EventFilter{Search: "диск", Type: domain.EventIncident}, 1, cursor)
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		for _, e := range page.Items {
			got = append(got, e.ID)
		}
		if page.NextCursor == "" {
			break
		}
		cursor = page.NextCursor
	}
	if len(got) != 2 || got[0] != "b" || got[1] != "a" {
		t.Fatalf("search диск paged by one = %v, want [b a]", got)
	}

	page, err := s.ListEvents(ctx, EventFilter{Search: "100%"}, 10, "")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(page.Items) != 1 || page.Items[0].ID != "a" {
		t.Fatalf("search 100%% matched %d events, want only the one with a literal 100%%", len(page.Items))
	}
}

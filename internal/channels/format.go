package channels

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/golovanov-dev/alertloop/internal/domain"
)

// subjectLine builds a short one-line summary used for email subjects and
// message headers.
func subjectLine(e *domain.Event) string {
	return fmt.Sprintf("[%s/%s] %s", strings.ToUpper(string(e.Severity)), e.Type, e.Message)
}

// plainBody renders a human-readable multi-line description of an event for
// email and Telegram delivery.
func plainBody(e *domain.Event) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n\n", e.Message)
	fmt.Fprintf(&b, "Type:     %s\n", e.Type)
	fmt.Fprintf(&b, "Severity: %s\n", e.Severity)
	fmt.Fprintf(&b, "State:    %s\n", e.State)
	fmt.Fprintf(&b, "Source:   %s\n", e.Source)
	if e.Category != "" {
		fmt.Fprintf(&b, "Category: %s\n", e.Category)
	}
	if e.EntityType != "" || e.EntityID != "" {
		fmt.Fprintf(&b, "Entity:   %s/%s\n", e.EntityType, e.EntityID)
	}
	if e.TraceID != "" {
		fmt.Fprintf(&b, "Trace:    %s\n", e.TraceID)
	}
	fmt.Fprintf(&b, "Event ID: %s\n", e.ID)
	fmt.Fprintf(&b, "Time:     %s\n", e.CreatedAt.Format("2006-01-02 15:04:05 MST"))
	if len(e.Payload) > 0 && string(e.Payload) != "{}" {
		if pretty := prettyJSON(e.Payload); pretty != "" {
			fmt.Fprintf(&b, "\nPayload:\n%s\n", pretty)
		}
	}
	return b.String()
}

func prettyJSON(raw json.RawMessage) string {
	var out bytes.Buffer
	if err := json.Indent(&out, raw, "", "  "); err != nil {
		return string(raw)
	}
	return out.String()
}

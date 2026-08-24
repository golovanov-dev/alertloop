package channels

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/golovanov-dev/alertloop/internal/domain"
)

// subjectLine builds a short one-line summary used for email subjects and
// message headers.
//
// A recovery leads with RESOLVED rather than with the severity. Someone
// scanning a phone notification needs to know in the first two words that this
// is good news; repeating "[CRITICAL]" on the message that says the problem is
// over is how a recovery gets read as another failure.
func subjectLine(n domain.Notification) string {
	e := n.Event
	if n.IsRecovery() {
		return fmt.Sprintf("[RESOLVED] %s", e.Message)
	}
	return fmt.Sprintf("[%s/%s] %s", strings.ToUpper(string(e.Severity)), e.Type, e.Message)
}

// plainBody renders a human-readable multi-line description of an event for
// email and Telegram delivery.
//
// The event message is deliberately NOT repeated here: it is already carried by
// subjectLine, which becomes the email Subject header and the first line of a
// Telegram message. Including it again duplicated the text in both channels.
func plainBody(n domain.Notification) string {
	e := n.Event
	var b strings.Builder
	if n.IsRecovery() {
		// Lead with the fact, then the identity of what recovered. The reader
		// is matching this against an alert they saw earlier.
		b.WriteString("The incident below is resolved.\n\n")
	}
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
	if e.DedupeKey != "" {
		// The stable identity of the check. It is what lets a reader tie this
		// message to the alert they got earlier, and it is the one field a
		// monitoring integration always sets.
		fmt.Fprintf(&b, "Key:      %s\n", e.DedupeKey)
	}
	fmt.Fprintf(&b, "Event ID: %s\n", e.ID)
	fmt.Fprintf(&b, "Started:  %s\n", e.CreatedAt.Format("2006-01-02 15:04:05 MST"))
	if n.IsRecovery() && e.ResolvedAt != nil {
		fmt.Fprintf(&b, "Resolved: %s\n", e.ResolvedAt.Format("2006-01-02 15:04:05 MST"))
		fmt.Fprintf(&b, "Duration: %s\n", humanDuration(e.ResolvedAt.Sub(e.CreatedAt)))
	} else if !e.LastSeenAt.IsZero() && e.LastSeenAt.After(e.CreatedAt) {
		// Only worth printing once it differs from the start: for an event
		// reported exactly once the two are the same timestamp.
		fmt.Fprintf(&b, "Last seen: %s\n", e.LastSeenAt.Format("2006-01-02 15:04:05 MST"))
	}
	if len(e.Payload) > 0 && string(e.Payload) != "{}" {
		if pretty := prettyJSON(e.Payload); pretty != "" {
			fmt.Fprintf(&b, "\nPayload:\n%s\n", pretty)
		}
	}
	return b.String()
}

// humanDuration renders an outage length the way someone reading an alert says
// it out loud: "3m20s", "2h5m", "1d3h". time.Duration's own String would print
// an eleven-hour outage as "11h0m0s" and a two-day one as "51h0m0s".
func humanDuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	d = d.Round(time.Second)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm%ds", int(d.Minutes()), int(d.Seconds())%60)
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh%dm", int(d.Hours()), int(d.Minutes())%60)
	default:
		return fmt.Sprintf("%dd%dh", int(d.Hours())/24, int(d.Hours())%24)
	}
}

func prettyJSON(raw json.RawMessage) string {
	var out bytes.Buffer
	if err := json.Indent(&out, raw, "", "  "); err != nil {
		return string(raw)
	}
	return out.String()
}

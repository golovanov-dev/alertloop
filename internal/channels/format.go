package channels

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

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

// headline is subjectLine with the severity mark in front, for chat channels
// where the first line is all a notification preview shows.
func headline(n domain.Notification) string {
	return severityLook(n).emoji + " " + subjectLine(n)
}

// look is how a channel shows the severity: an emoji for the text, a colour
// for the bar or embed beside it, and the Adaptive Card colour name for Teams.
type look struct {
	emoji string
	color int // 0xRRGGBB
	card  string
}

// severityLook maps the notification to its mark. A recovery is green whatever
// the severity of the alert it closes.
func severityLook(n domain.Notification) look {
	if n.IsRecovery() {
		return look{"✅", 0x2EB67D, "good"}
	}
	switch n.Event.Severity {
	case domain.SeverityCritical:
		return look{"🔴", 0xD0021B, "attention"}
	case domain.SeverityError:
		return look{"🟠", 0xF5821F, "attention"}
	case domain.SeverityWarning:
		return look{"🟡", 0xF2C744, "warning"}
	case domain.SeveritySuccess:
		return look{"🟢", 0x2EB67D, "good"}
	default:
		return look{"🔵", 0x439FE0, "accent"}
	}
}

// preamble is what a reader must learn before the details: that the incident
// is over, or that this alert was redirected from a broken channel.
func preamble(n domain.Notification) string {
	var b strings.Builder
	if n.IsRecovery() {
		// Lead with the fact, then the identity of what recovered. The reader
		// is matching this against an alert they saw earlier.
		b.WriteString("The incident below is resolved.\n")
	}
	if f := n.Fallback; f != nil {
		// First, so the reader learns a channel is broken before the details.
		fmt.Fprintf(&b, "Redirected: channel %q could not deliver this alert.\n", f.Channel)
		if f.Error != "" {
			fmt.Fprintf(&b, "Its error: %s\n", f.Error)
		}
	}
	return b.String()
}

// fact is one labelled detail of an event. Channels with native fields (Slack
// attachments, Discord embeds, Teams fact sets) show them as such; the others
// print them as aligned lines.
type fact struct{ label, value string }

// facts lists the event details in the order every channel shows them. The
// event message is not among them: the subject or headline carries it.
func facts(n domain.Notification) []fact {
	e := n.Event
	out := []fact{
		{"Type", string(e.Type)},
		{"Severity", string(e.Severity)},
		{"State", string(e.State)},
		{"Source", e.Source},
	}
	if e.Category != "" {
		out = append(out, fact{"Category", e.Category})
	}
	if e.EntityType != "" || e.EntityID != "" {
		out = append(out, fact{"Entity", e.EntityType + "/" + e.EntityID})
	}
	if e.TraceID != "" {
		out = append(out, fact{"Trace", e.TraceID})
	}
	if e.DedupeKey != "" {
		// The stable identity of the check. It is what lets a reader tie this
		// message to the alert they got earlier, and it is the one field a
		// monitoring integration always sets.
		out = append(out, fact{"Key", e.DedupeKey})
	}
	out = append(out,
		fact{"Event ID", e.ID},
		fact{"Started", e.CreatedAt.Format("2006-01-02 15:04:05 MST")})
	if n.IsRecovery() && e.ResolvedAt != nil {
		out = append(out,
			fact{"Resolved", e.ResolvedAt.Format("2006-01-02 15:04:05 MST")},
			fact{"Duration", humanDuration(e.ResolvedAt.Sub(e.CreatedAt))})
	} else if !e.LastSeenAt.IsZero() && e.LastSeenAt.After(e.CreatedAt) {
		// Only worth printing once it differs from the start: for an event
		// reported exactly once the two are the same timestamp.
		out = append(out, fact{"Last seen", e.LastSeenAt.Format("2006-01-02 15:04:05 MST")})
	}
	return out
}

// payloadText is the event payload, indented, or "" when there is none.
func payloadText(n domain.Notification) string {
	p := n.Event.Payload
	if len(p) == 0 || string(p) == "{}" {
		return ""
	}
	return prettyJSON(p)
}

// plainBody renders a human-readable multi-line description of an event for
// email, Telegram and the push channels.
//
// The event message is deliberately NOT repeated here: it is already carried by
// subjectLine, which becomes the email Subject header and the first line of a
// Telegram message. Including it again duplicated the text in both channels.
func plainBody(n domain.Notification) string {
	return textBody(n, true)
}

// textBody is plainBody, with or without the console link line: a channel that
// has a native link (a push notification's click action) leaves it out.
func textBody(n domain.Notification, withLink bool) string {
	var b strings.Builder
	if p := preamble(n); p != "" {
		b.WriteString(p)
		b.WriteString("\n")
	}
	for _, f := range facts(n) {
		fmt.Fprintf(&b, "%-9s %s\n", f.label+":", f.value)
	}
	if withLink && n.EventURL != "" {
		// Before the payload, so a length limit cuts the payload, not the link.
		fmt.Fprintf(&b, "%-9s %s\n", "Link:", n.EventURL)
	}
	if p := payloadText(n); p != "" {
		fmt.Fprintf(&b, "\nPayload:\n%s\n", p)
	}
	return b.String()
}

// truncatedMark ends a text that was cut to fit a service's length limit.
const truncatedMark = "… [truncated]"

// clip limits s to at most n runes, ending a cut text with truncatedMark so the
// reader knows there was more.
func clip(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	keep := n - utf8.RuneCountInString(truncatedMark)
	return string([]rune(s)[:max(keep, 0)]) + truncatedMark
}

// clipBytes is clip for a limit counted in bytes; it never splits a character.
func clipBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	keep := max(n-len(truncatedMark), 0)
	for keep > 0 && !utf8.RuneStart(s[keep]) {
		keep--
	}
	return s[:keep] + truncatedMark
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

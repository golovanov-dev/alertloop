package channels

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/golovanov-dev/alertloop/internal/domain"
)

// linkTitle is the text of the link to the event in the admin console.
const linkTitle = "Open in AlertLoop"

// chatLimits bound the text a chat channel sends. They are below every limit of
// the services that accept the format (Rocket.Chat keeps 5000 characters per
// message by default, Discord 6000 per embed), so a long payload is cut by
// AlertLoop, with a mark, rather than refused by the service.
const (
	chatHeadlineMax = 1000
	chatPreambleMax = 400
	chatFactMax     = 200
	chatPayloadMax  = 2500
)

// chatPayload is the payload as a fenced code block, or "".
func chatPayload(n domain.Notification) string {
	p := payloadText(n)
	if p == "" {
		return ""
	}
	return "```\n" + clip(p, chatPayloadMax) + "\n```"
}

// joinNonEmpty joins the non-empty parts with a blank line.
func joinNonEmpty(parts ...string) string {
	var out []string
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return strings.Join(out, "\n\n")
}

// Slack posts to a Slack incoming webhook. Mattermost and Rocket.Chat incoming
// webhooks accept the same message: text plus a coloured legacy attachment,
// which all three render (Block Kit is Slack-only).
type Slack struct{ httpTarget }

// NewSlack builds a named Slack channel. The webhook URL is its secret.
func NewSlack(name, webhookURL string, timeout time.Duration) *Slack {
	return &Slack{newHTTPTarget(name, "slack", webhookURL, timeout, urlSecrets(webhookURL)...)}
}

func (s *Slack) Type() domain.ChannelType { return domain.ChannelSlack }

type slackMessage struct {
	Text        string            `json:"text"`
	Attachments []slackAttachment `json:"attachments"`
}

type slackAttachment struct {
	Fallback  string       `json:"fallback"`
	Color     string       `json:"color"`
	Title     string       `json:"title,omitempty"`
	TitleLink string       `json:"title_link,omitempty"`
	Text      string       `json:"text,omitempty"`
	Fields    []slackField `json:"fields"`
	Footer    string       `json:"footer"`
	MrkdwnIn  []string     `json:"mrkdwn_in"`
}

type slackField struct {
	Title string `json:"title"`
	Value string `json:"value"`
	Short bool   `json:"short"`
}

// slackEscape keeps an event message from mentioning anyone or forging a link.
// It escapes the three characters Slack reads as control syntax (<!channel>),
// and puts a zero-width space after the @ of the plain-text group mentions that
// Mattermost and Rocket.Chat act on (@channel, @here, @all, @everyone).
func slackEscape(s string) string {
	return slackMention.ReplaceAllString(slackEntities.Replace(s), "@\u200b$1")
}

var (
	slackEntities = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")
	slackMention  = regexp.MustCompile(`(?i)@(channel|here|all|everyone)`)
)

func (s *Slack) Send(ctx context.Context, n domain.Notification) error {
	lk := severityLook(n)
	head := slackEscape(clip(headline(n), chatHeadlineMax))
	att := slackAttachment{
		Fallback: head,
		Color:    fmt.Sprintf("#%06X", lk.color),
		Text:     slackEscape(joinNonEmpty(clip(preamble(n), chatPreambleMax), chatPayload(n))),
		Footer:   "AlertLoop",
		MrkdwnIn: []string{"text"},
	}
	if n.EventURL != "" {
		att.Title, att.TitleLink = linkTitle, n.EventURL
	}
	for _, f := range facts(n) {
		att.Fields = append(att.Fields, slackField{Title: f.label, Value: slackEscape(clip(f.value, chatFactMax)), Short: true})
	}
	return s.postJSON(ctx, slackMessage{Text: head, Attachments: []slackAttachment{att}}, "")
}

// Teams posts an Adaptive Card to a Microsoft Teams Workflows webhook ("Post to
// a channel when a webhook request is received").
type Teams struct{ httpTarget }

// NewTeams builds a named Teams channel. The workflow URL, signature included,
// is its secret.
func NewTeams(name, webhookURL string, timeout time.Duration) *Teams {
	return &Teams{newHTTPTarget(name, "teams", webhookURL, timeout, urlSecrets(webhookURL)...)}
}

func (t *Teams) Type() domain.ChannelType { return domain.ChannelTeams }

type teamsMessage struct {
	Type        string            `json:"type"`
	Attachments []teamsAttachment `json:"attachments"`
}

type teamsAttachment struct {
	ContentType string         `json:"contentType"`
	Content     map[string]any `json:"content"`
}

func (t *Teams) Send(ctx context.Context, n domain.Notification) error {
	textBlock := func(text string) map[string]any {
		return map[string]any{"type": "TextBlock", "text": text, "wrap": true}
	}
	head := textBlock(clip(headline(n), chatHeadlineMax))
	head["weight"], head["size"], head["color"] = "Bolder", "Medium", severityLook(n).card
	body := []any{head}
	for _, line := range strings.Split(strings.TrimSpace(clip(preamble(n), chatPreambleMax)), "\n") {
		if line != "" {
			body = append(body, textBlock(line))
		}
	}
	var fs []map[string]string
	for _, f := range facts(n) {
		fs = append(fs, map[string]string{"title": f.label, "value": clip(f.value, chatFactMax)})
	}
	body = append(body, map[string]any{"type": "FactSet", "facts": fs})
	if p := compactPayload(n); p != "" {
		// One line: a TextBlock does not keep the indentation of pretty JSON.
		pb := textBlock(clip(p, chatPayloadMax))
		pb["fontType"] = "Monospace"
		body = append(body, pb)
	}
	card := map[string]any{
		"$schema": "http://adaptivecards.io/schemas/adaptive-card.json",
		"type":    "AdaptiveCard",
		"version": "1.4",
		"body":    body,
		"msteams": map[string]string{"width": "Full"},
	}
	if n.EventURL != "" {
		card["actions"] = []any{map[string]string{"type": "Action.OpenUrl", "title": linkTitle, "url": n.EventURL}}
	}
	return t.postJSON(ctx, teamsMessage{
		Type: "message",
		Attachments: []teamsAttachment{{
			ContentType: "application/vnd.microsoft.card.adaptive",
			Content:     card,
		}},
	}, "")
}

// compactPayload is the payload on one line, or "".
func compactPayload(n domain.Notification) string {
	p := n.Event.Payload
	if len(p) == 0 || string(p) == "{}" {
		return ""
	}
	var out bytes.Buffer
	if err := json.Compact(&out, p); err != nil {
		return string(p)
	}
	return out.String()
}

// Discord posts to a Discord channel webhook: the headline as the message text,
// which is what a push notification shows, and the details in a coloured embed.
type Discord struct{ httpTarget }

// NewDiscord builds a named Discord channel. The webhook URL is its secret.
func NewDiscord(name, webhookURL string, timeout time.Duration) *Discord {
	return &Discord{newHTTPTarget(name, "discord", webhookURL, timeout, urlSecrets(webhookURL)...)}
}

func (d *Discord) Type() domain.ChannelType { return domain.ChannelDiscord }

// discordContentMax is Discord's limit on the message text.
const discordContentMax = 2000

type discordMessage struct {
	Content string         `json:"content"`
	Embeds  []discordEmbed `json:"embeds"`
	// AllowedMentions with an empty parse list keeps an @everyone in an event
	// message from pinging the server.
	AllowedMentions struct {
		Parse []string `json:"parse"`
	} `json:"allowed_mentions"`
}

type discordEmbed struct {
	Title       string         `json:"title,omitempty"`
	URL         string         `json:"url,omitempty"`
	Description string         `json:"description,omitempty"`
	Color       int            `json:"color"`
	Fields      []discordField `json:"fields"`
	Footer      struct {
		Text string `json:"text"`
	} `json:"footer"`
}

type discordField struct {
	Name   string `json:"name"`
	Value  string `json:"value"`
	Inline bool   `json:"inline"`
}

func (d *Discord) Send(ctx context.Context, n domain.Notification) error {
	embed := discordEmbed{
		Description: joinNonEmpty(clip(preamble(n), chatPreambleMax), chatPayload(n)),
		Color:       severityLook(n).color,
	}
	embed.Footer.Text = "AlertLoop"
	if n.EventURL != "" {
		embed.Title, embed.URL = linkTitle, n.EventURL
	}
	for _, f := range facts(n) {
		v := clip(f.value, chatFactMax)
		if v == "" {
			v = "-" // Discord refuses a field with an empty value.
		}
		embed.Fields = append(embed.Fields, discordField{Name: f.label, Value: v, Inline: true})
	}
	msg := discordMessage{
		Content: clip(headline(n), min(chatHeadlineMax, discordContentMax)),
		Embeds:  []discordEmbed{embed},
	}
	msg.AllowedMentions.Parse = []string{}
	return d.postJSON(ctx, msg, "")
}

package channels

import (
	"context"
	"encoding/base64"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/golovanov-dev/alertloop/internal/domain"
)

// NtfyConfig configures one ntfy topic, on ntfy.sh or a self-hosted server.
type NtfyConfig struct {
	Name string
	// Server is the ntfy base URL; empty means https://ntfy.sh.
	Server string
	Topic  string
	// Token is an access token, sent as a Bearer token. Username and Password
	// are the alternative; both are optional on a server without access control.
	Token    string
	Username string
	Password string
	Timeout  time.Duration
}

// Ntfy publishes to an ntfy topic. It uses JSON publishing rather than the
// X-Title/X-Tags headers because HTTP headers cannot carry UTF-8 text.
type Ntfy struct {
	httpTarget
	topic string
	auth  string
}

// NewNtfy builds a named ntfy channel.
func NewNtfy(c NtfyConfig) *Ntfy {
	server := strings.TrimRight(c.Server, "/")
	if server == "" {
		server = "https://ntfy.sh"
	}
	var auth, basic string
	switch {
	case c.Token != "":
		auth = "Bearer " + c.Token
	case c.Username != "":
		basic = base64.StdEncoding.EncodeToString([]byte(c.Username + ":" + c.Password))
		auth = "Basic " + basic
	}
	return &Ntfy{
		// JSON publishing posts to the server root; the topic is in the body.
		// Without access control the topic is the password: whoever knows it
		// reads the messages.
		httpTarget: newHTTPTarget(c.Name, "ntfy", server+"/", c.Timeout, c.Token, c.Password, basic, c.Topic),
		topic:      c.Topic,
		auth:       auth,
	}
}

func (t *Ntfy) Type() domain.ChannelType { return domain.ChannelNtfy }

// ntfyMessageMax is below ntfy's default 4096-byte message limit, past which
// the server turns the text into a file attachment. ntfyBodyMax is the JSON
// body ntfy accepts by default: twice the message limit.
const (
	ntfyMessageMax = 4000
	ntfyBodyMax    = 8192
)

type ntfyMessage struct {
	Topic    string   `json:"topic"`
	Title    string   `json:"title"`
	Message  string   `json:"message"`
	Priority int      `json:"priority"`
	Tags     []string `json:"tags"`
	Click    string   `json:"click,omitempty"`
}

// ntfyMark maps a notification to an ntfy priority (1 min … 5 max) and tags.
// The first tag is an emoji short code, which ntfy shows before the title.
func ntfyMark(n domain.Notification) (int, []string) {
	if n.IsRecovery() {
		return 2, []string{"white_check_mark", "resolved"}
	}
	sev := string(n.Event.Severity)
	switch n.Event.Severity {
	case domain.SeverityCritical:
		return 5, []string{"rotating_light", sev}
	case domain.SeverityError:
		return 4, []string{"red_circle", sev}
	case domain.SeverityWarning:
		return 3, []string{"warning", sev}
	case domain.SeveritySuccess:
		return 2, []string{"green_circle", sev}
	default:
		return 2, []string{"information_source", sev}
	}
}

func (t *Ntfy) Send(ctx context.Context, n domain.Notification) error {
	prio, tags := ntfyMark(n)
	msg := ntfyMessage{
		Topic:    t.topic,
		Title:    clip(subjectLine(n), 250),
		Message:  clipBytes(textBody(n, false), ntfyMessageMax),
		Priority: prio,
		Tags:     tags,
		Click:    n.EventURL,
	}
	body, err := t.encodeJSON(msg)
	if err != nil {
		return err
	}
	// Quotes, backslashes and newlines take two bytes once encoded. Cutting the
	// excess from the message is enough: a cut part never encodes shorter.
	if over := len(body) - ntfyBodyMax; over > 0 {
		msg.Message = clipBytes(msg.Message, max(len(msg.Message)-over, 0))
		if body, err = t.encodeJSON(msg); err != nil {
			return err
		}
	}
	return t.post(ctx, "application/json", body, t.auth)
}

// PushoverConfig configures one Pushover recipient.
type PushoverConfig struct {
	Name string
	// Token is the application API token; UserKey is the user or group key.
	Token   string
	UserKey string
	// APIBase replaces https://api.pushover.net in tests.
	APIBase string
	Timeout time.Duration
}

// Pushover sends through the Pushover Messages API.
type Pushover struct {
	httpTarget
	token   string
	userKey string
}

// NewPushover builds a named Pushover channel.
func NewPushover(c PushoverConfig) *Pushover {
	base := strings.TrimRight(c.APIBase, "/")
	if base == "" {
		base = "https://api.pushover.net"
	}
	return &Pushover{
		httpTarget: newHTTPTarget(c.Name, "pushover", base+"/1/messages.json", c.Timeout, c.Token, c.UserKey),
		token:      c.Token,
		userKey:    c.UserKey,
	}
}

func (p *Pushover) Type() domain.ChannelType { return domain.ChannelPushover }

// Pushover limits, in characters.
const (
	pushoverTitleMax   = 250
	pushoverMessageMax = 1024
	pushoverURLMax     = 512
)

// pushoverPriority maps a notification to a Pushover priority: 1 bypasses
// the recipient's quiet hours, -1 arrives without sound. Emergency (2), which
// repeats until acknowledged, is not used.
func pushoverPriority(n domain.Notification) int {
	if n.IsRecovery() {
		return -1
	}
	switch n.Event.Severity {
	case domain.SeverityCritical:
		return 1
	case domain.SeverityError, domain.SeverityWarning:
		return 0
	default:
		return -1
	}
}

func (p *Pushover) Send(ctx context.Context, n domain.Notification) error {
	form := url.Values{
		"token":    {p.token},
		"user":     {p.userKey},
		"title":    {clip(subjectLine(n), pushoverTitleMax)},
		"message":  {clip(textBody(n, false), pushoverMessageMax)},
		"priority": {strconv.Itoa(pushoverPriority(n))},
	}
	if n.EventURL != "" && len(n.EventURL) <= pushoverURLMax {
		form.Set("url", n.EventURL)
		form.Set("url_title", linkTitle)
	}
	return p.post(ctx, "application/x-www-form-urlencoded", []byte(form.Encode()), "")
}

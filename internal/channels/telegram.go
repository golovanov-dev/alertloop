package channels

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/golovanov-dev/alertloop/internal/domain"
)

// Telegram delivers events through the Telegram Bot API sendMessage method.
type Telegram struct {
	name     string
	botToken string
	chatID   string
	apiBase  string
	// secrets are the strings that must never appear in an error text: the bot
	// token and, when the channel runs through an authenticated proxy, the proxy
	// password (both raw and percent-encoded, since transport errors may quote
	// the URL form).
	secrets []string
	client  *http.Client
}

// TelegramConfig configures one Telegram Bot API delivery target.
type TelegramConfig struct {
	Name     string
	BotToken string
	ChatID   string
	// APIBase overrides the public Bot API host, e.g. with a mirror or a reverse
	// proxy in front of it. Empty falls back to https://api.telegram.org.
	APIBase string
	// Proxy routes requests through an HTTP, HTTPS, or SOCKS5 proxy. Nil keeps
	// http.ProxyFromEnvironment, so HTTP_PROXY/HTTPS_PROXY/NO_PROXY still apply.
	// The URL is parsed and validated at config load time.
	Proxy   *url.URL
	Timeout time.Duration
}

// NewTelegram builds a named Telegram channel. A zero timeout falls back to 10s
// and an empty APIBase falls back to the public Bot API host.
func NewTelegram(c TelegramConfig) *Telegram {
	if c.Timeout <= 0 {
		c.Timeout = 10 * time.Second
	}
	if c.APIBase == "" {
		c.APIBase = "https://api.telegram.org"
	}
	t := &Telegram{
		name:     c.Name,
		botToken: c.BotToken,
		chatID:   c.ChatID,
		apiBase:  strings.TrimRight(c.APIBase, "/"),
		client:   &http.Client{Timeout: c.Timeout, Transport: newTransport(c.Proxy)},
	}
	if c.BotToken != "" {
		t.secrets = append(t.secrets, c.BotToken)
	}
	if c.Proxy != nil {
		if pw, ok := c.Proxy.User.Password(); ok && pw != "" {
			t.secrets = append(t.secrets, pw)
			if enc := url.QueryEscape(pw); enc != pw {
				t.secrets = append(t.secrets, enc)
			}
		}
	}
	return t
}

// newTransport builds this channel's HTTP transport. It is created once per
// channel, not per request, so the connection pool (and with it the established
// proxy tunnel) is reused across messages. Everything except the proxy keeps
// http.DefaultTransport's settings — including, when proxy is nil, the
// ProxyFromEnvironment behavior the channel has always had.
func newTransport(proxy *url.URL) *http.Transport {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	if proxy != nil {
		tr.Proxy = http.ProxyURL(proxy)
	}
	return tr
}

func (t *Telegram) Type() domain.ChannelType { return domain.ChannelTelegram }
func (t *Telegram) Name() string             { return t.name }

type telegramRequest struct {
	ChatID    string `json:"chat_id"`
	Text      string `json:"text"`
	ParseMode string `json:"parse_mode,omitempty"`
}

type telegramResponse struct {
	OK          bool   `json:"ok"`
	ErrorCode   int    `json:"error_code"`
	Description string `json:"description"`
}

// telegramMaxText is the Telegram Bot API hard limit for a message body.
const telegramMaxText = 4096

func (t *Telegram) Send(ctx context.Context, n domain.Notification) error {
	text := truncateRunes(subjectLine(n)+"\n\n"+plainBody(n), telegramMaxText)
	body, err := json.Marshal(telegramRequest{ChatID: t.chatID, Text: text})
	if err != nil {
		return fmt.Errorf("marshal telegram request: %w", err)
	}

	endpoint := fmt.Sprintf("%s/bot%s/sendMessage", t.apiBase, t.botToken)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		// Error text can embed the URL (with the bot token); redact it.
		return fmt.Errorf("build telegram request: %s", t.redact(err.Error()))
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := t.client.Do(req)
	if err != nil {
		// url.Error includes the full request URL, which contains the bot token;
		// proxy dial and SOCKS handshake errors can quote the proxy URL with its
		// credentials.
		return fmt.Errorf("telegram request failed: %s", t.redact(err.Error()))
	}
	defer resp.Body.Close()

	data, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
	var tr telegramResponse
	_ = json.Unmarshal(data, &tr)

	if resp.StatusCode < 200 || resp.StatusCode >= 300 || !tr.OK {
		desc := tr.Description
		if desc == "" {
			desc = strings.TrimSpace(string(data))
		}
		return fmt.Errorf("telegram send failed (status %d): %s", resp.StatusCode, t.redact(desc))
	}
	return nil
}

// redact replaces this channel's secrets (bot token, proxy password) with "***"
// so they never reach stored delivery errors, the API, the web UI, or logs.
func (t *Telegram) redact(s string) string {
	for _, secret := range t.secrets {
		s = strings.ReplaceAll(s, secret, "***")
	}
	return s
}

// truncateRunes limits s to at most n runes (not bytes), preserving valid UTF-8.
func truncateRunes(s string, n int) string {
	if len(s) <= n { // fast path: byte length already within the rune limit
		return s
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

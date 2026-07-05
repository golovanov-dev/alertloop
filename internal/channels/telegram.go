package channels

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
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
	client   *http.Client
}

// NewTelegram builds a named Telegram channel. A zero timeout falls back to 10s
// and an empty apiBase falls back to the public Bot API host.
func NewTelegram(name, botToken, chatID, apiBase string, timeout time.Duration) *Telegram {
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	if apiBase == "" {
		apiBase = "https://api.telegram.org"
	}
	return &Telegram{
		name:     name,
		botToken: botToken,
		chatID:   chatID,
		apiBase:  strings.TrimRight(apiBase, "/"),
		client:   &http.Client{Timeout: timeout},
	}
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

func (t *Telegram) Send(ctx context.Context, e *domain.Event) error {
	text := truncateRunes(subjectLine(e)+"\n\n"+plainBody(e), telegramMaxText)
	body, err := json.Marshal(telegramRequest{ChatID: t.chatID, Text: text})
	if err != nil {
		return fmt.Errorf("marshal telegram request: %w", err)
	}

	url := fmt.Sprintf("%s/bot%s/sendMessage", t.apiBase, t.botToken)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		// Error text can embed the URL (with the bot token); redact it.
		return fmt.Errorf("build telegram request: %s", t.redact(err.Error()))
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := t.client.Do(req)
	if err != nil {
		// url.Error includes the full request URL, which contains the bot token.
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

// redact replaces the bot token in a string with "***" so it never reaches
// stored delivery errors, the API, the web UI, or logs.
func (t *Telegram) redact(s string) string {
	if t.botToken == "" {
		return s
	}
	return strings.ReplaceAll(s, t.botToken, "***")
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

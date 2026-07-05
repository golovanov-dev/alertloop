package channels

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/golovanov-dev/alertloop/internal/domain"
)

// SignatureHeader carries the hex-encoded HMAC-SHA256 of the request body.
const SignatureHeader = "X-AlertLoop-Signature"

// SignatureVersionHeader identifies the signing scheme so receivers can adapt
// to future changes.
const SignatureVersionHeader = "X-AlertLoop-Signature-Version"

// signatureVersion is the current signing scheme: "v1=<hex sha256 hmac>".
const signatureVersion = "v1"

// Webhook delivers events to a generic outbound HTTP endpoint. The request body
// is HMAC-SHA256 signed with a per-webhook secret so receivers can verify
// authenticity.
type Webhook struct {
	name   string
	url    string
	secret string
	client *http.Client
}

// NewWebhook builds a named Webhook channel. A zero timeout falls back to 10s.
func NewWebhook(name, url, secret string, timeout time.Duration) *Webhook {
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	return &Webhook{
		name:   name,
		url:    url,
		secret: secret,
		client: &http.Client{Timeout: timeout},
	}
}

func (w *Webhook) Name() string { return w.name }

// webhookPayload is the JSON body posted to the receiver.
type webhookPayload struct {
	Event     *domain.Event `json:"event"`
	Timestamp string        `json:"timestamp"`
}

func (w *Webhook) Type() domain.ChannelType { return domain.ChannelWebhook }

func (w *Webhook) Send(ctx context.Context, e *domain.Event) error {
	body, err := json.Marshal(webhookPayload{
		Event:     e,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	})
	if err != nil {
		return fmt.Errorf("marshal webhook payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build webhook request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "AlertLoop/1")
	if w.secret != "" {
		req.Header.Set(SignatureVersionHeader, signatureVersion)
		req.Header.Set(SignatureHeader, Sign(w.secret, body))
	}

	resp, err := w.client.Do(req)
	if err != nil {
		return fmt.Errorf("webhook request failed: %w", err)
	}
	defer resp.Body.Close()
	// Drain a small amount so the connection can be reused.
	_, _ = io.CopyN(io.Discard, resp.Body, 4096)

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("webhook returned status %d", resp.StatusCode)
	}
	return nil
}

// Sign returns the hex-encoded HMAC-SHA256 of body keyed by secret. Exported so
// receivers (and tests) can verify signatures with the same routine.
func Sign(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

// Verify reports whether signature matches body under secret, using a
// constant-time comparison.
func Verify(secret string, body []byte, signature string) bool {
	expected := Sign(secret, body)
	return hmac.Equal([]byte(expected), []byte(signature))
}

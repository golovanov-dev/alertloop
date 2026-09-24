package channels

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/golovanov-dev/alertloop/internal/domain"
)

// SignatureHeader carries the bare lowercase hex HMAC-SHA256 of the raw request
// body, with no prefix. The format is a contract with receivers.
const SignatureHeader = "X-AlertLoop-Signature"

// SignatureVersionHeader identifies the signing scheme so receivers can adapt
// to future changes.
const SignatureVersionHeader = "X-AlertLoop-Signature-Version"

// signatureVersion is the value of SignatureVersionHeader for the scheme above.
const signatureVersion = "v1"

// Webhook delivers events to a generic outbound HTTP endpoint. The request body
// is HMAC-SHA256 signed with a per-webhook secret so receivers can verify
// authenticity.
type Webhook struct {
	name   string
	url    string
	origin string // scheme://host of url: the only part of it errors may show
	secret string
	client *http.Client
}

// NewWebhook builds a named Webhook channel. A zero timeout falls back to
// domain.DefaultChannelTimeout.
func NewWebhook(name, url, secret string, timeout time.Duration) *Webhook {
	if timeout <= 0 {
		timeout = domain.DefaultChannelTimeout
	}
	return &Webhook{
		name:   name,
		url:    url,
		origin: originOf(url),
		secret: secret,
		client: &http.Client{Timeout: timeout, CheckRedirect: noRedirect},
	}
}

// originOf returns scheme://host of a webhook URL. The path and query of a
// Slack or Discord webhook URL are its secret, and delivery errors reach
// last_error, the read API, the console and the log.
func originOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "(unparsable url)"
	}
	return u.Scheme + "://" + u.Host
}

// redactURLError drops the full URL that a *url.Error carries in its text.
func (w *Webhook) redactURLError(err error) error {
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return fmt.Errorf("%s: %w", w.origin, urlErr.Err)
	}
	return err
}

func (w *Webhook) Name() string { return w.name }

func (w *Webhook) Timeout() time.Duration { return w.client.Timeout }

// webhookPayload is the JSON body posted to the receiver.
//
// `kind` was added in 0.4.0 and is always present. A receiver that ignores it
// keeps working exactly as before; one that reads it can close its own ticket
// when an incident recovers instead of opening a second one.
//
// `fallback` (0.8.0) is present only on an alert redirected from a channel that
// could not deliver it: that channel's name and its last error.
//
// `event_url` (0.8.0) is the event's page in the admin console, present only
// when public_url is configured.
type webhookPayload struct {
	Event     *domain.Event          `json:"event"`
	Kind      domain.DeliveryKind    `json:"kind"`
	Fallback  *domain.FallbackOrigin `json:"fallback,omitempty"`
	EventURL  string                 `json:"event_url,omitempty"`
	Timestamp string                 `json:"timestamp"`
}

func (w *Webhook) Type() domain.ChannelType { return domain.ChannelWebhook }

func (w *Webhook) Send(ctx context.Context, n domain.Notification) error {
	kind := n.Kind.OrAlert()
	body, err := json.Marshal(webhookPayload{
		Event:     n.Event,
		Kind:      kind,
		Fallback:  n.Fallback,
		EventURL:  n.EventURL,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	})
	if err != nil {
		return fmt.Errorf("marshal webhook payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build webhook request: %w", w.redactURLError(err))
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "AlertLoop/1")
	if w.secret != "" {
		req.Header.Set(SignatureVersionHeader, signatureVersion)
		req.Header.Set(SignatureHeader, Sign(w.secret, body))
	}

	resp, err := w.client.Do(req)
	if err != nil {
		return fmt.Errorf("webhook request failed: %w", w.redactURLError(err))
	}
	defer resp.Body.Close()
	// Drain a small amount so the connection can be reused.
	_, _ = io.CopyN(io.Discard, resp.Body, 4096)

	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		return redirectError("webhook", resp)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("webhook returned status %d", resp.StatusCode)
	}
	return nil
}

// Sign returns the hex-encoded HMAC-SHA256 of body keyed by secret.
func Sign(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

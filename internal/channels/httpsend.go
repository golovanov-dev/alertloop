package channels

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/golovanov-dev/alertloop/internal/domain"
)

// httpTarget is what the Slack, Teams, Discord, ntfy and Pushover channels
// share: one HTTP endpoint, the part of it an error may show, and the secrets an
// error must not show. Delivery errors reach last_error, the read API, the
// console and the log.
type httpTarget struct {
	name    string
	service string // names the service in every error: "slack", "ntfy"
	url     string
	origin  string // scheme://host of url
	secrets []string
	client  *http.Client
}

// newHTTPTarget builds the shared part of an HTTP channel. The client keeps
// http.DefaultTransport, so HTTP_PROXY/HTTPS_PROXY/NO_PROXY apply.
func newHTTPTarget(name, service, rawURL string, timeout time.Duration, secrets ...string) httpTarget {
	if timeout <= 0 {
		timeout = domain.DefaultChannelTimeout
	}
	h := httpTarget{
		name:    name,
		service: service,
		url:     rawURL,
		origin:  originOf(rawURL),
		client:  &http.Client{Timeout: timeout, CheckRedirect: noRedirect},
	}
	for _, s := range secrets {
		if s != "" {
			h.secrets = append(h.secrets, s)
		}
	}
	return h
}

// noRedirect keeps an HTTP channel from following a redirect. Go repeats a
// POST answered with 301, 302 or 303 as a GET without the body, and a 2xx to
// that GET would record a message nobody received as sent.
func noRedirect(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

// redirectError reports a 3xx answer: where it pointed, scheme and host only.
func redirectError(service string, resp *http.Response) error {
	to := "(no location)"
	if loc, err := resp.Location(); err == nil {
		to = originOf(loc.String())
	}
	return fmt.Errorf("%s answered status %d: redirect to %s not followed", service, resp.StatusCode, to)
}

// urlSecrets are the parts of a chat webhook URL that authenticate the sender:
// whoever has the path (Slack, Discord, Mattermost) or the query (Teams
// Workflows signature) can post to the channel.
func urlSecrets(raw string) []string {
	out := []string{raw}
	if u, err := url.Parse(raw); err == nil {
		if p := u.EscapedPath(); len(p) > 1 {
			out = append(out, p)
		}
		out = append(out, u.RawQuery)
	}
	return out
}

func (h *httpTarget) Name() string           { return h.name }
func (h *httpTarget) Timeout() time.Duration { return h.client.Timeout }

// post sends body and classifies the answer. Any 2xx is a delivered message.
// Everything else is an error naming the status and what the service said:
// the worker retries it, and on a 4xx the text tells the operator what to fix.
func (h *httpTarget) post(ctx context.Context, contentType string, body []byte, auth string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.url, bytes.NewReader(body))
	if err != nil {
		// The error text quotes the URL; the origin is all it may show.
		return fmt.Errorf("build %s request for %s: invalid url", h.service, h.origin)
	}
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("User-Agent", "AlertLoop/1")
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}

	resp, err := h.client.Do(req)
	if err != nil {
		// *url.Error quotes the whole URL; its cause does not. Wrapped, so the
		// worker still sees a send cut short at shutdown as cancelled.
		var urlErr *url.Error
		if errors.As(err, &urlErr) {
			err = urlErr.Err
		}
		return fmt.Errorf("%s request to %s failed: %w", h.service, h.origin, err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))

	code := resp.StatusCode
	if code >= 200 && code < 300 {
		return nil
	}
	if code >= 300 && code < 400 {
		return redirectError(h.service, resp) // scheme and host: no secret
	}
	// Redacted before it is cut: a cut through a secret would leave its start.
	detail := clip(h.redact(responseDetail(data)), 300)
	if detail != "" {
		detail = ": " + detail
	}
	switch {
	case code == http.StatusTooManyRequests:
		return fmt.Errorf("%s rate limited the message (status 429)%s", h.service, detail)
	case code >= 500:
		return fmt.Errorf("%s server error (status %d)%s", h.service, code, detail)
	default:
		return fmt.Errorf("%s rejected the message (status %d)%s", h.service, code, detail)
	}
}

// encodeJSON is json.Marshal without HTML escaping: "<" stays one byte
// instead of six, which matters where a service limits the body size.
func (h *httpTarget) encodeJSON(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, fmt.Errorf("marshal %s message: %w", h.service, err)
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}

// postJSON encodes v and posts it.
func (h *httpTarget) postJSON(ctx context.Context, v any, auth string) error {
	body, err := h.encodeJSON(v)
	if err != nil {
		return err
	}
	return h.post(ctx, "application/json", body, auth)
}

// redact replaces this channel's secrets with "***".
func (h *httpTarget) redact(s string) string {
	for _, secret := range h.secrets {
		s = strings.ReplaceAll(s, secret, "***")
	}
	return s
}

// responseDetail is the service's own words about a refused message: the error
// field of a JSON answer (ntfy "error", Discord "message", Pushover "errors"),
// or the body text (Slack "invalid_token"), on one line. The caller cuts it.
func responseDetail(data []byte) string {
	var m map[string]any
	if json.Unmarshal(data, &m) == nil {
		for _, key := range []string{"error", "message", "description", "errors"} {
			switch v := m[key].(type) {
			case string:
				if v != "" {
					return v
				}
			case []any:
				var parts []string
				for _, p := range v {
					if s, ok := p.(string); ok {
						parts = append(parts, s)
					}
				}
				if len(parts) > 0 {
					return strings.Join(parts, "; ")
				}
			}
		}
	}
	return strings.Join(strings.Fields(string(data)), " ")
}

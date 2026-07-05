package channels

import (
	"context"
	"crypto/tls"
	"fmt"
	"mime"
	"net"
	"net/smtp"
	"strings"
	"time"

	"github.com/golovanov-dev/alertloop/internal/domain"
)

// mailSender abstracts the SMTP transport so the Email channel can be tested
// without a live server. Production uses smtpMailer.
type mailSender interface {
	send(ctx context.Context, from string, to []string, msg []byte) error
}

// Email delivers events as plain-text messages over SMTP.
type Email struct {
	name   string
	from   string
	to     []string
	sender mailSender
}

// EmailConfig configures the SMTP transport.
type EmailConfig struct {
	Name     string
	Host     string
	Port     int
	Username string
	Password string
	From     string
	To       []string
	// STARTTLS upgrades a plaintext connection to TLS; when true it is REQUIRED
	// (delivery fails if the server does not offer it) so mail is never sent in
	// the clear by accident.
	STARTTLS bool
	// TLS uses implicit TLS from the start (SMTPS, typically port 465).
	TLS     bool
	Timeout time.Duration
}

// NewEmail builds a named Email channel backed by a real SMTP transport.
func NewEmail(c EmailConfig) *Email {
	if c.Timeout <= 0 {
		c.Timeout = 10 * time.Second
	}
	return &Email{
		name: c.Name,
		from: c.From,
		to:   c.To,
		sender: &smtpMailer{
			addr:     net.JoinHostPort(c.Host, fmt.Sprint(c.Port)),
			host:     c.Host,
			username: c.Username,
			password: c.Password,
			starttls: c.STARTTLS,
			implicit: c.TLS,
			timeout:  c.Timeout,
		},
	}
}

// newEmailWithSender builds an Email channel with a custom transport (tests).
func newEmailWithSender(name, from string, to []string, s mailSender) *Email {
	return &Email{name: name, from: from, to: to, sender: s}
}

func (e *Email) Type() domain.ChannelType { return domain.ChannelEmail }
func (e *Email) Name() string             { return e.name }

func (e *Email) Send(ctx context.Context, ev *domain.Event) error {
	msg := buildMessage(e.from, e.to, subjectLine(ev), plainBody(ev))
	if err := e.sender.send(ctx, e.from, e.to, msg); err != nil {
		return fmt.Errorf("smtp send: %w", err)
	}
	return nil
}

// buildMessage renders an RFC 5322 plain-text message. Header values are
// sanitized against CR/LF (header injection) and the subject is RFC 2047
// encoded so non-ASCII and any residual special characters are safe.
func buildMessage(from string, to []string, subject, body string) []byte {
	sanitizedTo := make([]string, len(to))
	for i, addr := range to {
		sanitizedTo[i] = sanitizeHeader(addr)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "From: %s\r\n", sanitizeHeader(from))
	fmt.Fprintf(&b, "To: %s\r\n", strings.Join(sanitizedTo, ", "))
	fmt.Fprintf(&b, "Subject: %s\r\n", mime.QEncoding.Encode("utf-8", sanitizeHeader(subject)))
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: text/plain; charset=UTF-8\r\n")
	b.WriteString("\r\n")
	b.WriteString(strings.ReplaceAll(body, "\n", "\r\n"))
	return []byte(b.String())
}

// sanitizeHeader strips CR/LF (and other control characters) from a header
// value to prevent SMTP header injection, and caps its length.
func sanitizeHeader(v string) string {
	v = strings.Map(func(r rune) rune {
		if r == '\r' || r == '\n' || r < 0x20 {
			return -1
		}
		return r
	}, v)
	const maxHeaderLen = 200
	if len(v) > maxHeaderLen {
		v = v[:maxHeaderLen]
	}
	return strings.TrimSpace(v)
}

// smtpMailer is the production SMTP transport. It dials the server, optionally
// upgrades with STARTTLS, authenticates when credentials are present, and sends
// the message. The whole exchange is bounded by the context deadline.
type smtpMailer struct {
	addr     string
	host     string
	username string
	password string
	starttls bool // require STARTTLS upgrade on a plaintext connection
	implicit bool // implicit TLS from the start (SMTPS)
	timeout  time.Duration
}

func (m *smtpMailer) send(ctx context.Context, from string, to []string, msg []byte) error {
	dialer := &net.Dialer{Timeout: m.timeout}
	var conn net.Conn
	var err error
	if m.implicit {
		// SMTPS: TLS handshake before any SMTP command (typically port 465).
		conn, err = tls.DialWithDialer(dialer, "tcp", m.addr, &tls.Config{ServerName: m.host})
	} else {
		conn, err = dialer.DialContext(ctx, "tcp", m.addr)
	}
	if err != nil {
		return fmt.Errorf("dial smtp: %w", err)
	}
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}

	client, err := smtp.NewClient(conn, m.host)
	if err != nil {
		_ = conn.Close()
		return fmt.Errorf("smtp client: %w", err)
	}
	defer client.Close()

	// On a plaintext connection, STARTTLS (when requested) is REQUIRED: if the
	// server does not advertise it, fail rather than send the event in the clear.
	if !m.implicit && m.starttls {
		if ok, _ := client.Extension("STARTTLS"); !ok {
			return fmt.Errorf("smtp server %q does not support STARTTLS but it is required", m.host)
		}
		if err := client.StartTLS(&tls.Config{ServerName: m.host}); err != nil {
			return fmt.Errorf("starttls: %w", err)
		}
	}

	if m.username != "" {
		auth := smtp.PlainAuth("", m.username, m.password, m.host)
		if err := client.Auth(auth); err != nil {
			return fmt.Errorf("smtp auth: %w", err)
		}
	}

	if err := client.Mail(from); err != nil {
		return fmt.Errorf("smtp mail from: %w", err)
	}
	for _, rcpt := range to {
		if err := client.Rcpt(rcpt); err != nil {
			return fmt.Errorf("smtp rcpt %q: %w", rcpt, err)
		}
	}
	w, err := client.Data()
	if err != nil {
		return fmt.Errorf("smtp data: %w", err)
	}
	if _, err := w.Write(msg); err != nil {
		return fmt.Errorf("smtp write: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("smtp close data: %w", err)
	}
	return client.Quit()
}

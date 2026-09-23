package channels

import (
	"context"
	"crypto/tls"
	"errors"
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
	name    string
	from    string
	to      []string
	sender  mailSender
	timeout time.Duration
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
		c.Timeout = domain.DefaultChannelTimeout
	}
	return &Email{
		name:    c.Name,
		from:    c.From,
		to:      c.To,
		timeout: c.Timeout,
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
func (e *Email) Timeout() time.Duration   { return e.timeout }

func (e *Email) Send(ctx context.Context, n domain.Notification) error {
	msg := buildMessage(e.from, e.to, subjectLine(n), plainBody(n), messageID(n, e.from), time.Now())
	if err := e.sender.send(ctx, e.from, e.to, msg); err != nil {
		return fmt.Errorf("smtp send: %w", err)
	}
	return nil
}

// buildMessage renders an RFC 5322 plain-text message. Header values are
// sanitized against CR/LF (header injection) and the subject is RFC 2047
// encoded so non-ASCII and any residual special characters are safe.
func buildMessage(from string, to []string, subject, body, msgID string, sentAt time.Time) []byte {
	sanitizedTo := make([]string, len(to))
	for i, addr := range to {
		sanitizedTo[i] = sanitizeHeader(addr)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "From: %s\r\n", sanitizeHeader(from))
	fmt.Fprintf(&b, "To: %s\r\n", strings.Join(sanitizedTo, ", "))
	fmt.Fprintf(&b, "Subject: %s\r\n", mime.QEncoding.Encode("utf-8", sanitizeHeader(subject)))
	// Date and Message-ID are not optional in practice. RFC 5322 requires both
	// on an originated message, and Gmail and Microsoft 365 both treat their
	// absence as a spam signal. For a product whose entire job is getting a
	// notification in front of a person, landing in the spam folder is total
	// failure, and it would look like "email does not work" rather than like a
	// missing header.
	fmt.Fprintf(&b, "Date: %s\r\n", sentAt.Format(time.RFC1123Z))
	fmt.Fprintf(&b, "Message-ID: %s\r\n", sanitizeHeader(msgID))
	// Tells mailing lists and out-of-office responders not to reply. Without it
	// an autoresponder on the receiving side can answer every alert.
	b.WriteString("Auto-Submitted: auto-generated\r\n")
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: text/plain; charset=UTF-8\r\n")
	b.WriteString("\r\n")
	b.WriteString(strings.ReplaceAll(body, "\n", "\r\n"))
	return []byte(b.String())
}

// messageID builds a stable, unique Message-ID for one notification.
//
// The event id plus the kind: an alert and its recovery are two messages about
// the same incident and must not share an id, or a client threading by
// Message-ID would treat the second as a duplicate of the first and hide it.
// Retries of the SAME notification deliberately reuse the id, so a delivery
// that succeeds on the second attempt does not arrive twice.
func messageID(n domain.Notification, from string) string {
	domainPart := "alertloop.local"
	if at := strings.LastIndex(from, "@"); at >= 0 && at+1 < len(from) {
		domainPart = from[at+1:]
	}
	kind := n.Kind.OrAlert()
	id := "unknown"
	if n.Event != nil && n.Event.ID != "" {
		id = n.Event.ID
	}
	return fmt.Sprintf("<%s.%s@%s>", id, kind, domainPart)
}

// sanitizeHeader strips CR/LF (and other control characters) from a header
// value to prevent SMTP header injection, and caps its length in RUNES.
//
// Bytes would cut a Cyrillic or CJK subject through the middle of a character;
// mime.QEncoding then encodes the broken tail and the recipient sees a
// replacement glyph in the one line of the message they are most likely to
// read.
func sanitizeHeader(v string) string {
	v = strings.Map(func(r rune) rune {
		if r == '\r' || r == '\n' || r < 0x20 {
			return -1
		}
		return r
	}, v)
	return strings.TrimSpace(domain.TruncateRunes(v, domain.MaxHeaderRunes))
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

func (m *smtpMailer) send(ctx context.Context, from string, to []string, msg []byte) (err error) {
	dialer := &net.Dialer{Timeout: m.timeout}
	var conn net.Conn
	if m.implicit {
		// SMTPS: TLS handshake before any SMTP command (typically port 465).
		td := &tls.Dialer{NetDialer: dialer, Config: &tls.Config{ServerName: m.host}}
		conn, err = td.DialContext(ctx, "tcp", m.addr)
	} else {
		conn, err = dialer.DialContext(ctx, "tcp", m.addr)
	}
	if err != nil {
		return fmt.Errorf("dial smtp: %w", err)
	}
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	// net/smtp takes no context, so cancellation is delivered through the
	// connection: a deadline in the past fails every pending and later read or
	// write at once. Without this a shutdown that cancels ctx would still wait
	// for a silent server until the channel timeout. The STARTTLS client wraps
	// this same conn, so the deadline reaches it too.
	stop := context.AfterFunc(ctx, func() { _ = conn.SetDeadline(time.Unix(1, 0)) })
	defer func() {
		stop()
		if err != nil && ctx.Err() != nil && !errors.Is(err, context.Cause(ctx)) {
			err = fmt.Errorf("%w (%w)", err, context.Cause(ctx))
		}
	}()

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
	// The server accepted the message at the end of DATA. A failed QUIT (a
	// shutdown cutting the connection now) must not turn a delivered mail into
	// a retry that sends it twice.
	_ = client.Quit()
	return nil
}

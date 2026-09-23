package channels

import (
	"bufio"
	"context"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/golovanov-dev/alertloop/internal/domain"
)

// fakeSMTP is a minimal SMTP server on 127.0.0.1:0 for one session. It returns
// its port and a channel that yields every line the client sent, once the
// client hangs up. It never offers STARTTLS.
func fakeSMTP(t *testing.T) (int, <-chan []string) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	lines := make(chan []string, 1)
	go func() {
		var got []string
		defer func() { lines <- got }()
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
		r := bufio.NewReader(conn)
		reply := func(s string) { _, _ = conn.Write([]byte(s + "\r\n")) }

		reply("220 fake ESMTP")
		inData := false
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				return
			}
			line = strings.TrimRight(line, "\r\n")
			got = append(got, line)
			if inData {
				if line == "." {
					inData = false
					reply("250 queued")
				}
				continue
			}
			switch strings.ToUpper(strings.Fields(line + " x")[0]) {
			case "EHLO":
				reply("250 fake")
			case "MAIL", "RCPT":
				reply("250 ok")
			case "DATA":
				inData = true
				reply("354 go ahead")
			case "QUIT":
				reply("221 bye")
				return
			default:
				reply("502 not implemented")
			}
		}
	}()
	return ln.Addr().(*net.TCPAddr).Port, lines
}

func sendSMTP(t *testing.T, port int, starttls bool) error {
	t.Helper()
	e := NewEmail(EmailConfig{
		Name: "ops", Host: "127.0.0.1", Port: port, STARTTLS: starttls,
		From: "alerts@example.com", To: []string{"ops@example.com"}, Timeout: 2 * time.Second,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return e.Send(ctx, domain.Alert(sampleEvent()))
}

func TestSMTPSendsOverTheWire(t *testing.T) {
	port, lines := fakeSMTP(t)
	if err := sendSMTP(t, port, false); err != nil {
		t.Fatalf("send: %v", err)
	}
	session := strings.Join(<-lines, "\n")
	for _, want := range []string{
		"MAIL FROM:<alerts@example.com>",
		"RCPT TO:<ops@example.com>",
		"Subject: [CRITICAL/incident] Feed processing failed",
		"QUIT",
	} {
		if !strings.Contains(session, want) {
			t.Fatalf("session missing %q:\n%s", want, session)
		}
	}
}

// starttls: true is a requirement, not a preference: a server that does not
// offer STARTTLS gets no envelope and no message in the clear.
func TestSMTPRefusesToSendWithoutSTARTTLS(t *testing.T) {
	port, lines := fakeSMTP(t)
	err := sendSMTP(t, port, true)
	if err == nil || !strings.Contains(err.Error(), "STARTTLS") {
		t.Fatalf("want a STARTTLS refusal, got %v", err)
	}
	session := strings.Join(<-lines, "\n")
	if strings.Contains(session, "MAIL FROM") || strings.Contains(session, "Feed processing failed") {
		t.Fatalf("mail went out without STARTTLS:\n%s", session)
	}
}

// silentSMTP accepts connections and never says a word: the server that hangs
// mid-dialogue. It returns the port.
func silentSMTP(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	done := make(chan struct{})
	t.Cleanup(func() { close(done); _ = ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				<-done
				_ = conn.Close()
			}()
		}
	}()
	return ln.Addr().(*net.TCPAddr).Port
}

// Shutdown cancels the context of a send in flight. net/smtp has no context of
// its own, so without the cancellation reaching the connection a silent server
// would keep the send, and the worker's shutdown, waiting for the channel
// timeout.
func TestSMTPSendStopsWhenContextIsCancelled(t *testing.T) {
	for _, implicitTLS := range []bool{false, true} {
		port := silentSMTP(t)
		e := NewEmail(EmailConfig{
			Name: "ops", Host: "127.0.0.1", Port: port, TLS: implicitTLS,
			From: "alerts@example.com", To: []string{"ops@example.com"}, Timeout: time.Minute,
		})
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		time.AfterFunc(100*time.Millisecond, cancel)
		began := time.Now()
		err := e.Send(ctx, domain.Alert(sampleEvent()))
		took := time.Since(began)
		cancel()
		if err == nil {
			t.Fatalf("tls=%v: send to a silent server succeeded", implicitTLS)
		}
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("tls=%v: error does not say the send was cancelled: %v", implicitTLS, err)
		}
		if took > 3*time.Second {
			t.Fatalf("tls=%v: send returned %v after cancellation, want promptly", implicitTLS, took)
		}
	}
}

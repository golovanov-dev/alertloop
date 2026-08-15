package channels

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"strconv"
	"testing"
	"time"
)

// mustParse is the test spelling of a proxy URL that config validation already
// accepted.
func mustParse(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse %q: %v", raw, err)
	}
	return u
}

// telegramHost is a host name that does not resolve and is not loopback:
// requests to it can only succeed through a proxy, which is exactly what these
// tests assert. (ProxyFromEnvironment deliberately bypasses the proxy for
// localhost, so a loopback target would prove nothing.)
const telegramHost = "telegram.invalid"

func TestTelegramSendsThroughHTTPProxy(t *testing.T) {
	gotHost := make(chan string, 1)
	gotPath := make(chan string, 1)
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// An HTTP proxy receives the absolute URI of the target request.
		select {
		case gotHost <- r.Host:
		default:
		}
		select {
		case gotPath <- r.URL.Path:
		default:
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer proxy.Close()

	tg := NewTelegram(TelegramConfig{
		Name: "tg", BotToken: "BOT123", ChatID: "-100",
		APIBase: "http://" + telegramHost,
		Proxy:   mustParse(t, proxy.URL),
		Timeout: 5 * time.Second,
	})
	if err := tg.Send(context.Background(), sampleEvent()); err != nil {
		t.Fatalf("send through http proxy: %v", err)
	}
	if host := <-gotHost; host != telegramHost {
		t.Fatalf("proxy saw host %q, want %q", host, telegramHost)
	}
	if path := <-gotPath; path != "/botBOT123/sendMessage" {
		t.Fatalf("proxy saw path %q", path)
	}
}

// TestTelegramSendsThroughSOCKS5Proxy also answers the DNS question the README
// documents: Go's http.Transport hands the SOCKS5 proxy the target HOST NAME
// (address type 0x03), so the proxy resolves it, not AlertLoop. That is why
// "socks5" and "socks5h" behave identically here.
func TestTelegramSendsThroughSOCKS5Proxy(t *testing.T) {
	for _, scheme := range []string{"socks5", "socks5h"} {
		t.Run(scheme, func(t *testing.T) {
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte(`{"ok":true}`))
			}))
			defer api.Close()

			proxy := startSOCKS5(t, api.Listener.Addr().String(), "", "")
			tg := NewTelegram(TelegramConfig{
				Name: "tg", BotToken: "BOT123", ChatID: "-100",
				APIBase: "http://" + telegramHost,
				Proxy:   mustParse(t, scheme+"://"+proxy.addr),
				Timeout: 5 * time.Second,
			})
			if err := tg.Send(context.Background(), sampleEvent()); err != nil {
				t.Fatalf("send through %s proxy: %v", scheme, err)
			}
			if got := <-proxy.requested; got != telegramHost+":80" {
				t.Fatalf("proxy was asked to connect to %q, want the unresolved host %q", got, telegramHost+":80")
			}
		})
	}
}

// TestTelegramProxyPasswordNotInDeliveryError drives a real failing delivery
// through an authenticating proxy (wrong password) and requires the password to
// be absent from the stored error. Redaction is defensive: the Go transport is
// not known to quote proxy credentials, but a delivery error is persisted and
// shown in the API and the web UI, so the guarantee must hold by construction.
func TestTelegramProxyPasswordNotInDeliveryError(t *testing.T) {
	proxy := startSOCKS5(t, "127.0.0.1:1", "operator", "correct-horse")
	tg := NewTelegram(TelegramConfig{
		Name: "tg", BotToken: "BOT123", ChatID: "-100",
		APIBase: "http://" + telegramHost,
		Proxy:   mustParse(t, "socks5://operator:hunter2@"+proxy.addr),
		Timeout: 5 * time.Second,
	})
	err := tg.Send(context.Background(), sampleEvent())
	if err == nil {
		t.Fatal("expected the delivery to fail against a rejecting proxy")
	}
	if contains(err.Error(), "hunter2") {
		t.Fatalf("proxy password leaked in delivery error: %v", err)
	}
	if contains(err.Error(), "BOT123") {
		t.Fatalf("bot token leaked in delivery error: %v", err)
	}

	// The same guarantee for any error text the transport may produce: whatever
	// quotes the proxy URL is masked before it reaches storage.
	masked := tg.redact("dial socks5://operator:hunter2@" + proxy.addr + ": refused")
	if contains(masked, "hunter2") {
		t.Fatalf("redact left the proxy password in place: %s", masked)
	}
}

// TestTelegramUsesEnvironmentProxyWhenProxyUnset is the compatibility guard for
// installs that already route Telegram through HTTP_PROXY: with no `proxy` in
// the channel config the environment must still be honored. It runs in a child
// process because net/http reads the proxy environment once per process.
func TestTelegramUsesEnvironmentProxyWhenProxyUnset(t *testing.T) {
	if os.Getenv("ALERTLOOP_TEST_ENV_PROXY_CHILD") == "1" {
		tg := NewTelegram(TelegramConfig{
			Name: "tg", BotToken: "BOT123", ChatID: "-100",
			APIBase: "http://" + telegramHost,
			Timeout: 5 * time.Second,
		})
		if err := tg.Send(context.Background(), sampleEvent()); err != nil {
			t.Fatalf("child send: %v", err)
		}
		return
	}

	gotHost := make(chan string, 1)
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case gotHost <- r.Host:
		default:
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer proxy.Close()

	cmd := exec.Command(os.Args[0], "-test.run=^TestTelegramUsesEnvironmentProxyWhenProxyUnset$", "-test.v")
	cmd.Env = append(os.Environ(),
		"ALERTLOOP_TEST_ENV_PROXY_CHILD=1",
		"HTTP_PROXY="+proxy.URL,
		"NO_PROXY=", "no_proxy=",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("child test failed: %v\n%s", err, out)
	}
	select {
	case host := <-gotHost:
		if host != telegramHost {
			t.Fatalf("environment proxy saw host %q, want %q", host, telegramHost)
		}
	default:
		t.Fatal("HTTP_PROXY was ignored: the environment proxy received no request")
	}
}

// socks5Proxy is a minimal SOCKS5 server for tests. It performs the handshake,
// records the address the client asked for, and relays the connection to
// dialTo. No external network is involved.
type socks5Proxy struct {
	addr string
	// requested carries the "host:port" the client asked the proxy to reach.
	requested chan string
}

// startSOCKS5 starts the test proxy. A non-empty user/pass makes it require
// RFC 1929 username/password authentication and reject anything else.
func startSOCKS5(t *testing.T, dialTo, user, pass string) *socks5Proxy {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	p := &socks5Proxy{addr: ln.Addr().String(), requested: make(chan string, 4)}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go p.serve(conn, dialTo, user, pass)
		}
	}()
	return p
}

func (p *socks5Proxy) serve(conn net.Conn, dialTo, user, pass string) {
	defer conn.Close()
	br := bufio.NewReader(conn)

	// Greeting: version, method count, methods.
	head := make([]byte, 2)
	if _, err := io.ReadFull(br, head); err != nil {
		return
	}
	methods := make([]byte, head[1])
	if _, err := io.ReadFull(br, methods); err != nil {
		return
	}
	if user != "" {
		if !bytes.Contains(methods, []byte{0x02}) {
			_, _ = conn.Write([]byte{0x05, 0xff})
			return
		}
		if _, err := conn.Write([]byte{0x05, 0x02}); err != nil {
			return
		}
		gotUser, gotPass, err := readUserPassAuth(br)
		if err != nil {
			return
		}
		if gotUser != user || gotPass != pass {
			_, _ = conn.Write([]byte{0x01, 0x01}) // authentication failure
			return
		}
		if _, err := conn.Write([]byte{0x01, 0x00}); err != nil {
			return
		}
	} else if _, err := conn.Write([]byte{0x05, 0x00}); err != nil {
		return
	}

	// CONNECT request: version, command, reserved, address type.
	req := make([]byte, 4)
	if _, err := io.ReadFull(br, req); err != nil {
		return
	}
	host, err := readSOCKSAddr(br, req[3])
	if err != nil {
		return
	}
	portBytes := make([]byte, 2)
	if _, err := io.ReadFull(br, portBytes); err != nil {
		return
	}
	port := int(portBytes[0])<<8 | int(portBytes[1])
	select {
	case p.requested <- net.JoinHostPort(host, strconv.Itoa(port)):
	default:
	}

	upstream, err := net.Dial("tcp", dialTo)
	if err != nil {
		_, _ = conn.Write([]byte{0x05, 0x01, 0x00, 0x01, 0, 0, 0, 0, 0, 0}) // general failure
		return
	}
	defer upstream.Close()
	if _, err := conn.Write([]byte{0x05, 0x00, 0x00, 0x01, 0, 0, 0, 0, 0, 0}); err != nil {
		return
	}
	go func() { _, _ = io.Copy(upstream, br) }()
	_, _ = io.Copy(conn, upstream)
}

// readUserPassAuth reads an RFC 1929 username/password sub-negotiation.
func readUserPassAuth(br *bufio.Reader) (string, string, error) {
	head := make([]byte, 2) // version, username length
	if _, err := io.ReadFull(br, head); err != nil {
		return "", "", err
	}
	user := make([]byte, head[1])
	if _, err := io.ReadFull(br, user); err != nil {
		return "", "", err
	}
	passLen := make([]byte, 1)
	if _, err := io.ReadFull(br, passLen); err != nil {
		return "", "", err
	}
	pass := make([]byte, passLen[0])
	if _, err := io.ReadFull(br, pass); err != nil {
		return "", "", err
	}
	return string(user), string(pass), nil
}

// readSOCKSAddr reads the destination address of a CONNECT request. Address
// type 0x03 (a host name) is the interesting one: it means the client left DNS
// resolution to the proxy.
func readSOCKSAddr(br *bufio.Reader, addrType byte) (string, error) {
	switch addrType {
	case 0x01: // IPv4
		b := make([]byte, 4)
		if _, err := io.ReadFull(br, b); err != nil {
			return "", err
		}
		return net.IP(b).String(), nil
	case 0x03: // host name
		length := make([]byte, 1)
		if _, err := io.ReadFull(br, length); err != nil {
			return "", err
		}
		b := make([]byte, length[0])
		if _, err := io.ReadFull(br, b); err != nil {
			return "", err
		}
		return string(b), nil
	case 0x04: // IPv6
		b := make([]byte, 16)
		if _, err := io.ReadFull(br, b); err != nil {
			return "", err
		}
		return net.IP(b).String(), nil
	}
	return "", io.ErrUnexpectedEOF
}

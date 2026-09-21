package channels

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/golovanov-dev/alertloop/internal/domain"
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
	if err := tg.Send(context.Background(), domain.Alert(sampleEvent())); err != nil {
		t.Fatalf("send through http proxy: %v", err)
	}
	if host := <-gotHost; host != telegramHost {
		t.Fatalf("proxy saw host %q, want %q", host, telegramHost)
	}
	if path := <-gotPath; path != "/botBOT123/sendMessage" {
		t.Fatalf("proxy saw path %q", path)
	}
}

// TestTelegramProxyPasswordNotInDeliveryError drives a real failing delivery
// through an authenticating proxy (wrong password) and requires the password to
// be absent from the stored error. Redaction is defensive: the Go transport is
// not known to quote proxy credentials, but a delivery error is persisted and
// shown in the API and the web UI, so the guarantee must hold by construction.
func TestTelegramProxyPasswordNotInDeliveryError(t *testing.T) {
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Proxy-Authenticate", `Basic realm="proxy"`)
		w.WriteHeader(http.StatusProxyAuthRequired)
	}))
	defer proxy.Close()
	proxyURL := "http://operator:hunter2@" + proxy.Listener.Addr().String()

	tg := NewTelegram(TelegramConfig{
		Name: "tg", BotToken: "BOT123", ChatID: "-100",
		APIBase: "http://" + telegramHost,
		Proxy:   mustParse(t, proxyURL),
		Timeout: 5 * time.Second,
	})
	err := tg.Send(context.Background(), domain.Alert(sampleEvent()))
	if err == nil {
		t.Fatal("expected the delivery to fail against a rejecting proxy")
	}
	if !strings.Contains(err.Error(), "status 407") {
		t.Fatalf("the delivery did not reach the proxy: %v", err)
	}
	if strings.Contains(err.Error(), "hunter2") {
		t.Fatalf("proxy password leaked in delivery error: %v", err)
	}
	if strings.Contains(err.Error(), "BOT123") {
		t.Fatalf("bot token leaked in delivery error: %v", err)
	}

	// The same guarantee for any error text the transport may produce: whatever
	// quotes the proxy URL is masked before it reaches storage.
	masked := tg.redact("proxyconnect tcp " + proxyURL + ": refused")
	if strings.Contains(masked, "hunter2") {
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
		if err := tg.Send(context.Background(), domain.Alert(sampleEvent())); err != nil {
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

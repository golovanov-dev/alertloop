package config

import (
	"strings"
	"testing"
)

func TestParseProxyURLAcceptsSupportedSchemes(t *testing.T) {
	cases := []string{
		"http://127.0.0.1:3128",
		"https://proxy.example.com:8443",
		"socks5://127.0.0.1:1080",
		"socks5h://127.0.0.1:1080",
		"socks5://user:pass@127.0.0.1:1080",
		"http://proxy.example.com", // http has a default port
	}
	for _, raw := range cases {
		u, err := ParseProxyURL(raw)
		if err != nil {
			t.Errorf("ParseProxyURL(%q) = %v, want success", raw, err)
			continue
		}
		if u == nil {
			t.Errorf("ParseProxyURL(%q) returned no URL", raw)
		}
	}

	// An empty setting means "no channel proxy" (environment proxy still applies).
	u, err := ParseProxyURL("   ")
	if err != nil || u != nil {
		t.Fatalf("empty proxy = (%v, %v), want (nil, nil)", u, err)
	}
}

func TestParseProxyURLRejectsBadValues(t *testing.T) {
	cases := map[string]string{
		"unsupported scheme":  "ftp://127.0.0.1:1080",
		"mtproto is not http": "mtproto://127.0.0.1:443",
		"no scheme":           "127.0.0.1:1080",
		"unparseable":         "http://127.0.0.1:1080/%zz",
		"missing host":        "socks5://",
		"socks without port":  "socks5://proxy.example.com",
	}
	for name, raw := range cases {
		if _, err := ParseProxyURL(raw); err == nil {
			t.Errorf("%s: ParseProxyURL(%q) succeeded, want a startup error", name, raw)
		}
	}
}

// A rejected proxy URL must not echo its own credentials: the message goes to
// the startup log, where a password does not belong.
func TestParseProxyURLErrorHidesCredentials(t *testing.T) {
	_, err := ParseProxyURL("ftp://operator:hunter2@127.0.0.1:1080")
	if err == nil {
		t.Fatal("expected an unsupported-scheme error")
	}
	if strings.Contains(err.Error(), "hunter2") {
		t.Fatalf("proxy password leaked in the config error: %v", err)
	}
}

func TestSafeProxyURLDropsCredentials(t *testing.T) {
	u, err := ParseProxyURL("socks5://operator:hunter2@127.0.0.1:1080")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got := SafeProxyURL(u); got != "socks5://127.0.0.1:1080" {
		t.Fatalf("SafeProxyURL = %q, want the credential-free form", got)
	}
	if SafeProxyURL(nil) != "" {
		t.Fatal("SafeProxyURL(nil) should be empty")
	}
}

func TestValidateRejectsBadTelegramProxy(t *testing.T) {
	cfg := Default()
	cfg.Channels.Telegram = []TelegramChannel{{
		Name: "tg", BotToken: "t", ChatID: "c", Proxy: "ftp://127.0.0.1:1080",
	}}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected startup to fail on an unsupported proxy scheme")
	}
	if !strings.Contains(err.Error(), "tg") {
		t.Fatalf("error should name the channel: %v", err)
	}

	cfg.Channels.Telegram[0].Proxy = "socks5://127.0.0.1:1080"
	if err := cfg.Validate(); err != nil {
		t.Fatalf("valid proxy rejected: %v", err)
	}
}

// The startup channel list shows the proxy so an operator can confirm it is in
// effect — without its credentials.
func TestEnabledChannelsShowsProxyWithoutCredentials(t *testing.T) {
	cfg := Default()
	cfg.Channels.Telegram = []TelegramChannel{
		{Name: "with-proxy", BotToken: "t", ChatID: "c", Proxy: "socks5://operator:hunter2@127.0.0.1:1080"},
		{Name: "direct", BotToken: "t", ChatID: "c"},
	}
	got := strings.Join(cfg.EnabledChannels(), " | ")
	if strings.Contains(got, "hunter2") || strings.Contains(got, "operator") {
		t.Fatalf("proxy credentials leaked into the startup channel list: %s", got)
	}
	if !strings.Contains(got, "telegram:with-proxy (proxy socks5://127.0.0.1:1080)") {
		t.Fatalf("proxy not shown for the proxied channel: %s", got)
	}
	if !strings.Contains(got, "telegram:direct") {
		t.Fatalf("unproxied channel missing: %s", got)
	}
}

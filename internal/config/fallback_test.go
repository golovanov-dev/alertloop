package config

import (
	"strings"
	"testing"
)

func TestFallbackMustNameAnotherConfiguredChannel(t *testing.T) {
	cases := []struct {
		name, hookFallback, wantErr string
	}{
		{"chain of two is allowed", "tg", ""},
		{"itself", "hook", `channel "hook": fallback names the channel itself`},
		{"missing", "pager", `channel "hook": fallback "pager" is not a configured channel`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := loadYAML(t, `
database: {driver: sqlite, dsn: x.db}
channels:
  telegram:
    - {name: tg, bot_token: "1:x", chat_id: "1", fallback: mail}
  email:
    - {name: mail, host: smtp.example.com, from: a@example.com, to: [b@example.com]}
  webhook:
    - {name: hook, url: "https://example.com/h", fallback: `+tc.hookFallback+`}
`)
			err := cfg.Validate()
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("Validate: %v", err)
				}
				if got := cfg.Channels.Fallbacks(); got["hook"] != "tg" || got["tg"] != "mail" || len(got) != 2 {
					t.Fatalf("Fallbacks() = %v", got)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("Validate: %v, want %q", err, tc.wantErr)
			}
		})
	}
}

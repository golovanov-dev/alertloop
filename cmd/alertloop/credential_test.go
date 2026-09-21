package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The entry point is where the credential check happens, so this is the test
// that it happens at all, and before anything is opened: `all` with neither
// admin_token nor api_keys must fail instead of serving — from a config file,
// and with no config file at all. Which modes need a credential is
// app.RequireCredential's own business and is tested there.
func TestTheEntryPointRefusesToServeWithoutACredential(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "alertloop.yaml")
	// No database is created: the refusal happens before storage is opened, so
	// a run that gets as far as the DSN has already failed the test.
	cfg := "addr: \"127.0.0.1:0\"\ndatabase:\n  driver: sqlite\n  dsn: '" +
		filepath.ToSlash(filepath.Join(dir, "alertloop.db")) + "'\n"
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ALERTLOOP_CONFIG", "")
	// Without a file the defaults point at ./alertloop.db: run where a created
	// file would not litter the source tree.
	t.Chdir(dir)

	for name, args := range map[string][]string{
		"config file":    {"--config", cfgPath, "all"},
		"no config file": {"all"},
	} {
		t.Run(name, func(t *testing.T) {
			// In a goroutine with a deadline: without the check this call
			// SERVES, and a test that hangs reports nothing useful.
			done := make(chan error, 1)
			go func() { done <- runArgs(t, args...) }()
			select {
			case err := <-done:
				if err == nil {
					t.Fatal("`all` started with no admin_token and no api_keys")
				}
				if !strings.Contains(err.Error(), "admin_token") || !strings.Contains(err.Error(), "api_keys") {
					t.Fatalf("the refusal does not say what to add:\n%v", err)
				}
			case <-time.After(15 * time.Second):
				t.Fatal("`all` is serving with no admin_token and no api_keys: the API is open")
			}
			if _, err := os.Stat(filepath.Join(dir, "alertloop.db")); !os.IsNotExist(err) {
				t.Fatal("the database was opened before the credential check")
			}
		})
	}
}

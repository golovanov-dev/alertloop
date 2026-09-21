package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The entry point is where the credential check happens, so this is the test
// that it happens at all, and before anything is opened: `all` on a config file
// with neither admin_token nor api_keys must fail instead of serving. Which
// modes need a credential is app.RequireCredential's own business and is tested
// there.
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

	// In a goroutine with a deadline: without the check this call SERVES, and a
	// test that hangs reports nothing useful.
	done := make(chan error, 1)
	go func() { done <- runArgs(t, "--config", cfgPath, "all") }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("`all` started on a config file with no admin_token and no api_keys")
		}
		if !strings.Contains(err.Error(), "admin_token") || !strings.Contains(err.Error(), "api_keys") {
			t.Fatalf("the refusal does not say what to add:\n%v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("`all` is serving on a config file with no admin_token and no api_keys: the API is open")
	}
}

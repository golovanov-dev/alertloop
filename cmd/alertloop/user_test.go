package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/golovanov-dev/alertloop/internal/auth"
	"github.com/golovanov-dev/alertloop/internal/config"
	"github.com/golovanov-dev/alertloop/internal/storage"
)

// A mistyped user command is refused before the config is read, like a
// mistyped mode: nothing is opened or migrated.
func TestAnUnknownUserCommandTouchesNoDatabase(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "alertloop.db")
	cfgPath := writeConfig(t, dir, dbPath, filepath.Join(dir, "alertloop.log"))
	if err := runArgs(t, "--config", cfgPath, "user", "remove", "alice"); err == nil || !strings.Contains(err.Error(), "unknown user command") {
		t.Fatalf("err = %v, want an unknown-command refusal", err)
	}
	if _, err := os.Stat(dbPath); !os.IsNotExist(err) {
		t.Fatalf("an unknown user command opened the database (stat err %v)", err)
	}
}

func TestUserCommands(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Default()
	cfg.AdminToken = "a-real-token"
	cfg.Database = config.Database{Driver: "sqlite", DSN: filepath.ToSlash(filepath.Join(dir, "alertloop.db"))}
	ctx := context.Background()
	user := func(stdin string, args ...string) string {
		t.Helper()
		cmd, err := parseUserArgs(args)
		if err != nil {
			t.Fatal(err)
		}
		var out bytes.Buffer
		if err := runUser(ctx, cfg, cmd, strings.NewReader(stdin), &out); err != nil {
			t.Fatalf("user %v: %v", args, err)
		}
		return out.String()
	}

	// The password from stdin; a Windows line ending is not part of it.
	user("a long enough password\r\n", "add", "--password-stdin", "Alice")
	// A generated password is printed once and works.
	out := user("", "add", "bob")
	generated := strings.TrimSpace(out[strings.Index(out, "(shown once): ")+len("(shown once): "):])

	store, err := storage.Open("sqlite", cfg.Database.DSN)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	check := func(login, password string) bool {
		u, err := store.UserByLogin(ctx, login)
		return err == nil && auth.CheckPassword(u.PasswordHash, password)
	}
	if !check("alice", "a long enough password") || !check("bob", generated) {
		t.Fatalf("passwords do not match what was set (generated %q)", generated)
	}

	user("another long password\n", "passwd", "--password-stdin", "alice")
	if !check("alice", "another long password") {
		t.Fatal("passwd did not change the password")
	}
	user("", "disable", "bob")
	if list := user("", "list"); !strings.Contains(list, "bob") || !strings.Contains(list, "disabled") {
		t.Fatalf("list after disable:\n%s", list)
	}

	// A new password alone does not let a disabled user in; passwd says so
	// and names the command that does.
	if out := user("third long password\n", "passwd", "--password-stdin", "bob"); !strings.Contains(out, "is disabled") ||
		!strings.Contains(out, "alertloop user enable bob") {
		t.Fatalf("passwd of a disabled user does not say it is disabled:\n%s", out)
	}
	if out := user("", "enable", "bob"); !strings.Contains(out, "bob enabled") {
		t.Fatalf("enable:\n%s", out)
	}
	if u, err := store.UserByLogin(ctx, "bob"); err != nil || u.DisabledAt != nil || !check("bob", "third long password") {
		t.Fatalf("after enable: %+v, %v; want active with the password set while disabled", u, err)
	}
	if out := user("", "enable", "bob"); !strings.Contains(out, "already enabled") {
		t.Fatalf("enable twice:\n%s", out)
	}
}

package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/golovanov-dev/alertloop/internal/storage"
)

// runArgs runs the command as if invoked with args.
func runArgs(t *testing.T, args ...string) error {
	t.Helper()
	orig := os.Args
	os.Args = append([]string{"alertloop"}, args...)
	defer func() { os.Args = orig }()
	return run()
}

// writeConfig writes a minimal config for a SQLite database at dbPath that
// also asks for a log file at logPath.
func writeConfig(t *testing.T, dir, dbPath, logPath string) string {
	t.Helper()
	cfg := "admin_token: a-real-token\n" +
		"database:\n  driver: sqlite\n  dsn: '" + filepath.ToSlash(dbPath) + "'\n" +
		"log:\n  file: '" + filepath.ToSlash(logPath) + "'\n"
	path := filepath.Join(dir, "alertloop.yaml")
	if err := os.WriteFile(path, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// check-db is the worker container's health check: Docker runs it every ten
// seconds next to the real worker, from the same config. It must answer about
// the database and touch nothing the worker owns — in particular not the
// worker's log file, which it would otherwise append to.
func TestCheckDBReportsAReachableDatabaseAndWritesNoLog(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "alertloop.db")
	logPath := filepath.Join(dir, "logs", "worker.log")

	s, err := storage.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	s.Close()

	cfgPath := writeConfig(t, dir, dbPath, logPath)
	var runErr error
	out := captureStdout(t, func() { runErr = runArgs(t, "--config", cfgPath, "check-db") })
	if runErr != nil {
		t.Fatalf("check-db failed on a reachable database: %v", runErr)
	}
	if !strings.Contains(out, "ok") {
		t.Fatalf("check-db printed %q; a person running it by hand should see it passed", out)
	}
	// And which build answered: the same command is the pre-flight check of an
	// upgrade, and run before the new image is in place it answers "ok" about a
	// config the new version may refuse.
	if !strings.Contains(out, version) {
		t.Errorf("check-db does not name the version that answered: %q", out)
	}
	if _, err := os.Stat(logPath); !os.IsNotExist(err) {
		t.Fatalf("check-db created or opened the log file %s (stat err %v)", logPath, err)
	}
}

func TestCheckDBFailsWhenTheDatabaseIsMissing(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "missing.db")
	cfgPath := writeConfig(t, dir, dbPath, filepath.Join(dir, "worker.log"))

	if err := runArgs(t, "--config", cfgPath, "check-db"); err == nil {
		t.Fatal("check-db passed with no database behind it")
	}
	if _, err := os.Stat(dbPath); !os.IsNotExist(err) {
		t.Fatal("check-db created the database it was asked to check")
	}
}

// check-db passes only a config the start of `server` or `all` would accept:
// here, one with no credential, and one whose log file cannot be opened.
func TestCheckDBRefusesWhatStartWouldRefuse(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "alertloop.db")
	s, err := storage.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	s.Close()

	noCredential := filepath.Join(dir, "no-credential.yaml")
	if err := os.WriteFile(noCredential, []byte("database:\n  dsn: '"+filepath.ToSlash(dbPath)+"'\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := runArgs(t, "--config", noCredential, "check-db"); err == nil || !strings.Contains(err.Error(), "admin_token") {
		t.Errorf("check-db with no credential: err = %v, want the refusal server and all give", err)
	}

	// A directory where the log file should be: it cannot be opened for writing.
	if err := runArgs(t, "--config", writeConfig(t, dir, dbPath, dir), "check-db"); err == nil || !strings.Contains(err.Error(), "log file") {
		t.Errorf("check-db with an unwritable log file: err = %v, want one naming the log file", err)
	}
}

// A mistyped mode is refused before the config is read, so it migrates
// nothing: check_db for check-db must not upgrade a production database.
func TestAnUnknownModeTouchesNoDatabase(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "alertloop.db")
	cfgPath := writeConfig(t, dir, dbPath, filepath.Join(dir, "alertloop.log"))

	if err := runArgs(t, "--config", cfgPath, "check_db"); err == nil || !strings.Contains(err.Error(), "unknown mode") {
		t.Fatalf("err = %v, want an unknown-mode refusal", err)
	}
	if _, err := os.Stat(dbPath); !os.IsNotExist(err) {
		t.Fatalf("an unknown mode created or opened the database (stat err %v)", err)
	}
}

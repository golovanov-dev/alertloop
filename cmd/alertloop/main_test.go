package main

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/golovanov-dev/alertloop/internal/config"
)

// captureStdout swaps os.Stdout for a pipe and returns what was written to it
// while fn ran. setupLogger reads os.Stdout when it is called, so the swap has
// to be in place before it runs.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	orig := os.Stdout
	os.Stdout = w
	defer func() { os.Stdout = orig }()

	done := make(chan string, 1)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()

	fn()
	w.Close()
	out := <-done
	r.Close()
	return out
}

// The 2026-09-09 defect, as a test: before this, configuring log.file redirected
// output to the file INSTEAD of stdout, which silenced `docker compose logs` and
// every log shipper reading the container's output. Both must receive the line.
func TestSetupLoggerWritesToBothStdoutAndFile(t *testing.T) {
	// A nested path on purpose: the directory must be created, not required.
	path := filepath.Join(t.TempDir(), "var", "log", "alertloop", "alertloop.log")

	out := captureStdout(t, func() {
		log, closer, err := setupLogger(config.Logging{Level: "info", Format: "text", File: path})
		if err != nil {
			t.Fatalf("setupLogger: %v", err)
		}
		log.Info("delivery failed", "channel", "telegram")
		if closer == nil {
			t.Fatal("a configured log file returned no closer; the file is never released")
		}
		if err := closer.Close(); err != nil {
			t.Fatalf("close: %v", err)
		}
	})

	if !strings.Contains(out, "delivery failed") {
		t.Fatalf("stdout got %q; configuring a log file must not silence stdout", out)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read log file: %v", err)
	}
	if !strings.Contains(string(b), "delivery failed") {
		t.Fatalf("log file got %q", b)
	}
}

func TestSetupLoggerWithoutAFileWritesToStdoutOnly(t *testing.T) {
	var closer io.Closer
	out := captureStdout(t, func() {
		log, c, err := setupLogger(config.Logging{Level: "info", Format: "text"})
		if err != nil {
			t.Fatalf("setupLogger: %v", err)
		}
		closer = c
		log.Info("starting alertloop")
	})
	if closer != nil {
		t.Fatal("no file was configured, yet a closer was returned")
	}
	if !strings.Contains(out, "starting alertloop") {
		t.Fatalf("stdout got %q", out)
	}
}

// logrotate's copytruncate copies the file and truncates it in place while
// AlertLoop keeps it open. The next line must start the truncated file, not
// land at the old offset behind a run of zero bytes.
func TestTheLogFileSurvivesCopytruncate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "alertloop.log")

	captureStdout(t, func() {
		log, closer, err := setupLogger(config.Logging{Level: "info", Format: "text", File: path})
		if err != nil {
			t.Fatalf("setupLogger: %v", err)
		}
		defer closer.Close()
		log.Info("before rotation")
		if err := os.Truncate(path, 0); err != nil {
			t.Fatalf("truncate: %v", err)
		}
		log.Info("after rotation")
	})

	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(b), "time=") || !strings.Contains(string(b), "after rotation") ||
		strings.Contains(string(b), "before rotation") {
		t.Fatalf("after truncation the file holds %q", b)
	}
}

// A log file that cannot be opened stops startup instead of running blind. The
// message names the path, which is the whole diagnosis when a bind-mounted
// directory belongs to root and the container runs as uid 10001.
func TestSetupLoggerFailsWhenTheFileCannotBeOpened(t *testing.T) {
	dir := t.TempDir()
	blocker := filepath.Join(dir, "alertloop.log")
	if err := os.Mkdir(blocker, 0o750); err != nil { // a directory where the file should be
		t.Fatal(err)
	}

	_, _, err := setupLogger(config.Logging{Level: "info", Format: "text", File: blocker})
	if err == nil {
		t.Fatal("an unusable log path was accepted")
	}
	if !strings.Contains(err.Error(), blocker) {
		t.Fatalf("error does not name the path: %v", err)
	}
}

func TestSetupLoggerHonoursFormatAndLevel(t *testing.T) {
	out := captureStdout(t, func() {
		log, _, err := setupLogger(config.Logging{Level: "warn", Format: "json"})
		if err != nil {
			t.Fatalf("setupLogger: %v", err)
		}
		log.Info("should be filtered out")
		log.Warn("kept")
	})

	if strings.Contains(out, "should be filtered out") {
		t.Fatalf("level=warn did not filter info records: %q", out)
	}
	var rec map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &rec); err != nil {
		t.Fatalf("format=json did not produce JSON (%q): %v", out, err)
	}
	if rec["msg"] != "kept" {
		t.Fatalf("record = %v", rec)
	}
	// Timestamps are UTC on purpose; stored event times are UTC too.
	if ts, _ := rec["time"].(string); !strings.HasSuffix(ts, "Z") {
		t.Fatalf("time %q is not UTC", ts)
	}
}

// A log file that does not exist yet passes when its directory can be created,
// and the check leaves nothing behind: neither the file nor its probe.
func TestCheckLogFileMissingFileLeavesNothing(t *testing.T) {
	dir := t.TempDir()
	if err := checkLogFile(filepath.Join(dir, "logs", "alertloop.log")); err != nil {
		t.Fatalf("checkLogFile: %v", err)
	}
	if left, _ := os.ReadDir(dir); len(left) != 0 {
		t.Fatalf("the check left %d entries in %s", len(left), dir)
	}
}

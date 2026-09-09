package logging

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// A log path whose directory does not exist is the common first attempt at
// configuring log.file. It must work rather than fail at startup.
func TestOpenCreatesTheDirectory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "deeper", "alertloop.log")

	f, err := Open(path, 1, 1)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer f.Close()

	if _, err := f.Write([]byte("hello\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(b) != "hello\n" {
		t.Fatalf("file contains %q", b)
	}
}

func TestOpenAppendsToAnExistingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "alertloop.log")
	if err := os.WriteFile(path, []byte("earlier run\n"), 0o640); err != nil {
		t.Fatal(err)
	}

	f, err := Open(path, 0, 0)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer f.Close()
	if _, err := f.Write([]byte("this run\n")); err != nil {
		t.Fatalf("write: %v", err)
	}

	b, _ := os.ReadFile(path)
	if string(b) != "earlier run\nthis run\n" {
		t.Fatalf("a restart truncated the log: %q", b)
	}
}

// The point of built-in rotation: the file stops growing, and what was in it is
// still there under a numbered name.
func TestRotatesAtTheSizeLimit(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "alertloop.log")

	f, err := Open(path, 1, 3) // 1 MB
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer f.Close()

	line := []byte(strings.Repeat("x", 1024) + "\n")
	for i := 0; i < 1200; i++ { // ~1.2 MB, so exactly one rotation
		if _, err := f.Write(line); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
	}

	st, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat active file: %v", err)
	}
	if st.Size() >= 1<<20 {
		t.Fatalf("active log is %d bytes; it was never rotated", st.Size())
	}
	rotated, err := os.Stat(path + ".1")
	if err != nil {
		t.Fatalf("no rotated file: %v", err)
	}
	if rotated.Size() == 0 {
		t.Fatal("the rotated file is empty; the log was lost rather than rotated")
	}
	if _, err := os.Stat(path + ".2"); !os.IsNotExist(err) {
		t.Fatal("rotated twice where once was due")
	}
}

// max_files bounds disk use: max_size_mb * (max_files + 1) and nothing beyond.
func TestKeepsAtMostMaxFilesGenerations(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "alertloop.log")

	f, err := Open(path, 1, 2)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer f.Close()

	line := []byte(strings.Repeat("x", 1024) + "\n")
	for i := 0; i < 5000; i++ { // ~5 MB: four rotations, two survivors
		if _, err := f.Write(line); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
	}

	for _, name := range []string{"alertloop.log", "alertloop.log.1", "alertloop.log.2"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatalf("expected %s to exist: %v", name, err)
		}
	}
	if _, err := os.Stat(path + ".3"); !os.IsNotExist(err) {
		t.Fatal("a third generation was kept; max_files does not bound disk use")
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("log directory holds %v, want exactly the active file and two rotations", names)
	}
}

// The newest rotation is always .1: an operator reading the directory must not
// have to work out which end is recent.
func TestRotationShiftsGenerationsUp(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "alertloop.log")

	f, err := Open(path, 1, 3)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer f.Close()

	// Each round writes a marker and then enough filler to force a rotation.
	filler := []byte(strings.Repeat("x", 1024) + "\n")
	for round := 1; round <= 3; round++ {
		if _, err := fmt.Fprintf(f, "round-%d\n", round); err != nil {
			t.Fatalf("write marker: %v", err)
		}
		for i := 0; i < 1024; i++ {
			if _, err := f.Write(filler); err != nil {
				t.Fatalf("write filler: %v", err)
			}
		}
	}

	// Round 3 is the most recent completed file, so it must be in .1.
	for gen, round := range map[string]int{".1": 3, ".2": 2, ".3": 1} {
		b, err := os.ReadFile(path + gen)
		if err != nil {
			t.Fatalf("read %s: %v", gen, err)
		}
		want := fmt.Sprintf("round-%d\n", round)
		if !strings.Contains(string(b), want) {
			t.Fatalf("%s does not contain %q; generations are not shifted newest-first", gen, want)
		}
	}
}

// Zero means "somebody else rotates this" — logrotate, or a shipping agent.
func TestZeroMaxSizeDisablesRotation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "alertloop.log")

	f, err := Open(path, 0, 3)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer f.Close()

	line := []byte(strings.Repeat("x", 1024) + "\n")
	for i := 0; i < 3000; i++ {
		if _, err := f.Write(line); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	if _, err := os.Stat(path + ".1"); !os.IsNotExist(err) {
		t.Fatal("rotated despite max_size_mb: 0")
	}
	st, _ := os.Stat(path)
	if st.Size() < 3000*1025 {
		t.Fatalf("active log is %d bytes; something rotated or truncated it", st.Size())
	}
}

func TestZeroMaxFilesKeepsNoHistory(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "alertloop.log")

	f, err := Open(path, 1, 0)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer f.Close()

	line := []byte(strings.Repeat("x", 1024) + "\n")
	for i := 0; i < 1200; i++ {
		if _, err := f.Write(line); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("%d files in the log directory, want only the active one", len(entries))
	}
}

// A record larger than the limit is written whole rather than split across two
// files: half a log line in each of two files is not a log.
func TestARecordLargerThanTheLimitIsNotSplit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "alertloop.log")

	f, err := Open(path, 1, 2)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer f.Close()

	big := []byte(strings.Repeat("y", 2<<20) + "\n")
	if _, err := f.Write([]byte("first\n")); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write(big); err != nil {
		t.Fatalf("write oversized record: %v", err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(b) != len(big) {
		t.Fatalf("active file holds %d bytes, want the whole %d-byte record", len(b), len(big))
	}
}

// slog handlers write from whatever goroutine logs, so a rotation must never
// interleave with a write.
//
// Without the race detector this catches what a broken lock actually does here
// — a write to a nil file handle mid-rotation, a torn size counter, a panic —
// but not a benign-looking data race. CI runs `go test ./...` without `-race`
// (see .github/workflows/ci.yml), and the detector needs cgo, which the
// CGO_ENABLED=0 build does not have. Enabling it is a CI decision, not
// something this test can make: run `go test -race ./internal/logging/` by hand
// on a machine with a C toolchain when touching the locking here.
func TestConcurrentWritesAreSerialized(t *testing.T) {
	path := filepath.Join(t.TempDir(), "alertloop.log")

	f, err := Open(path, 1, 2)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer f.Close()

	var wg sync.WaitGroup
	line := []byte(strings.Repeat("z", 512) + "\n")
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 500; i++ {
				if _, err := f.Write(line); err != nil {
					t.Errorf("write: %v", err)
					return
				}
			}
		}()
	}
	wg.Wait()
}

func TestCloseIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "alertloop.log")
	f, err := Open(path, 1, 1)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("first close: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("second close: %v", err)
	}
}

// A rotation that cannot happen must not end logging. slog throws away whatever
// a writer returns, so a Write that fails here is invisible: the file would go
// quiet with no explanation until somebody restarted the process. Instead the
// reason goes to stderr once and the lines keep landing in the file.
func TestFailedRotationKeepsWritingAndReportsOnce(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "alertloop.log")

	// A non-empty DIRECTORY where the first generation belongs: both the remove
	// and the rename of ".1" fail, on every platform.
	blocker := path + ".1"
	if err := os.Mkdir(blocker, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(blocker, "keep"), []byte("x"), 0o640); err != nil {
		t.Fatal(err)
	}

	f, err := Open(path, 1, 1)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer f.Close()
	var stderr bytes.Buffer
	f.errOut = &stderr

	line := []byte(strings.Repeat("x", 1024) + "\n")
	written := 0
	for i := 0; i < 4000; i++ { // ~4 MB against a 1 MB limit: several rotation attempts
		n, err := f.Write(line)
		if err != nil {
			t.Fatalf("write %d failed after a broken rotation: %v", i, err)
		}
		written += n
	}

	st, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat the active file: %v", err)
	}
	if st.Size() != int64(written) {
		t.Fatalf("file holds %d bytes of the %d written; lines were lost", st.Size(), written)
	}

	msg := stderr.String()
	if !strings.Contains(msg, path) {
		t.Fatalf("stderr does not name the log file: %q", msg)
	}
	if got := strings.Count(msg, "cannot rotate"); got != 1 {
		t.Fatalf("the failure was reported %d times, want exactly 1:\n%s", got, msg)
	}
}

// Rotation is retried at most once per max_size_mb after a failure, not once
// per line: a close-and-reopen on every log record would be its own outage.
func TestFailedRotationBacksOff(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "alertloop.log")
	blocker := path + ".1"
	if err := os.Mkdir(blocker, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(blocker, "keep"), []byte("x"), 0o640); err != nil {
		t.Fatal(err)
	}

	f, err := Open(path, 1, 1)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer f.Close()
	f.errOut = io.Discard

	line := []byte(strings.Repeat("x", 1024) + "\n")
	for i := 0; i < 1100; i++ { // just past the 1 MB limit: one failed attempt
		if _, err := f.Write(line); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	if f.retryAt == 0 {
		t.Fatal("no back-off was armed after a failed rotation")
	}
	first := f.retryAt

	for i := 0; i < 100; i++ { // still inside the same segment
		if _, err := f.Write(line); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	if f.retryAt != first {
		t.Fatalf("rotation was retried inside the back-off window (%d -> %d)", first, f.retryAt)
	}
}

// The obstruction goes away; rotation must start working again on its own, and
// a later failure must be reported again rather than swallowed as "already
// said that".
func TestRotationRecoversAfterTheObstructionIsRemoved(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "alertloop.log")
	blocker := path + ".1"
	if err := os.Mkdir(blocker, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(blocker, "keep"), []byte("x"), 0o640); err != nil {
		t.Fatal(err)
	}

	f, err := Open(path, 1, 1)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer f.Close()
	var stderr bytes.Buffer
	f.errOut = &stderr

	line := []byte(strings.Repeat("x", 1024) + "\n")
	for i := 0; i < 1100; i++ {
		if _, err := f.Write(line); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	if strings.Count(stderr.String(), "cannot rotate") != 1 {
		t.Fatalf("expected one report while blocked:\n%s", stderr.String())
	}

	if err := os.RemoveAll(blocker); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 1100; i++ { // enough to reach the back-off point and rotate
		if _, err := f.Write(line); err != nil {
			t.Fatalf("write after unblocking: %v", err)
		}
	}
	if st, err := os.Stat(blocker); err != nil || st.IsDir() {
		t.Fatalf("rotation did not resume once the path was free (err=%v)", err)
	}
	if f.reported {
		t.Fatal("a successful rotation did not clear the reported flag; a later failure would stay silent")
	}
}

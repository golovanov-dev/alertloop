// Package logging provides the log file AlertLoop writes to when `log.file` is
// configured: it creates the directory it lives in and rotates it by size.
//
// Rotation is built in rather than left to logrotate because `log.file` is used
// in places where no log rotator exists — a container with a bind-mounted log
// directory, or a host where nobody thought to add a logrotate snippet. An
// alerting service that fills the disk with its own log stops being able to
// store the events it exists to store, and OPERATIONS.md already carries a
// runbook for exactly that.
package logging

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
)

// dirPerm/filePerm are the modes REQUESTED for the directory and the file;
// neither is world-readable, because logs carry event messages. The umask of
// the process narrows them further and is not fought: under the shipped systemd
// unit (`UMask=0077`) the file ends up 0600, owned by the service user, which is
// the intended outcome of that hardening. Reading the file may therefore need
// sudo — OPERATIONS.md says so.
const (
	dirPerm  os.FileMode = 0o750
	filePerm os.FileMode = 0o640
)

// File is an io.WriteCloser writing to a log file, rotating it once it grows
// past maxBytes. Rotation renames the active file to "<name>.1", shifting any
// existing "<name>.N" up by one and dropping what falls past maxFiles.
//
// Writes are serialized: slog handlers may be called from any goroutine, and a
// rotation must not interleave with a write.
//
// One process per file. Two processes appending to the same path both survive
// (POSIX append writes do not interleave), but each keeps its own size counter
// and would rotate the other's file out from under it — which is why the
// Compose file gives the api and the worker separate paths.
type File struct {
	mu       sync.Mutex
	path     string
	maxBytes int64
	maxFiles int
	f        *os.File
	size     int64

	// retryAt gates rotation retries after a failure: rotating again is not
	// attempted until the file has grown by another maxBytes. Without it a
	// permanently unrotatable path (a directory sitting where <file>.1 belongs,
	// a full disk) would mean a close-and-reopen attempt on every single log
	// line.
	retryAt int64
	// errOut receives the one-line report about a failed rotation; tests
	// replace it. reported keeps that report to one per failure, cleared by the
	// next rotation that works, so a problem that comes back is reported again.
	errOut   io.Writer
	reported bool
}

// Open opens (creating it if needed) the log file at path, creating its
// directory as well, and returns a writer that rotates it by size.
//
// maxSizeMB is the size at which the file is rotated; 0 disables rotation, for
// installations that rotate externally (logrotate, a shipping agent). maxFiles
// is how many rotated files are kept besides the active one; 0 keeps none, so
// the current file is simply started over.
func Open(path string, maxSizeMB, maxFiles int) (*File, error) {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		// Create the directory rather than fail: `log.file` naming a path whose
		// parent does not exist is the common first attempt, and refusing it
		// only teaches the operator to run one mkdir. A directory that cannot
		// be created (a read-only filesystem, a bind mount owned by root) still
		// fails loudly here, at startup, which is the failure worth keeping.
		if err := os.MkdirAll(dir, dirPerm); err != nil {
			return nil, fmt.Errorf("create log directory %q: %w", dir, err)
		}
	}
	f := &File{
		path:     path,
		maxBytes: int64(maxSizeMB) * 1024 * 1024,
		maxFiles: maxFiles,
	}
	if err := f.open(); err != nil {
		return nil, err
	}
	return f, nil
}

// open opens the active file for appending and records its current size, so a
// restart does not forget how large the file already is.
func (w *File) open() error {
	f, err := os.OpenFile(w.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, filePerm)
	if err != nil {
		return fmt.Errorf("open log file %q: %w", w.path, err)
	}
	size := int64(0)
	if st, err := f.Stat(); err == nil {
		size = st.Size()
	}
	w.f = f
	w.size = size
	return nil
}

// Write appends p, rotating first if p would push the file past its size limit.
// A single record larger than the limit is written whole: a log line split
// across two files is worse than one file briefly over its size.
//
// A rotation that fails must never end logging. slog discards whatever a
// handler's writer returns, so an error here is invisible: the file would
// simply go quiet, with no line anywhere saying why, until somebody restarted
// the process. Instead the failure is reported once on stderr — which is
// visible in `docker compose logs` and in the journal — and writing continues
// to the file that is open. The size limit is the thing given up, not the log.
func (w *File) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.maxBytes > 0 && w.size > 0 && w.size+int64(len(p)) > w.maxBytes && w.size >= w.retryAt {
		if err := w.rotate(); err != nil {
			w.report(err)
			// Try again only after another full segment, so an obstruction that
			// is never cleared costs one attempt per maxBytes rather than one
			// per line. It self-heals: the next attempt succeeds once the
			// obstruction is gone.
			w.retryAt = w.size + w.maxBytes
		} else {
			w.retryAt = 0
			w.reported = false
		}
	}
	if w.f == nil {
		// Rotation could not reopen anything (a directory replaced the path, a
		// full disk). Keep trying, quietly: the write is what matters, and the
		// reason was already reported.
		if err := w.open(); err != nil {
			return 0, err
		}
	}
	n, err := w.f.Write(p)
	w.size += int64(n)
	return n, err
}

// report prints one line to stderr about a rotation that failed, at most once
// until a rotation succeeds again.
func (w *File) report(err error) {
	if w.reported {
		return
	}
	w.reported = true
	out := w.errOut
	if out == nil {
		out = os.Stderr
	}
	// Ignoring the result on purpose: this IS the error path. If stderr cannot
	// be written either, there is nowhere left to say so.
	_, _ = fmt.Fprintf(out, "alertloop: cannot rotate log file %s: %v; still writing to it, so it may now grow past log.max_size_mb\n", w.path, err)
}

// Close releases the file.
func (w *File) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.f == nil {
		return nil
	}
	err := w.f.Close()
	w.f = nil
	return err
}

// rotate closes the active file, shifts the numbered generations up by one, and
// opens a fresh file. The caller holds the lock.
//
// It ALWAYS reopens, whatever went wrong in between: if the rename never
// happened, the reopened path is the same file with its history intact, and
// logging carries on. Returning early with the file closed is what would turn a
// failed rename into a silently dead log.
func (w *File) rotate() error {
	closeErr := w.f.Close()
	w.f = nil

	shiftErr := w.shift()

	openErr := w.open()
	return errors.Join(closeErr, shiftErr, openErr)
}

// shift moves the numbered generations up and clears the way for a fresh active
// file. The active file is already closed when it runs.
func (w *File) shift() error {
	if w.maxFiles <= 0 {
		// Keep no history: start the current file over. Remove rather than
		// truncate so a reader holding the old inode is not surprised mid-line.
		if err := os.Remove(w.path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("rotate log file: %w", err)
		}
		return nil
	}

	// Drop the oldest, then shift every generation up. Renaming from the oldest
	// down means each destination is free by the time it is used.
	oldest := fmt.Sprintf("%s.%d", w.path, w.maxFiles)
	if err := os.Remove(oldest); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("rotate log file: %w", err)
	}
	for i := w.maxFiles - 1; i >= 1; i-- {
		from := fmt.Sprintf("%s.%d", w.path, i)
		to := fmt.Sprintf("%s.%d", w.path, i+1)
		if err := os.Rename(from, to); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("rotate log file: %w", err)
		}
	}
	if err := os.Rename(w.path, w.path+".1"); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("rotate log file: %w", err)
	}
	return nil
}

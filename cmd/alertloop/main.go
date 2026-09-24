// Command alertloop is the single AlertLoop binary. The subcommand selects the
// mode (all by default); app.Modes is the list of them.
//
// Configuration comes from one YAML file (--config, or ALERTLOOP_CONFIG) on top
// of built-in defaults. The environment only fills ${VAR} references inside that
// file, so there is no precedence puzzle: the file is what runs.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/golovanov-dev/alertloop/internal/app"
	"github.com/golovanov-dev/alertloop/internal/config"
)

// version is overridden at build time via -ldflags "-X main.version=...".
var version = "dev"

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run() error {
	fs := flag.NewFlagSet("alertloop", flag.ContinueOnError)
	var (
		configPath  = fs.String("config", os.Getenv("ALERTLOOP_CONFIG"), "path to the YAML config file")
		showVersion = fs.Bool("version", false, "print version and exit")
	)
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "AlertLoop %s\n\nUsage: alertloop [flags] [%s]\n\n"+
			"  check-db  check the config, the log file and the database as a start would, change nothing,\n"+
			"            exit 0 if all pass (a health check and the pre-flight check of an upgrade)\n"+
			"  user      manage console users: user add|passwd [--password-stdin] <login>,\n"+
			"            user disable <login>, user list\n\nFlags:\n",
			version, strings.Join(app.Modes, "|"))
		fs.PrintDefaults()
	}
	if err := fs.Parse(os.Args[1:]); err != nil {
		return err
	}
	if *showVersion {
		fmt.Println("alertloop", version)
		return nil
	}

	mode := app.ModeAll
	if fs.NArg() > 0 {
		mode = fs.Arg(0)
	}
	// Before the config is loaded: every mode but check-db migrates the
	// database, and a mistyped check-db must not.
	if !slices.Contains(app.Modes, mode) {
		return fmt.Errorf("unknown mode %q (want %s)", mode, strings.Join(app.Modes, ", "))
	}
	var userCmd userCommand
	if mode == app.ModeUser {
		var err error
		if userCmd, err = parseUserArgs(fs.Args()[1:]); err != nil {
			return err
		}
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}

	// Before storage, the logger, or anything else is opened: a process that
	// would serve HTTP with no credential configured is refused. Which modes
	// serve is app's to know, not this switch's.
	if err := app.RequireCredential(cfg, mode); err != nil {
		return err
	}

	// Before the logger: a health check runs every few seconds next to the
	// real process, and must neither append to that process's log file nor run
	// migrations under it.
	if mode == app.ModeCheckDB {
		return checkDB(cfg)
	}
	// Before the logger, like check-db: an account command run next to the
	// real process must not write to its log file.
	if mode == app.ModeUser {
		return runUser(context.Background(), cfg, userCmd, os.Stdin, os.Stdout)
	}

	log, logCloser, err := setupLogger(cfg.Log)
	if err != nil {
		return err
	}
	if logCloser != nil {
		defer logCloser.Close()
	}
	slog.SetDefault(log)
	for _, w := range app.WeakCredentialWarnings(cfg, mode) {
		log.Warn(w)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	a, err := app.New(ctx, cfg, version, log)
	if err != nil {
		return err
	}
	defer a.Close()

	log.Info("starting alertloop", "mode", mode, "version", version)

	switch mode {
	case app.ModeServer:
		return a.RunServer(ctx)
	case app.ModeWorker:
		return a.RunWorker(ctx)
	case app.ModeAll:
		return a.RunAll(ctx)
	default: // a mode added to app.Modes without a runner here
		return fmt.Errorf("mode %q has no runner", mode)
	}
}

// checkDBTimeout bounds check-db below the 5s timeout the Compose health check
// gives it, so a slow database is reported by AlertLoop's own error rather than
// by Docker killing the process.
const checkDBTimeout = 4 * time.Second

// checkDB is `alertloop check-db`: refuse what a start of `server` or `all`
// would refuse, reach the database, report. It is the worker's health check,
// since the worker has no HTTP listener to probe, and the pre-flight check of
// an upgrade. It migrates nothing, changes nothing in the database, and writes
// nothing to the log file.
func checkDB(cfg config.Config) error {
	ctx, cancel := context.WithTimeout(context.Background(), checkDBTimeout)
	defer cancel()
	if err := app.CheckDatabase(ctx, cfg); err != nil {
		return err
	}
	if err := checkLogFile(cfg.Log.File); err != nil {
		return err
	}
	// The version, because this is also the pre-flight check of an upgrade: run
	// from a directory still pointing at the old image, it answers "ok" about a
	// config the new version would refuse, and nothing in the output said which
	// version had answered.
	fmt.Printf("ok: alertloop %s, the database answered\n", version)
	return nil
}

// setupLogger builds the application logger from config: log level, output
// format (text or json), and an optional log file. Timestamps are always UTC,
// matching stored event times.
//
// A configured file is written IN ADDITION to stdout, never instead of it.
// Earlier versions let a file replace stdout, which under Docker silenced
// `docker compose logs` and every shipper reading the container's output — so
// the setting meant to make logs easier to read made them harder. The file is
// created with its directory and opened for appending; see openLogFile.
//
// The returned io.Closer, when non-nil, must be closed on shutdown to release
// the log file.
func setupLogger(c config.Logging) (*slog.Logger, io.Closer, error) {
	level, err := c.SlogLevel()
	if err != nil {
		return nil, nil, err
	}
	asJSON, err := c.JSON()
	if err != nil {
		return nil, nil, err
	}

	var out io.Writer = os.Stdout
	var closer io.Closer
	if c.File != "" {
		f, err := openLogFile(c.File)
		if err != nil {
			return nil, nil, err
		}
		out = io.MultiWriter(os.Stdout, f)
		closer = f
	}

	opts := &slog.HandlerOptions{
		Level: level,
		// Log in UTC, always. Event timestamps are stored and served in UTC, and
		// a log line that reads in the server's local zone cannot be compared
		// with them without arithmetic. It is deliberately not configurable:
		// which zone a log line is in should not depend on how the process was
		// deployed (a container has no TZ, a systemd host usually does).
		ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
			if len(groups) == 0 && a.Key == slog.TimeKey {
				a.Value = slog.TimeValue(a.Value.Time().UTC())
			}
			return a
		},
	}
	var handler slog.Handler
	if asJSON {
		handler = slog.NewJSONHandler(out, opts)
	} else {
		handler = slog.NewTextHandler(out, opts)
	}
	return slog.New(handler), closer, nil
}

// openLogFile opens the log file for appending, creating it and its directory
// if missing. Neither is world-readable: log lines carry event messages.
//
// O_APPEND is what keeps logrotate's copytruncate safe: after the file is
// truncated, the next write lands at its new end rather than at the old offset
// behind a run of zero bytes.
func openLogFile(path string) (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return nil, fmt.Errorf("create log directory for %q: %w", path, err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o640)
	if err != nil {
		return nil, fmt.Errorf("open log file %q: %w", path, err)
	}
	return f, nil
}

// checkLogFile reports whether the process could open log.file for appending,
// without creating it or writing to it: an existing file is opened and closed,
// for a missing one the nearest existing directory must accept a new file.
func checkLogFile(path string) error {
	if path == "" {
		return nil
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
	if err == nil {
		return f.Close()
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("open log file %q: %w", path, err)
	}
	dir := filepath.Dir(path)
	for {
		if _, err := os.Stat(dir); err == nil {
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	probe, err := os.CreateTemp(dir, ".alertloop-check-*")
	if err != nil {
		return fmt.Errorf("log file %q cannot be created: %w", path, err)
	}
	_ = probe.Close()
	return os.Remove(probe.Name())
}

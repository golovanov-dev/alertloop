// Command alertloop is the single AlertLoop binary. It supports three runtime
// modes selected by subcommand: `server` (HTTP API and web UI), `worker`
// (delivery workers only), and `all` (both in one process, the default) — plus
// `check-db`, a one-shot check that the configured database answers, which is
// the health check of a container running `worker`.
//
// Usage:
//
//	alertloop [flags] [server|worker|all|check-db]
//
// Configuration comes from one YAML file (--config, or ALERTLOOP_CONFIG) on top
// of built-in defaults. The environment only fills ${VAR} references inside that
// file, so there is no precedence puzzle: the file is what runs.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/golovanov-dev/alertloop/internal/app"
	"github.com/golovanov-dev/alertloop/internal/config"
	"github.com/golovanov-dev/alertloop/internal/logging"
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
		fmt.Fprintf(os.Stderr, "AlertLoop %s\n\nUsage: alertloop [flags] [server|worker|all|check-db]\n\n"+
			"  check-db  load the config, ping the database, exit 0 if it answered (a health check)\n\nFlags:\n", version)
		fs.PrintDefaults()
	}
	if err := fs.Parse(os.Args[1:]); err != nil {
		return err
	}
	if *showVersion {
		fmt.Println("alertloop", version)
		return nil
	}

	mode := "all"
	if fs.NArg() > 0 {
		mode = fs.Arg(0)
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}

	// Before the logger: a health check runs every few seconds next to the
	// real process, and must neither append to nor rotate that process's log
	// file, nor run migrations under it.
	if mode == "check-db" {
		return checkDB(cfg)
	}

	log, logCloser, err := setupLogger(cfg.Log)
	if err != nil {
		return err
	}
	if logCloser != nil {
		defer logCloser.Close()
	}
	slog.SetDefault(log)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	a, err := app.New(ctx, cfg, version, log)
	if err != nil {
		return err
	}
	defer a.Close()

	log.Info("starting alertloop", "mode", mode, "version", version)

	switch mode {
	case "server":
		return a.RunServer(ctx)
	case "worker":
		return a.RunWorker(ctx)
	case "all":
		return a.RunAll(ctx)
	default:
		return fmt.Errorf("unknown mode %q (want server, worker, all, or check-db)", mode)
	}
}

// checkDBTimeout bounds check-db below the 5s timeout the Compose health check
// gives it, so a slow database is reported by AlertLoop's own error rather than
// by Docker killing the process.
const checkDBTimeout = 4 * time.Second

// checkDB is `alertloop check-db`: load the config as the worker would, reach
// the database, report. It exists because the worker has no HTTP listener for a
// health check to probe, and adding one only for that would be a second port to
// secure. It migrates nothing and changes nothing in the database.
func checkDB(cfg config.Config) error {
	ctx, cancel := context.WithTimeout(context.Background(), checkDBTimeout)
	defer cancel()
	if err := app.CheckDatabase(ctx, cfg); err != nil {
		return err
	}
	fmt.Println("ok: the database answered")
	return nil
}

// setupLogger builds the application logger from config: log level, output
// format (text or json), and an optional log file. Timestamps are always UTC,
// matching stored event times.
//
// A configured file is written IN ADDITION to stdout, never instead of it.
// Earlier versions let a file replace stdout, which under Docker silenced
// `docker compose logs` and every shipper reading the container's output — so
// the setting meant to make logs easier to read made them harder. The file
// itself is created (with its directory) and rotated by size; see
// internal/logging.
//
// The returned io.Closer, when non-nil, must be closed on shutdown to release
// the log file.
func setupLogger(c config.Logging) (*slog.Logger, io.Closer, error) {
	var level slog.Level
	switch strings.ToLower(c.Level) {
	case "debug":
		level = slog.LevelDebug
	case "warn", "warning":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	default:
		level = slog.LevelInfo
	}

	var out io.Writer = os.Stdout
	var closer io.Closer
	if c.File != "" {
		f, err := logging.Open(c.File, c.MaxSizeMB, c.MaxFiles)
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
	if strings.ToLower(c.Format) == "json" {
		handler = slog.NewJSONHandler(out, opts)
	} else {
		handler = slog.NewTextHandler(out, opts)
	}
	return slog.New(handler), closer, nil
}

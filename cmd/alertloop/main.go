// Command alertloop is the single AlertLoop binary. It supports three runtime
// modes selected by subcommand: `server` (HTTP API and web UI), `worker`
// (delivery workers only), and `all` (both in one process, the default).
//
// Usage:
//
//	alertloop [flags] [server|worker|all]
//
// Configuration precedence is flags > environment variables > YAML config file
// > built-in defaults.
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
		configPath  = fs.String("config", "", "path to YAML config file")
		addr        = fs.String("addr", "", "HTTP listen address (overrides config)")
		dbDSN       = fs.String("db-dsn", "", "database DSN (overrides config)")
		dbDriver    = fs.String("db-driver", "", "database driver: sqlite or postgres")
		showVersion = fs.Bool("version", false, "print version and exit")
	)
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "AlertLoop %s\n\nUsage: alertloop [flags] [server|worker|all]\n\nFlags:\n", version)
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
	// Flags have the highest precedence.
	if *addr != "" {
		cfg.Addr = *addr
	}
	if *dbDSN != "" {
		cfg.Database.DSN = *dbDSN
	}
	if *dbDriver != "" {
		cfg.Database.Driver = *dbDriver
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
		return fmt.Errorf("unknown mode %q (want server, worker, or all)", mode)
	}
}

// setupLogger builds the application logger from config: log level, output
// format (text or json), and an optional log file. When a file is configured,
// logs are appended so external tools (tail, log shippers, journald) can read
// them; otherwise output goes to stdout. The returned io.Closer, when non-nil,
// must be closed on shutdown to flush and release the log file.
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
		f, err := os.OpenFile(c.File, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o640)
		if err != nil {
			return nil, nil, fmt.Errorf("open log file %q: %w", c.File, err)
		}
		out = f
		closer = f
	}

	opts := &slog.HandlerOptions{Level: level}
	var handler slog.Handler
	if strings.ToLower(c.Format) == "json" {
		handler = slog.NewJSONHandler(out, opts)
	} else {
		handler = slog.NewTextHandler(out, opts)
	}
	return slog.New(handler), closer, nil
}

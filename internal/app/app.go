// Package app wires AlertLoop's configuration, storage, channels, services, and
// runtime modes (server, worker, all) into runnable processes.
package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/golovanov-dev/alertloop/internal/api"
	"github.com/golovanov-dev/alertloop/internal/channels"
	"github.com/golovanov-dev/alertloop/internal/config"
	"github.com/golovanov-dev/alertloop/internal/delivery"
	"github.com/golovanov-dev/alertloop/internal/domain"
	"github.com/golovanov-dev/alertloop/internal/service"
	"github.com/golovanov-dev/alertloop/internal/storage"
)

// App holds the shared, wired dependencies for a running AlertLoop process.
type App struct {
	cfg      config.Config
	version  string
	log      *slog.Logger
	store    storage.Store
	registry *channels.Registry
	ingest   *service.IngestService
	events   *service.EventService
	delivery *service.DeliveryService
}

// New builds an App: opens storage, runs migrations, and assembles channels and
// services from cfg.
func New(ctx context.Context, cfg config.Config, version string, log *slog.Logger) (*App, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	store, err := storage.Open(cfg.Database.Driver, cfg.Database.DSN)
	if err != nil {
		return nil, fmt.Errorf("open storage: %w", err)
	}
	if err := store.Migrate(ctx); err != nil {
		_ = store.Close()
		return nil, fmt.Errorf("run migrations: %w", err)
	}

	registry := buildRegistry(cfg.Channels)

	app := &App{
		cfg:      cfg,
		version:  version,
		log:      log,
		store:    store,
		registry: registry,
		ingest:   service.NewIngestService(store, registry.Targets(), cfg.Worker.MaxAttempts, time.Now),
		events:   service.NewEventService(store, time.Now),
		delivery: service.NewDeliveryService(store, time.Now),
	}

	log.Info("alertloop initialized",
		"driver", cfg.Database.Driver,
		"channels", cfg.EnabledChannels(),
	)
	if len(cfg.APIKeys) == 0 {
		log.Warn("no API keys configured — the JSON API is open to anyone who can reach it")
	}
	if registry.Len() == 0 {
		log.Warn("no delivery channels configured — events will be STORED BUT NOT DELIVERED. " +
			"Configure channels in your config file/env. In split server+worker deployments, " +
			"BOTH processes must load the SAME channel config (the server decides which delivery " +
			"jobs to create; the worker sends them).")
	}
	app.warnOrphanedDeliveries(ctx)
	return app, nil
}

// warnOrphanedDeliveries logs a warning if the store has undelivered attempts
// whose channel name is not in the current config (e.g. a channel was renamed
// or removed) — those attempts cannot be delivered and will dead-letter.
func (a *App) warnOrphanedDeliveries(ctx context.Context) {
	names, err := a.store.ActiveChannelNames(ctx)
	if err != nil {
		a.log.Warn("could not check for orphaned deliveries", "error", err)
		return
	}
	known := map[string]bool{}
	for _, t := range a.registry.Targets() {
		known[t.Name] = true
	}
	var orphaned []string
	for _, n := range names {
		if !known[n] {
			orphaned = append(orphaned, n)
		}
	}
	if len(orphaned) > 0 {
		a.log.Warn("undelivered attempts reference channels not in the current config; "+
			"they will dead-letter until the channels are restored",
			"orphaned_channels", orphaned)
	}
}

// Close releases the App's resources.
func (a *App) Close() error { return a.store.Close() }

// buildRegistry constructs the channel registry from the global channel config.
// Every configured channel of every type is registered under its unique name.
func buildRegistry(c config.Channels) *channels.Registry {
	var chans []channels.Channel
	for _, e := range c.Email {
		chans = append(chans, channels.NewEmail(channels.EmailConfig{
			Name:     e.Name,
			Host:     e.Host,
			Port:     e.Port,
			Username: e.Username,
			Password: e.Password,
			From:     e.From,
			To:       e.To,
			STARTTLS: e.STARTTLS,
			TLS:      e.TLS,
			Timeout:  e.Timeout,
		}))
	}
	for _, t := range c.Telegram {
		chans = append(chans, channels.NewTelegram(t.Name, t.BotToken, t.ChatID, t.APIBase, t.Timeout))
	}
	for _, w := range c.Webhook {
		chans = append(chans, channels.NewWebhook(w.Name, w.URL, w.Secret, w.Timeout))
	}
	return channels.NewRegistry(chans...)
}

// RunServer starts the HTTP server and blocks until ctx is cancelled.
func (a *App) RunServer(ctx context.Context) error {
	keyScopes := make(map[string]string, len(a.cfg.APIKeys))
	for _, k := range a.cfg.APIKeys {
		keyScopes[k.Key] = k.Scope
	}
	srv := api.NewServer(api.Config{
		Store:       a.store,
		Ingest:      a.ingest,
		Events:      a.events,
		Deliveries:  a.delivery,
		APIKeys:     keyScopes,
		AdminToken:  a.cfg.AdminToken,
		Version:     a.version,
		RateLimit:   a.cfg.RateLimit,
		CORSOrigins: a.cfg.CORSOrigins,
		Logger:      a.log,
	})
	httpSrv := &http.Server{
		Addr:              a.cfg.Addr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		// Bound slow clients (slowloris on the body) and hung connections.
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		a.log.Info("http server listening", "addr", a.cfg.Addr)
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return httpSrv.Shutdown(shutdownCtx)
	case err := <-errCh:
		return err
	}
}

// RunWorker starts the delivery worker (plus the retention cleanup loop, which
// is background maintenance owned by the worker role) and blocks until ctx is
// cancelled.
func (a *App) RunWorker(ctx context.Context) error {
	w := delivery.NewWorker(a.store, a.registry, delivery.Options{
		Concurrency:  a.cfg.Worker.Concurrency,
		PollInterval: a.cfg.Worker.PollInterval,
		BaseBackoff:  a.cfg.Worker.BaseBackoff,
		MaxBackoff:   a.cfg.Worker.MaxBackoff,
	}, a.log)
	go a.runRetention(ctx)
	w.Run(ctx)
	return nil
}

// RunAll runs the HTTP server and the worker (which includes retention cleanup)
// together in one process.
func (a *App) RunAll(ctx context.Context) error {
	errCh := make(chan error, 2)
	go func() { errCh <- a.RunServer(ctx) }()
	go func() { errCh <- a.RunWorker(ctx) }()
	return <-errCh
}

// runRetention periodically deletes events (and their delivery attempts) older
// than the fixed Community retention window.
func (a *App) runRetention(ctx context.Context) {
	if a.cfg.RetentionDays <= 0 {
		return
	}
	ticker := time.NewTicker(6 * time.Hour)
	defer ticker.Stop()
	for {
		a.cleanupOnce(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (a *App) cleanupOnce(ctx context.Context) {
	cutoff := time.Now().UTC().AddDate(0, 0, -a.cfg.RetentionDays)
	// Deleting events cascades to delivery_attempts via FK; also sweep any
	// orphaned attempts defensively.
	n, err := a.store.DeleteEventsBefore(ctx, cutoff)
	if err != nil {
		a.log.Error("retention cleanup failed", "error", err)
		return
	}
	if _, err := a.store.DeleteDeliveryAttemptsBefore(ctx, cutoff); err != nil {
		a.log.Error("retention cleanup (deliveries) failed", "error", err)
	}
	if n > 0 {
		a.log.Info("retention cleanup", "deleted_events", n, "cutoff", cutoff.Format(time.RFC3339))
	}
}

// ChannelTargets reports the configured channel instances wired into the App.
func (a *App) ChannelTargets() []domain.ChannelTarget { return a.registry.Targets() }

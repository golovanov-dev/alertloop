// Package app wires AlertLoop's configuration, storage, channels, services, and
// runtime modes (server, worker, all) into runnable processes.
package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/golovanov-dev/alertloop/internal/api"
	"github.com/golovanov-dev/alertloop/internal/channels"
	"github.com/golovanov-dev/alertloop/internal/config"
	"github.com/golovanov-dev/alertloop/internal/delivery"
	"github.com/golovanov-dev/alertloop/internal/domain"
	"github.com/golovanov-dev/alertloop/internal/routing"
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
	router   *routing.Router
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

	registry, err := buildRegistry(cfg.Channels)
	if err != nil {
		_ = store.Close()
		return nil, err
	}

	// nil when notify_on_resolve is off, which makes "do not notify" a nil
	// pointer rather than a flag every call site has to remember.
	recovery := service.NewRecoveryNotifier(store, cfg.ShouldNotifyOnResolve(), cfg.Worker.MaxAttempts, log)

	router, err := buildRouter(cfg.Routing, registry.Targets())
	if err != nil {
		_ = store.Close()
		return nil, err
	}

	app := &App{
		cfg:      cfg,
		version:  version,
		log:      log,
		store:    store,
		registry: registry,
		router:   router,
		ingest:   service.NewIngestService(store, router, cfg.Worker.MaxAttempts, recovery, time.Now, log),
		events:   service.NewEventService(store, recovery, time.Now),
		delivery: service.NewDeliveryService(store, time.Now),
	}

	log.Info("alertloop initialized",
		"driver", cfg.Database.Driver,
		"channels", cfg.EnabledChannels(),
	)
	for _, w := range cfg.Warnings {
		log.Warn(w)
	}
	// Both empty, not either: apiKeyAuth opens the API only when there is
	// neither a key nor an admin token. Warning about a configuration that has
	// an admin token - which is what the image ships and what the systemd
	// installer generates - trains operators to ignore the message, and then
	// they ignore the real one.
	if len(cfg.APIKeys) == 0 && cfg.AdminToken == "" {
		log.Warn("no API keys configured — the JSON API is open to anyone who can reach it")
	}
	if registry.Len() == 0 {
		log.Warn("no delivery channels configured — events will be STORED BUT NOT DELIVERED. " +
			"Configure channels in your config file. In split server+worker deployments, " +
			"BOTH processes must load the SAME channel config (the server decides which delivery " +
			"jobs to create; the worker sends them).")
	}
	app.logRoutingTable()
	app.warnOrphanedDeliveries(ctx)
	return app, nil
}

// CheckDatabase validates cfg and reaches its database, without migrating it.
// It is `alertloop check-db`, the health check of a worker container: the
// worker has no HTTP listener to probe. See storage.Check for what it will not
// do on the way.
func CheckDatabase(ctx context.Context, cfg config.Config) error {
	if err := cfg.Validate(); err != nil {
		return err
	}
	return storage.Check(ctx, cfg.Database.Driver, cfg.Database.DSN)
}

// buildRouter assembles the router from the optional routing section. A nil
// section means no routing is configured, which keeps the historical behavior:
// every event goes to every channel.
func buildRouter(cfg *config.Routing, targets []domain.ChannelTarget) (*routing.Router, error) {
	if cfg == nil {
		return routing.NewAllChannels(targets), nil
	}
	return routing.New(*cfg, targets)
}

// logRoutingTable prints the routing table that actually took effect, plus the
// two mistakes that are otherwise silent: rules made unreachable by an earlier
// catch-all, and configured channels nothing routes to. The operator should be
// able to see what applied without opening the config file.
func (a *App) logRoutingTable() {
	if !a.router.Configured() {
		a.log.Info("routing not configured — every event is delivered to every configured channel",
			"channels", a.router.Default())
		return
	}
	for i, rule := range a.router.Rules() {
		a.log.Info("routing rule", "order", i+1, "rule", rule.Name,
			"match", describeMatch(rule.Match), "channels", rule.Channels)
	}
	a.log.Info("routing default", "channels", a.router.Default())
	if len(a.router.Rules()) == 0 && len(a.router.Default()) == 0 {
		a.log.Warn("routing is configured with no rules and no default — no event will be delivered anywhere")
	}
	if catchAll, unreachable := a.router.UnreachableRules(); len(unreachable) > 0 {
		a.log.Warn("routing rules below a catch-all rule can never match; move the catch-all last",
			"catch_all_rule", catchAll, "unreachable_rules", unreachable)
	}
	if unused := a.router.UnusedChannels(); len(unused) > 0 {
		a.log.Warn("configured channels that no routing rule and no default sends to; check for a typo",
			"unused_channels", unused)
	}
}

// describeMatch renders a rule's conditions as one log-friendly string, e.g.
// `type=[incident] min_severity=warning`. An empty match reads "any event".
func describeMatch(m routing.MatchView) string {
	var parts []string
	add := func(name string, values []string) {
		if len(values) > 0 {
			parts = append(parts, name+"=["+strings.Join(values, " ")+"]")
		}
	}
	add("type", m.Type)
	add("severity", m.Severity)
	if m.MinSeverity != "" {
		parts = append(parts, "min_severity="+m.MinSeverity)
	}
	add("source", m.Source)
	add("category", m.Category)
	if len(parts) == 0 {
		return "any event"
	}
	return strings.Join(parts, " ")
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
// It returns an error only for input Config.Validate would already have
// rejected (an unusable Telegram proxy URL).
func buildRegistry(c config.Channels) (*channels.Registry, error) {
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
		proxy, err := config.ParseProxyURL(t.Proxy)
		if err != nil {
			return nil, fmt.Errorf("telegram channel %q: %w", t.Name, err)
		}
		chans = append(chans, channels.NewTelegram(channels.TelegramConfig{
			Name:     t.Name,
			BotToken: t.BotToken,
			ChatID:   t.ChatID,
			APIBase:  t.APIBase,
			Proxy:    proxy,
			Timeout:  t.Timeout,
		}))
	}
	for _, w := range c.Webhook {
		chans = append(chans, channels.NewWebhook(w.Name, w.URL, w.Secret, w.Timeout))
	}
	return channels.NewRegistry(chans...), nil
}

// RunServer starts the HTTP server and blocks until ctx is cancelled.
func (a *App) RunServer(ctx context.Context) error {
	keyScopes := make(map[string]string, len(a.cfg.APIKeys))
	for _, k := range a.cfg.APIKeys {
		keyScopes[k.Key] = k.Scope
	}
	trusted, err := api.NewTrustedProxies(a.cfg.RateLimit.TrustedProxies)
	if err != nil {
		return fmt.Errorf("rate_limit.trusted_proxies: %w", err)
	}
	if trusted.Configured() {
		a.log.Info("trusting X-Forwarded-For from configured proxies",
			"proxies", a.cfg.RateLimit.TrustedProxies)
	}

	srv := api.NewServer(api.Config{
		Store:          a.store,
		Ingest:         a.ingest,
		Events:         a.events,
		Deliveries:     a.delivery,
		Routing:        a.router,
		APIKeys:        keyScopes,
		AdminToken:     a.cfg.AdminToken,
		Version:        a.version,
		RateLimit:      a.cfg.RateLimit,
		CORSOrigins:    a.cfg.CORSOrigins,
		TrustedProxies: trusted,
		Logger:         a.log,
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
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		a.runRetention(ctx)
	}()
	w.Run(ctx)
	// Wait for retention too: main closes the database as soon as this returns,
	// and a sweep still deleting rows would find its connection pulled.
	wg.Wait()
	return nil
}

// RunAll runs the HTTP server and the worker (which includes retention cleanup)
// together in one process.
//
// It waits for BOTH to finish. Returning on the first one to exit let main's
// `defer a.Close()` shut the database while the HTTP server was still inside
// its ten-second graceful shutdown, so requests in flight during a restart
// failed on a closed pool instead of completing - the one thing graceful
// shutdown exists to prevent.
func (a *App) RunAll(ctx context.Context) error {
	// A failure in either half should bring the other down rather than leaving
	// a half-running process: a server with no worker delivers nothing.
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	errCh := make(chan error, 2)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		defer cancel()
		errCh <- a.RunServer(runCtx)
	}()
	go func() {
		defer wg.Done()
		defer cancel()
		errCh <- a.RunWorker(runCtx)
	}()
	wg.Wait()
	close(errCh)

	// Report the first real failure, if there was one.
	for err := range errCh {
		if err != nil {
			return err
		}
	}
	return nil
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

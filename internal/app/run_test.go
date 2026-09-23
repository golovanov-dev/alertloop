package app

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/golovanov-dev/alertloop/internal/config"
	"github.com/golovanov-dev/alertloop/internal/domain"
)

// runnableApp is an App on a SQLite file of its own, listening on addr.
func runnableApp(t *testing.T, addr string) *App {
	t.Helper()
	cfg := config.Default()
	cfg.Database.DSN = filepath.Join(t.TempDir(), "alertloop.db")
	cfg.Addr = addr
	cfg.AdminToken = "test-token"
	a, err := New(context.Background(), cfg, "test", slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Close() })
	return a
}

// freeAddr returns a loopback address nothing listens on.
func freeAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close()
	return addr
}

// start runs fn in the background and returns its result channel.
func start(fn func() error) <-chan error {
	done := make(chan error, 1)
	go func() { done <- fn() }()
	return done
}

// waitResult fails the test if fn has not returned within the shutdown budget.
func waitResult(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(20 * time.Second):
		t.Fatal("did not return after its context was cancelled")
		return nil
	}
}

func waitReady(t *testing.T, addr string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get("http://" + addr + "/health/ready")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("nothing answered /health/ready on %s", addr)
}

func TestRunServerServesUntilCancelled(t *testing.T) {
	addr := freeAddr(t)
	a := runnableApp(t, addr)
	ctx, cancel := context.WithCancel(context.Background())
	done := start(func() error { return a.RunServer(ctx) })

	waitReady(t, addr)
	cancel()
	if err := waitResult(t, done); err != nil {
		t.Fatalf("a cancelled server returned %v; a clean stop is nil", err)
	}
}

func TestRunWorkerStopsWhenCancelled(t *testing.T) {
	a := runnableApp(t, freeAddr(t))
	ctx, cancel := context.WithCancel(context.Background())
	done := start(func() error { return a.RunWorker(ctx) })

	time.Sleep(100 * time.Millisecond)
	cancel()
	if err := waitResult(t, done); err != nil {
		t.Fatalf("a cancelled worker returned %v", err)
	}
}

// `all`, the default mode: both halves run, and a cancel stops both cleanly.
func TestRunAllServesAndStopsCleanly(t *testing.T) {
	addr := freeAddr(t)
	a := runnableApp(t, addr)
	ctx, cancel := context.WithCancel(context.Background())
	done := start(func() error { return a.RunAll(ctx) })

	waitReady(t, addr)
	cancel()
	if err := waitResult(t, done); err != nil {
		t.Fatalf("a cancelled `all` returned %v", err)
	}
}

// A server that cannot listen takes the worker down with it and the process
// exits with the error, rather than running half of `all`.
func TestRunAllStopsWhenTheServerCannotListen(t *testing.T) {
	taken, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer taken.Close()
	a := runnableApp(t, taken.Addr().String())

	if err := waitResult(t, start(func() error { return a.RunAll(context.Background()) })); err == nil {
		t.Fatal("`all` returned nil with its port taken")
	}
}

// Retention deletes what is past retention_days and nothing younger. A sign
// error in the cutoff would delete the history, and there is no undo.
func TestCleanupDeletesOnlyEventsPastRetention(t *testing.T) {
	a := runnableApp(t, freeAddr(t))
	ctx := context.Background()
	now := time.Now().UTC()

	resolvedEvent := func(id string, created, resolved time.Time) {
		t.Helper()
		e := &domain.Event{
			ID: id, Type: domain.EventIncident, Severity: domain.SeverityError, State: domain.StateResolved,
			Source: "test", Message: "m", Payload: []byte(`{}`),
			CreatedAt: created, UpdatedAt: resolved, LastSeenAt: resolved, ResolvedAt: &resolved,
		}
		alert := &domain.DeliveryAttempt{
			ID: id + "-alert", EventID: id, Channel: domain.ChannelWebhook, ChannelName: "hook",
			Kind: domain.KindAlert, State: domain.DeliverySent, MaxAttempts: 5,
			CreatedAt: created, UpdatedAt: created,
		}
		if _, _, err := a.store.CreateEventWithDeliveries(ctx, e, []*domain.DeliveryAttempt{alert}); err != nil {
			t.Fatal(err)
		}
	}
	// Open for 11 days, closed 29 days ago: its alert is 40 days old and stays.
	resolvedEvent("closed-29-days-ago", now.AddDate(0, 0, -40), now.AddDate(0, 0, -29))
	resolvedEvent("closed-31-days-ago", now.AddDate(0, 0, -31), now.AddDate(0, 0, -31))

	a.cleanupOnce(ctx)

	if _, err := a.store.GetEvent(ctx, "closed-29-days-ago"); err != nil {
		t.Errorf("an event inside the 30-day window was deleted: %v", err)
	}
	if _, err := a.store.GetDeliveryAttempt(ctx, "closed-29-days-ago-alert"); err != nil {
		t.Errorf("the alert of a kept event was deleted: %v", err)
	}
	if _, err := a.store.GetEvent(ctx, "closed-31-days-ago"); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("an event past the 30-day window was kept (err %v)", err)
	}
}

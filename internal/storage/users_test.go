package storage

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/golovanov-dev/alertloop/internal/domain"
)

func addUser(t *testing.T, s Store, id, login string) {
	t.Helper()
	if err := s.CreateUser(context.Background(), &User{ID: id, Login: login, PasswordHash: "h", CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
}

func addSession(t *testing.T, s Store, id, userID string, at time.Time) {
	t.Helper()
	if err := s.CreateSession(context.Background(), &Session{
		ID: id, UserID: userID, CreatedAt: at, LastSeenAt: at, ExpiresAt: at.Add(30 * 24 * time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
}

func TestFirstUserOnlyWhileThereAreNone(t *testing.T) {
	testFirstUserOnlyWhileThereAreNone(t, newTestStore(t))
}
func TestPostgresFirstUserOnlyWhileThereAreNone(t *testing.T) {
	testFirstUserOnlyWhileThereAreNone(t, postgresStore(t))
}

func testFirstUserOnlyWhileThereAreNone(t *testing.T, s Store) {
	ctx := context.Background()
	if err := s.CreateFirstUser(ctx, &User{ID: "1", Login: "alice", PasswordHash: "h", CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateFirstUser(ctx, &User{ID: "2", Login: "bob", PasswordHash: "h", CreatedAt: time.Now()}); !errors.Is(err, ErrUsersExist) {
		t.Fatalf("second first user: err = %v, want ErrUsersExist", err)
	}
	if err := s.CreateUser(ctx, &User{ID: "3", Login: "alice", PasswordHash: "h", CreatedAt: time.Now()}); !errors.Is(err, ErrLoginTaken) {
		t.Fatalf("duplicate login: err = %v, want ErrLoginTaken", err)
	}
}

// Concurrent first-administrator requests create exactly one user. The race
// window is too short to hit reliably here; the PostgreSQL advisory lock is
// checked deterministically by TestPostgresFirstUserWaitsForASetupInFlight.
func TestFirstUserConcurrentSetupCreatesOne(t *testing.T) {
	testFirstUserConcurrentSetupCreatesOne(t, newTestStore(t))
}
func TestPostgresFirstUserConcurrentSetupCreatesOne(t *testing.T) {
	testFirstUserConcurrentSetupCreatesOne(t, postgresStore(t))
}

func testFirstUserConcurrentSetupCreatesOne(t *testing.T, s Store) {
	ctx := context.Background()
	const n = 8
	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		created int
		errs    []error
	)
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			err := s.CreateFirstUser(ctx, &User{
				ID: fmt.Sprintf("u%d", i), Login: fmt.Sprintf("admin%d", i), PasswordHash: "h", CreatedAt: time.Now(),
			})
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				created++
			case !errors.Is(err, ErrUsersExist):
				errs = append(errs, err)
			}
		}(i)
	}
	close(start)
	wg.Wait()
	if len(errs) > 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if created != 1 {
		t.Fatalf("%d first administrators created, want 1", created)
	}
	if c, err := s.CountUsers(ctx); err != nil || c != 1 {
		t.Fatalf("CountUsers = %d, %v; want 1", c, err)
	}
}

// A session ends after 7 idle days or 30 days from sign-in, whichever comes
// first, and retention removes it.
func TestSessionIdleAndAbsoluteExpiry(t *testing.T) {
	testSessionIdleAndAbsoluteExpiry(t, newTestStore(t))
}
func TestPostgresSessionIdleAndAbsoluteExpiry(t *testing.T) {
	testSessionIdleAndAbsoluteExpiry(t, postgresStore(t))
}

func testSessionIdleAndAbsoluteExpiry(t *testing.T, s Store) {
	ctx := context.Background()
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	idle := 7 * 24 * time.Hour
	addUser(t, s, "u1", "alice")
	addSession(t, s, "s1", "u1", start)

	valid := func(now time.Time) bool {
		_, err := s.SessionUser(ctx, "s1", now, now.Add(-idle))
		if err != nil && !errors.Is(err, domain.ErrNotFound) {
			t.Fatal(err)
		}
		return err == nil
	}
	// Used every 6 days: survives idleness, not the 30-day limit.
	for day := 6; day < 30; day += 6 {
		if !valid(start.AddDate(0, 0, day)) {
			t.Fatalf("day %d: session refused while in use", day)
		}
	}
	if valid(start.AddDate(0, 0, 30)) {
		t.Fatal("day 30: session still valid past its absolute limit")
	}

	addSession(t, s, "s2", "u1", start)
	now := start.Add(idle + time.Minute)
	if _, err := s.SessionUser(ctx, "s2", now, now.Add(-idle)); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("idle 7 days: err = %v, want ErrNotFound", err)
	}
	n, err := s.DeleteExpiredSessions(ctx, start.AddDate(0, 0, 31), start.AddDate(0, 0, 31).Add(-idle))
	if err != nil || n != 2 {
		t.Fatalf("DeleteExpiredSessions = %d, %v; want 2", n, err)
	}
}

// Disabling a user and changing a password end sessions at once; the caller's
// own session survives its own password change.
func TestDisableAndPasswordChangeEndSessions(t *testing.T) {
	testDisableAndPasswordChangeEndSessions(t, newTestStore(t))
}
func TestPostgresDisableAndPasswordChangeEndSessions(t *testing.T) {
	testDisableAndPasswordChangeEndSessions(t, postgresStore(t))
}

func testDisableAndPasswordChangeEndSessions(t *testing.T, s Store) {
	ctx := context.Background()
	now := time.Now().UTC()
	addUser(t, s, "u1", "alice")
	addSession(t, s, "s1", "u1", now)
	addSession(t, s, "s2", "u1", now)

	if err := s.SetUserPassword(ctx, "u1", "h2", "s1"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SessionUser(ctx, "s1", now, now.Add(-time.Hour)); err != nil {
		t.Errorf("kept session: %v", err)
	}
	if _, err := s.SessionUser(ctx, "s2", now, now.Add(-time.Hour)); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("other session after a password change: err = %v, want ErrNotFound", err)
	}

	if err := s.DisableUser(ctx, "u1", now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SessionUser(ctx, "s1", now, now.Add(-time.Hour)); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("session of a disabled user: err = %v, want ErrNotFound", err)
	}

	// Enabling brings the account back, not its ended sessions.
	if err := s.EnableUser(ctx, "u1"); err != nil {
		t.Fatal(err)
	}
	if u, err := s.GetUser(ctx, "u1"); err != nil || u.DisabledAt != nil {
		t.Fatalf("after enable: %+v, %v; want not disabled", u, err)
	}
	if _, err := s.SessionUser(ctx, "s1", now, now.Add(-time.Hour)); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("session ended by disable came back after enable: err = %v", err)
	}
	if err := s.EnableUser(ctx, "nobody"); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("enable an unknown user: err = %v, want ErrNotFound", err)
	}
}

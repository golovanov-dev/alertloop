package auth

import (
	"errors"
	"strconv"
	"testing"
	"time"
)

func TestPasswordHashRoundTrip(t *testing.T) {
	hash, err := HashPassword("correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	if !CheckPassword(hash, "correct horse battery") {
		t.Error("the right password was refused")
	}
	if CheckPassword(hash, "correct horse batterY") {
		t.Error("a wrong password was accepted")
	}
	// No user: the dummy hash is checked and the answer is no.
	if CheckPassword("", "correct horse battery") {
		t.Error("an empty hash accepted a password")
	}
}

func TestPasswordShorterThan12IsRefused(t *testing.T) {
	if _, err := HashPassword("elevenchars"); !errors.Is(err, ErrInvalid) {
		t.Errorf("11 characters: err = %v, want ErrInvalid", err)
	}
	// Characters, not bytes: 12 Cyrillic letters are enough.
	if _, err := HashPassword("парольпароль"); err != nil {
		t.Errorf("12 Cyrillic characters: %v", err)
	}
}

func TestLoginIsCaseInsensitiveAndHasNoSpaces(t *testing.T) {
	if l, err := NormalizeLogin("  Alice "); err != nil || l != "alice" {
		t.Errorf("NormalizeLogin(\"  Alice \") = %q, %v", l, err)
	}
	if _, err := NormalizeLogin("al ice"); !errors.Is(err, ErrInvalid) {
		t.Errorf("a login with a space: err = %v, want ErrInvalid", err)
	}
}

// Five failures are free; from the sixth each failure blocks the pair (login,
// address) twice as long as the one before; a success clears it.
func TestThrottleBacksOffAfterFiveFailures(t *testing.T) {
	now := time.Unix(0, 0)
	th := NewThrottle(func() time.Time { return now })
	const ip = "203.0.113.7"
	for range 5 {
		th.Fail("alice", ip)
	}
	if w := th.Wait("alice", ip); w != 0 {
		t.Fatalf("after 5 failures: wait %v, want 0", w)
	}
	th.Fail("alice", ip)
	if w := th.Wait("alice", ip); w != time.Second {
		t.Fatalf("after 6 failures: wait %v, want 1s", w)
	}
	th.Fail("alice", ip)
	if w := th.Wait("alice", ip); w != 2*time.Second {
		t.Fatalf("after 7 failures: wait %v, want 2s", w)
	}
	if w := th.Wait("bob", ip); w != 0 {
		t.Fatalf("another login waits %v", w)
	}
	th.Succeed("alice", ip)
	if w := th.Wait("alice", ip); w != 0 {
		t.Fatalf("after a success: wait %v, want 0", w)
	}
}

// A stranger failing against alice from their address neither delays alice
// signing in from hers nor blocks anyone for longer than five minutes.
func TestThrottleIsPerAddressAndCappedAtFiveMinutes(t *testing.T) {
	now := time.Unix(0, 0)
	th := NewThrottle(func() time.Time { return now })
	for range 40 {
		th.Fail("alice", "198.51.100.9")
	}
	if w := th.Wait("alice", "198.51.100.9"); w != 5*time.Minute {
		t.Fatalf("after 40 failures: wait %v, want the 5m ceiling", w)
	}
	if w := th.Wait("alice", "203.0.113.7"); w != 0 {
		t.Fatalf("alice from her own address waits %v, want 0", w)
	}
}

// The map is bounded: idle pairs are forgotten, and it never grows past
// maxTracked.
func TestThrottleMapIsBounded(t *testing.T) {
	now := time.Unix(0, 0)
	th := NewThrottle(func() time.Time { return now })
	for i := range maxTracked {
		th.Fail("login"+strconv.Itoa(i), "198.51.100.9")
	}
	now = now.Add(forgetAfter + time.Minute)
	th.Fail("fresh", "198.51.100.9")
	if n := th.tracked(); n != 1 {
		t.Fatalf("after idle pairs expired: %d tracked, want 1", n)
	}
	for i := range maxTracked + 50 {
		th.Fail("again"+strconv.Itoa(i), "198.51.100.9")
	}
	if n := th.tracked(); n > maxTracked {
		t.Fatalf("%d pairs tracked, want at most %d", n, maxTracked)
	}
}

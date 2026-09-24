// Package auth holds what console sign-in needs regardless of transport:
// argon2id password hashes, session tokens, login rules, and the per-login
// back-off after failed attempts. The HTTP handlers live in internal/api, the
// CLI in cmd/alertloop.
package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/golovanov-dev/alertloop/internal/storage"
	"github.com/google/uuid"
	"golang.org/x/crypto/argon2"
)

const (
	// MinPasswordLen is the shortest password accepted, in characters.
	MinPasswordLen = 12
	// maxPasswordLen bounds the input to the hash function.
	maxPasswordLen = 256
	// maxLoginLen bounds a login, in characters.
	maxLoginLen = 64

	// IdleTimeout ends a session not used for this long.
	IdleTimeout = 7 * 24 * time.Hour
	// MaxLifetime ends a session this long after sign-in, used or not.
	MaxLifetime = 30 * 24 * time.Hour
)

// ErrInvalid wraps every rejection of a login or password the user can fix.
var ErrInvalid = errors.New("invalid")

// argon2id parameters: the OWASP minimum (19 MiB, 2 passes, 1 lane).
const (
	argonTime    = 2
	argonMemory  = 19 * 1024
	argonThreads = 1
	argonKeyLen  = 32
	argonSaltLen = 16
)

// NormalizeLogin trims and lower-cases a login and checks it: 1 to 64
// characters, no spaces or control characters.
func NormalizeLogin(login string) (string, error) {
	l := strings.ToLower(strings.TrimSpace(login))
	if l == "" {
		return "", fmt.Errorf("%w: login is empty", ErrInvalid)
	}
	if n := len([]rune(l)); n > maxLoginLen {
		return "", fmt.Errorf("%w: login is longer than %d characters", ErrInvalid, maxLoginLen)
	}
	for _, r := range l {
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			return "", fmt.Errorf("%w: login contains a space or a control character", ErrInvalid)
		}
	}
	return l, nil
}

// HashPassword checks the password rules and returns an argon2id hash in the
// PHC string format.
func HashPassword(password string) (string, error) {
	n := len([]rune(password))
	if n < MinPasswordLen {
		return "", fmt.Errorf("%w: password is shorter than %d characters", ErrInvalid, MinPasswordLen)
	}
	if n > maxPasswordLen {
		return "", fmt.Errorf("%w: password is longer than %d characters", ErrInvalid, maxPasswordLen)
	}
	salt := make([]byte, argonSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := argon2.IDKey([]byte(password), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	b64 := base64.RawStdEncoding
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, argonMemory, argonTime, argonThreads, b64.EncodeToString(salt), b64.EncodeToString(key)), nil
}

// dummyHash is checked against when a login does not exist, so that answer
// takes as long as a wrong password.
var dummyHash = sync.OnceValue(func() string {
	h, err := HashPassword("not-a-password-anyone-has")
	if err != nil {
		panic(err)
	}
	return h
})

// CheckPassword reports whether password matches hash. An empty hash means
// "no such user": the work is done against a dummy hash and false returned.
func CheckPassword(hash, password string) bool {
	if hash == "" {
		checkPHC(dummyHash(), password)
		return false
	}
	return checkPHC(hash, password)
}

func checkPHC(hash, password string) bool {
	parts := strings.Split(hash, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false
	}
	var (
		version int
		m       uint32
		t       uint32
		p       uint8
	)
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return false
	}
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &m, &t, &p); err != nil {
		return false
	}
	b64 := base64.RawStdEncoding
	salt, err := b64.DecodeString(parts[4])
	if err != nil {
		return false
	}
	want, err := b64.DecodeString(parts[5])
	if err != nil || len(want) == 0 {
		return false
	}
	got := argon2.IDKey([]byte(password), salt, t, m, p, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1
}

// NewUser checks login and password and builds the user to store.
func NewUser(login, password string) (*storage.User, error) {
	l, err := NormalizeLogin(login)
	if err != nil {
		return nil, err
	}
	hash, err := HashPassword(password)
	if err != nil {
		return nil, err
	}
	return &storage.User{ID: uuid.NewString(), Login: l, PasswordHash: hash, CreatedAt: time.Now().UTC()}, nil
}

// NewSessionToken returns a new cookie value and the session id stored for it.
func NewSessionToken() (token, id string, err error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", "", err
	}
	token = base64.RawURLEncoding.EncodeToString(b)
	return token, SessionID(token), nil
}

// SessionID is the stored id of a session token: its SHA-256 in hex.
func SessionID(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// GeneratePassword returns a random password of 24 URL-safe characters.
func GeneratePassword() (string, error) {
	b := make([]byte, 18)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// Throttle slows down guessing a password: after freeFailures failed attempts
// in a row for one login from one client address, each further failure blocks
// that pair for twice as long as the one before, up to maxDelay. A success
// clears the pair. Failures from another address do not delay the right
// password typed here, so a stranger cannot lock an administrator out.
//
// State lives in the memory of the server process and ends with it. Entries
// idle for forgetAfter are forgotten; the map never holds more than
// maxTracked pairs.
type Throttle struct {
	mu    sync.Mutex
	state map[throttleKey]*throttleEntry
	now   func() time.Time
}

type throttleKey struct{ login, ip string }

type throttleEntry struct {
	failures int
	until    time.Time
	last     time.Time
}

const (
	freeFailures = 5
	baseDelay    = time.Second
	maxDelay     = 5 * time.Minute
	// forgetAfter drops a pair with no failed attempt for this long.
	forgetAfter = time.Hour
	// maxTracked bounds the map: guessing random logins from many addresses
	// cannot grow it without limit.
	maxTracked = 10000
)

// NewThrottle returns an empty Throttle; now defaults to time.Now.
func NewThrottle(now func() time.Time) *Throttle {
	if now == nil {
		now = time.Now
	}
	return &Throttle{state: map[throttleKey]*throttleEntry{}, now: now}
}

// Wait returns how long login must wait before its next attempt from ip; zero
// when it may try now.
func (t *Throttle) Wait(login, ip string) time.Duration {
	t.mu.Lock()
	defer t.mu.Unlock()
	e, ok := t.state[throttleKey{login, ip}]
	if !ok {
		return 0
	}
	if d := e.until.Sub(t.now()); d > 0 {
		return d
	}
	return 0
}

// Fail records a failed attempt for login from ip.
func (t *Throttle) Fail(login, ip string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.now()
	k := throttleKey{login, ip}
	e, ok := t.state[k]
	if ok && now.Sub(e.last) > forgetAfter {
		e.failures = 0
	}
	if !ok {
		t.makeRoom(now)
		e = &throttleEntry{}
		t.state[k] = e
	}
	e.failures++
	e.last = now
	if over := e.failures - freeFailures; over > 0 {
		d := maxDelay
		if over <= 20 {
			d = min(baseDelay<<(over-1), maxDelay)
		}
		e.until = now.Add(d)
	}
}

// makeRoom keeps the map below maxTracked: first by forgetting idle pairs,
// then, if every pair is recent, by dropping any one of them.
func (t *Throttle) makeRoom(now time.Time) {
	if len(t.state) < maxTracked {
		return
	}
	for k, e := range t.state {
		if now.Sub(e.last) > forgetAfter {
			delete(t.state, k)
		}
	}
	for k := range t.state {
		if len(t.state) < maxTracked {
			return
		}
		delete(t.state, k)
	}
}

// Succeed clears the failures of login from ip.
func (t *Throttle) Succeed(login, ip string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.state, throttleKey{login, ip})
}

// tracked is the number of pairs held, for tests.
func (t *Throttle) tracked() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.state)
}

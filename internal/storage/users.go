package storage

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/golovanov-dev/alertloop/internal/domain"
)

// User is a console account. Every user has the one role Community knows,
// admin. Login is stored lower-cased.
type User struct {
	ID           string
	Login        string
	PasswordHash string
	CreatedAt    time.Time
	DisabledAt   *time.Time
	LastLoginAt  *time.Time
}

// Session is a console sign-in. ID is the SHA-256 of the cookie value.
type Session struct {
	ID         string
	UserID     string
	CreatedAt  time.Time
	LastSeenAt time.Time
	ExpiresAt  time.Time
	IP         string
	UserAgent  string
}

var (
	// ErrLoginTaken is returned when a user with the same login exists.
	ErrLoginTaken = errors.New("a user with this login already exists")
	// ErrUsersExist is returned by CreateFirstUser once any user exists.
	ErrUsersExist = errors.New("the first administrator has already been created")
)

// UserStore persists console users and their sessions.
type UserStore interface {
	CountUsers(ctx context.Context) (int64, error)
	// CreateUser inserts u; ErrLoginTaken when the login is in use.
	CreateUser(ctx context.Context, u *User) error
	// CreateFirstUser inserts u only while the users table is empty;
	// ErrUsersExist otherwise.
	CreateFirstUser(ctx context.Context, u *User) error
	GetUser(ctx context.Context, id string) (*User, error)
	UserByLogin(ctx context.Context, login string) (*User, error)
	ListUsers(ctx context.Context) ([]User, error)
	// SetUserPassword replaces the password hash and deletes every session of
	// the user except keepSession (empty: all of them).
	SetUserPassword(ctx context.Context, userID, hash, keepSession string) error
	// DisableUser marks the user disabled and deletes all their sessions.
	DisableUser(ctx context.Context, userID string, at time.Time) error
	// EnableUser clears the disabled mark; domain.ErrNotFound when there is
	// no such user.
	EnableUser(ctx context.Context, userID string) error
	// CreateSession stores s and records the sign-in as the user's last login.
	CreateSession(ctx context.Context, s *Session) error
	// SessionUser returns the user of session id when the session exists, the
	// user is not disabled, it has not passed expires_at and has been used
	// after idleSince. last_seen_at moves to now when it is a minute old.
	// domain.ErrNotFound otherwise.
	SessionUser(ctx context.Context, id string, now, idleSince time.Time) (*User, error)
	DeleteSession(ctx context.Context, id string) error
	DeleteUserSessions(ctx context.Context, userID string) error
	// DeleteExpiredSessions removes sessions past expires_at or unused since
	// idleSince. Retention cleanup.
	DeleteExpiredSessions(ctx context.Context, now, idleSince time.Time) (int64, error)
}

const userColumns = `id, login, password_hash, created_at, disabled_at, last_login_at`

func scanUser(sc interface{ Scan(...any) error }) (*User, error) {
	var (
		u                     User
		created               string
		disabled, lastLoginAt sql.NullString
	)
	if err := sc.Scan(&u.ID, &u.Login, &u.PasswordHash, &created, &disabled, &lastLoginAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, domain.ErrNotFound
		}
		return nil, err
	}
	u.CreatedAt = parseTime(created)
	u.DisabledAt = nullTime(disabled)
	u.LastLoginAt = nullTime(lastLoginAt)
	return &u, nil
}

func nullTime(s sql.NullString) *time.Time {
	if !s.Valid {
		return nil
	}
	t := parseTime(s.String)
	return &t
}

func (s *sqlStore) CountUsers(ctx context.Context) (int64, error) {
	var n int64
	err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM users`).Scan(&n)
	return n, err
}

func (s *sqlStore) CreateUser(ctx context.Context, u *User) error {
	_, err := s.db.ExecContext(ctx, s.d.rebind(
		`INSERT INTO users (id, login, password_hash, created_at) VALUES (?, ?, ?, ?)`),
		u.ID, u.Login, u.PasswordHash, formatTime(u.CreatedAt))
	if isUniqueViolation(err) {
		return ErrLoginTaken
	}
	return err
}

// firstUserLock is the PostgreSQL advisory lock key that serialises
// CreateFirstUser. Under READ COMMITTED two concurrent INSERT … WHERE NOT
// EXISTS both see an empty table and both insert; holding the lock until
// commit makes the second one see the first one's row.
const firstUserLock int64 = 0x616c657274 // "alert"

func (s *sqlStore) CreateFirstUser(ctx context.Context, u *User) error {
	return s.inTx(ctx, func(tx *sql.Tx) error {
		if s.d.name == "postgres" {
			if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1)`, firstUserLock); err != nil {
				return err
			}
		}
		// On SQLite the statement is a write from its start: the write lock is
		// taken before NOT EXISTS is read, and the pool holds one connection.
		res, err := tx.ExecContext(ctx, s.d.rebind(
			`INSERT INTO users (id, login, password_hash, created_at)
			 SELECT CAST(? AS TEXT), CAST(? AS TEXT), CAST(? AS TEXT), CAST(? AS TEXT)
			 WHERE NOT EXISTS (SELECT 1 FROM users)`),
			u.ID, u.Login, u.PasswordHash, formatTime(u.CreatedAt))
		if isUniqueViolation(err) {
			return ErrUsersExist
		}
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrUsersExist
		}
		return nil
	})
}

func (s *sqlStore) GetUser(ctx context.Context, id string) (*User, error) {
	return scanUser(s.db.QueryRowContext(ctx, s.d.rebind(
		`SELECT `+userColumns+` FROM users WHERE id = ?`), id))
}

func (s *sqlStore) UserByLogin(ctx context.Context, login string) (*User, error) {
	return scanUser(s.db.QueryRowContext(ctx, s.d.rebind(
		`SELECT `+userColumns+` FROM users WHERE login = ?`), login))
}

func (s *sqlStore) ListUsers(ctx context.Context) ([]User, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+userColumns+` FROM users ORDER BY login`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []User
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *u)
	}
	return out, rows.Err()
}

// inTx runs fn in a transaction and commits it.
func (s *sqlStore) inTx(ctx context.Context, fn func(tx *sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // rolled back only if commit fails
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}

// execOne runs q and returns domain.ErrNotFound when it touched no row.
func execOne(ctx context.Context, ex execer, q string, args ...any) error {
	res, err := ex.ExecContext(ctx, q, args...)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return domain.ErrNotFound
	}
	return nil
}

func (s *sqlStore) SetUserPassword(ctx context.Context, userID, hash, keepSession string) error {
	return s.inTx(ctx, func(tx *sql.Tx) error {
		if err := execOne(ctx, tx, s.d.rebind(`UPDATE users SET password_hash = ? WHERE id = ?`), hash, userID); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, s.d.rebind(`DELETE FROM sessions WHERE user_id = ? AND id <> ?`), userID, keepSession)
		return err
	})
}

func (s *sqlStore) DisableUser(ctx context.Context, userID string, at time.Time) error {
	return s.inTx(ctx, func(tx *sql.Tx) error {
		if err := execOne(ctx, tx, s.d.rebind(
			`UPDATE users SET disabled_at = COALESCE(disabled_at, ?) WHERE id = ?`), formatTime(at), userID); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, s.d.rebind(`DELETE FROM sessions WHERE user_id = ?`), userID)
		return err
	})
}

func (s *sqlStore) EnableUser(ctx context.Context, userID string) error {
	return execOne(ctx, s.db, s.d.rebind(`UPDATE users SET disabled_at = NULL WHERE id = ?`), userID)
}

func (s *sqlStore) CreateSession(ctx context.Context, ss *Session) error {
	return s.inTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, s.d.rebind(
			`INSERT INTO sessions (id, user_id, created_at, last_seen_at, expires_at, ip, user_agent)
			 VALUES (?, ?, ?, ?, ?, ?, ?)`),
			ss.ID, ss.UserID, formatTime(ss.CreatedAt), formatTime(ss.LastSeenAt), formatTime(ss.ExpiresAt),
			ss.IP, ss.UserAgent); err != nil {
			return err
		}
		return execOne(ctx, tx, s.d.rebind(`UPDATE users SET last_login_at = ? WHERE id = ?`),
			formatTime(ss.CreatedAt), ss.UserID)
	})
}

// touchEvery bounds how often a session's last_seen_at is written: every
// console request would otherwise be a write, and SQLite has one writer.
const touchEvery = time.Minute

func (s *sqlStore) SessionUser(ctx context.Context, id string, now, idleSince time.Time) (*User, error) {
	var lastSeen string
	row := s.db.QueryRowContext(ctx, s.d.rebind(
		`SELECT s.last_seen_at, u.id, u.login, u.password_hash, u.created_at, u.disabled_at, u.last_login_at
		 FROM sessions s JOIN users u ON u.id = s.user_id
		 WHERE s.id = ? AND u.disabled_at IS NULL AND s.expires_at > ? AND s.last_seen_at > ?`),
		id, formatTime(now), formatTime(idleSince))
	var (
		u                     User
		created               string
		disabled, lastLoginAt sql.NullString
	)
	if err := row.Scan(&lastSeen, &u.ID, &u.Login, &u.PasswordHash, &created, &disabled, &lastLoginAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, domain.ErrNotFound
		}
		return nil, err
	}
	u.CreatedAt, u.DisabledAt, u.LastLoginAt = parseTime(created), nullTime(disabled), nullTime(lastLoginAt)
	if now.Sub(parseTime(lastSeen)) >= touchEvery {
		if _, err := s.db.ExecContext(ctx, s.d.rebind(`UPDATE sessions SET last_seen_at = ? WHERE id = ?`),
			formatTime(now), id); err != nil {
			return nil, err
		}
	}
	return &u, nil
}

func (s *sqlStore) DeleteSession(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, s.d.rebind(`DELETE FROM sessions WHERE id = ?`), id)
	return err
}

func (s *sqlStore) DeleteUserSessions(ctx context.Context, userID string) error {
	_, err := s.db.ExecContext(ctx, s.d.rebind(`DELETE FROM sessions WHERE user_id = ?`), userID)
	return err
}

func (s *sqlStore) DeleteExpiredSessions(ctx context.Context, now, idleSince time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx, s.d.rebind(
		`DELETE FROM sessions WHERE expires_at <= ? OR last_seen_at <= ?`), formatTime(now), formatTime(idleSince))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

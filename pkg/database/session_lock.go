package database

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"time"
)

// Session advisory locks live as long as the database session that took
// them, so each is taken on one pinned connection. The two-key form keeps
// these names apart from other advisory locks that hash a single key.
const (
	sessionLockClass = "frameworks_session_lock"
	tryLockSQL       = `SELECT pg_try_advisory_lock(hashtext($1), hashtext($2))`
	lockSQL          = `SELECT pg_advisory_lock(hashtext($1), hashtext($2))`
	unlockSQL        = `SELECT pg_advisory_unlock(hashtext($1), hashtext($2))`
)

// WithSessionLock runs fn while holding the session advisory lock name,
// waiting for another holder to release it first. A replica that dies while
// holding the lock releases it with its connection.
func WithSessionLock(ctx context.Context, db *sql.DB, name string, fn func() error) error {
	conn, err := db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("session lock %s: pin connection: %w", name, err)
	}
	defer func() { _ = conn.Close() }()
	if _, err := conn.ExecContext(ctx, lockSQL, sessionLockClass, name); err != nil {
		return fmt.Errorf("session lock %s: acquire: %w", name, err)
	}
	defer func() {
		unlockCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = conn.ExecContext(unlockCtx, unlockSQL, sessionLockClass, name) //nolint:errcheck // closing the pinned connection releases the lock when the unlock fails
	}()
	return fn()
}

// SessionLeader elects one replica of a service to run a job that must not
// run twice at once. The leader holds the session advisory lock name on a
// pinned connection for as long as that connection lives; a leader that
// stops or loses its database session hands the lock to the next replica
// that asks.
type SessionLeader struct {
	db   *sql.DB
	name string

	mu   sync.Mutex
	conn *sql.Conn
}

// NewSessionLeader returns a leader election over the lock name. It takes
// nothing until Lead is called.
func NewSessionLeader(db *sql.DB, name string) *SessionLeader {
	return &SessionLeader{db: db, name: name}
}

// Lead reports whether this replica holds the lock, taking it when it is
// free. It returns false with a nil error while another replica leads, and
// false with the reason when the database could not be asked.
func (l *SessionLeader) Lead(ctx context.Context) (bool, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.conn != nil {
		if err := l.conn.PingContext(ctx); err == nil {
			return true, nil
		}
		// The session is gone, and the lock with it.
		_ = l.conn.Close()
		l.conn = nil
	}
	conn, err := l.db.Conn(ctx)
	if err != nil {
		return false, fmt.Errorf("leader %s: pin connection: %w", l.name, err)
	}
	var acquired bool
	if err := conn.QueryRowContext(ctx, tryLockSQL, sessionLockClass, l.name).Scan(&acquired); err != nil {
		_ = conn.Close()
		return false, fmt.Errorf("leader %s: try lock: %w", l.name, err)
	}
	if !acquired {
		_ = conn.Close()
		return false, nil
	}
	l.conn = conn
	return true, nil
}

// Release gives up leadership so another replica can take it at once.
func (l *SessionLeader) Release() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.conn == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, _ = l.conn.ExecContext(ctx, unlockSQL, sessionLockClass, l.name) //nolint:errcheck // closing the pinned connection releases the lock when the unlock fails
	_ = l.conn.Close()
	l.conn = nil
}

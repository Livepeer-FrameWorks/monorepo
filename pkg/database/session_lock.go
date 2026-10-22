package database

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
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
	unlockSQL        = `SELECT pg_advisory_unlock(hashtext($1), hashtext($2))`
)

// sessionLockPollInterval is how often WithSessionLock asks again for a lock
// another session holds.
const sessionLockPollInterval = 250 * time.Millisecond

// sessionUnlockTimeout bounds the unlock that ends a hold, which runs after
// the holder's own context may already have ended.
const sessionUnlockTimeout = 5 * time.Second

// AdvisoryLock is one session-level advisory lock, named by the statements
// that take and drop it. TrySQL yields whether this session took the lock and
// UnlockSQL whether this session held it; both take Args.
type AdvisoryLock struct {
	TrySQL    string
	UnlockSQL string
	Args      []any
}

// NamedLock is the lock name in the two-key namespace WithSessionLock and
// SessionLeader use.
func NamedLock(name string) AdvisoryLock {
	return AdvisoryLock{TrySQL: tryLockSQL, UnlockSQL: unlockSQL, Args: []any{sessionLockClass, name}}
}

// HashtextLock is the single-key lock hashtext(key), the same key as a
// literal pg_try_advisory_lock(hashtext('<key>')).
func HashtextLock(key string) AdvisoryLock {
	return AdvisoryLock{
		TrySQL:    `SELECT pg_try_advisory_lock(hashtext($1))`,
		UnlockSQL: `SELECT pg_advisory_unlock(hashtext($1))`,
		Args:      []any{key},
	}
}

// DiscardSession closes the physical database session behind conn instead of
// returning it to the pool. A session whose lock statement failed is in an
// unknown state: YugabyteDB, for one, expires a session's lock transaction
// during a storage stall and then fails every later lock statement on that
// backend with SQLSTATE 40001, and a pooled session that still holds a lock
// keeps it from every other replica. database/sql discards a connection only
// when it reports driver.ErrBadConn; closing the session ends any lock it
// holds.
func DiscardSession(conn *sql.Conn) {
	_ = conn.Raw(func(any) error { return driver.ErrBadConn }) //nolint:errcheck // Raw returns the ErrBadConn that discards the session
	_ = conn.Close()                                           //nolint:errcheck // the discarded Conn reports ErrConnDone
}

// TrySessionLock takes lock on a session pinned for the hold when no other
// session holds it. While acquired, release drops the lock and returns the
// session to the pool. Any session whose lock state is not known to be clean
// is discarded: one whose try failed, or one whose unlock failed or found the
// lock not held.
func TrySessionLock(ctx context.Context, db *sql.DB, lock AdvisoryLock) (release func(context.Context) error, acquired bool, err error) {
	conn, err := db.Conn(ctx)
	if err != nil {
		return nil, false, fmt.Errorf("pin session: %w", err)
	}
	if err := conn.QueryRowContext(ctx, lock.TrySQL, lock.Args...).Scan(&acquired); err != nil {
		DiscardSession(conn)
		return nil, false, fmt.Errorf("try lock: %w", err)
	}
	if !acquired {
		_ = conn.Close()
		return nil, false, nil
	}
	return func(releaseCtx context.Context) error { return ReleaseSessionLock(releaseCtx, conn, lock) }, true, nil
}

// ReleaseSessionLock drops lock held by conn and ends the hold: a clean
// unlock returns the session to the pool, and an unlock that failed or found
// the lock not held discards the session, which ends the lock with it.
func ReleaseSessionLock(ctx context.Context, conn *sql.Conn, lock AdvisoryLock) error {
	if err := dropHold(ctx, conn, lock); err != nil {
		DiscardSession(conn)
		return err
	}
	return conn.Close()
}

// takeHold adds one hold of lock to conn's session. Advisory locks are
// re-entrant per session, so a session that already holds lock always gets
// another hold.
func takeHold(ctx context.Context, conn *sql.Conn, lock AdvisoryLock) error {
	var taken bool
	if err := conn.QueryRowContext(ctx, lock.TrySQL, lock.Args...).Scan(&taken); err != nil {
		return fmt.Errorf("take lock: %w", err)
	}
	if !taken {
		return errors.New("take lock: another session holds the lock")
	}
	return nil
}

// dropHold removes one hold of lock from conn's session. The unlock yields
// false, with no error, when the session holds the lock no more.
func dropHold(ctx context.Context, conn *sql.Conn, lock AdvisoryLock) error {
	var unlocked bool
	if err := conn.QueryRowContext(ctx, lock.UnlockSQL, lock.Args...).Scan(&unlocked); err != nil {
		return fmt.Errorf("unlock: %w", err)
	}
	if !unlocked {
		return errors.New("unlock: the session did not hold the lock")
	}
	return nil
}

// WithSessionLock runs fn while holding the session advisory lock name,
// waiting until ctx ends for another holder to release it first. A replica
// that dies while holding the lock releases it with its connection. The wait
// polls instead of blocking in one statement, so the runtime roles'
// statement_timeout bounds each attempt rather than the whole wait. A
// session whose lock statement fails is discarded rather than pooled.
func WithSessionLock(ctx context.Context, db *sql.DB, name string, fn func() error) error {
	lock := NamedLock(name)
	conn, err := db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("session lock %s: pin connection: %w", name, err)
	}
	for {
		var acquired bool
		if err := conn.QueryRowContext(ctx, lock.TrySQL, lock.Args...).Scan(&acquired); err != nil {
			DiscardSession(conn)
			return fmt.Errorf("session lock %s: acquire: %w", name, err)
		}
		if acquired {
			break
		}
		select {
		case <-ctx.Done():
			_ = conn.Close()
			return fmt.Errorf("session lock %s: acquire: %w", name, ctx.Err())
		case <-time.After(sessionLockPollInterval):
		}
	}
	defer func() {
		unlockCtx, cancel := context.WithTimeout(context.Background(), sessionUnlockTimeout)
		defer cancel()
		_ = ReleaseSessionLock(unlockCtx, conn, lock) //nolint:errcheck // a failed unlock discards the session, which ends the lock
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

// ErrLostLeadership is the error Lead wraps when the session this replica led
// on no longer proves it holds the lock. The session is discarded, and the
// next Lead asks for the lock on a new one.
var ErrLostLeadership = errors.New("lost the lock")

// NewSessionLeader returns a leader election over the lock name. It takes
// nothing until Lead is called.
func NewSessionLeader(db *sql.DB, name string) *SessionLeader {
	return &SessionLeader{db: db, name: name}
}

// Lead reports whether this replica holds the lock, taking it when it is
// free. It returns false with a nil error while another replica leads, and
// false with the reason when the database could not be asked or the held
// session no longer proves it holds the lock.
func (l *SessionLeader) Lead(ctx context.Context) (bool, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	lock := NamedLock(l.name)
	if l.conn != nil {
		err := stillHolds(ctx, l.conn, lock)
		if err == nil {
			return true, nil
		}
		DiscardSession(l.conn)
		l.conn = nil
		return false, fmt.Errorf("leader %s: %w: %w", l.name, ErrLostLeadership, err)
	}
	conn, err := l.db.Conn(ctx)
	if err != nil {
		return false, fmt.Errorf("leader %s: pin connection: %w", l.name, err)
	}
	var acquired bool
	if err := conn.QueryRowContext(ctx, lock.TrySQL, lock.Args...).Scan(&acquired); err != nil {
		DiscardSession(conn)
		return false, fmt.Errorf("leader %s: try lock: %w", l.name, err)
	}
	if !acquired {
		_ = conn.Close()
		return false, nil
	}
	if err := takeHold(ctx, conn, lock); err != nil {
		DiscardSession(conn)
		return false, fmt.Errorf("leader %s: second hold: %w", l.name, err)
	}
	l.conn = conn
	return true, nil
}

// stillHolds proves that conn's session still holds lock. A live connection
// is not proof: YugabyteDB can expire the session's lock transaction, dropping
// the lock while the connection still runs other statements. YugabyteDB's
// pg_locks lists advisory locks without the holder's pid and only some time
// after they were taken, so the session itself is asked: the leader holds the
// lock twice, an unlock yields false once the session no longer holds the
// lock, whether or not another session took it since, and the hold that
// remains keeps the lock while the dropped one is taken again.
func stillHolds(ctx context.Context, conn *sql.Conn, lock AdvisoryLock) error {
	if err := dropHold(ctx, conn, lock); err != nil {
		return err
	}
	return takeHold(ctx, conn, lock)
}

// Release gives up leadership so another replica can take it at once.
func (l *SessionLeader) Release() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.conn == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), sessionUnlockTimeout)
	defer cancel()
	lock := NamedLock(l.name)
	if err := dropHold(ctx, l.conn, lock); err != nil {
		DiscardSession(l.conn)
	} else {
		_ = ReleaseSessionLock(ctx, l.conn, lock) //nolint:errcheck // a failed unlock discards the session, which ends the lock
	}
	l.conn = nil
}

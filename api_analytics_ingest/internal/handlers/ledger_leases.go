package handlers

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

const ledgerLeaseNamespace = "periscope-ingest:"

// LedgerLease serializes one named rebuild or stale-close pass across service
// replicas. The returned release function owns a dedicated database session;
// closing that session also releases the advisory lock after process failure.
type LedgerLease interface {
	TryAcquire(context.Context, string) (release func(context.Context) error, acquired bool, err error)
}

type postgresLedgerLease struct {
	db *sql.DB
}

func NewPostgresLedgerLease(db *sql.DB) LedgerLease {
	return &postgresLedgerLease{db: db}
}

func (l *postgresLedgerLease) TryAcquire(ctx context.Context, worker string) (func(context.Context) error, bool, error) {
	if l == nil || l.db == nil {
		return nil, false, errors.New("ledger lease database is unavailable")
	}
	conn, err := l.db.Conn(ctx)
	if err != nil {
		return nil, false, fmt.Errorf("reserve ledger lease connection: %w", err)
	}
	key := ledgerLeaseNamespace + worker
	var acquired bool
	if err := conn.QueryRowContext(ctx, `SELECT pg_try_advisory_lock(hashtext($1))`, key).Scan(&acquired); err != nil {
		_ = conn.Close()
		return nil, false, fmt.Errorf("acquire ledger advisory lock: %w", err)
	}
	if !acquired {
		_ = conn.Close()
		return nil, false, nil
	}
	release := func(releaseCtx context.Context) error {
		defer func() { _ = conn.Close() }()
		var unlocked bool
		if err := conn.QueryRowContext(releaseCtx, `SELECT pg_advisory_unlock(hashtext($1))`, key).Scan(&unlocked); err != nil {
			return fmt.Errorf("release ledger advisory lock: %w", err)
		}
		if !unlocked {
			return fmt.Errorf("release ledger advisory lock: lock was not held")
		}
		return nil
	}
	return release, true, nil
}

// localLedgerLease keeps direct/unit construction deterministic. Production
// wiring always supplies the Postgres implementation.
type localLedgerLease struct{}

func (localLedgerLease) TryAcquire(context.Context, string) (func(context.Context) error, bool, error) {
	return func(context.Context) error { return nil }, true, nil
}

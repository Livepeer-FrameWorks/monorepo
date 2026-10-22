package handlers

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/database"
)

const ledgerLeaseNamespace = "periscope-ingest:"

// LedgerLease serializes one named rebuild or stale-close pass across service
// replicas. The lease is a session advisory lock on a database session pinned
// for the hold; it ends with that session when the process dies, and release
// discards a session whose unlock did not cleanly drop the lock.
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
	release, acquired, err := database.TrySessionLock(ctx, l.db, database.HashtextLock(ledgerLeaseNamespace+worker))
	if err != nil {
		return nil, false, fmt.Errorf("acquire ledger advisory lock: %w", err)
	}
	if !acquired {
		return nil, false, nil
	}
	return func(releaseCtx context.Context) error {
		if err := release(releaseCtx); err != nil {
			return fmt.Errorf("release ledger advisory lock: %w", err)
		}
		return nil
	}, true, nil
}

// localLedgerLease keeps direct/unit construction deterministic. Production
// wiring always supplies the Postgres implementation.
type localLedgerLease struct{}

func (localLedgerLease) TryAcquire(context.Context, string) (func(context.Context) error, bool, error) {
	return func(context.Context) error { return nil }, true, nil
}

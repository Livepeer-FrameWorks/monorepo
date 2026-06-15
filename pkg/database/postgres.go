package database

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"strings"
	"time"

	// YugabyteDB smart driver registered under the database/sql name "pgx".
	// It provides cluster-aware connection load balancing + failover across
	// tservers (load_balance=true + multi-host DSN); see pkg/database/errors.go
	// and arrays.go for the driver-portability helpers the swap requires.
	_ "github.com/yugabyte/pgx/v5/stdlib"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
)

// PostgresConn represents a PostgreSQL database connection
type PostgresConn = *sql.DB

// ErrNoRows is returned when a query returns no rows
var ErrNoRows = sql.ErrNoRows

// Config holds database configuration
type Config struct {
	URL             string
	MaxOpenConns    int
	MaxIdleConns    int
	ConnMaxLifetime time.Duration
	// ConnMaxIdleTime recycles idle connections. With the smart driver,
	// failover is connection-level: recycling is what rebalances the pool
	// across tservers after a node fails/recovers/joins, and bounds how long a
	// connection to a degraded node lingers in the pool.
	ConnMaxIdleTime time.Duration
	// PingTimeout bounds the startup connectivity probe. Zero uses
	// defaultPingTimeout so a hung node fails fast instead of blocking startup.
	PingTimeout time.Duration
}

const (
	mustConnectTimeout      = 60 * time.Second
	mustConnectInitialDelay = 500 * time.Millisecond
	mustConnectMaxDelay     = 5 * time.Second

	defaultPingTimeout = 5 * time.Second
)

// DefaultConfig returns default database configuration
func DefaultConfig() Config {
	return Config{
		MaxOpenConns:    25,
		MaxIdleConns:    5,
		ConnMaxLifetime: 5 * time.Minute,
		ConnMaxIdleTime: 5 * time.Minute,
		PingTimeout:     defaultPingTimeout,
	}
}

// withPgxExecMode adds default_query_exec_mode=exec to a DSN unless already set.
// pgx's ParseConfig consumes this param (it is stripped from RuntimeParams, not
// sent to the server). Applied at connect time rather than baked into the
// provisioned DATABASE_URL so the pgx-only param stays out of psql diagnostics.
//
// Handles both DSN shapes pgx/lib/pq accept: URL form (postgres://...) and
// keyword/value form (host=... dbname=...). Operator-supplied DATABASE_URL
// bypasses provisioning and may use either, so we must not corrupt the latter.
func withPgxExecMode(rawDSN string) (string, error) {
	const param = "default_query_exec_mode"
	trimmed := strings.TrimSpace(rawDSN)
	isURL := strings.HasPrefix(trimmed, "postgres://") || strings.HasPrefix(trimmed, "postgresql://")
	if !isURL {
		// Keyword/value DSN (or empty/unknown): append the keyword form unless
		// it is already present. Do not url.Parse it (that would corrupt it).
		if trimmed == "" || strings.Contains(rawDSN, param+"=") {
			return rawDSN, nil
		}
		return rawDSN + " " + param + "=exec", nil
	}
	u, err := url.Parse(rawDSN)
	if err != nil {
		return "", fmt.Errorf("failed to parse database URL: %w", err)
	}
	q := u.Query()
	if q.Get(param) == "" {
		q.Set(param, "exec")
		u.RawQuery = q.Encode()
	}
	return u.String(), nil
}

// Connect establishes a database connection with the given configuration
func Connect(cfg Config, logger logging.Logger) (PostgresConn, error) {
	if cfg.URL == "" {
		return nil, fmt.Errorf("database URL is required")
	}

	// Use pgx's unnamed-statement exec mode (no server-side prepared-statement
	// cache). The smart driver's default (cache_statement) returns "cached plan
	// must not change result type" TO THE CALLER on the first query after online
	// (expand/contract) DDL changes a table's result columns; it invalidates the
	// cache for the next call but does NOT transparently retry the failing one,
	// and most query paths are not wrapped in RetryPostgres. Exec mode avoids that
	// class entirely (and is closest to lib/pq's per-query behavior). Set here,
	// not in the provisioned DATABASE_URL, so it never leaks into psql diagnostics.
	// See docs/architecture/database-ha.md.
	dsn, err := withPgxExecMode(cfg.URL)
	if err != nil {
		return nil, err
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	// Apply pool settings before probing so the probe borrows a connection
	// under the same limits the service will use.
	db.SetMaxOpenConns(cfg.MaxOpenConns)
	db.SetMaxIdleConns(cfg.MaxIdleConns)
	db.SetConnMaxLifetime(cfg.ConnMaxLifetime)
	db.SetConnMaxIdleTime(cfg.ConnMaxIdleTime)

	pingTimeout := cfg.PingTimeout
	if pingTimeout <= 0 {
		pingTimeout = defaultPingTimeout
	}
	ctx, cancel := context.WithTimeout(context.Background(), pingTimeout)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("failed to ping database: %w", err)
	}

	logger.WithFields(logging.Fields{
		"max_open_conns":     cfg.MaxOpenConns,
		"max_idle_conns":     cfg.MaxIdleConns,
		"conn_max_lifetime":  cfg.ConnMaxLifetime,
		"conn_max_idle_time": cfg.ConnMaxIdleTime,
	}).Info("Database connected")

	return db, nil
}

// MustConnect is like Connect but exits after bounded startup retries.
func MustConnect(cfg Config, logger logging.Logger) PostgresConn {
	deadline := time.Now().Add(mustConnectTimeout)
	delay := mustConnectInitialDelay
	attempt := 1

	for {
		db, err := Connect(cfg, logger)
		if err == nil {
			return db
		}

		if time.Now().Add(delay).After(deadline) {
			logger.WithError(err).WithField("attempt", attempt).Fatal("Failed to connect to database")
		}

		logger.WithError(err).WithFields(logging.Fields{
			"attempt":  attempt,
			"retry_in": delay.String(),
		}).Warn("Database connection failed; retrying")

		time.Sleep(delay)
		if delay < mustConnectMaxDelay {
			delay *= 2
			if delay > mustConnectMaxDelay {
				delay = mustConnectMaxDelay
			}
		}
		attempt++
	}
}

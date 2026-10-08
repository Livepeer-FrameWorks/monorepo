package database

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"math/rand/v2"
	"net/url"
	"strings"
	"time"

	"github.com/yugabyte/pgx/v5"
	// YugabyteDB smart driver registered under the database/sql name "pgx".
	// It provides cluster-aware connection load balancing + failover across
	// tservers (load_balance=true + multi-host DSN); see pkg/database/errors.go
	// and arrays.go for the driver-portability helpers the swap requires.
	"github.com/yugabyte/pgx/v5/stdlib"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
)

// PostgresConn represents a PostgreSQL database connection
type PostgresConn = *sql.DB

// ErrNoRows is returned when a query returns no rows
var ErrNoRows = sql.ErrNoRows

// Config holds database configuration
type Config struct {
	URL string
	// ServiceName selects the binary's executable schema capability contract.
	// Empty is supported for generic tooling and tests that do not own a schema.
	ServiceName     string
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

type postgresContractTracer struct {
	service string
}

func (t postgresContractTracer) TraceQueryStart(ctx context.Context, _ *pgx.Conn, _ pgx.TraceQueryStartData) context.Context {
	return ctx
}

func (t postgresContractTracer) TraceQueryEnd(_ context.Context, _ *pgx.Conn, data pgx.TraceQueryEndData) {
	ObserveDatabaseError(t.service, EnginePostgres, data.Err, false)
}

// DefaultConfig returns default database configuration.
//
// A new YSQL connection starts a backend with a cold catalog cache, and its
// first query on a relation loads that relation's catalog entries one read at a
// time from the master leader: the Purser admission read issues 638 such reads
// on a fresh backend, about 140 ms on a three-node cluster with sub-millisecond
// links and 1.5 s at a 2 ms round trip, against 3 to 16 ms once warm
// (make measure-yugabyte-connection-warmth). Every connection the pool closes
// makes some later request pay that again, so connections live long:
// MaxIdleConns equals MaxOpenConns, so the pool keeps what a burst opened, and
// a connection retires only after ConnMaxIdleTime unused or near
// ConnMaxLifetime. A connection to a tserver that died is still replaced at
// once: database/sql discards a connection whose query fails at the
// transport, the driver pings a connection reused after more than a second
// idle and discards it when the ping fails, and TCP keepalive (Go's 15 s probes)
// closes an idle one whose host stopped answering.
func DefaultConfig() Config {
	return Config{
		MaxOpenConns:    25,
		MaxIdleConns:    25,
		ConnMaxLifetime: 60 * time.Minute,
		ConnMaxIdleTime: 30 * time.Minute,
		PingTimeout:     defaultPingTimeout,
	}
}

func configurePool(db *sql.DB, cfg Config) {
	db.SetMaxOpenConns(cfg.MaxOpenConns)
	db.SetMaxIdleConns(cfg.MaxIdleConns)
	db.SetConnMaxLifetime(cfg.ConnMaxLifetime)
	db.SetConnMaxIdleTime(cfg.ConnMaxIdleTime)
}

// retireAtKey holds, in a connection's pgconn custom data, the time after which
// the pool must not hand that connection out again.
const retireAtKey = "frameworks.retire_at"

// connectionLifetimeJitter returns a fraction in [0, 1) that places one
// connection's retirement within the last quarter of ConnMaxLifetime.
var connectionLifetimeJitter = rand.Float64

// connectionLifetime is how long one connection may serve before it retires.
// Connections a burst opened together would otherwise all retire together, and
// every request that follows would pay a cold catalog at the same moment.
func connectionLifetime(maxLifetime time.Duration) time.Duration {
	if maxLifetime <= 0 {
		return 0
	}
	return maxLifetime - time.Duration(connectionLifetimeJitter()*float64(maxLifetime)/4)
}

// lifetimeOptions retires each connection at its own jittered lifetime. The
// driver calls the reset hook when the pool hands out a connection it used
// before, and database/sql replaces a connection whose reset returns
// driver.ErrBadConn without failing the caller. SetConnMaxLifetime still bounds
// connections that sit idle past their retirement.
func lifetimeOptions(maxLifetime time.Duration) []stdlib.OptionOpenDB {
	if maxLifetime <= 0 {
		return nil
	}
	return []stdlib.OptionOpenDB{
		stdlib.OptionAfterConnect(func(_ context.Context, conn *pgx.Conn) error {
			conn.PgConn().CustomData()[retireAtKey] = time.Now().Add(connectionLifetime(maxLifetime))
			return nil
		}),
		stdlib.OptionResetSession(func(_ context.Context, conn *pgx.Conn) error {
			if retireAt, ok := conn.PgConn().CustomData()[retireAtKey].(time.Time); ok && !time.Now().Before(retireAt) {
				return driver.ErrBadConn
			}
			return nil
		}),
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
	pgxConfig, err := pgx.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("failed to parse database configuration: %w", err)
	}
	if cfg.ServiceName != "" {
		pgxConfig.Tracer = postgresContractTracer{service: cfg.ServiceName}
	}
	db := stdlib.OpenDB(*pgxConfig, lifetimeOptions(cfg.ConnMaxLifetime)...)

	// Apply pool settings before probing so the probe borrows a connection
	// under the same limits the service will use.
	configurePool(db, cfg)

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
	if cfg.ServiceName != "" {
		if err := VerifyCapabilities(ctx, cfg.ServiceName, EnginePostgres, func(ctx context.Context, probe string) error {
			rows, queryErr := db.QueryContext(ctx, probe)
			if queryErr != nil {
				return queryErr
			}
			defer func() { _ = rows.Close() }()
			return rows.Err()
		}); err != nil {
			_ = db.Close()
			return nil, err
		}
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

package database

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	_ "github.com/lib/pq"

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
}

const (
	mustConnectTimeout      = 60 * time.Second
	mustConnectInitialDelay = 500 * time.Millisecond
	mustConnectMaxDelay     = 5 * time.Second
)

// DefaultConfig returns default database configuration
func DefaultConfig() Config {
	return Config{
		MaxOpenConns:    25,
		MaxIdleConns:    5,
		ConnMaxLifetime: 5 * time.Minute,
	}
}

// Connect establishes a database connection with the given configuration
func Connect(cfg Config, logger logging.Logger) (PostgresConn, error) {
	if cfg.URL == "" {
		return nil, fmt.Errorf("database URL is required")
	}

	db, err := sql.Open("postgres", cfg.URL)
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	// Test connection
	if err := db.PingContext(context.Background()); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("failed to ping database: %w", err)
	}

	// Set connection pool settings
	db.SetMaxOpenConns(cfg.MaxOpenConns)
	db.SetMaxIdleConns(cfg.MaxIdleConns)
	db.SetConnMaxLifetime(cfg.ConnMaxLifetime)

	logger.WithFields(logging.Fields{
		"max_open_conns":    cfg.MaxOpenConns,
		"max_idle_conns":    cfg.MaxIdleConns,
		"conn_max_lifetime": cfg.ConnMaxLifetime,
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

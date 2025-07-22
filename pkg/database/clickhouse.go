package database

import (
	"context"
	"database/sql"

	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"

	"frameworks/pkg/logging"
)

// ClickHouseConn represents a ClickHouse database connection using database/sql interface
// This is used for SELECT queries and standard SQL operations
type ClickHouseConn = *sql.DB

// ClickHouseNativeConn represents a native ClickHouse driver connection
// This is used for batch operations and ClickHouse-specific features
type ClickHouseNativeConn = driver.Conn

// ClickHouseBatch represents a ClickHouse batch for bulk inserts
type ClickHouseBatch = driver.Batch

// ClickHouseConfig holds ClickHouse configuration
type ClickHouseConfig struct {
	Addr     []string
	Database string
	Username string
	Password string
	Debug    bool
}

// DefaultClickHouseConfig returns default ClickHouse configuration
func DefaultClickHouseConfig() ClickHouseConfig {
	return ClickHouseConfig{
		Addr:     []string{"127.0.0.1:9000"},
		Database: "default",
		Username: "default",
		Password: "",
		Debug:    false,
	}
}

// ConnectClickHouse establishes a connection to ClickHouse using database/sql interface
// Use this for SELECT queries and standard SQL operations
func ConnectClickHouse(cfg ClickHouseConfig, logger logging.Logger) (ClickHouseConn, error) {
	conn := clickhouse.OpenDB(&clickhouse.Options{
		Addr: cfg.Addr,
		Auth: clickhouse.Auth{
			Database: cfg.Database,
			Username: cfg.Username,
			Password: cfg.Password,
		},
		Debug: cfg.Debug,
	})

	// Test the connection
	if err := conn.Ping(); err != nil {
		logger.WithError(err).Error("Failed to ping ClickHouse")
		return nil, err
	}

	logger.WithFields(logging.Fields{
		"addr":     cfg.Addr,
		"database": cfg.Database,
	}).Info("Connected to ClickHouse (SQL interface)")

	return conn, nil
}

// ConnectClickHouseNative establishes a native connection to ClickHouse
// Use this for batch operations and ClickHouse-specific features
func ConnectClickHouseNative(cfg ClickHouseConfig, logger logging.Logger) (ClickHouseNativeConn, error) {
	conn, err := clickhouse.Open(&clickhouse.Options{
		Addr: cfg.Addr,
		Auth: clickhouse.Auth{
			Database: cfg.Database,
			Username: cfg.Username,
			Password: cfg.Password,
		},
		Debug: cfg.Debug,
	})

	if err != nil {
		logger.WithError(err).Error("Failed to connect to ClickHouse native")
		return nil, err
	}

	// Test the connection
	if err := conn.Ping(context.Background()); err != nil {
		logger.WithError(err).Error("Failed to ping ClickHouse native")
		return nil, err
	}

	logger.WithFields(logging.Fields{
		"addr":     cfg.Addr,
		"database": cfg.Database,
	}).Info("Connected to ClickHouse (native interface)")

	return conn, nil
}

// MustConnectClickHouse connects to ClickHouse using database/sql interface or panics
func MustConnectClickHouse(cfg ClickHouseConfig, logger logging.Logger) ClickHouseConn {
	conn, err := ConnectClickHouse(cfg, logger)
	if err != nil {
		logger.WithError(err).Fatal("Failed to connect to ClickHouse")
	}
	return conn
}

// MustConnectClickHouseNative connects to ClickHouse using native interface or panics
func MustConnectClickHouseNative(cfg ClickHouseConfig, logger logging.Logger) ClickHouseNativeConn {
	conn, err := ConnectClickHouseNative(cfg, logger)
	if err != nil {
		logger.WithError(err).Fatal("Failed to connect to ClickHouse native")
	}
	return conn
}

package database

import (
	"context"
	"database/sql"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
)

// clickHouseConnectRetries bounds boot-time connection attempts. ClickHouse's
// image starts a throwaway server to run init scripts before restarting as the
// real server, so a healthcheck can briefly pass against a node that then
// refuses connections. Retrying with backoff rides out that window instead of
// exiting and relying on the container restart policy.
const (
	clickHouseConnectRetries      = 12
	clickHouseConnectBackoff      = 3 * time.Second
	defaultClickHouseProbeTimeout = 5 * time.Second
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
	// ProbeTimeout bounds ping plus executable capability checks for one
	// connection attempt. Zero uses defaultClickHouseProbeTimeout.
	ProbeTimeout time.Duration
	// ServiceName selects the binary's executable ClickHouse capability contract.
	ServiceName string
}

// DefaultClickHouseConfig returns default ClickHouse configuration
func DefaultClickHouseConfig() ClickHouseConfig {
	return ClickHouseConfig{
		Addr:         []string{"127.0.0.1:9000"},
		Database:     "default",
		Username:     "default",
		Password:     "",
		Debug:        false,
		ProbeTimeout: defaultClickHouseProbeTimeout,
	}
}

func clickHouseProbeContext(cfg ClickHouseConfig) (context.Context, context.CancelFunc) {
	timeout := cfg.ProbeTimeout
	if timeout <= 0 {
		timeout = defaultClickHouseProbeTimeout
	}
	return context.WithTimeout(context.Background(), timeout)
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

	probeCtx, cancel := clickHouseProbeContext(cfg)
	defer cancel()
	// Test the connection
	if err := conn.PingContext(probeCtx); err != nil {
		logger.WithError(err).Error("Failed to ping ClickHouse")
		return nil, err
	}
	if cfg.ServiceName != "" {
		if err := VerifyCapabilities(probeCtx, cfg.ServiceName, EngineClickHouse, func(ctx context.Context, probe string) error {
			rows, queryErr := conn.QueryContext(ctx, probe)
			if queryErr != nil {
				return queryErr
			}
			defer func() { _ = rows.Close() }()
			return rows.Err()
		}); err != nil {
			_ = conn.Close()
			return nil, err
		}
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

	probeCtx, cancel := clickHouseProbeContext(cfg)
	defer cancel()
	// Test the connection
	if err := conn.Ping(probeCtx); err != nil {
		logger.WithError(err).Error("Failed to ping ClickHouse native")
		return nil, err
	}
	if cfg.ServiceName != "" {
		if err := VerifyCapabilities(probeCtx, cfg.ServiceName, EngineClickHouse, func(ctx context.Context, probe string) error {
			rows, queryErr := conn.Query(ctx, probe)
			if queryErr != nil {
				return queryErr
			}
			defer func() { _ = rows.Close() }()
			return rows.Err()
		}); err != nil {
			_ = conn.Close()
			return nil, err
		}
	}

	logger.WithFields(logging.Fields{
		"addr":     cfg.Addr,
		"database": cfg.Database,
	}).Info("Connected to ClickHouse (native interface)")

	return conn, nil
}

// MustConnectClickHouse connects to ClickHouse using database/sql interface or panics
func MustConnectClickHouse(cfg ClickHouseConfig, logger logging.Logger) ClickHouseConn {
	var conn ClickHouseConn
	var err error
	for attempt := 1; attempt <= clickHouseConnectRetries; attempt++ {
		if conn, err = ConnectClickHouse(cfg, logger); err == nil {
			return conn
		}
		if attempt < clickHouseConnectRetries {
			logger.WithError(err).WithFields(logging.Fields{
				"attempt":  attempt,
				"retry_in": clickHouseConnectBackoff.String(),
			}).Warn("ClickHouse not ready; retrying connection")
			time.Sleep(clickHouseConnectBackoff)
		}
	}
	logger.WithError(err).Fatal("Failed to connect to ClickHouse")
	return conn
}

// MustConnectClickHouseNative connects to ClickHouse using native interface or panics
func MustConnectClickHouseNative(cfg ClickHouseConfig, logger logging.Logger) ClickHouseNativeConn {
	var conn ClickHouseNativeConn
	var err error
	for attempt := 1; attempt <= clickHouseConnectRetries; attempt++ {
		if conn, err = ConnectClickHouseNative(cfg, logger); err == nil {
			return conn
		}
		if attempt < clickHouseConnectRetries {
			logger.WithError(err).WithFields(logging.Fields{
				"attempt":  attempt,
				"retry_in": clickHouseConnectBackoff.String(),
			}).Warn("ClickHouse not ready; retrying native connection")
			time.Sleep(clickHouseConnectBackoff)
		}
	}
	logger.WithError(err).Fatal("Failed to connect to ClickHouse native")
	return conn
}

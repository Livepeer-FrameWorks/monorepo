package provisioner

import (
	"context"
	"fmt"

	"frameworks/cli/pkg/detect"
	"frameworks/cli/pkg/health"
	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/ssh"
	dbsql "frameworks/pkg/database/sql"

	"github.com/ClickHouse/clickhouse-go/v2"
)

// ClickHouseProvisioner provisions ClickHouse
type ClickHouseProvisioner struct {
	*BaseProvisioner
}

// NewClickHouseProvisioner creates a new ClickHouse provisioner
func NewClickHouseProvisioner(pool *ssh.Pool) (*ClickHouseProvisioner, error) {
	return &ClickHouseProvisioner{
		BaseProvisioner: NewBaseProvisioner("clickhouse", pool),
	}, nil
}

// Detect checks if ClickHouse is installed and running
func (c *ClickHouseProvisioner) Detect(ctx context.Context, host inventory.Host) (*detect.ServiceState, error) {
	return c.CheckExists(ctx, host, "clickhouse")
}

// Provision installs ClickHouse
func (c *ClickHouseProvisioner) Provision(ctx context.Context, host inventory.Host, config ServiceConfig) error {
	// Check if already installed
	state, err := c.Detect(ctx, host)
	if err == nil && state.Exists && state.Running {
		return nil // Already provisioned
	}

	// Install ClickHouse via shell script
	// For MVP, we'll use the official installation script
	installScript := `#!/bin/bash
set -e

# Install ClickHouse
curl https://clickhouse.com/ | sh

# Start ClickHouse server
sudo /usr/bin/clickhouse-server start

# Wait for server to be ready
sleep 5
`

	result, err := c.ExecuteScript(ctx, host, installScript)
	if err != nil {
		return fmt.Errorf("failed to install ClickHouse: %w", err)
	}

	if result.ExitCode != 0 {
		return fmt.Errorf("ClickHouse installation failed: %s", result.Stderr)
	}

	return nil
}

// Validate checks if ClickHouse is healthy
func (c *ClickHouseProvisioner) Validate(ctx context.Context, host inventory.Host, config ServiceConfig) error {
	checker := &health.ClickHouseChecker{
		User:     "default",
		Password: "",
		Database: "default",
	}

	result := checker.Check(host.Address, config.Port)
	if !result.OK {
		return fmt.Errorf("clickhouse health check failed: %s", result.Error)
	}

	return nil
}

// Initialize creates databases and tables
func (c *ClickHouseProvisioner) Initialize(ctx context.Context, host inventory.Host, config ServiceConfig) error {
	// Get databases from config
	databases, ok := config.Metadata["databases"].([]string)
	if !ok {
		databases = []string{"periscope"}
	}

	// Connect to ClickHouse
	conn, err := clickhouse.Open(&clickhouse.Options{
		Addr: []string{fmt.Sprintf("%s:%d", host.Address, config.Port)},
		Auth: clickhouse.Auth{
			Database: "default",
			Username: "default",
			Password: "",
		},
	})
	if err != nil {
		return fmt.Errorf("failed to connect to ClickHouse: %w", err)
	}
	defer conn.Close()

	// Create each database
	for _, dbName := range databases {
		// Create database if not exists
		query := fmt.Sprintf("CREATE DATABASE IF NOT EXISTS %s", dbName)
		if err := conn.Exec(ctx, query); err != nil {
			return fmt.Errorf("failed to create database %s: %w", dbName, err)
		}

		fmt.Printf("✓ Database %s ready\n", dbName)

		// Run initialization SQL for periscope database
		if dbName == "periscope" {
			if err := c.initializePeriscopeDatabase(ctx, conn); err != nil {
				return fmt.Errorf("failed to initialize periscope database: %w", err)
			}
		}
	}

	return nil
}

// initializePeriscopeDatabase runs ClickHouse schema for periscope
func (c *ClickHouseProvisioner) initializePeriscopeDatabase(ctx context.Context, conn clickhouse.Conn) error {
	sqlContent, err := dbsql.Content.ReadFile("clickhouse/periscope.sql")
	if err != nil {
		return fmt.Errorf("failed to read embedded ClickHouse schema: %w", err)
	}

	// Execute SQL (ClickHouse Go driver requires splitting statements)
	// For simplicity, execute as single multi-statement (may need splitting for complex schemas)
	if err := conn.Exec(ctx, string(sqlContent)); err != nil {
		return fmt.Errorf("failed to execute SQL: %w", err)
	}

	fmt.Println("✓ ClickHouse periscope schema initialized")
	return nil
}

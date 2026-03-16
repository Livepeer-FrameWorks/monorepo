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
	version := config.Version
	installScript := fmt.Sprintf(`#!/bin/bash
set -e

VERSION="%s"
if [ "$VERSION" = "stable" ]; then
  VERSION=""
fi

install_clickhouse_apt() {
  apt-get update
  apt-get install -y apt-transport-https ca-certificates curl gnupg
  mkdir -p /etc/apt/keyrings
  curl -fsSL https://packages.clickhouse.com/CLICKHOUSE-KEY.GPG | gpg --dearmor -o /etc/apt/keyrings/clickhouse.gpg
  echo "deb [signed-by=/etc/apt/keyrings/clickhouse.gpg] https://packages.clickhouse.com/deb stable main" > /etc/apt/sources.list.d/clickhouse.list
  apt-get update
  if [ -n "$VERSION" ]; then
    apt-get install -y clickhouse-server="$VERSION" clickhouse-client="$VERSION"
  else
    apt-get install -y clickhouse-server clickhouse-client
  fi
}

install_clickhouse_yum() {
  cat >/etc/yum.repos.d/clickhouse.repo <<'REPO'
[clickhouse]
name=ClickHouse
baseurl=https://packages.clickhouse.com/rpm/stable/
enabled=1
gpgcheck=1
gpgkey=https://packages.clickhouse.com/rpm/stable/repodata/repomd.xml.key
REPO
  if [ -n "$VERSION" ]; then
    yum install -y "clickhouse-server-$VERSION" "clickhouse-client-$VERSION"
  else
    yum install -y clickhouse-server clickhouse-client
  fi
}

if command -v apt-get >/dev/null; then
  install_clickhouse_apt
elif command -v yum >/dev/null; then
  install_clickhouse_yum
else
  echo "Unsupported package manager"
  exit 1
fi

if command -v systemctl >/dev/null; then
  systemctl enable --now clickhouse-server
else
  /usr/bin/clickhouse-server start
fi

# Wait for server to be ready
sleep 5
`, version)

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

	result := checker.Check(host.ExternalIP, config.Port)
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
		Addr: []string{fmt.Sprintf("%s:%d", host.ExternalIP, config.Port)},
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

// Configure deploys users.xml and sets the CLICKHOUSE_PASSWORD env var
// on the ClickHouse host so the "frameworks" user has a password.
// Must be called after Provision (ClickHouse is installed and running).
func (c *ClickHouseProvisioner) Configure(ctx context.Context, host inventory.Host, config ServiceConfig) error {
	password, _ := config.Metadata["clickhouse_password"].(string)
	if password == "" {
		return nil // no password configured — skip (dev mode)
	}

	readonlyPassword, _ := config.Metadata["clickhouse_readonly_password"].(string)
	if readonlyPassword == "" {
		readonlyPassword = password // reuse primary password for readonly if not set
	}

	// Deploy users.xml and set env vars via a single script.
	// ClickHouse reads CLICKHOUSE_PASSWORD from the process environment
	// when users.xml uses <password from_env="CLICKHOUSE_PASSWORD"/>.
	script := fmt.Sprintf(`#!/bin/bash
set -e

# Write users.xml with from_env references
cat > /etc/clickhouse-server/users.xml <<'USERSXML'
<?xml version="1.0"?>
<clickhouse>
    <profiles>
        <default>
            <log_queries>1</log_queries>
            <log_query_threads>1</log_query_threads>
            <allow_ddl>1</allow_ddl>
            <readonly>0</readonly>
        </default>
        <readonly>
            <log_queries>0</log_queries>
            <readonly>1</readonly>
            <allow_ddl>0</allow_ddl>
        </readonly>
    </profiles>
    <users>
        <default>
            <password></password>
            <profile>default</profile>
            <quota>default</quota>
            <networks>
                <ip>::1</ip>
                <ip>127.0.0.1</ip>
            </networks>
            <access_management>1</access_management>
        </default>
        <frameworks>
            <password from_env="CLICKHOUSE_PASSWORD"></password>
            <profile>default</profile>
            <quota>frameworks_quota</quota>
            <networks>
                <ip>::/0</ip>
            </networks>
            <access_management>0</access_management>
            <allow_databases>
                <database>periscope</database>
            </allow_databases>
        </frameworks>
        <readonly_user>
            <password from_env="CLICKHOUSE_READONLY_PASSWORD"></password>
            <profile>readonly</profile>
            <quota>readonly_quota</quota>
            <networks>
                <ip>::/0</ip>
            </networks>
            <allow_databases>
                <database>periscope</database>
            </allow_databases>
        </readonly_user>
    </users>
    <quotas>
        <default>
            <interval>
                <duration>3600</duration>
                <queries>0</queries>
                <errors>0</errors>
                <result_rows>0</result_rows>
                <read_rows>0</read_rows>
                <execution_time>0</execution_time>
            </interval>
        </default>
        <frameworks_quota>
            <interval>
                <duration>3600</duration>
                <queries>0</queries>
                <errors>0</errors>
                <result_rows>0</result_rows>
                <read_rows>0</read_rows>
                <execution_time>0</execution_time>
            </interval>
        </frameworks_quota>
        <readonly_quota>
            <interval>
                <duration>3600</duration>
                <queries>1000</queries>
                <errors>50</errors>
                <result_rows>10000000</result_rows>
                <read_rows>100000000</read_rows>
                <execution_time>1800</execution_time>
            </interval>
        </readonly_quota>
    </quotas>
</clickhouse>
USERSXML

chown clickhouse:clickhouse /etc/clickhouse-server/users.xml
chmod 640 /etc/clickhouse-server/users.xml

# Set password env vars for systemd so ClickHouse reads them at startup
mkdir -p /etc/systemd/system/clickhouse-server.service.d
cat > /etc/systemd/system/clickhouse-server.service.d/passwords.conf <<EOF
[Service]
Environment="CLICKHOUSE_PASSWORD=%s"
Environment="CLICKHOUSE_READONLY_PASSWORD=%s"
EOF

chmod 600 /etc/systemd/system/clickhouse-server.service.d/passwords.conf

systemctl daemon-reload
systemctl restart clickhouse-server

sleep 3
echo "ClickHouse configured with application credentials"
`, password, readonlyPassword)

	result, err := c.ExecuteScript(ctx, host, script)
	if err != nil {
		return fmt.Errorf("failed to configure ClickHouse credentials: %w", err)
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("ClickHouse configuration failed: %s", result.Stderr)
	}

	fmt.Printf("✓ ClickHouse credentials configured on %s\n", host.ExternalIP)
	return nil
}

// initializePeriscopeDatabase runs ClickHouse schema for periscope
// ApplyDemoSeeds applies ClickHouse demo data for development.
// Uses the "frameworks" user when a password is provided in config.Metadata["clickhouse_password"],
// falling back to "default" (unauthenticated) for local/unconfigured clusters.
func (c *ClickHouseProvisioner) ApplyDemoSeeds(ctx context.Context, host inventory.Host, config ServiceConfig) error {
	sqlContent, err := dbsql.Content.ReadFile("seeds/demo/clickhouse_demo_data.sql")
	if err != nil {
		return fmt.Errorf("read clickhouse demo seed: %w", err)
	}

	username := "default"
	password := ""
	if pw, ok := config.Metadata["clickhouse_password"].(string); ok && pw != "" {
		username = "frameworks"
		password = pw
	}

	conn, err := clickhouse.Open(&clickhouse.Options{
		Addr: []string{fmt.Sprintf("%s:%d", host.ExternalIP, config.Port)},
		Auth: clickhouse.Auth{Database: "periscope", Username: username, Password: password},
	})
	if err != nil {
		return fmt.Errorf("connect to clickhouse: %w", err)
	}
	defer conn.Close()

	if err := conn.Exec(ctx, string(sqlContent)); err != nil {
		return fmt.Errorf("apply clickhouse demo seed: %w", err)
	}
	return nil
}

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

package provisioner

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"frameworks/cli/pkg/detect"
	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/ssh"
	dbsql "frameworks/pkg/database/sql"

	"github.com/lib/pq"
)

// YugabyteProvisioner provisions YugabyteDB nodes (yb-master + yb-tserver)
type YugabyteProvisioner struct {
	*BaseProvisioner
}

// NewYugabyteProvisioner creates a new YugabyteDB provisioner
func NewYugabyteProvisioner(pool *ssh.Pool) (Provisioner, error) {
	return &YugabyteProvisioner{
		BaseProvisioner: NewBaseProvisioner("yugabyte", pool),
	}, nil
}

// Detect checks if YugabyteDB is installed and running
func (y *YugabyteProvisioner) Detect(ctx context.Context, host inventory.Host) (*detect.ServiceState, error) {
	result, err := y.RunCommand(ctx, host, "pgrep -x yb-master && pgrep -x yb-tserver && echo RUNNING || echo NOT_RUNNING")
	if err != nil { //nolint:nilerr // pgrep failure means service not running, not an error
		return &detect.ServiceState{Exists: false, Running: false}, nil
	}

	running := strings.Contains(result.Stdout, "RUNNING") && !strings.Contains(result.Stdout, "NOT_RUNNING")

	// Check if binaries exist
	binResult, _ := y.RunCommand(ctx, host, "test -x /opt/yugabyte/bin/yb-master && echo EXISTS")
	exists := binResult != nil && strings.Contains(binResult.Stdout, "EXISTS")

	return &detect.ServiceState{
		Exists:  exists,
		Running: running,
	}, nil
}

// Provision installs YugabyteDB on a single node
func (y *YugabyteProvisioner) Provision(ctx context.Context, host inventory.Host, config ServiceConfig) error {
	state, err := y.Detect(ctx, host)
	if err == nil && state.Exists && state.Running {
		return nil
	}

	version := config.Version
	masterAddresses, _ := config.Metadata["master_addresses"].(string)
	nodeID, _ := config.Metadata["node_id"].(int)
	rf, _ := config.Metadata["replication_factor"].(int)
	if rf == 0 {
		rf = 3
	}

	port := config.Port
	if port == 0 {
		port = 5433
	}

	// Placement info — defaults to single zone
	cloud := "frameworks"
	region := "eu"
	zone := fmt.Sprintf("eu-%d", nodeID)

	installScript := fmt.Sprintf(`#!/bin/bash
set -e

VERSION="%s"
MASTER_ADDRESSES="%s"
NODE_IP="%s"
NODE_ID=%d
RF=%d
YSQL_PORT=%d
CLOUD="%s"
REGION="%s"
ZONE="%s"

# System prerequisites
echo "Configuring system settings..."

# Create yugabyte user
id -u yugabyte &>/dev/null || useradd -r -s /bin/false yugabyte

# Set ulimits for yugabyte
cat > /etc/security/limits.d/yugabyte.conf <<'LIMITS'
yugabyte soft core unlimited
yugabyte hard core unlimited
yugabyte soft nofile 1048576
yugabyte hard nofile 1048576
yugabyte soft nproc 12000
yugabyte hard nproc 12000
LIMITS

# Kernel tuning
cat > /etc/sysctl.d/99-yugabyte.conf <<'SYSCTL'
vm.swappiness=0
vm.max_map_count=262144
kernel.core_pattern=/var/lib/yugabyte/cores/core_%%p_%%t_%%e
SYSCTL
sysctl --system >/dev/null 2>&1 || true

# Disable transparent hugepages
if [ -f /sys/kernel/mm/transparent_hugepage/enabled ]; then
  echo never > /sys/kernel/mm/transparent_hugepage/enabled
  echo never > /sys/kernel/mm/transparent_hugepage/defrag
fi

# Install NTP (chrony) for clock sync — critical for distributed consensus
if command -v apt-get >/dev/null; then
  apt-get update -qq && apt-get install -y -qq chrony curl >/dev/null 2>&1
elif command -v yum >/dev/null; then
  yum install -y -q chrony curl >/dev/null 2>&1
fi
systemctl enable --now chronyd 2>/dev/null || systemctl enable --now chrony 2>/dev/null || true

# Download and install YugabyteDB
INSTALL_DIR="/opt/yugabyte"
DATA_DIR="/var/lib/yugabyte/data"
CONF_DIR="/opt/yugabyte/conf"
CORES_DIR="/var/lib/yugabyte/cores"

mkdir -p "$DATA_DIR" "$CONF_DIR" "$CORES_DIR"

if [ ! -x "$INSTALL_DIR/bin/yb-master" ]; then
  echo "Downloading YugabyteDB $VERSION..."
  ARCH=$(uname -m)
  case "$ARCH" in
    x86_64)  YB_ARCH="x86_64" ;;
    aarch64) YB_ARCH="aarch64" ;;
    *)       echo "Unsupported architecture: $ARCH"; exit 1 ;;
  esac

  TARBALL="yugabyte-${VERSION}-linux-${YB_ARCH}.tar.gz"
  URL="https://downloads.yugabyte.com/releases/${VERSION}/${TARBALL}"

  cd /tmp
  curl -sSLO "$URL"
  mkdir -p "$INSTALL_DIR"
  tar xzf "$TARBALL" -C "$INSTALL_DIR" --strip-components=1
  rm -f "$TARBALL"

  # Post-install
  "$INSTALL_DIR/bin/post_install.sh" 2>/dev/null || true
fi

chown -R yugabyte:yugabyte "$INSTALL_DIR" "$DATA_DIR" "$CORES_DIR"

# Write master gflags
cat > "$CONF_DIR/master.conf" <<MASTERCONF
--master_addresses=${MASTER_ADDRESSES}
--rpc_bind_addresses=${NODE_IP}:7100
--webserver_interface=${NODE_IP}
--fs_data_dirs=${DATA_DIR}
--replication_factor=${RF}
--placement_cloud=${CLOUD}
--placement_region=${REGION}
--placement_zone=${ZONE}
--leader_failure_max_missed_heartbeat_periods=10
--callhome_enabled=false
MASTERCONF

# Write tserver gflags
cat > "$CONF_DIR/tserver.conf" <<TSERVERCONF
--tserver_master_addrs=${MASTER_ADDRESSES}
--rpc_bind_addresses=${NODE_IP}:9100
--webserver_interface=${NODE_IP}
--pgsql_proxy_bind_address=0.0.0.0:${YSQL_PORT}
--cql_proxy_bind_address=0.0.0.0:9042
--fs_data_dirs=${DATA_DIR}
--ysql_enable_auth=true
--ysql_hba_conf_csv="host all all 0.0.0.0/0 scram-sha-256,host all all ::0/0 scram-sha-256"
--placement_cloud=${CLOUD}
--placement_region=${REGION}
--placement_zone=${ZONE}
--callhome_enabled=false
TSERVERCONF

chown -R yugabyte:yugabyte "$CONF_DIR"

# Create systemd units
cat > /etc/systemd/system/yb-master.service <<'YBMASTER'
[Unit]
Description=YugabyteDB Master
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=yugabyte
Group=yugabyte
ExecStart=/opt/yugabyte/bin/yb-master --flagfile /opt/yugabyte/conf/master.conf
Restart=always
RestartSec=5
LimitNOFILE=1048576
LimitNPROC=12000
LimitCORE=infinity

[Install]
WantedBy=multi-user.target
YBMASTER

cat > /etc/systemd/system/yb-tserver.service <<'YBTSERVER'
[Unit]
Description=YugabyteDB TServer
After=network-online.target yb-master.service
Wants=network-online.target

[Service]
Type=simple
User=yugabyte
Group=yugabyte
ExecStart=/opt/yugabyte/bin/yb-tserver --flagfile /opt/yugabyte/conf/tserver.conf
Restart=always
RestartSec=5
LimitNOFILE=1048576
LimitNPROC=12000
LimitCORE=infinity

[Install]
WantedBy=multi-user.target
YBTSERVER

systemctl daemon-reload

# Start yb-master first
systemctl enable --now yb-master
echo "Waiting for yb-master to start..."
sleep 5

# Then start yb-tserver
systemctl enable --now yb-tserver
echo "Waiting for yb-tserver to start..."
sleep 5

echo "YugabyteDB node $NODE_ID provisioned"
`, version, masterAddresses, host.ExternalIP, nodeID, rf, port, cloud, region, zone)

	result, err := y.ExecuteScript(ctx, host, installScript)
	if err != nil {
		return fmt.Errorf("failed to install YugabyteDB: %w", err)
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("YugabyteDB installation failed: %s", result.Stderr)
	}

	fmt.Printf("✓ YugabyteDB node %d provisioned on %s\n", nodeID, host.ExternalIP)
	return nil
}

// Validate checks if YugabyteDB YSQL is healthy
func (y *YugabyteProvisioner) Validate(ctx context.Context, host inventory.Host, config ServiceConfig) error {
	port := config.Port
	if port == 0 {
		port = 5433
	}

	// YSQL connectivity check — connect as yugabyte superuser
	connStr := fmt.Sprintf("host=%s port=%d user=yugabyte dbname=yugabyte sslmode=disable",
		host.ExternalIP, port)

	db, err := sql.Open("postgres", connStr)
	if err != nil {
		return fmt.Errorf("yugabyte YSQL connection failed: %w", err)
	}
	defer db.Close()

	var version string
	if err = db.QueryRowContext(ctx, "SELECT version()").Scan(&version); err != nil {
		return fmt.Errorf("yugabyte YSQL query failed: %w", err)
	}

	if !strings.Contains(version, "YugabyteDB") {
		return fmt.Errorf("unexpected version string (expected YugabyteDB): %s", version)
	}

	// Verify tablet servers are live
	tsResult, err := y.RunCommand(ctx, host,
		"/opt/yugabyte/bin/yb-admin -master_addresses $(cat /opt/yugabyte/conf/master.conf | grep master_addresses | cut -d= -f2) list_all_tablet_servers 2>/dev/null | grep -c ALIVE || echo 0")
	if err == nil && tsResult.ExitCode == 0 {
		count := strings.TrimSpace(tsResult.Stdout)
		fmt.Printf("  tablet servers alive: %s\n", count)
	}

	fmt.Printf("✓ YugabyteDB healthy on %s (YSQL port %d)\n", host.ExternalIP, port)
	return nil
}

// Initialize creates databases and application user via YSQL
func (y *YugabyteProvisioner) Initialize(ctx context.Context, host inventory.Host, config ServiceConfig) error {
	port := config.Port
	if port == 0 {
		port = 5433
	}

	// Connect as yugabyte superuser
	connStr := fmt.Sprintf("host=%s port=%d user=yugabyte dbname=yugabyte sslmode=disable",
		host.ExternalIP, port)

	db, err := sql.Open("postgres", connStr)
	if err != nil {
		return fmt.Errorf("failed to connect to YugabyteDB: %w", err)
	}
	defer db.Close()

	// Get databases from config
	databases, ok := config.Metadata["databases"].([]map[string]string)
	if !ok {
		databases = []map[string]string{{"name": "periscope"}}
	}

	var dbNames []string
	for _, dbEntry := range databases {
		dbName := dbEntry["name"]
		dbNames = append(dbNames, dbName)

		// CREATE DATABASE IF NOT EXISTS
		var exists bool
		err := db.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM pg_database WHERE datname = $1)", dbName).Scan(&exists)
		if err != nil {
			return fmt.Errorf("check database %s: %w", dbName, err)
		}
		if !exists {
			if _, err := db.ExecContext(ctx, fmt.Sprintf("CREATE DATABASE %s", pq.QuoteIdentifier(dbName))); err != nil {
				return fmt.Errorf("create database %s: %w", dbName, err)
			}
			fmt.Printf("Created database: %s\n", dbName)
		} else {
			fmt.Printf("Database %s already exists\n", dbName)
		}

		// Run embedded schema if available
		if err := y.initializeDatabase(ctx, host, port, dbName); err != nil {
			return fmt.Errorf("failed to initialize database %s: %w", dbName, err)
		}
	}

	// Create application user
	pgUser, _ := config.Metadata["postgres_user"].(string)
	pgPass, _ := config.Metadata["postgres_password"].(string)
	if pgUser != "" && pgPass != "" {
		if err := y.createApplicationUser(ctx, host, port, pgUser, pgPass, dbNames); err != nil {
			return fmt.Errorf("failed to create application user: %w", err)
		}
	}

	return nil
}

// Configure is a no-op for YugabyteDB — auth is configured via gflags during Provision
func (y *YugabyteProvisioner) Configure(ctx context.Context, host inventory.Host, config ServiceConfig) error {
	return nil
}

// initializeDatabase runs embedded SQL schema for a specific database
func (y *YugabyteProvisioner) initializeDatabase(ctx context.Context, host inventory.Host, port int, dbName string) error {
	schemaFiles := map[string]string{
		"quartermaster": "schema/quartermaster.sql",
		"commodore":     "schema/commodore.sql",
		"foghorn":       "schema/foghorn.sql",
		"periscope":     "schema/periscope.sql",
		"purser":        "schema/purser.sql",
		"listmonk":      "schema/listmonk.sql",
		"navigator":     "schema/navigator.sql",
		"skipper":       "schema/skipper.sql",
	}

	schemaFile, ok := schemaFiles[dbName]
	if !ok {
		return nil
	}

	sqlContent, err := dbsql.Content.ReadFile(schemaFile)
	if err != nil {
		return fmt.Errorf("failed to read embedded SQL file %s: %w", schemaFile, err)
	}

	connStr := fmt.Sprintf("host=%s port=%d user=yugabyte dbname=%s sslmode=disable",
		host.ExternalIP, port, dbName)

	fmt.Printf("Applying schema for %s...\n", dbName)
	return ExecuteSQLFile(ctx, connStr, string(sqlContent))
}

// createApplicationUser creates a YSQL role for application services (same SQL as Postgres)
func (y *YugabyteProvisioner) createApplicationUser(ctx context.Context, host inventory.Host, port int, user, password string, databases []string) error {
	connStr := fmt.Sprintf("host=%s port=%d user=yugabyte dbname=yugabyte sslmode=disable",
		host.ExternalIP, port)

	db, err := sql.Open("postgres", connStr)
	if err != nil {
		return fmt.Errorf("connect as superuser: %w", err)
	}
	defer db.Close()

	var exists bool
	err = db.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM pg_roles WHERE rolname = $1)", user).Scan(&exists)
	if err != nil {
		return fmt.Errorf("check role existence: %w", err)
	}

	quotedUser := pq.QuoteIdentifier(user)
	if !exists {
		if _, err := db.ExecContext(ctx, fmt.Sprintf("CREATE ROLE %s WITH LOGIN", quotedUser)); err != nil {
			return fmt.Errorf("create role %s: %w", user, err)
		}
		fmt.Printf("Created YSQL role: %s\n", user)
	}

	// Set password (handles rotation on re-provision)
	if _, err := db.ExecContext(ctx, fmt.Sprintf("ALTER ROLE %s WITH PASSWORD %s", quotedUser, pq.QuoteLiteral(password))); err != nil {
		return fmt.Errorf("set password for %s: %w", user, err)
	}

	for _, dbName := range databases {
		quotedDB := pq.QuoteIdentifier(dbName)
		if _, err := db.ExecContext(ctx, fmt.Sprintf("GRANT ALL PRIVILEGES ON DATABASE %s TO %s", quotedDB, quotedUser)); err != nil {
			return fmt.Errorf("grant on %s: %w", dbName, err)
		}

		dbConn, err := sql.Open("postgres", fmt.Sprintf("host=%s port=%d user=yugabyte dbname=%s sslmode=disable", host.ExternalIP, port, dbName))
		if err != nil {
			return fmt.Errorf("connect to %s: %w", dbName, err)
		}
		schemaGrants := []string{
			fmt.Sprintf("GRANT USAGE ON SCHEMA public TO %s", quotedUser),
			fmt.Sprintf("GRANT ALL PRIVILEGES ON ALL TABLES IN SCHEMA public TO %s", quotedUser),
			fmt.Sprintf("GRANT ALL PRIVILEGES ON ALL SEQUENCES IN SCHEMA public TO %s", quotedUser),
			fmt.Sprintf("ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT ALL PRIVILEGES ON TABLES TO %s", quotedUser),
			fmt.Sprintf("ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT ALL PRIVILEGES ON SEQUENCES TO %s", quotedUser),
		}
		for _, stmt := range schemaGrants {
			if _, err := dbConn.ExecContext(ctx, stmt); err != nil {
				dbConn.Close()
				return fmt.Errorf("schema grant on %s: %w", dbName, err)
			}
		}
		dbConn.Close()
	}

	fmt.Printf("✓ YSQL role %s ready (grants on %d databases)\n", user, len(databases))
	return nil
}

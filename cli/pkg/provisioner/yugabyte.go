package provisioner

import (
	"context"
	"fmt"
	"strings"

	"frameworks/cli/pkg/detect"
	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/ssh"
)

// YugabyteProvisioner provisions YugabyteDB nodes (yb-master + yb-tserver)
type YugabyteProvisioner struct {
	*BaseProvisioner
	sql SQLExecutor
}

// NewYugabyteProvisioner creates a new YugabyteDB provisioner
func NewYugabyteProvisioner(pool *ssh.Pool, opts ...ProvisionerOption) (Provisioner, error) {
	p := &YugabyteProvisioner{
		BaseProvisioner: NewBaseProvisioner("yugabyte", pool),
		sql:             &DirectExecutor{},
	}
	for _, opt := range opts {
		opt.applyYugabyte(p)
	}
	return p, nil
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
shell=/usr/bin/nologin
[ ! -x "$shell" ] && shell=/sbin/nologin
[ ! -x "$shell" ] && shell=/bin/false
id -u yugabyte &>/dev/null || useradd -r -s "$shell" yugabyte

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
elif command -v dnf >/dev/null; then
  dnf install -y -q chrony curl >/dev/null 2>&1
elif command -v pacman >/dev/null; then
  pacman -Syu --noconfirm --needed chrony curl >/dev/null 2>&1
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

	conn := ConnParams{Host: host.ExternalIP, Port: port, User: "yugabyte", Database: "yugabyte"}

	var version string
	if err := y.sql.QueryRow(ctx, conn, "SELECT version()", nil, &version); err != nil {
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

	conn := ConnParams{Host: host.ExternalIP, Port: port, User: "yugabyte", Database: "yugabyte"}

	databases, ok := config.Metadata["databases"].([]map[string]string)
	if !ok {
		databases = []map[string]string{{"name": "periscope"}}
	}

	var dbNames []string
	for _, dbEntry := range databases {
		dbName := dbEntry["name"]
		dbNames = append(dbNames, dbName)

		created, err := CreateDatabaseIfNotExists(ctx, y.sql, conn, dbName, "")
		if err != nil {
			return fmt.Errorf("failed to create database %s: %w", dbName, err)
		}

		if created {
			fmt.Printf("Created database: %s\n", dbName)
		} else {
			fmt.Printf("Database %s already exists\n", dbName)
		}

		dbConn := ConnParams{Host: host.ExternalIP, Port: port, User: "yugabyte", Database: dbName}
		if err := pgInitializeDatabase(ctx, y.sql, dbConn, dbName); err != nil {
			return fmt.Errorf("failed to initialize database %s: %w", dbName, err)
		}
	}

	pgPass, _ := config.Metadata["postgres_password"].(string)
	if pgPass != "" {
		ownerDBs := make(map[string][]string)
		for _, db := range databases {
			owner := db["owner"]
			if owner == "" {
				owner = db["name"]
			}
			ownerDBs[owner] = append(ownerDBs[owner], db["name"])
		}
		for owner, dbs := range ownerDBs {
			if err := pgCreateApplicationUser(ctx, y.sql, conn, owner, pgPass, dbs); err != nil {
				return fmt.Errorf("failed to create application user %s: %w", owner, err)
			}
		}
	}

	return nil
}

// Configure is a no-op for YugabyteDB — auth is configured via gflags during Provision
func (y *YugabyteProvisioner) Configure(ctx context.Context, host inventory.Host, config ServiceConfig) error {
	return nil
}

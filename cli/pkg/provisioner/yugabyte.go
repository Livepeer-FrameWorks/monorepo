package provisioner

import (
	"context"
	"fmt"
	"strings"

	"frameworks/cli/pkg/ansible"
	"frameworks/cli/pkg/detect"
	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/ssh"
)

// YugabyteNativeParams bundles the inputs needed to render the flagfile
// configs for a single Yugabyte node.
type YugabyteNativeParams struct {
	MasterAddresses string
	NodeIP          string
	DataDir         string
	RF              int
	YSQLPort        int
	Cloud           string
	Region          string
	Zone            string
}

// BuildYugabyteMasterConf returns the yb-master flagfile bytes.
func BuildYugabyteMasterConf(p YugabyteNativeParams) []byte {
	return fmt.Appendf(nil, `--master_addresses=%s
--rpc_bind_addresses=%s:7100
--webserver_interface=%s
--fs_data_dirs=%s
--replication_factor=%d
--placement_cloud=%s
--placement_region=%s
--placement_zone=%s
--leader_failure_max_missed_heartbeat_periods=10
--callhome_enabled=false
`, p.MasterAddresses, p.NodeIP, p.NodeIP, p.DataDir, p.RF, p.Cloud, p.Region, p.Zone)
}

// BuildYugabyteTServerConf returns the yb-tserver flagfile bytes.
func BuildYugabyteTServerConf(p YugabyteNativeParams) []byte {
	return fmt.Appendf(nil, `--tserver_master_addrs=%s
--rpc_bind_addresses=%s:9100
--webserver_interface=%s
--pgsql_proxy_bind_address=0.0.0.0:%d
--cql_proxy_bind_address=0.0.0.0:9042
--fs_data_dirs=%s
--ysql_enable_auth=true
--ysql_hba_conf_csv="host all all 0.0.0.0/0 scram-sha-256,host all all ::0/0 scram-sha-256"
--placement_cloud=%s
--placement_region=%s
--placement_zone=%s
--callhome_enabled=false
`, p.MasterAddresses, p.NodeIP, p.NodeIP, p.YSQLPort, p.DataDir, p.Cloud, p.Region, p.Zone)
}

// BuildYugabyteMasterUnit returns the yb-master.service bytes.
func BuildYugabyteMasterUnit() []byte {
	return []byte(`[Unit]
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
`)
}

// BuildYugabyteTServerUnit returns the yb-tserver.service bytes.
func BuildYugabyteTServerUnit() []byte {
	return []byte(`[Unit]
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
`)
}

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

	cloud := "frameworks"
	region := "eu"
	zone := fmt.Sprintf("eu-%d", nodeID)

	params := YugabyteNativeParams{
		MasterAddresses: masterAddresses,
		NodeIP:          host.ExternalIP,
		DataDir:         "/var/lib/yugabyte/data",
		RF:              rf,
		YSQLPort:        port,
		Cloud:           cloud,
		Region:          region,
		Zone:            zone,
	}
	masterConf := string(BuildYugabyteMasterConf(params))
	tserverConf := string(BuildYugabyteTServerConf(params))
	masterUnit := string(BuildYugabyteMasterUnit())
	tserverUnit := string(BuildYugabyteTServerUnit())

	installScript := fmt.Sprintf(`#!/bin/bash
set -e

VERSION="%s"
NODE_ID=%d
MASTER_CONF_CONTENT=$(cat <<'FRAMEWORKS_YB_MASTER_CONF_EOF'
%s
FRAMEWORKS_YB_MASTER_CONF_EOF
)
TSERVER_CONF_CONTENT=$(cat <<'FRAMEWORKS_YB_TSERVER_CONF_EOF'
%s
FRAMEWORKS_YB_TSERVER_CONF_EOF
)
MASTER_UNIT_CONTENT=$(cat <<'FRAMEWORKS_YB_MASTER_UNIT_EOF'
%s
FRAMEWORKS_YB_MASTER_UNIT_EOF
)
TSERVER_UNIT_CONTENT=$(cat <<'FRAMEWORKS_YB_TSERVER_UNIT_EOF'
%s
FRAMEWORKS_YB_TSERVER_UNIT_EOF
)

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

# Ensure curl is available (yugabyte installer downloads the tarball).
if ! command -v curl >/dev/null 2>&1; then
  if command -v apt-get >/dev/null; then
    apt-get -o DPkg::Lock::Timeout=300 update -qq
    apt-get -o DPkg::Lock::Timeout=300 install -y -qq curl
  elif command -v dnf >/dev/null; then
    dnf install -y -q curl
  elif command -v yum >/dev/null; then
    yum install -y -q curl
  elif command -v pacman >/dev/null; then
    pacman -Syu --noconfirm --needed curl
  fi
fi

# Clock sync — critical for distributed consensus. Skip if any time-sync
# daemon is already active; install chrony only when none is.
__FRAMEWORKS_INSTALL_TIMESYNC__

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

printf '%%s' "${MASTER_CONF_CONTENT}" > "$CONF_DIR/master.conf"
printf '%%s' "${TSERVER_CONF_CONTENT}" > "$CONF_DIR/tserver.conf"

chown -R yugabyte:yugabyte "$CONF_DIR"

printf '%%s' "${MASTER_UNIT_CONTENT}" > /etc/systemd/system/yb-master.service
printf '%%s' "${TSERVER_UNIT_CONTENT}" > /etc/systemd/system/yb-tserver.service

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
`, version, nodeID, masterConf, tserverConf, masterUnit, tserverUnit)
	installScript = strings.Replace(installScript, "__FRAMEWORKS_INSTALL_TIMESYNC__", ansible.TimeSyncInstallSnippet, 1)

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

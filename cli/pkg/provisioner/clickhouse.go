package provisioner

import (
	"context"
	"fmt"

	"frameworks/cli/pkg/detect"
	"frameworks/cli/pkg/health"
	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/ssh"
	dbsql "frameworks/pkg/database/sql"
)

// ClickHouseProvisioner provisions ClickHouse
type ClickHouseProvisioner struct {
	*BaseProvisioner
	ch CHExecutor
}

// NewClickHouseProvisioner creates a new ClickHouse provisioner
func NewClickHouseProvisioner(pool *ssh.Pool, opts ...ProvisionerOption) (*ClickHouseProvisioner, error) {
	p := &ClickHouseProvisioner{
		BaseProvisioner: NewBaseProvisioner("clickhouse", pool),
		ch:              &DirectCHExecutor{},
	}
	for _, opt := range opts {
		opt.applyClickHouse(p)
	}
	return p, nil
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
  PKG_MGR="$1"
  cat >/etc/yum.repos.d/clickhouse.repo <<'REPO'
[clickhouse]
name=ClickHouse
baseurl=https://packages.clickhouse.com/rpm/stable/
enabled=1
gpgcheck=1
gpgkey=https://packages.clickhouse.com/rpm/stable/repodata/repomd.xml.key
REPO
  if [ -n "$VERSION" ]; then
    "$PKG_MGR" install -y "clickhouse-server-$VERSION" "clickhouse-client-$VERSION"
  else
    "$PKG_MGR" install -y clickhouse-server clickhouse-client
  fi
}

install_clickhouse_arch() {
  pacman -Syu --noconfirm --needed curl tar

  checksum_value() {
    awk 'NF { print $1; exit }' "$1"
  }

  verify_checksum() {
    local algorithm="$1" file="$2" checksum_file="$3" expected actual
    expected="$(checksum_value "$checksum_file")"
    [ -n "$expected" ] || { echo "missing checksum in $checksum_file" >&2; exit 1; }
    case "$algorithm" in
      sha512)
        if command -v sha512sum >/dev/null 2>&1; then
          actual="$(sha512sum "$file" | awk '{print $1}')"
        elif command -v shasum >/dev/null 2>&1; then
          actual="$(shasum -a 512 "$file" | awk '{print $1}')"
        else
          actual="$(openssl dgst -sha512 "$file" | awk '{print $NF}')"
        fi
        ;;
      *)
        echo "unsupported checksum algorithm: $algorithm" >&2
        exit 1
        ;;
    esac
    [ "$actual" = "$expected" ] || {
      echo "checksum mismatch for $file" >&2
      echo "expected: $expected" >&2
      echo "actual:   $actual" >&2
      exit 1
    }
  }

  arch=$(uname -m)
  case "$arch" in
    x86_64) ch_arch="amd64" ;;
    aarch64|arm64) ch_arch="arm64" ;;
    *)
      echo "unsupported architecture: $arch" >&2
      exit 1
      ;;
  esac

  if [ -n "$VERSION" ]; then
    archive="clickhouse-common-static-${VERSION}-${ch_arch}.tgz"
  else
    archive=$(curl -fsSL https://packages.clickhouse.com/tgz/stable/ | grep -o "clickhouse-common-static-[0-9][^\"']*-${ch_arch}\.tgz" | sort -V | tail -n 1)
  fi
  if [ -z "$archive" ]; then
    echo "failed to resolve clickhouse static archive" >&2
    exit 1
  fi

  curl -fsSL -o /tmp/clickhouse.tgz "https://packages.clickhouse.com/tgz/stable/${archive}"
  curl -fsSL -o /tmp/clickhouse.tgz.sha512 "https://packages.clickhouse.com/tgz/stable/${archive}.sha512"
  verify_checksum sha512 /tmp/clickhouse.tgz /tmp/clickhouse.tgz.sha512
  topdir=$(tar -tzf /tmp/clickhouse.tgz | head -n 1 | cut -d/ -f1)
  rm -rf "/tmp/${topdir}"
  tar -xzf /tmp/clickhouse.tgz -C /tmp
  "/tmp/${topdir}/usr/bin/clickhouse" install --noninteractive --user clickhouse --group clickhouse
  rm -rf "/tmp/${topdir}" /tmp/clickhouse.tgz /tmp/clickhouse.tgz.sha512
}

if command -v apt-get >/dev/null; then
  install_clickhouse_apt
elif command -v dnf >/dev/null; then
  install_clickhouse_yum dnf
elif command -v yum >/dev/null; then
  install_clickhouse_yum yum
elif command -v pacman >/dev/null; then
  install_clickhouse_arch
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

// Initialize creates databases and tables.
// Connects as "default" with no password because this runs before Configure
// sets up authenticated users. Callers must ensure Initialize runs before
// Configure to avoid an unauthenticated-access window after auth is applied.
func (c *ClickHouseProvisioner) Initialize(ctx context.Context, host inventory.Host, config ServiceConfig) error {
	databases, ok := config.Metadata["databases"].([]string)
	if !ok {
		databases = []string{"periscope"}
	}

	for _, dbName := range databases {
		query := fmt.Sprintf("CREATE DATABASE IF NOT EXISTS %s", dbName)
		if err := c.ch.Exec(ctx, host.ExternalIP, config.Port, "default", "", "default", query); err != nil {
			return fmt.Errorf("failed to create database %s: %w", dbName, err)
		}

		fmt.Printf("✓ Database %s ready\n", dbName)

		if dbName == "periscope" {
			sqlContent, err := dbsql.Content.ReadFile("clickhouse/periscope.sql")
			if err != nil {
				return fmt.Errorf("failed to read embedded ClickHouse schema: %w", err)
			}
			if err := c.ch.Exec(ctx, host.ExternalIP, config.Port, "default", "", "periscope", string(sqlContent)); err != nil {
				return fmt.Errorf("failed to initialize periscope database: %w", err)
			}
			fmt.Println("✓ ClickHouse periscope schema initialized")
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

	if err := c.ch.Exec(ctx, host.ExternalIP, config.Port, username, password, "periscope", string(sqlContent)); err != nil {
		return fmt.Errorf("apply clickhouse demo seed: %w", err)
	}
	return nil
}

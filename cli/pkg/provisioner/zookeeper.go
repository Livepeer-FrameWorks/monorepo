package provisioner

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"frameworks/cli/pkg/detect"
	"frameworks/cli/pkg/health"
	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/ssh"
)

const defaultZookeeperImage = "confluentinc/cp-zookeeper:7.4.0"
const defaultApacheZookeeperVersion = "3.9.2"

// ZookeeperProvisioner provisions Zookeeper nodes.
type ZookeeperProvisioner struct {
	*BaseProvisioner
}

// NewZookeeperProvisioner creates a new Zookeeper provisioner.
func NewZookeeperProvisioner(pool *ssh.Pool) (*ZookeeperProvisioner, error) {
	return &ZookeeperProvisioner{
		BaseProvisioner: NewBaseProvisioner("zookeeper", pool),
	}, nil
}

// Detect checks if Zookeeper is installed and running.
func (z *ZookeeperProvisioner) Detect(ctx context.Context, host inventory.Host) (*detect.ServiceState, error) {
	return z.CheckExists(ctx, host, "zookeeper")
}

// Provision installs Zookeeper using Docker or native systemd.
func (z *ZookeeperProvisioner) Provision(ctx context.Context, host inventory.Host, config ServiceConfig) error {
	state, err := z.Detect(ctx, host)
	if err == nil && state.Exists && state.Running {
		return nil
	}

	port := config.Port
	if port == 0 {
		port = 2181
	}

	switch config.Mode {
	case "docker":
		return z.provisionDocker(ctx, host, config, port)
	case "native":
		if err := validateApacheZookeeperVersion(config.Version); err != nil {
			return err
		}
		return z.provisionNative(ctx, host, config, port)
	default:
		return fmt.Errorf("unsupported zookeeper mode %q (must be docker or native)", config.Mode)
	}
}

func (z *ZookeeperProvisioner) provisionDocker(ctx context.Context, host inventory.Host, config ServiceConfig, port int) error {

	image := config.Image
	if image == "" {
		image = defaultZookeeperImage
	}

	envVars := map[string]string{
		"ZOOKEEPER_CLIENT_PORT": fmt.Sprintf("%d", port),
		"ZOOKEEPER_TICK_TIME":   "2000",
	}

	if serverID, ok := config.Metadata["server_id"].(int); ok && serverID > 0 {
		envVars["ZOOKEEPER_SERVER_ID"] = fmt.Sprintf("%d", serverID)
	}

	if servers, ok := config.Metadata["servers"].([]string); ok && len(servers) > 0 {
		envVars["ZOOKEEPER_SERVERS"] = strings.Join(servers, " ")
	}

	envFileContent := GenerateEnvFile("zookeeper", envVars)
	tmpEnvFile := filepath.Join(os.TempDir(), "zookeeper.env")
	if writeErr := os.WriteFile(tmpEnvFile, []byte(envFileContent), 0600); writeErr != nil {
		return writeErr
	}
	defer os.Remove(tmpEnvFile)

	remoteEnvFile := "/etc/frameworks/zookeeper.env"
	if uploadErr := z.UploadFile(ctx, host, ssh.UploadOptions{LocalPath: tmpEnvFile, RemotePath: remoteEnvFile, Mode: 0600}); uploadErr != nil {
		return uploadErr
	}

	composeData := DockerComposeData{
		ServiceName: "zookeeper",
		Image:       image,
		Port:        port,
		EnvFile:     remoteEnvFile,
		Networks:    []string{"frameworks"},
		Volumes: []string{
			"/var/lib/frameworks/zookeeper:/var/lib/zookeeper",
		},
	}

	composeYAML, err := GenerateDockerCompose(composeData)
	if err != nil {
		return fmt.Errorf("failed to generate docker-compose: %w", err)
	}

	tmpDir, err := os.MkdirTemp("", "zookeeper-*")
	if err != nil {
		return fmt.Errorf("failed to create temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	composePath := filepath.Join(tmpDir, "docker-compose.yml")
	if err := os.WriteFile(composePath, []byte(composeYAML), 0644); err != nil {
		return fmt.Errorf("failed to write docker-compose.yml: %w", err)
	}

	if _, err := z.RunCommand(ctx, host, "mkdir -p /opt/frameworks/zookeeper"); err != nil {
		return fmt.Errorf("failed to create remote zookeeper directory: %w", err)
	}

	remotePath := "/opt/frameworks/zookeeper/docker-compose.yml"
	if err := z.UploadFile(ctx, host, ssh.UploadOptions{
		LocalPath:  composePath,
		RemotePath: remotePath,
		Mode:       0644,
	}); err != nil {
		return fmt.Errorf("failed to upload docker-compose.yml: %w", err)
	}

	composeCmd := "cd /opt/frameworks/zookeeper && docker compose pull && docker compose up -d"
	result, err := z.RunCommand(ctx, host, composeCmd)
	if err != nil || result.ExitCode != 0 {
		return fmt.Errorf("docker compose failed: %s\nStderr: %s", composeCmd, result.Stderr)
	}

	return nil
}

func (z *ZookeeperProvisioner) provisionNative(ctx context.Context, host inventory.Host, config ServiceConfig, port int) error {
	version := resolveZookeeperNativeVersion(config.Version)
	serverID := zookeeperServerID(config.Metadata["server_id"])
	serverLines := strings.Join(zookeeperServerList(config.Metadata["servers"]), "\n")

	installScript := fmt.Sprintf(`#!/bin/bash
set -euo pipefail

VERSION="%s"
CLIENT_PORT="%d"
SERVER_ID="%d"
SERVER_LINES=$(cat <<'EOF'
%s
EOF
)

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

shell=/usr/bin/nologin
[ ! -x "$shell" ] && shell=/sbin/nologin
[ ! -x "$shell" ] && shell=/bin/false

if command -v apt-get >/dev/null 2>&1; then
  apt-get -o DPkg::Lock::Timeout=300 update
  DEBIAN_FRONTEND=noninteractive apt-get -o DPkg::Lock::Timeout=300 install -y curl ca-certificates default-jre-headless
elif command -v dnf >/dev/null 2>&1; then
  dnf install -y curl java-17-openjdk-headless
elif command -v yum >/dev/null 2>&1; then
  yum install -y curl java-17-openjdk-headless
elif command -v pacman >/dev/null 2>&1; then
  pacman -Syu --noconfirm --needed curl jre-openjdk-headless
else
  echo "unsupported package manager" >&2
  exit 1
fi

getent group zookeeper >/dev/null || groupadd --system zookeeper
id -u zookeeper >/dev/null 2>&1 || useradd -r -g zookeeper -s "$shell" zookeeper

mkdir -p /opt /etc/zookeeper /var/lib/zookeeper/data /var/lib/zookeeper/log
if [ ! -x /opt/zookeeper/bin/zkServer.sh ]; then
  rm -rf /opt/zookeeper /tmp/apache-zookeeper-${VERSION}-bin
  curl -fsSL -o /tmp/zookeeper.tgz "https://downloads.apache.org/zookeeper/zookeeper-${VERSION}/apache-zookeeper-${VERSION}-bin.tar.gz"
  curl -fsSL -o /tmp/zookeeper.tgz.sha512 "https://downloads.apache.org/zookeeper/zookeeper-${VERSION}/apache-zookeeper-${VERSION}-bin.tar.gz.sha512"
  verify_checksum sha512 /tmp/zookeeper.tgz /tmp/zookeeper.tgz.sha512
  tar -xzf /tmp/zookeeper.tgz -C /tmp
  mv /tmp/apache-zookeeper-${VERSION}-bin /opt/zookeeper
  rm -f /tmp/zookeeper.tgz /tmp/zookeeper.tgz.sha512
fi

cat > /etc/zookeeper/zoo.cfg <<EOF
tickTime=2000
initLimit=10
syncLimit=5
dataDir=/var/lib/zookeeper/data
dataLogDir=/var/lib/zookeeper/log
clientPort=${CLIENT_PORT}
autopurge.snapRetainCount=3
autopurge.purgeInterval=24
${SERVER_LINES}
EOF

if [ "${SERVER_ID}" -gt 0 ]; then
  echo "${SERVER_ID}" > /var/lib/zookeeper/data/myid
fi

cat > /etc/systemd/system/frameworks-zookeeper.service <<'EOF'
[Unit]
Description=FrameWorks ZooKeeper
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=zookeeper
Group=zookeeper
Environment=ZOO_LOG_DIR=/var/lib/zookeeper/log
ExecStart=/opt/zookeeper/bin/zkServer.sh start-foreground /etc/zookeeper/zoo.cfg
Restart=always
RestartSec=5
LimitNOFILE=65535

[Install]
WantedBy=multi-user.target
EOF

chown -R zookeeper:zookeeper /opt/zookeeper /etc/zookeeper /var/lib/zookeeper
systemctl daemon-reload
systemctl enable --now frameworks-zookeeper
`, version, port, serverID, serverLines)

	result, err := z.ExecuteScript(ctx, host, installScript)
	if err != nil {
		return fmt.Errorf("failed to install Zookeeper: %w", err)
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("zookeeper installation failed: %s", result.Stderr)
	}

	return nil
}

// Validate checks if Zookeeper is healthy.
func (z *ZookeeperProvisioner) Validate(ctx context.Context, host inventory.Host, config ServiceConfig) error {
	checker := &health.TCPChecker{}
	result := checker.Check(host.ExternalIP, config.Port)
	if !result.OK {
		return fmt.Errorf("zookeeper health check failed: %s", result.Error)
	}
	return nil
}

// Initialize is a no-op for Zookeeper.
func (z *ZookeeperProvisioner) Initialize(ctx context.Context, host inventory.Host, config ServiceConfig) error {
	return nil
}

func resolveZookeeperNativeVersion(version string) string {
	version = strings.TrimSpace(version)
	if version == "" {
		return defaultApacheZookeeperVersion
	}
	return version
}

func validateApacheZookeeperVersion(version string) error {
	version = strings.TrimSpace(version)
	if version == "" {
		return nil
	}
	if strings.HasPrefix(version, "7.") {
		return fmt.Errorf("zookeeper native mode expects an Apache ZooKeeper version such as 3.9.2; got %q", version)
	}
	return nil
}

func zookeeperServerID(raw any) int {
	switch v := raw.(type) {
	case int:
		return v
	case int32:
		return int(v)
	case int64:
		return int(v)
	case float64:
		return int(v)
	default:
		return 0
	}
}

func zookeeperServerList(raw any) []string {
	switch v := raw.(type) {
	case []string:
		return v
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			if value, ok := item.(string); ok && strings.TrimSpace(value) != "" {
				out = append(out, value)
			}
		}
		return out
	default:
		return nil
	}
}

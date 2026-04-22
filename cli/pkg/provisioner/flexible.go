package provisioner

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"frameworks/cli/pkg/detect"
	"frameworks/cli/pkg/gitops"
	"frameworks/cli/pkg/health"
	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/ssh"
)

// FlexibleProvisioner provisions services in either Docker or native mode
type FlexibleProvisioner struct {
	*BaseProvisioner
	serviceName string
	port        int
	healthPath  string
}

// NewFlexibleProvisioner creates a new flexible provisioner
func NewFlexibleProvisioner(serviceName string, port int, pool *ssh.Pool) *FlexibleProvisioner {
	return &FlexibleProvisioner{
		BaseProvisioner: NewBaseProvisioner(serviceName, pool),
		serviceName:     serviceName,
		port:            port,
		healthPath:      "/health",
	}
}

// Detect checks if the service exists
func (f *FlexibleProvisioner) Detect(ctx context.Context, host inventory.Host) (*detect.ServiceState, error) {
	return f.CheckExists(ctx, host, f.serviceName)
}

// Provision installs the service in Docker or native mode
func (f *FlexibleProvisioner) Provision(ctx context.Context, host inventory.Host, config ServiceConfig) error {
	state, err := f.Detect(ctx, host)
	if err != nil {
		state = nil
	}

	// Allow explicit image/binary overrides from manifest
	if config.Mode == "docker" && config.Image != "" {
		if skip, reason := shouldSkipProvision(state, config, "", config.Image); skip {
			fmt.Printf("Service %s already running (%s), skipping...\n", f.serviceName, reason)
			return nil
		}
		return f.provisionDocker(ctx, host, config, &gitops.ServiceInfo{FullImage: config.Image})
	}
	if config.Mode == "native" && config.BinaryURL != "" {
		desiredVersion := ""
		if config.Version != "" && config.Version != "stable" {
			desiredVersion = config.Version
		}
		if skip, reason := shouldSkipProvision(state, config, desiredVersion, ""); skip {
			fmt.Printf("Service %s already running (%s), skipping...\n", f.serviceName, reason)
			return nil
		}
		return f.provisionNative(ctx, host, config, &gitops.ServiceInfo{Binaries: map[string]string{"*": config.BinaryURL}})
	}

	// Fetch manifest from gitops
	channel, version := gitops.ResolveVersion(config.Version)
	manifest, err := fetchGitopsManifest(channel, version, config.Metadata)
	if err != nil {
		return fmt.Errorf("failed to fetch gitops manifest: %w", err)
	}

	svcInfo, err := manifest.GetServiceInfo(f.serviceName)
	if err != nil {
		return fmt.Errorf("service not found in manifest: %w", err)
	}

	if skip, reason := shouldSkipProvision(state, config, svcInfo.Version, svcInfo.FullImage); skip {
		fmt.Printf("Service %s already running (%s), skipping...\n", f.serviceName, reason)
		return nil
	}

	// Provision based on mode
	switch config.Mode {
	case "docker":
		return f.provisionDocker(ctx, host, config, svcInfo)
	case "native":
		return f.provisionNative(ctx, host, config, svcInfo)
	default:
		return fmt.Errorf("unsupported mode: %s (must be docker or native)", config.Mode)
	}
}

func shouldSkipProvision(state *detect.ServiceState, config ServiceConfig, desiredVersion, desiredImage string) (bool, string) {
	if config.Force || state == nil || !state.Exists || !state.Running {
		return false, ""
	}

	if desiredImage != "" {
		if state.Metadata["image"] == desiredImage {
			return true, fmt.Sprintf("image matches %s", desiredImage)
		}
		return false, ""
	}

	if desiredVersion != "" {
		if state.Version != "" && state.Version == desiredVersion {
			return true, fmt.Sprintf("version matches %s", desiredVersion)
		}
		return false, ""
	}

	return true, "already running"
}

// provisionDocker provisions the service using Docker
func (f *FlexibleProvisioner) provisionDocker(ctx context.Context, host inventory.Host, config ServiceConfig, svcInfo *gitops.ServiceInfo) error {
	fmt.Printf("Provisioning %s in Docker mode...\n", f.serviceName)

	port := f.port
	if config.Port != 0 {
		port = config.Port
	}

	// Write env file to remote host before compose references it
	svcEnvFile := fmt.Sprintf("/etc/frameworks/%s.env", f.serviceName)
	if err := f.writeServiceEnvFile(ctx, host, svcEnvFile, config); err != nil {
		fmt.Printf("    Warning: could not write env file %s: %v\n", svcEnvFile, err)
	}

	// Generate docker-compose.yml
	envFile := config.EnvFile
	if envFile == "" {
		envFile = svcEnvFile
	}

	// Use merged env vars from config, falling back to metadata for CLUSTER_ID/NODE_ID
	envVars := make(map[string]string)
	for k, v := range config.EnvVars {
		envVars[k] = v
	}
	if len(envVars) == 0 {
		if clusterID, ok := config.Metadata["cluster_id"].(string); ok && clusterID != "" {
			envVars["CLUSTER_ID"] = clusterID
		}
		if nodeID, ok := config.Metadata["node_id"].(string); ok && nodeID != "" {
			envVars["NODE_ID"] = nodeID
		}
	}

	composeData := DockerComposeData{
		ServiceName: f.serviceName,
		Image:       svcInfo.FullImage, // image@sha256:digest format
		Port:        port,
		EnvFile:     envFile,
		Environment: envVars,
		HealthCheck: &HealthCheckConfig{
			Test:     []string{"CMD", "curl", "-f", fmt.Sprintf("http://localhost:%d%s", port, f.healthPath)},
			Interval: "30s",
			Timeout:  "10s",
			Retries:  3,
		},
		Networks: []string{"frameworks"},
		Volumes: []string{
			fmt.Sprintf("/var/log/frameworks/%s:/var/log/frameworks", f.serviceName),
			fmt.Sprintf("/var/lib/frameworks/%s:/var/lib/frameworks", f.serviceName),
		},
	}

	composeYAML, err := GenerateDockerCompose(composeData)
	if err != nil {
		return fmt.Errorf("failed to generate docker-compose: %w", err)
	}

	// Create local temp file
	tmpDir, err := os.MkdirTemp("", f.serviceName+"-*")
	if err != nil {
		return fmt.Errorf("failed to create temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	composePath := filepath.Join(tmpDir, "docker-compose.yml")
	if err := os.WriteFile(composePath, []byte(composeYAML), 0644); err != nil {
		return fmt.Errorf("failed to write docker-compose.yml: %w", err)
	}

	// Upload to host
	remotePath := fmt.Sprintf("/opt/frameworks/%s/docker-compose.yml", f.serviceName)
	if err := f.UploadFile(ctx, host, ssh.UploadOptions{
		LocalPath:  composePath,
		RemotePath: remotePath,
		Mode:       0644,
	}); err != nil {
		return fmt.Errorf("failed to upload docker-compose.yml: %w", err)
	}

	// Pull and start with docker compose
	composeCmd := fmt.Sprintf("cd /opt/frameworks/%s && docker compose pull", f.serviceName)
	if !config.DeferStart {
		composeCmd += " && docker compose up -d"
	}
	result, err := f.RunCommand(ctx, host, composeCmd)
	if err != nil || result.ExitCode != 0 {
		return fmt.Errorf("docker compose failed: %s\nStderr: %s", composeCmd, result.Stderr)
	}

	if config.DeferStart {
		fmt.Printf("⏸ %s deployed but NOT started (missing required config)\n", f.serviceName)
	} else {
		fmt.Printf("✓ %s provisioned in Docker mode\n", f.serviceName)
	}
	return nil
}

// provisionNative provisions the service as a native binary with systemd
func (f *FlexibleProvisioner) provisionNative(ctx context.Context, host inventory.Host, config ServiceConfig, svcInfo *gitops.ServiceInfo) error {
	fmt.Printf("Provisioning %s in native mode...\n", f.serviceName)

	// Get binary URL for target OS/arch (or explicit override)
	binaryURL := config.BinaryURL
	var err error
	if binaryURL == "" {
		if svcInfo.Binaries != nil {
			if v, ok := svcInfo.Binaries["*"]; ok && v != "" {
				binaryURL = v
			}
		}
	}
	if binaryURL == "" {
		remoteOS, remoteArch, archErr := f.DetectRemoteArch(ctx, host)
		if archErr != nil {
			return fmt.Errorf("failed to detect remote architecture: %w", archErr)
		}
		binaryURL, err = svcInfo.GetBinaryURL(remoteOS, remoteArch)
	}
	if err != nil {
		return fmt.Errorf("binary not available: %w", err)
	}

	// Download and install binary with checksum verification
	checksumURL := binaryURL + ".sha256"
	installScript := fmt.Sprintf(`#!/bin/bash
set -e

checksum_value() {
  awk '{print $1}' "$1" | tr -d '[:space:]'
}

verify_checksum() {
  local algorithm="$1" file="$2" checksum_file="$3" expected actual
  expected="$(checksum_value "$checksum_file")"
  [ -n "$expected" ] || { echo "missing checksum in $checksum_file" >&2; exit 1; }
  case "$algorithm" in
    sha256)
      if command -v sha256sum >/dev/null 2>&1; then
        actual="$(sha256sum "$file" | awk '{print $1}')"
      elif command -v shasum >/dev/null 2>&1; then
        actual="$(shasum -a 256 "$file" | awk '{print $1}')"
      else
        actual="$(openssl dgst -sha256 "$file" | awk '{print $NF}')"
      fi
      ;;
    *) echo "unsupported checksum algorithm: $algorithm" >&2; exit 1 ;;
  esac
  [ "$actual" = "$expected" ] || {
    echo "checksum mismatch for $file" >&2
    echo "expected: $expected" >&2
    echo "actual:   $actual" >&2
    exit 1
  }
}
ASSET_URL=%[2]q
ASSET_PATH=/tmp/%[1]s.asset
CHECKSUM_PATH="${ASSET_PATH}.sha256"
EXTRACT_DIR="$(mktemp -d)"
trap 'rm -rf "$EXTRACT_DIR" "$ASSET_PATH" "$CHECKSUM_PATH"' EXIT

extract_zip() {
  if command -v unzip >/dev/null 2>&1; then
    unzip -q "$1" -d "$2"
  elif command -v ditto >/dev/null 2>&1; then
    ditto -x -k "$1" "$2"
  elif command -v python3 >/dev/null 2>&1; then
    python3 -m zipfile -e "$1" "$2"
  else
    echo "zip extractor not available" >&2
    exit 1
  fi
}

wget -q -O "$ASSET_PATH" "$ASSET_URL"
wget -q -O "$CHECKSUM_PATH" "%[3]s"
verify_checksum sha256 "$ASSET_PATH" "$CHECKSUM_PATH"
rm -f "$CHECKSUM_PATH"

mkdir -p /opt/frameworks/%[1]s
if [[ "$ASSET_URL" == *.zip ]]; then
  extract_zip "$ASSET_PATH" "$EXTRACT_DIR"
else
  tar -xzf "$ASSET_PATH" -C "$EXTRACT_DIR"
fi
mv "$EXTRACT_DIR"/frameworks-%[1]s-* /opt/frameworks/%[1]s/%[1]s 2>/dev/null || mv "$EXTRACT_DIR"/%[1]s /opt/frameworks/%[1]s/%[1]s 2>/dev/null || mv "$EXTRACT_DIR"/frameworks /opt/frameworks/%[1]s/%[1]s
chmod +x /opt/frameworks/%[1]s/%[1]s

echo "Binary installed"
`, f.serviceName, binaryURL, checksumURL)

	result, errExec := f.ExecuteScript(ctx, host, installScript)
	if errExec != nil || result.ExitCode != 0 {
		return fmt.Errorf("failed to install binary: %w\nStderr: %s", errExec, result.Stderr)
	}

	// Write merged env vars to the service env file
	svcEnvFile := fmt.Sprintf("/etc/frameworks/%s.env", f.serviceName)
	if err = f.writeServiceEnvFile(ctx, host, svcEnvFile, config); err != nil {
		fmt.Printf("    Warning: could not write env file %s: %v\n", svcEnvFile, err)
	}

	// Generate systemd unit
	unitData := SystemdUnitData{
		ServiceName: f.serviceName,
		Description: fmt.Sprintf("Frameworks %s", f.serviceName),
		WorkingDir:  fmt.Sprintf("/opt/frameworks/%s", f.serviceName),
		ExecStart:   fmt.Sprintf("/opt/frameworks/%s/%s", f.serviceName, f.serviceName),
		User:        "frameworks",
		EnvFile:     fmt.Sprintf("/etc/frameworks/%s.env", f.serviceName),
		After:       []string{"network-online"},
	}

	unitContent, err := GenerateSystemdUnit(unitData)
	if err != nil {
		return fmt.Errorf("failed to generate systemd unit: %w", err)
	}

	// Upload systemd unit
	tmpUnit := filepath.Join(os.TempDir(), f.serviceName+".service")
	if errWrite := os.WriteFile(tmpUnit, []byte(unitContent), 0644); errWrite != nil {
		return fmt.Errorf("failed to write systemd unit: %w", errWrite)
	}

	unitPath := fmt.Sprintf("/etc/systemd/system/frameworks-%s.service", f.serviceName)
	if errUpload := f.UploadFile(ctx, host, ssh.UploadOptions{
		LocalPath:  tmpUnit,
		RemotePath: unitPath,
		Mode:       0644,
	}); errUpload != nil {
		return fmt.Errorf("failed to upload systemd unit: %w", errUpload)
	}

	// Reload systemd and optionally enable + start
	reloadCmd := "systemctl daemon-reload"
	result, err = f.RunCommand(ctx, host, reloadCmd)
	if err != nil || result.ExitCode != 0 {
		return fmt.Errorf("failed to reload systemd: %w\nStderr: %s", err, result.Stderr)
	}

	if config.DeferStart {
		fmt.Printf("⏸ %s deployed but NOT started (missing required config)\n", f.serviceName)
	} else {
		enableCmd := fmt.Sprintf("systemctl enable frameworks-%s && systemctl start frameworks-%s", f.serviceName, f.serviceName)
		result, err = f.RunCommand(ctx, host, enableCmd)
		if err != nil || result.ExitCode != 0 {
			return fmt.Errorf("failed to start service: %w\nStderr: %s", err, result.Stderr)
		}
		fmt.Printf("✓ %s provisioned in native mode\n", f.serviceName)
	}
	return nil
}

// writeServiceEnvFile writes merged environment variables to the remote host.
// If config.EnvVars is populated, writes those. Otherwise falls back to CLUSTER_ID/NODE_ID from metadata.
func (f *FlexibleProvisioner) writeServiceEnvFile(ctx context.Context, host inventory.Host, envFilePath string, config ServiceConfig) error {
	envVars := config.EnvVars
	if len(envVars) == 0 {
		envVars = make(map[string]string)
		if clusterID, ok := config.Metadata["cluster_id"].(string); ok && clusterID != "" {
			envVars["CLUSTER_ID"] = clusterID
		}
		if nodeID, ok := config.Metadata["node_id"].(string); ok && nodeID != "" {
			envVars["NODE_ID"] = nodeID
		}
	}
	if len(envVars) == 0 {
		return nil
	}

	// Build env file content with sorted keys for deterministic output
	keys := make([]string, 0, len(envVars))
	for k := range envVars {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	lines := make([]string, 0, len(keys))
	for _, k := range keys {
		lines = append(lines, fmt.Sprintf("%s=%s", k, envVars[k]))
	}

	// Write atomically: create file with all env vars (not append)
	content := strings.Join(lines, "\n") + "\n"
	writeCmd := fmt.Sprintf("mkdir -p /etc/frameworks && cat > %s << 'ENVEOF'\n%sENVEOF\nchmod 0600 %s", envFilePath, content, envFilePath)
	result, err := f.RunCommand(ctx, host, writeCmd)
	if err != nil {
		return fmt.Errorf("failed to write env file: %w", err)
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("failed to write env file: %s", result.Stderr)
	}
	return nil
}

// Validate checks if the service is healthy
func (f *FlexibleProvisioner) Validate(ctx context.Context, host inventory.Host, config ServiceConfig) error {
	if f.port == 0 {
		// No HTTP health check for this service
		return nil
	}

	checker := &health.HTTPChecker{
		Path:    f.healthPath,
		Timeout: 5,
	}

	result := checker.Check(host.ExternalIP, f.port)
	if !result.OK {
		return fmt.Errorf("%s health check failed: %s", f.serviceName, result.Error)
	}

	return nil
}

// Initialize is a no-op for most application services
func (f *FlexibleProvisioner) Initialize(ctx context.Context, host inventory.Host, config ServiceConfig) error {
	// Most services don't need initialization
	// (Unlike Postgres/Kafka/ClickHouse which need databases/topics/tables)
	return nil
}

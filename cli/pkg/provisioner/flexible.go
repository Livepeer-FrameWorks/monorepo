package provisioner

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

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
	// Check if already provisioned
	state, err := f.Detect(ctx, host)
	if err == nil && state.Exists && state.Running {
		fmt.Printf("Service %s already running, skipping...\n", f.serviceName)
		return nil
	}

	// Allow explicit image/binary overrides from manifest
	if config.Mode == "docker" && config.Image != "" {
		return f.provisionDocker(ctx, host, config, &gitops.ServiceInfo{FullImage: config.Image})
	}
	if config.Mode == "native" && config.BinaryURL != "" {
		return f.provisionNative(ctx, host, config, &gitops.ServiceInfo{Binaries: map[string]string{"*": config.BinaryURL}})
	}

	// Fetch manifest from gitops
	channel, version := gitops.ResolveVersion(config.Version)
	fetcher, err := gitops.NewFetcher(gitops.FetchOptions{})
	if err != nil {
		return fmt.Errorf("failed to create gitops fetcher: %w", err)
	}

	manifest, err := fetcher.Fetch(channel, version)
	if err != nil {
		return fmt.Errorf("failed to fetch gitops manifest: %w", err)
	}

	svcInfo, err := manifest.GetServiceInfo(f.serviceName)
	if err != nil {
		return fmt.Errorf("service not found in manifest: %w", err)
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

// provisionDocker provisions the service using Docker
func (f *FlexibleProvisioner) provisionDocker(ctx context.Context, host inventory.Host, config ServiceConfig, svcInfo *gitops.ServiceInfo) error {
	fmt.Printf("Provisioning %s in Docker mode...\n", f.serviceName)

	port := f.port
	if config.Port != 0 {
		port = config.Port
	}

	// Generate docker-compose.yml
	envFile := config.EnvFile
	if envFile == "" {
		envFile = fmt.Sprintf("/etc/frameworks/%s.env", f.serviceName)
	}

	composeData := DockerComposeData{
		ServiceName: f.serviceName,
		Image:       svcInfo.FullImage, // image@sha256:digest format
		Port:        port,
		EnvFile:     envFile,
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
	commands := []string{
		fmt.Sprintf("cd /opt/frameworks/%s", f.serviceName),
		"docker compose pull",
		"docker compose up -d",
	}

	for _, cmd := range commands {
		result, err := f.RunCommand(ctx, host, cmd)
		if err != nil || result.ExitCode != 0 {
			return fmt.Errorf("docker compose command failed: %s\nStderr: %s", cmd, result.Stderr)
		}
	}

	fmt.Printf("✓ %s provisioned in Docker mode\n", f.serviceName)
	return nil
}

// provisionNative provisions the service as a native binary with systemd
func (f *FlexibleProvisioner) provisionNative(ctx context.Context, host inventory.Host, config ServiceConfig, svcInfo *gitops.ServiceInfo) error {
	fmt.Printf("Provisioning %s in native mode...\n", f.serviceName)

	// Get binary URL for current OS/arch (or explicit override)
	binaryURL := config.BinaryURL
	var err error
	if binaryURL == "" {
		// Allow wildcard "*" when using a single URL override via svcInfo.Binaries
		if svcInfo.Binaries != nil {
			if v, ok := svcInfo.Binaries["*"]; ok && v != "" {
				binaryURL = v
			}
		}
	}
	if binaryURL == "" {
		binaryURL, err = svcInfo.GetBinaryURL(runtime.GOOS, runtime.GOARCH)
	}
	if err != nil {
		return fmt.Errorf("binary not available: %w", err)
	}

	// Download and install binary
	installScript := fmt.Sprintf(`#!/bin/bash
set -e

# Download binary
wget -q -O /tmp/%s.tar.gz "%s"

# Extract
mkdir -p /opt/frameworks/%s
tar -xzf /tmp/%s.tar.gz -C /tmp/

# Move binary to installation directory
mv /tmp/frameworks-%s-* /opt/frameworks/%s/%s
chmod +x /opt/frameworks/%s/%s

# Cleanup
rm /tmp/%s.tar.gz

echo "Binary installed"
`, f.serviceName, binaryURL, f.serviceName, f.serviceName, f.serviceName, f.serviceName, f.serviceName, f.serviceName, f.serviceName, f.serviceName)

	result, errExec := f.ExecuteScript(ctx, host, installScript)
	if errExec != nil || result.ExitCode != 0 {
		return fmt.Errorf("failed to install binary: %w\nStderr: %s", errExec, result.Stderr)
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

	// Enable and start service
	enableCmd := fmt.Sprintf("systemctl daemon-reload && systemctl enable frameworks-%s && systemctl start frameworks-%s", f.serviceName, f.serviceName)
	result, err = f.RunCommand(ctx, host, enableCmd)
	if err != nil || result.ExitCode != 0 {
		return fmt.Errorf("failed to start service: %w\nStderr: %s", err, result.Stderr)
	}

	fmt.Printf("✓ %s provisioned in native mode\n", f.serviceName)
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

	result := checker.Check(host.Address, f.port)
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

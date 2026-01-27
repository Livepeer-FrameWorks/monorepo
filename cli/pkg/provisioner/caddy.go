package provisioner

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"frameworks/cli/pkg/gitops"
	"frameworks/cli/pkg/health"
	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/ssh"
)

// CaddyProvisioner provisions the Caddy reverse proxy
type CaddyProvisioner struct {
	*FlexibleProvisioner
	pool *ssh.Pool
}

// NewCaddyProvisioner creates a new Caddy provisioner
func NewCaddyProvisioner(pool *ssh.Pool) *CaddyProvisioner {
	return &CaddyProvisioner{
		FlexibleProvisioner: NewFlexibleProvisioner("caddy", 18090, pool), // Port 18090 used for dev detection compatibility
		pool:                pool,
	}
}

// Provision installs and configures Caddy
func (c *CaddyProvisioner) Provision(ctx context.Context, host inventory.Host, config ServiceConfig) error {
	// 1. Generate Caddyfile
	// RootDomain and Email from metadata
	rootDomain, _ := config.Metadata["root_domain"].(string)
	email, _ := config.Metadata["acme_email"].(string)
	if rootDomain == "" {
		rootDomain = "localhost" // Fallback for dev/local testing
	}

	// Determine Listen Address based on configured port or default to :80
	listenAddr := ":80"
	if config.Port != 0 {
		listenAddr = fmt.Sprintf(":%d", config.Port)
	}

	// Determine routes based on what services are planned/active on this host
	// Since we don't have the full manifest or plan here, we rely on Metadata injected by the Planner
	// containing the list of "local_services".
	var routes map[string]int // Declared routes here
	if localServicesMap, ok := config.Metadata["local_services"].(map[string]interface{}); ok {
		routes = make(map[string]int) // Initialized here
		for svcName, svcPort := range localServicesMap {
			if port, ok := svcPort.(int); ok {
				routes[svcName] = port
			}
		}
	} else {
		// Fallback: add all standard interfaces if no list provided (e.g. manual run)
		// This covers the "single host" case without planner updates.
		routes = map[string]int{ // Initialized here
			"website":  ServicePorts["website"],
			"webapp":   ServicePorts["webapp"],
			"bridge":   ServicePorts["bridge"],
			"docs":     ServicePorts["docs"],
			"forms":    ServicePorts["forms"],
			"listmonk": ServicePorts["listmonk"],
		}
	}

	caddyData := CaddyfileData{
		Email:         email,
		RootDomain:    rootDomain,
		ListenAddress: listenAddr,
		Routes:        routes,
	}

	caddyfileContent, err := GenerateCentralCaddyfile(caddyData)
	if err != nil {
		return fmt.Errorf("failed to generate Caddyfile: %w", err)
	}

	// 2. Upload Caddyfile
	tmpFile := filepath.Join(os.TempDir(), "Caddyfile")
	if err = os.WriteFile(tmpFile, []byte(caddyfileContent), 0644); err != nil { // Use = for err
		return err
	}
	defer os.Remove(tmpFile)

	var remoteCaddyfileDir string
	if config.Mode == "docker" {
		remoteCaddyfileDir = "/etc/frameworks/caddy" // Mounted into container
	} else {
		remoteCaddyfileDir = "/etc/caddy" // Standard native path
	}

	// Ensure remote directory exists
	if _, err = c.RunCommand(ctx, host, "mkdir -p "+remoteCaddyfileDir); err != nil { // Use = for err
		return fmt.Errorf("failed to create remote Caddy directory: %w", err)
	}

	// Upload Caddyfile
	remotePath := filepath.Join(remoteCaddyfileDir, "Caddyfile")
	if err = c.UploadFile(ctx, host, ssh.UploadOptions{
		LocalPath: tmpFile, RemotePath: remotePath, Mode: 0644,
	}); err != nil { // Use = for err
		return fmt.Errorf("failed to upload Caddyfile: %w", err)
	}

	// 3. Install Caddy (Binary or Docker)
	if err = c.installCaddy(ctx, host, config); err != nil { // Use = for err
		return fmt.Errorf("failed to install Caddy: %w", err)
	}

	// 4. Reload Caddy if native install
	if config.Mode == "native" {
		_, err = c.RunCommand(ctx, host, "systemctl reload caddy") // Use = for err
		if err != nil {
			return fmt.Errorf("failed to reload caddy: %w", err)
		}
	}

	fmt.Printf("✓ Caddy provisioned on %s\n", host.Address)
	return nil
}

// installCaddy uses FlexibleProvisioner's logic for installing Caddy itself
func (c *CaddyProvisioner) installCaddy(ctx context.Context, host inventory.Host, config ServiceConfig) error {
	// Check if already provisioned
	state, err := c.FlexibleProvisioner.Detect(ctx, host) // Corrected call to FlexibleProvisioner.Detect
	if err == nil && state.Exists && state.Running {
		fmt.Printf("Service %s already running, skipping...\n", c.GetName())
		return nil
	}

	// Fetch manifest from gitops
	channel, version := gitops.ResolveVersion(config.Version)
	fetcher, err := gitops.NewFetcher(gitops.FetchOptions{})
	if err != nil {
		return err
	}
	manifest, err := fetcher.Fetch(channel, version)
	if err != nil {
		return err
	}
	svcInfo, err := manifest.GetServiceInfo(c.GetName())
	if err != nil {
		return err
	}

	// Provision based on mode
	switch config.Mode {
	case "docker":
		return c.provisionDocker(ctx, host, config, svcInfo)
	case "native":
		return c.provisionNative(ctx, host, config, svcInfo)
	default:
		return fmt.Errorf("unsupported mode: %s (must be docker or native)", config.Mode)
	}
}

// provisionDocker overrides FlexibleProvisioner.provisionDocker to add Caddyfile mount and standard ports
func (c *CaddyProvisioner) provisionDocker(ctx context.Context, host inventory.Host, config ServiceConfig, svcInfo *gitops.ServiceInfo) error {
	fmt.Printf("Provisioning %s in Docker mode...\n", c.GetName())

	// Ensure EnvFile is generated for Caddy
	email, _ := config.Metadata["acme_email"].(string)
	rootDomain, _ := config.Metadata["root_domain"].(string)
	if email == "" {
		email = "caddy@example.com"
	}
	if rootDomain == "" {
		rootDomain = "localhost"
	}
	caddyEnvVars := map[string]string{
		"CADDY_EMAIL":       email,
		"CADDY_ROOT_DOMAIN": rootDomain,
	}
	envFileContent := GenerateEnvFile("caddy", caddyEnvVars)
	tmpEnvFile := filepath.Join(os.TempDir(), "caddy.env")
	if err := os.WriteFile(tmpEnvFile, []byte(envFileContent), 0600); err != nil {
		return err
	}
	defer os.Remove(tmpEnvFile)

	remoteEnvFile := "/etc/frameworks/caddy.env"
	if err := c.UploadFile(ctx, host, ssh.UploadOptions{LocalPath: tmpEnvFile, RemotePath: remoteEnvFile, Mode: 0600}); err != nil {
		return err
	}

	composeData := DockerComposeData{
		ServiceName: "caddy",
		Image:       svcInfo.FullImage,
		EnvFile:     remoteEnvFile,
		HealthCheck: &HealthCheckConfig{
			Test:     []string{"CMD", "curl", "-f", "http://localhost:2019/metrics"}, // Caddy admin
			Interval: "30s",
			Timeout:  "10s",
			Retries:  3,
		},
		Networks: []string{"frameworks"},
		Ports:    []string{"80:80", "443:443"}, // Caddy listens on 80/443
		Volumes: []string{
			"/etc/frameworks/caddy/Caddyfile:/etc/caddy/Caddyfile", // Mount generated Caddyfile
			"/var/lib/frameworks/caddy/data:/data",
			"/var/lib/frameworks/caddy/config:/config",
		},
		ExtraHosts: []string{"host.docker.internal:host-gateway"},
	}

	composeYAML, err := GenerateDockerCompose(composeData)
	if err != nil {
		return fmt.Errorf("failed to generate docker-compose: %w", err)
	}

	// Create local temp file for compose
	tmpDir, err := os.MkdirTemp("", "caddy-*")
	if err != nil {
		return fmt.Errorf("failed to create temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	composePath := filepath.Join(tmpDir, "docker-compose.yml")
	if err = os.WriteFile(composePath, []byte(composeYAML), 0644); err != nil {
		return fmt.Errorf("failed to write docker-compose.yml: %w", err)
	}

	// Upload to host
	remoteComposePath := fmt.Sprintf("/opt/frameworks/%s/docker-compose.yml", c.GetName())
	if err = c.UploadFile(ctx, host, ssh.UploadOptions{
		LocalPath:  composePath,
		RemotePath: remoteComposePath,
		Mode:       0644,
	}); err != nil {
		return fmt.Errorf("failed to upload docker-compose.yml: %w", err)
	}

	// Pull and start with docker compose
	commands := []string{
		fmt.Sprintf("cd %s", remoteComposePath), // cd to dir with compose file
		"docker compose pull",
		"docker compose up -d",
	}

	for _, cmd := range commands {
		result, err := c.RunCommand(ctx, host, cmd)
		if err != nil || result.ExitCode != 0 {
			return fmt.Errorf("docker compose command failed: %s\nStderr: %s", cmd, result.Stderr)
		}
	}

	fmt.Printf("✓ Caddy provisioned in Docker mode on %s\n", host.Address)
	return nil
}

// provisionNative overrides FlexibleProvisioner.provisionNative
func (c *CaddyProvisioner) provisionNative(ctx context.Context, host inventory.Host, config ServiceConfig, svcInfo *gitops.ServiceInfo) error {
	fmt.Printf("Provisioning %s in native mode...\n", c.GetName())

	// Download and install binary
	binaryURL, err := svcInfo.GetBinaryURL(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return fmt.Errorf("Caddy binary not available: %w", err)
	}

	installScript := fmt.Sprintf(`#!/bin/bash
set -e
wget -q -O /tmp/caddy.tar.gz "%s"
mkdir -p /opt/frameworks/caddy
tar -xzf /tmp/caddy.tar.gz -C /tmp/
mv /tmp/caddy /opt/frameworks/caddy/caddy # Caddy binary is usually just named caddy
chmod +x /opt/frameworks/caddy/caddy
rm /tmp/caddy.tar.gz
`, binaryURL)

	result, err := c.ExecuteScript(ctx, host, installScript)
	if err != nil || result.ExitCode != 0 {
		return fmt.Errorf("failed to install Caddy binary: %w (stderr: %s)", err, result.Stderr)
	}

	// Generate Systemd unit
	// Need to ensure Caddyfile is in /etc/caddy/Caddyfile
	unitData := SystemdUnitData{
		ServiceName: "caddy",
		Description: "FrameWorks Caddy Reverse Proxy",
		WorkingDir:  "/etc/caddy", // Caddy typically runs from its config directory
		ExecStart:   "/opt/frameworks/caddy/caddy run --config /etc/caddy/Caddyfile",
		User:        "caddy", // Caddy often runs as its own user
		EnvFile:     "/etc/frameworks/caddy.env",
		After:       []string{"network-online"},
	}

	unitContent, err := GenerateSystemdUnit(unitData)
	if err != nil {
		return fmt.Errorf("failed to generate systemd unit: %w", err)
	}

	// Upload systemd unit
	tmpUnit := filepath.Join(os.TempDir(), "caddy.service")
	if err = os.WriteFile(tmpUnit, []byte(unitContent), 0644); err != nil {
		return fmt.Errorf("failed to write systemd unit: %w", err)
	}
	defer os.Remove(tmpUnit)

	unitPath := "/etc/systemd/system/caddy.service" // Standard Caddy systemd unit name
	if err = c.UploadFile(ctx, host, ssh.UploadOptions{
		LocalPath: tmpUnit, RemotePath: unitPath, Mode: 0644,
	}); err != nil {
		return fmt.Errorf("failed to upload systemd unit: %w", err)
	}

	// Create caddy user/group
	createUserCmd := "id -u caddy &>/dev/null || useradd -s /sbin/nologin -g caddy -d /var/www caddy"
	_, err = c.RunCommand(ctx, host, createUserCmd)
	if err != nil {
		return fmt.Errorf("failed to create caddy user: %w", err)
	}

	// Ensure /etc/caddy and /var/lib/caddy exist and are owned by caddy
	_, err = c.RunCommand(ctx, host, "mkdir -p /etc/caddy /var/lib/caddy && chown -R caddy:caddy /etc/caddy /var/lib/caddy")
	if err != nil {
		return fmt.Errorf("failed to set caddy dirs: %w", err)
	}

	// Enable and start service
	enableCmd := "systemctl daemon-reload && systemctl enable caddy && systemctl start caddy"
	result, err = c.RunCommand(ctx, host, enableCmd)
	if err != nil || result.ExitCode != 0 {
		return fmt.Errorf("failed to start Caddy service: %w (stderr: %s)", err, result.Stderr)
	}

	fmt.Printf("✓ Caddy provisioned in native mode on %s\n", host.Address)
	return nil
}

// Validate checks health
func (c *CaddyProvisioner) Validate(ctx context.Context, host inventory.Host, config ServiceConfig) error {
	// Caddy's admin API runs on port 2019 by default.
	// We can try to hit its /health endpoint or metrics.
	// For web health, we can check 80/443.

	// Let's check the admin API for a reliable internal health check
	checker := &health.HTTPChecker{ // This is where health is used
		Path:    "/health",
		Timeout: 5,
	}
	// For a native Caddy, port 2019 is local. For Docker Caddy, if admin port is exposed, too.
	// Caddy's health endpoint on standard HTTP ports is /health or similar.
	// We should probably check one of the public facing routes (e.g. website) through Caddy.

	// But as a self-check, the internal admin API is best.
	result := checker.Check(host.Address, 2019) // Caddy admin API port
	if !result.OK {
		return fmt.Errorf("Caddy admin API health check failed: %s", result.Error)
	}

	return nil
}

// Initialize is a no-op
func (c *CaddyProvisioner) Initialize(ctx context.Context, host inventory.Host, config ServiceConfig) error {
	return nil
}

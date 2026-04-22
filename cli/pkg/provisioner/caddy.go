package provisioner

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

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
	if err := ensurePublicProxyPortsSafe(ctx, c.BaseProvisioner, host, c.GetName(), config.Mode); err != nil {
		return err
	}

	rootDomain, _ := config.Metadata["root_domain"].(string)
	email, _ := config.Metadata["acme_email"].(string)
	if rootDomain == "" {
		return fmt.Errorf("caddy: root_domain is required (set root_domain in cluster manifest)")
	}

	// Determine Listen Address based on configured port or default to :80
	listenAddr := ":80"
	if config.Port != 0 {
		listenAddr = fmt.Sprintf(":%d", config.Port)
	}

	if err := c.installCaddy(ctx, host, config); err != nil {
		return fmt.Errorf("failed to install Caddy: %w", err)
	}

	if config.Mode == "native" {
		if err := c.installIngressSync(ctx, host, config); err != nil {
			return fmt.Errorf("failed to install caddy ingress sync: %w", err)
		}
		if _, err := c.RunCommand(ctx, host, "/opt/frameworks/ingress-sync/caddy-sync.py"); err != nil {
			return fmt.Errorf("failed to run caddy ingress sync: %w", err)
		}
		fmt.Printf("✓ Caddy provisioned on %s\n", host.ExternalIP)
		return nil
	}

	routes := localServicePorts(config.Metadata)
	proxyRoutes := BuildLocalProxyRoutes(rootDomain, routes)
	proxyRoutes = append(proxyRoutes, BuildExtraProxyRoutes(config.Metadata["extra_proxy_routes"])...)
	caddyData := CaddyfileData{
		Email:         email,
		RootDomain:    rootDomain,
		ListenAddress: listenAddr,
		Routes:        proxyRoutes,
	}

	caddyfileContent, err := GenerateCentralCaddyfile(caddyData)
	if err != nil {
		return fmt.Errorf("failed to generate Caddyfile: %w", err)
	}

	tmpFile := filepath.Join(os.TempDir(), "Caddyfile")
	if err = os.WriteFile(tmpFile, []byte(caddyfileContent), 0644); err != nil {
		return err
	}
	defer os.Remove(tmpFile)

	remoteCaddyfileDir := "/etc/frameworks/caddy"
	if _, err = c.RunCommand(ctx, host, "mkdir -p "+remoteCaddyfileDir); err != nil {
		return fmt.Errorf("failed to create remote Caddy directory: %w", err)
	}

	remotePath := filepath.Join(remoteCaddyfileDir, "Caddyfile")
	if err = c.UploadFile(ctx, host, ssh.UploadOptions{
		LocalPath: tmpFile, RemotePath: remotePath, Mode: 0644,
	}); err != nil {
		return fmt.Errorf("failed to upload Caddyfile: %w", err)
	}

	fmt.Printf("✓ Caddy provisioned on %s\n", host.ExternalIP)
	return nil
}

// installCaddy uses FlexibleProvisioner's logic for installing Caddy itself
func (c *CaddyProvisioner) installCaddy(ctx context.Context, host inventory.Host, config ServiceConfig) error {
	// Check if already provisioned
	state, err := c.Detect(ctx, host) // Corrected call to FlexibleProvisioner.Detect
	if err == nil && state.Exists && state.Running {
		fmt.Printf("Service %s already running, skipping...\n", c.GetName())
		return nil
	}

	// Fetch manifest from gitops
	channel, version := gitops.ResolveVersion(config.Version)
	manifest, err := fetchGitopsManifest(channel, version, config.Metadata)
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
		return fmt.Errorf("caddy: root_domain is required (set root_domain in cluster manifest)")
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
	composeCmd := fmt.Sprintf("cd %s && docker compose pull && docker compose up -d",
		filepath.Dir(remoteComposePath))
	result, err := c.RunCommand(ctx, host, composeCmd)
	if err != nil || result.ExitCode != 0 {
		return fmt.Errorf("docker compose failed: %s\nStderr: %s", composeCmd, result.Stderr)
	}

	fmt.Printf("✓ Caddy provisioned in Docker mode on %s\n", host.ExternalIP)
	return nil
}

// provisionNative overrides FlexibleProvisioner.provisionNative
func (c *CaddyProvisioner) provisionNative(ctx context.Context, host inventory.Host, config ServiceConfig, _ *gitops.ServiceInfo) error {
	fmt.Printf("Provisioning %s in native mode...\n", c.GetName())
	family, err := c.DetectDistroFamily(ctx, host)
	if err != nil {
		return fmt.Errorf("failed to detect distro family: %w", err)
	}

	setupScript := fmt.Sprintf(`#!/bin/bash
set -e
if ! command -v caddy >/dev/null 2>&1; then
  %s
fi
mkdir -p /etc/caddy/conf.d
if [ ! -f /etc/caddy/Caddyfile ]; then
  cat > /etc/caddy/Caddyfile <<'EOF'
import /etc/caddy/conf.d/*.caddyfile
EOF
elif ! grep -q '/etc/caddy/conf.d/\*.caddyfile' /etc/caddy/Caddyfile; then
  printf '\nimport /etc/caddy/conf.d/*.caddyfile\n' >> /etc/caddy/Caddyfile
fi
systemctl enable caddy
systemctl start caddy
`, packageInstallCommand(family, "caddy"))

	result, err := c.ExecuteScript(ctx, host, setupScript)
	if err != nil || result.ExitCode != 0 {
		return fmt.Errorf("failed to install packaged caddy: %w (stderr: %s)", err, result.Stderr)
	}

	fmt.Printf("✓ Caddy provisioned in native mode on %s\n", host.ExternalIP)
	return nil
}

// Validate checks health
func (c *CaddyProvisioner) Validate(ctx context.Context, host inventory.Host, config ServiceConfig) error {
	// Caddy's admin API runs on port 2019 by default.
	// We can try to hit its /health endpoint or metrics.
	// For web health, we can check 80/443.

	// Let's check the admin API for a reliable internal health check.
	checker := &health.HTTPChecker{
		Path:    "/health",
		Timeout: 5,
	}
	// For a native Caddy, port 2019 is local. For Docker Caddy, if admin port is exposed, too.
	// Caddy's health endpoint on standard HTTP ports is /health or similar.
	// We should probably check one of the public facing routes (e.g. foredeck) through Caddy.

	// But as a self-check, the internal admin API is best.
	result := checker.Check(host.ExternalIP, 2019) // Caddy admin API port
	if !result.OK {
		return fmt.Errorf("caddy admin API health check failed: %s", result.Error)
	}

	// For the public listener, a TCP connect is safer than hard-coding an HTTP path.
	// Edge Caddy commonly redirects :80 -> :443 and templates may not define /health.
	publicHTTP := &health.TCPChecker{
		Timeout: 5 * time.Second,
	}
	httpResult := publicHTTP.Check(host.ExternalIP, 80)
	if !httpResult.OK {
		return fmt.Errorf("caddy public HTTP port check failed: %s", httpResult.Error)
	}

	publicTLS := &health.TCPChecker{
		Timeout: 5 * time.Second,
	}
	tlsResult := publicTLS.Check(host.ExternalIP, 443)
	if !tlsResult.OK {
		return fmt.Errorf("caddy public HTTPS port check failed: %s", tlsResult.Error)
	}

	return nil
}

// Initialize is a no-op
func (c *CaddyProvisioner) Initialize(ctx context.Context, host inventory.Host, config ServiceConfig) error {
	return nil
}

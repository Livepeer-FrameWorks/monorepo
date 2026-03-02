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

// NginxProvisioner provisions the Nginx reverse proxy with generated config
type NginxProvisioner struct {
	*FlexibleProvisioner
	pool *ssh.Pool
}

// NewNginxProvisioner creates a new Nginx provisioner
func NewNginxProvisioner(pool *ssh.Pool) *NginxProvisioner {
	return &NginxProvisioner{
		FlexibleProvisioner: NewFlexibleProvisioner("nginx", 18090, pool),
		pool:                pool,
	}
}

// Provision installs and configures Nginx
func (n *NginxProvisioner) Provision(ctx context.Context, host inventory.Host, config ServiceConfig) error {
	rootDomain, _ := config.Metadata["root_domain"].(string)
	if rootDomain == "" {
		return fmt.Errorf("nginx: root_domain is required (set root_domain in cluster manifest)")
	}

	listenAddr := "80"
	if config.Port != 0 {
		listenAddr = fmt.Sprintf("%d", config.Port)
	}

	var routes map[string]int
	if localServicesMap, ok := config.Metadata["local_services"].(map[string]interface{}); ok {
		routes = make(map[string]int)
		for svcName, svcPort := range localServicesMap {
			if port, ok := svcPort.(int); ok {
				routes[svcName] = port
			}
		}
	} else {
		routes = map[string]int{
			"foredeck":  ServicePorts["foredeck"],
			"chartroom": ServicePorts["chartroom"],
			"bridge":    ServicePorts["bridge"],
			"logbook":   ServicePorts["logbook"],
			"steward":   ServicePorts["steward"],
			"listmonk":  ServicePorts["listmonk"],
			"chatwoot":  ServicePorts["chatwoot"],
		}
	}

	confData := NginxConfData{
		RootDomain:    rootDomain,
		ListenAddress: listenAddr,
		Routes:        routes,
	}

	confContent, err := GenerateNginxConf(confData)
	if err != nil {
		return fmt.Errorf("failed to generate nginx.conf: %w", err)
	}

	tmpFile := filepath.Join(os.TempDir(), "frameworks-nginx.conf")
	if err = os.WriteFile(tmpFile, []byte(confContent), 0644); err != nil {
		return err
	}
	defer os.Remove(tmpFile)

	var remoteConfDir string
	if config.Mode == "docker" {
		remoteConfDir = "/etc/frameworks/nginx"
	} else {
		remoteConfDir = "/etc/nginx/conf.d"
	}

	if _, err = n.RunCommand(ctx, host, "mkdir -p "+remoteConfDir); err != nil {
		return fmt.Errorf("failed to create remote nginx directory: %w", err)
	}

	var remoteConfName string
	if config.Mode == "docker" {
		remoteConfName = "default.conf"
	} else {
		remoteConfName = "frameworks.conf"
	}

	remotePath := filepath.Join(remoteConfDir, remoteConfName)
	if err = n.UploadFile(ctx, host, ssh.UploadOptions{
		LocalPath: tmpFile, RemotePath: remotePath, Mode: 0644,
	}); err != nil {
		return fmt.Errorf("failed to upload nginx config: %w", err)
	}

	if err = n.installNginx(ctx, host, config); err != nil {
		return fmt.Errorf("failed to install nginx: %w", err)
	}

	if config.Mode == "native" {
		if _, err = n.RunCommand(ctx, host, "nginx -t && nginx -s reload"); err != nil {
			return fmt.Errorf("failed to reload nginx: %w", err)
		}
	}

	fmt.Printf("✓ Nginx provisioned on %s\n", host.ExternalIP)
	return nil
}

func (n *NginxProvisioner) installNginx(ctx context.Context, host inventory.Host, config ServiceConfig) error {
	state, err := n.Detect(ctx, host)
	if err == nil && state.Exists && state.Running {
		fmt.Printf("Service %s already running, skipping install...\n", n.GetName())
		return nil
	}

	channel, version := gitops.ResolveVersion(config.Version)
	fetcher, err := gitops.NewFetcher(gitops.FetchOptions{})
	if err != nil {
		return err
	}
	manifest, err := fetcher.Fetch(channel, version)
	if err != nil {
		return err
	}
	svcInfo, err := manifest.GetServiceInfo(n.GetName())
	if err != nil {
		return err
	}

	switch config.Mode {
	case "docker":
		return n.provisionDocker(ctx, host, config, svcInfo)
	case "native":
		return n.provisionNative(ctx, host, config, svcInfo)
	default:
		return fmt.Errorf("unsupported mode: %s (must be docker or native)", config.Mode)
	}
}

func (n *NginxProvisioner) provisionDocker(ctx context.Context, host inventory.Host, config ServiceConfig, svcInfo *gitops.ServiceInfo) error {
	fmt.Printf("Provisioning %s in Docker mode...\n", n.GetName())

	composeData := DockerComposeData{
		ServiceName: "nginx",
		Image:       svcInfo.FullImage,
		HealthCheck: &HealthCheckConfig{
			Test:     []string{"CMD", "curl", "-f", "http://localhost/health"},
			Interval: "30s",
			Timeout:  "10s",
			Retries:  3,
		},
		Networks: []string{"frameworks"},
		Ports:    []string{"80:80"},
		Volumes: []string{
			"/etc/frameworks/nginx/default.conf:/etc/nginx/conf.d/default.conf:ro",
		},
		ExtraHosts: []string{"host.docker.internal:host-gateway"},
	}

	composeYAML, err := GenerateDockerCompose(composeData)
	if err != nil {
		return fmt.Errorf("failed to generate docker-compose: %w", err)
	}

	tmpDir, err := os.MkdirTemp("", "nginx-*")
	if err != nil {
		return fmt.Errorf("failed to create temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	composePath := filepath.Join(tmpDir, "docker-compose.yml")
	if err = os.WriteFile(composePath, []byte(composeYAML), 0644); err != nil {
		return fmt.Errorf("failed to write docker-compose.yml: %w", err)
	}

	remoteComposePath := fmt.Sprintf("/opt/frameworks/%s/docker-compose.yml", n.GetName())
	if err = n.UploadFile(ctx, host, ssh.UploadOptions{
		LocalPath:  composePath,
		RemotePath: remoteComposePath,
		Mode:       0644,
	}); err != nil {
		return fmt.Errorf("failed to upload docker-compose.yml: %w", err)
	}

	commands := []string{
		fmt.Sprintf("cd %s", remoteComposePath),
		"docker compose pull",
		"docker compose up -d",
	}

	for _, cmd := range commands {
		result, err := n.RunCommand(ctx, host, cmd)
		if err != nil || result.ExitCode != 0 {
			return fmt.Errorf("docker compose command failed: %s\nStderr: %s", cmd, result.Stderr)
		}
	}

	fmt.Printf("✓ Nginx provisioned in Docker mode on %s\n", host.ExternalIP)
	return nil
}

func (n *NginxProvisioner) provisionNative(ctx context.Context, host inventory.Host, config ServiceConfig, svcInfo *gitops.ServiceInfo) error {
	fmt.Printf("Provisioning %s in native mode...\n", n.GetName())

	remoteOS, remoteArch, archErr := n.DetectRemoteArch(ctx, host)
	if archErr != nil {
		return fmt.Errorf("failed to detect remote architecture: %w", archErr)
	}
	binaryURL, err := svcInfo.GetBinaryURL(remoteOS, remoteArch)
	if err != nil {
		return fmt.Errorf("nginx binary not available: %w", err)
	}

	installScript := fmt.Sprintf(`#!/bin/bash
set -e
wget -q -O /tmp/nginx.tar.gz "%s"
mkdir -p /opt/frameworks/nginx
tar -xzf /tmp/nginx.tar.gz -C /tmp/
mv /tmp/nginx /opt/frameworks/nginx/nginx
chmod +x /opt/frameworks/nginx/nginx
rm /tmp/nginx.tar.gz
`, binaryURL)

	result, err := n.ExecuteScript(ctx, host, installScript)
	if err != nil || result.ExitCode != 0 {
		return fmt.Errorf("failed to install nginx binary: %w (stderr: %s)", err, result.Stderr)
	}

	unitData := SystemdUnitData{
		ServiceName: "nginx",
		Description: "FrameWorks Nginx Reverse Proxy",
		WorkingDir:  "/etc/nginx",
		ExecStart:   "/opt/frameworks/nginx/nginx -g 'daemon off;' -c /etc/nginx/nginx.conf",
		User:        "www-data",
		After:       []string{"network-online"},
	}

	unitContent, err := GenerateSystemdUnit(unitData)
	if err != nil {
		return fmt.Errorf("failed to generate systemd unit: %w", err)
	}

	tmpUnit := filepath.Join(os.TempDir(), "nginx.service")
	if err = os.WriteFile(tmpUnit, []byte(unitContent), 0644); err != nil {
		return fmt.Errorf("failed to write systemd unit: %w", err)
	}
	defer os.Remove(tmpUnit)

	unitPath := "/etc/systemd/system/nginx.service"
	if err = n.UploadFile(ctx, host, ssh.UploadOptions{
		LocalPath: tmpUnit, RemotePath: unitPath, Mode: 0644,
	}); err != nil {
		return fmt.Errorf("failed to upload systemd unit: %w", err)
	}

	enableCmd := "systemctl daemon-reload && systemctl enable nginx && systemctl start nginx"
	result, err = n.RunCommand(ctx, host, enableCmd)
	if err != nil || result.ExitCode != 0 {
		return fmt.Errorf("failed to start nginx service: %w (stderr: %s)", err, result.Stderr)
	}

	fmt.Printf("✓ Nginx provisioned in native mode on %s\n", host.ExternalIP)
	return nil
}

// Validate checks health
func (n *NginxProvisioner) Validate(ctx context.Context, host inventory.Host, config ServiceConfig) error {
	checker := &health.HTTPChecker{
		Path:    "/health",
		Timeout: 5,
	}
	result := checker.Check(host.ExternalIP, 80)
	if !result.OK {
		return fmt.Errorf("nginx health check failed: %s", result.Error)
	}

	publicHTTP := &health.TCPChecker{
		Timeout: 5 * time.Second,
	}
	httpResult := publicHTTP.Check(host.ExternalIP, 80)
	if !httpResult.OK {
		return fmt.Errorf("nginx HTTP port check failed: %s", httpResult.Error)
	}

	return nil
}

// Initialize is a no-op
func (n *NginxProvisioner) Initialize(ctx context.Context, host inventory.Host, config ServiceConfig) error {
	return nil
}

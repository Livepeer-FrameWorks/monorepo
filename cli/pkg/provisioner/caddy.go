package provisioner

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"frameworks/cli/pkg/ansible"
	"frameworks/cli/pkg/gitops"
	"frameworks/cli/pkg/health"
	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/ssh"
)

// CaddyProvisioner provisions the Caddy reverse proxy
type CaddyProvisioner struct {
	*FlexibleProvisioner
	pool     *ssh.Pool
	executor *ansible.Executor
}

// NewCaddyProvisioner creates a new Caddy provisioner
func NewCaddyProvisioner(pool *ssh.Pool) *CaddyProvisioner {
	executor, err := ansible.NewExecutor("")
	if err != nil {
		panic(fmt.Sprintf("create ansible executor for caddy: %v", err))
	}
	return &CaddyProvisioner{
		FlexibleProvisioner: NewFlexibleProvisioner("caddy", 18090, pool),
		pool:                pool,
		executor:            executor,
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

// installCaddy fetches the pinned manifest and dispatches to docker- or
// native-mode provisioning. Runs every apply — convergence is handled by the
// per-mode playbook's idempotence gates.
func (c *CaddyProvisioner) installCaddy(ctx context.Context, host inventory.Host, config ServiceConfig) error {
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
func (c *CaddyProvisioner) provisionNative(ctx context.Context, host inventory.Host, _ ServiceConfig, _ *gitops.ServiceInfo) error {
	fmt.Printf("Provisioning %s in native mode...\n", c.GetName())

	tasks := []ansible.Task{
		ansible.TaskPackage("caddy", ansible.PackagePresent),
		{
			Name:   "ensure /etc/caddy/conf.d exists",
			Module: "ansible.builtin.file",
			Args:   map[string]any{"path": "/etc/caddy/conf.d", "state": "directory", "mode": "0755"},
		},
		// blockinfile creates the file if absent and inserts the import
		// statement once, bracketed by a marker so re-runs are idempotent
		// and other tooling editing Caddyfile is not disturbed.
		{
			Name:   "ensure FrameWorks import block in /etc/caddy/Caddyfile",
			Module: "ansible.builtin.blockinfile",
			Args: map[string]any{
				"path":   "/etc/caddy/Caddyfile",
				"block":  "import /etc/caddy/conf.d/*.caddyfile",
				"marker": "# {mark} FrameWorks caddy include",
				"create": true,
				"mode":   "0644",
			},
		},
		ansible.TaskSystemdService("caddy", ansible.SystemdOpts{
			State:   "started",
			Enabled: ansible.BoolPtr(true),
		}),
	}

	playbook := &ansible.Playbook{
		Name:  "Install Caddy (native)",
		Hosts: host.ExternalIP,
		Plays: []ansible.Play{
			{
				Name:        "Install packaged Caddy",
				Hosts:       host.ExternalIP,
				Become:      true,
				GatherFacts: false,
				Tasks:       tasks,
			},
		},
	}
	inv := ansible.NewInventory()
	inv.AddHost(&ansible.InventoryHost{
		Name:    host.ExternalIP,
		Address: host.ExternalIP,
		Vars: map[string]string{
			"ansible_user":                 host.User,
			"ansible_ssh_private_key_file": c.pool.DefaultKeyPath(),
		},
	})
	result, execErr := c.executor.ExecutePlaybook(ctx, playbook, inv, ansible.ExecuteOptions{Verbose: true})
	if execErr != nil {
		return fmt.Errorf("caddy install failed: %w\nOutput: %s", execErr, result.Output)
	}
	if !result.Success {
		return fmt.Errorf("caddy install playbook failed\nOutput: %s", result.Output)
	}

	fmt.Printf("✓ Caddy provisioned in native mode on %s\n", host.ExternalIP)
	return nil
}

// Validate checks native package state via goss, then checks the admin API and
// public listeners.
func (c *CaddyProvisioner) Validate(ctx context.Context, host inventory.Host, config ServiceConfig) error {
	if config.Mode == "native" {
		if _, remoteArch, err := c.DetectRemoteArch(ctx, host); err == nil {
			spec := ansible.RenderGossYAML(ansible.GossSpec{
				Services: map[string]ansible.GossService{
					"caddy": {Running: true, Enabled: true},
				},
				Files: map[string]ansible.GossFile{
					"/etc/caddy/Caddyfile": {Exists: true},
				},
			})
			if gossErr := runGossValidate(ctx, c.executor, c.pool.DefaultKeyPath(), host,
				"caddy", platformChannelFromMetadata(config.Metadata), config.Metadata, remoteArch, spec); gossErr != nil {
				return fmt.Errorf("caddy goss validate failed: %w", gossErr)
			}
		}
	}

	// Admin API is 127.0.0.1 only; validate via uri from inside the host.
	adminTasks := []ansible.Task{
		waitForTCP("wait for caddy admin", "127.0.0.1", 2019, 10),
		uriOK("caddy admin /config/", "http://127.0.0.1:2019/config/", 200),
	}
	if err := runValidatePlaybook(ctx, c.executor, c.pool.DefaultKeyPath(), host, "caddy-admin", adminTasks); err != nil {
		return err
	}

	// Public 80/443 is deliberately internet-facing; external TCP from the
	// CLI is the correct network plane for those.
	publicHTTP := &health.TCPChecker{Timeout: 5 * time.Second}
	if result := publicHTTP.Check(host.ExternalIP, 80); !result.OK {
		return fmt.Errorf("caddy public HTTP port check failed: %s", result.Error)
	}
	publicTLS := &health.TCPChecker{Timeout: 5 * time.Second}
	if result := publicTLS.Check(host.ExternalIP, 443); !result.OK {
		return fmt.Errorf("caddy public HTTPS port check failed: %s", result.Error)
	}

	return nil
}

// Initialize is a no-op
func (c *CaddyProvisioner) Initialize(ctx context.Context, host inventory.Host, config ServiceConfig) error {
	return nil
}

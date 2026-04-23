package provisioner

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"frameworks/cli/pkg/ansible"
	"frameworks/cli/pkg/gitops"
	"frameworks/cli/pkg/health"
	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/ssh"
	"frameworks/pkg/maintenance"
)

// NginxProvisioner provisions the Nginx reverse proxy with generated config
type NginxProvisioner struct {
	*FlexibleProvisioner
	pool     *ssh.Pool
	executor *ansible.Executor
}

// NewNginxProvisioner creates a new Nginx provisioner
func NewNginxProvisioner(pool *ssh.Pool) *NginxProvisioner {
	executor, err := ansible.NewExecutor("")
	if err != nil {
		panic(fmt.Sprintf("create ansible executor for nginx: %v", err))
	}
	return &NginxProvisioner{
		FlexibleProvisioner: NewFlexibleProvisioner("nginx", 18090, pool),
		pool:                pool,
		executor:            executor,
	}
}

// Provision installs and configures Nginx
func (n *NginxProvisioner) Provision(ctx context.Context, host inventory.Host, config ServiceConfig) error {
	if err := ensurePublicProxyPortsSafe(ctx, n.BaseProvisioner, host, n.GetName(), config.Mode); err != nil {
		return err
	}
	var err error

	rootDomain, _ := config.Metadata["root_domain"].(string)
	if rootDomain == "" {
		return fmt.Errorf("nginx: root_domain is required (set root_domain in cluster manifest)")
	}

	listenAddr := "80"
	if config.Port != 0 {
		listenAddr = fmt.Sprintf("%d", config.Port)
	}

	if err = n.installNginx(ctx, host, config); err != nil {
		return fmt.Errorf("failed to install nginx: %w", err)
	}

	if config.Mode == "native" {
		if err = n.installIngressSync(ctx, host, config); err != nil {
			return fmt.Errorf("failed to install ingress sync: %w", err)
		}
		if _, err = n.RunCommand(ctx, host, "/opt/frameworks/ingress-sync/nginx-sync.py"); err != nil {
			return fmt.Errorf("failed to run ingress sync: %w", err)
		}
	} else {
		routes := BuildLocalProxyRoutes(rootDomain, localServicePorts(config.Metadata))
		routes = append(routes, BuildExtraProxyRoutes(config.Metadata["extra_proxy_routes"])...)
		confData := NginxConfData{
			RootDomain:    rootDomain,
			ListenAddress: listenAddr,
			Routes:        routes,
		}
		for _, route := range routes {
			if route.GeoProxy {
				confData.GeoIPDBPath = "/var/lib/GeoIP/GeoLite2-City.mmdb"
				break
			}
		}

		confContent, genErr := GenerateNginxConf(confData)
		if genErr != nil {
			return fmt.Errorf("failed to generate nginx.conf: %w", genErr)
		}

		tmpFile := filepath.Join(os.TempDir(), "frameworks-nginx.conf")
		if err = os.WriteFile(tmpFile, []byte(confContent), 0644); err != nil {
			return err
		}
		defer os.Remove(tmpFile)

		remoteConfDir := "/etc/frameworks/nginx"
		if _, err = n.RunCommand(ctx, host, "mkdir -p "+remoteConfDir); err != nil {
			return fmt.Errorf("failed to create remote nginx directory: %w", err)
		}

		remotePath := filepath.Join(remoteConfDir, "default.conf")
		if err = n.UploadFile(ctx, host, ssh.UploadOptions{
			LocalPath: tmpFile, RemotePath: remotePath, Mode: 0644,
		}); err != nil {
			return fmt.Errorf("failed to upload nginx config: %w", err)
		}
	}

	// Upload the maintenance/error page used by error_page directives.
	maintTmp := filepath.Join(os.TempDir(), "frameworks-maintenance.html")
	if err = os.WriteFile(maintTmp, maintenance.HTML, 0644); err != nil {
		return fmt.Errorf("failed to write maintenance page: %w", err)
	}
	defer os.Remove(maintTmp)

	remoteMaintPath := "/usr/share/nginx/html/maintenance.html"
	if config.Mode == "docker" {
		remoteMaintPath = "/etc/frameworks/nginx/maintenance.html"
	}
	if err = n.UploadFile(ctx, host, ssh.UploadOptions{
		LocalPath: maintTmp, RemotePath: remoteMaintPath, Mode: 0644,
	}); err != nil {
		return fmt.Errorf("failed to upload maintenance page: %w", err)
	}

	fmt.Printf("✓ Nginx provisioned on %s\n", host.ExternalIP)
	return nil
}

func (n *NginxProvisioner) installNginx(ctx context.Context, host inventory.Host, config ServiceConfig) error {
	channel, version := gitops.ResolveVersion(config.Version)
	manifest, err := fetchGitopsManifest(channel, version, config.Metadata)
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

func (n *NginxProvisioner) provisionDocker(ctx context.Context, host inventory.Host, _ ServiceConfig, svcInfo *gitops.ServiceInfo) error {
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
			"/etc/frameworks/nginx/maintenance.html:/usr/share/nginx/html/maintenance.html:ro",
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

	composeCmd := fmt.Sprintf("cd %s && docker compose pull && docker compose up -d",
		filepath.Dir(remoteComposePath))
	result, err := n.RunCommand(ctx, host, composeCmd)
	if err != nil || result.ExitCode != 0 {
		return fmt.Errorf("docker compose failed: %s\nStderr: %s", composeCmd, result.Stderr)
	}

	fmt.Printf("✓ Nginx provisioned in Docker mode on %s\n", host.ExternalIP)
	return nil
}

func (n *NginxProvisioner) provisionNative(ctx context.Context, host inventory.Host, config ServiceConfig, _ *gitops.ServiceInfo) error {
	fmt.Printf("Provisioning %s in native mode...\n", n.GetName())
	family, err := n.DetectDistroFamily(ctx, host)
	if err != nil {
		return fmt.Errorf("failed to detect distro family: %w", err)
	}
	requiresGeoIP := false
	rootDomain, _ := config.Metadata["root_domain"].(string) //nolint:errcheck // zero value acceptable
	for _, route := range BuildLocalProxyRoutes(rootDomain, localServicePorts(config.Metadata)) {
		if route.GeoProxy {
			requiresGeoIP = true
			break
		}
	}

	probeResult, err := n.RunCommand(ctx, host, `sh -c 'if ss -tlnp 2>/dev/null | grep -q "nginx"; then if command -v nginx >/dev/null 2>&1 && [ -f /etc/nginx/nginx.conf ]; then echo PACKAGED; else echo CUSTOM; fi; else echo ABSENT; fi'`)
	if err != nil {
		return fmt.Errorf("failed to inspect existing nginx runtime: %w", err)
	}
	if probeResult.ExitCode != 0 {
		return fmt.Errorf("failed to inspect existing nginx runtime: %s", probeResult.Stderr)
	}
	switch runtime := strings.TrimSpace(probeResult.Stdout); runtime {
	case "CUSTOM":
		return fmt.Errorf("detected a running nginx outside the packaged /etc/nginx layout on %s; migrate it first with scripts/migrate-central-nginx.sh before CLI-managed ingress can take ownership", host.ExternalIP)
	case "PACKAGED", "ABSENT":
	default:
		return fmt.Errorf("unexpected nginx runtime probe result: %q", runtime)
	}

	pythonPkg := distroPythonPackage(family)
	nginxPkgs := nginxPackageNames(family, requiresGeoIP)

	tasks := []ansible.Task{}
	for _, pkg := range nginxPkgs {
		tasks = append(tasks, ansible.TaskPackage(pkg, ansible.PackagePresent))
	}
	tasks = append(tasks,
		ansible.TaskPackage(pythonPkg, ansible.PackagePresent),
		ansible.Task{
			Name:   "ensure nginx sites-available dir",
			Module: "ansible.builtin.file",
			Args:   map[string]any{"path": "/etc/nginx/sites-available", "state": "directory", "mode": "0755"},
		},
		ansible.Task{
			Name:   "ensure nginx sites-enabled dir",
			Module: "ansible.builtin.file",
			Args:   map[string]any{"path": "/etc/nginx/sites-enabled", "state": "directory", "mode": "0755"},
		},
		ansible.Task{
			Name:   "ensure nginx frameworks-http.d dir",
			Module: "ansible.builtin.file",
			Args:   map[string]any{"path": "/etc/nginx/frameworks-http.d", "state": "directory", "mode": "0755"},
		},
		// Locate the GeoIP2 module (path varies by distro). Empty result means
		// no module shipped — we conditionally emit load_module below.
		ansible.Task{
			Name:   "locate nginx geoip2 module",
			Module: "ansible.builtin.shell",
			Args: map[string]any{
				"cmd": "find /usr/lib64/nginx/modules /usr/lib/nginx/modules -maxdepth 1 -type f " +
					`\( -name '*http_geoip2*.so' -o -name '*geoip2*.so' \) 2>/dev/null | head -n 1`,
			},
			Register:    "frameworks_geoip2",
			ChangedWhen: "false",
			Ignore:      true,
		},
		// load_module must appear at the top of nginx.conf; blockinfile with
		// insertbefore BOF + a FrameWorks-scoped marker keeps it idempotent.
		ansible.Task{
			Name:   "insert geoip2 load_module at top of /etc/nginx/nginx.conf",
			Module: "ansible.builtin.blockinfile",
			Args: map[string]any{
				"path":         "/etc/nginx/nginx.conf",
				"block":        "load_module {{ frameworks_geoip2.stdout | trim }};",
				"insertbefore": "BOF",
				"marker":       "# {mark} FrameWorks geoip2 load_module",
			},
			When: "frameworks_geoip2.stdout | default('') | trim | length > 0",
		},
		// Two independent include directives, each bracketed by its own marker
		// so neither can be accidentally deduped. insertafter puts them right
		// inside `http {` — matching the original Python munging exactly.
		ansible.Task{
			Name:   "ensure sites-enabled include inside http block",
			Module: "ansible.builtin.blockinfile",
			Args: map[string]any{
				"path":        "/etc/nginx/nginx.conf",
				"block":       "    include /etc/nginx/sites-enabled/*;",
				"insertafter": `^\s*http\s*\{`,
				"marker":      "    # {mark} FrameWorks sites-enabled include",
			},
		},
		ansible.Task{
			Name:   "ensure frameworks-http.d include inside http block",
			Module: "ansible.builtin.blockinfile",
			Args: map[string]any{
				"path":        "/etc/nginx/nginx.conf",
				"block":       "    include /etc/nginx/frameworks-http.d/*.conf;",
				"insertafter": `^\s*http\s*\{`,
				"marker":      "    # {mark} FrameWorks frameworks-http.d include",
			},
		},
		// Historical frameworks.conf locations: older versions dropped a single
		// conf file under sites-available/sites-enabled/conf.d. The new layout
		// spreads it across sites-enabled/ and frameworks-http.d/, so clear the
		// old locations to avoid duplicate-server-name warnings.
		ansible.Task{
			Name:   "remove legacy sites-available frameworks.conf",
			Module: "ansible.builtin.file",
			Args:   map[string]any{"path": "/etc/nginx/sites-available/frameworks.conf", "state": "absent"},
		},
		ansible.Task{
			Name:   "remove legacy sites-enabled frameworks.conf",
			Module: "ansible.builtin.file",
			Args:   map[string]any{"path": "/etc/nginx/sites-enabled/frameworks.conf", "state": "absent"},
		},
		ansible.Task{
			Name:   "remove legacy conf.d frameworks.conf",
			Module: "ansible.builtin.file",
			Args:   map[string]any{"path": "/etc/nginx/conf.d/frameworks.conf", "state": "absent"},
		},
		ansible.TaskSystemdService("nginx", ansible.SystemdOpts{
			State:   "started",
			Enabled: ansible.BoolPtr(true),
		}),
	)

	playbook := &ansible.Playbook{
		Name:  "Install Nginx (native)",
		Hosts: host.ExternalIP,
		Plays: []ansible.Play{
			{
				Name:        "Install packaged Nginx",
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
			"ansible_ssh_private_key_file": n.pool.DefaultKeyPath(),
		},
	})
	result, execErr := n.executor.ExecutePlaybook(ctx, playbook, inv, ansible.ExecuteOptions{Verbose: true})
	if execErr != nil {
		return fmt.Errorf("nginx install failed: %w\nOutput: %s", execErr, result.Output)
	}
	if !result.Success {
		return fmt.Errorf("nginx install playbook failed\nOutput: %s", result.Output)
	}

	fmt.Printf("✓ Nginx provisioned in native mode on %s\n", host.ExternalIP)
	return nil
}

// nginxPackageNames returns the distro-specific nginx package names. GeoIP2
// packages differ per distro: Debian/Ubuntu name them libnginx-mod-*, Arch
// has nginx-mod-geoip2, and RHEL may ship them separately (best-effort).
func nginxPackageNames(family string, requiresGeoIP bool) []string {
	switch family {
	case "debian":
		return []string{"nginx", "libnginx-mod-http-geoip2", "libnginx-mod-stream-geoip2"}
	case "rhel":
		if requiresGeoIP {
			// nginx-mod-http-geoip2 exists on AlmaLinux/Rocky; skipped if missing.
			return []string{"nginx", "nginx-mod-http-geoip2"}
		}
		return []string{"nginx"}
	case "arch":
		return []string{"nginx", "nginx-mod-geoip2"}
	default:
		return []string{"nginx"}
	}
}

// Validate runs goss in native mode, then the /health HTTP probe and TLS port
// check.
func (n *NginxProvisioner) Validate(ctx context.Context, host inventory.Host, config ServiceConfig) error {
	if config.Mode == "native" {
		if _, remoteArch, err := n.DetectRemoteArch(ctx, host); err == nil {
			spec := ansible.RenderGossYAML(ansible.GossSpec{
				Services: map[string]ansible.GossService{
					"nginx": {Running: true, Enabled: true},
				},
				Files: map[string]ansible.GossFile{
					"/etc/nginx/nginx.conf": {Exists: true},
				},
			})
			if gossErr := runGossValidate(ctx, n.executor, n.pool.DefaultKeyPath(), host,
				"nginx", platformChannelFromMetadata(config.Metadata), config.Metadata, remoteArch, spec); gossErr != nil {
				return fmt.Errorf("nginx goss validate failed: %w", gossErr)
			}
		}
	}

	checker := &health.HTTPChecker{Path: "/health", Timeout: 5}
	if result := checker.Check(host.ExternalIP, 80); !result.OK {
		return fmt.Errorf("nginx health check failed: %s", result.Error)
	}
	publicTLS := &health.TCPChecker{Timeout: 5 * time.Second}
	if result := publicTLS.Check(host.ExternalIP, 443); !result.OK {
		return fmt.Errorf("nginx HTTPS port check failed: %s", result.Error)
	}
	return nil
}

// Initialize is a no-op
func (n *NginxProvisioner) Initialize(ctx context.Context, host inventory.Host, config ServiceConfig) error {
	return nil
}

func localServicePorts(metadata map[string]any) map[string]int {
	if localServicesMap, ok := metadata["local_services"].(map[string]any); ok {
		routes := make(map[string]int)
		for svcName, svcPort := range localServicesMap {
			switch port := svcPort.(type) {
			case int:
				routes[svcName] = port
			case int32:
				routes[svcName] = int(port)
			case int64:
				routes[svcName] = int(port)
			case float64:
				routes[svcName] = int(port)
			}
		}
		return routes
	}

	return map[string]int{
		"foredeck":  ServicePorts["foredeck"],
		"chartroom": ServicePorts["chartroom"],
		"bridge":    ServicePorts["bridge"],
		"logbook":   ServicePorts["logbook"],
		"steward":   ServicePorts["steward"],
		"listmonk":  ServicePorts["listmonk"],
		"chatwoot":  ServicePorts["chatwoot"],
	}
}

func distroPythonPackage(family string) string {
	switch family {
	case "debian":
		return "python3"
	case "rhel":
		return "python3"
	case "arch":
		return "python"
	default:
		return "python3"
	}
}

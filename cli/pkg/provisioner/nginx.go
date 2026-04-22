package provisioner

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"frameworks/cli/pkg/gitops"
	"frameworks/cli/pkg/health"
	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/ssh"
	"frameworks/pkg/maintenance"
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
	state, err := n.Detect(ctx, host)
	if config.Mode == "docker" && err == nil && state.Exists && state.Running {
		fmt.Printf("Service %s already running, skipping install...\n", n.GetName())
		return nil
	}

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

	setupScript := fmt.Sprintf(`#!/bin/bash
set -e
if ! command -v nginx >/dev/null 2>&1; then
  %s
fi
if ! command -v python3 >/dev/null 2>&1; then
  %s
fi
export FRAMEWORKS_GEOIP2_MODULE="$(find /usr/lib64/nginx/modules /usr/lib/nginx/modules -maxdepth 1 -type f \( -name '*http_geoip2*.so' -o -name '*geoip2*.so' \) 2>/dev/null | head -n 1)"
mkdir -p /etc/nginx/sites-available /etc/nginx/sites-enabled /etc/nginx/frameworks-http.d
python3 - <<'PY'
import os
from pathlib import Path
path = Path("/etc/nginx/nginx.conf")
text = path.read_text()
module_path = os.environ.get("FRAMEWORKS_GEOIP2_MODULE", "").strip()
include_line = "    include /etc/nginx/sites-enabled/*;\n"
snippet_include_line = "    include /etc/nginx/frameworks-http.d/*.conf;\n"
if module_path:
    load_line = f"load_module {module_path};\n"
    if load_line not in text:
        text = load_line + text
if "/etc/nginx/sites-enabled/*" not in text:
    marker = "http {"
    idx = text.find(marker)
    if idx == -1:
        raise SystemExit("http block not found in /etc/nginx/nginx.conf")
    insert_at = idx + len(marker)
    text = text[:insert_at] + "\n" + include_line + text[insert_at:]
if "/etc/nginx/frameworks-http.d/*.conf" not in text:
    marker = "http {"
    idx = text.find(marker)
    if idx == -1:
        raise SystemExit("http block not found in /etc/nginx/nginx.conf")
    insert_at = idx + len(marker)
    text = text[:insert_at] + "\n" + snippet_include_line + text[insert_at:]
path.write_text(text)
PY
rm -f /etc/nginx/sites-available/frameworks.conf /etc/nginx/sites-enabled/frameworks.conf /etc/nginx/conf.d/frameworks.conf
systemctl enable nginx
systemctl start nginx
`, nginxPackageInstallCommand(family, requiresGeoIP), packageInstallCommand(family, distroPythonPackage(family)))

	result, err := n.ExecuteScript(ctx, host, setupScript)
	if err != nil || result.ExitCode != 0 {
		return fmt.Errorf("failed to install packaged nginx: %w (stderr: %s)", err, result.Stderr)
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

	publicTLS := &health.TCPChecker{
		Timeout: 5 * time.Second,
	}
	tlsResult := publicTLS.Check(host.ExternalIP, 443)
	if !tlsResult.OK {
		return fmt.Errorf("nginx HTTPS port check failed: %s", tlsResult.Error)
	}

	return nil
}

// Initialize is a no-op
func (n *NginxProvisioner) Initialize(ctx context.Context, host inventory.Host, config ServiceConfig) error {
	return nil
}

func localServicePorts(metadata map[string]interface{}) map[string]int {
	if localServicesMap, ok := metadata["local_services"].(map[string]interface{}); ok {
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

func packageInstallCommand(family, pkg string) string {
	switch family {
	case "debian":
		return fmt.Sprintf("apt-get -o DPkg::Lock::Timeout=300 update && apt-get -o DPkg::Lock::Timeout=300 install -y %s", pkg)
	case "rhel":
		return fmt.Sprintf("(dnf install -y %s || yum install -y %s)", pkg, pkg)
	case "arch":
		return fmt.Sprintf("pacman -Syu --noconfirm --needed %s", pkg)
	default:
		return "echo unsupported distro && exit 1"
	}
}

func nginxPackageInstallCommand(family string, requiresGeoIP bool) string {
	switch family {
	case "debian":
		return "apt-get -o DPkg::Lock::Timeout=300 update && apt-get -o DPkg::Lock::Timeout=300 install -y nginx libnginx-mod-http-geoip2 libnginx-mod-stream-geoip2"
	case "rhel":
		if !requiresGeoIP {
			return "(dnf install -y nginx || yum install -y nginx)"
		}
		return `(dnf install -y nginx || yum install -y nginx)
if command -v dnf >/dev/null 2>&1; then
  dnf install -y nginx-module-geoip2 || dnf install -y nginx-mod-http-geoip2 || true
elif command -v yum >/dev/null 2>&1; then
  yum install -y nginx-module-geoip2 || yum install -y nginx-mod-http-geoip2 || true
fi
MODULE_PATH=$(find /usr/lib64/nginx/modules /usr/lib/nginx/modules -maxdepth 1 -type f \( -name '*http_geoip2*.so' -o -name '*geoip2*.so' \) 2>/dev/null | head -n 1)
[ -n "$MODULE_PATH" ] || { echo "GeoIP2 module required but not available on this RHEL host" >&2; exit 1; }`
	case "arch":
		return "pacman -Syu --noconfirm --needed nginx nginx-mod-geoip2"
	default:
		return "echo unsupported distro && exit 1"
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

package provisioner

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"frameworks/cli/pkg/detect"
	"frameworks/cli/pkg/gitops"
	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/ssh"
	"frameworks/cli/pkg/system"
	infra "frameworks/pkg/models"
)

// PrivateerProvisioner provisions the Privateer mesh agent
type PrivateerProvisioner struct {
	*BaseProvisioner
}

// NewPrivateerProvisioner creates a new Privateer provisioner
func NewPrivateerProvisioner(pool *ssh.Pool) *PrivateerProvisioner {
	return &PrivateerProvisioner{
		BaseProvisioner: NewBaseProvisioner("privateer", pool),
	}
}

// Detect checks if Privateer is installed
func (p *PrivateerProvisioner) Detect(ctx context.Context, host inventory.Host) (*detect.ServiceState, error) {
	return p.CheckExists(ctx, host, "privateer")
}

// Provision installs and configures Privateer
func (p *PrivateerProvisioner) Provision(ctx context.Context, host inventory.Host, config ServiceConfig) error {
	state, err := p.Detect(ctx, host)
	if err != nil {
		state = nil
	}
	if skip, reason := shouldSkipProvision(state, config, "", ""); skip {
		fmt.Printf("Service %s already running (%s), skipping...\n", p.name, reason)
		return nil
	}

	// 1. Install WireGuard tools
	if err := p.installDependencies(ctx, host); err != nil {
		return fmt.Errorf("failed to install dependencies: %w", err)
	}

	// 2. Fetch and Install Binary
	if err := p.installBinary(ctx, host, config.Version); err != nil {
		return fmt.Errorf("failed to install binary: %w", err)
	}

	// 3. Configure Systemd
	if err := p.configureSystemd(ctx, host, config); err != nil {
		return fmt.Errorf("failed to configure systemd: %w", err)
	}

	// 4. Start Service
	startCmd := "systemctl daemon-reload && systemctl enable frameworks-privateer && systemctl restart frameworks-privateer"
	result, err := p.RunCommand(ctx, host, startCmd)
	if err != nil || result.ExitCode != 0 {
		return fmt.Errorf("failed to start privateer: %w\nStderr: %s", err, result.Stderr)
	}

	if err := p.WaitForService(ctx, host, "privateer", 30*time.Second); err != nil {
		return fmt.Errorf("privateer did not become ready: %w", err)
	}

	if err := p.waitForInitialPKISync(ctx, host, config); err != nil {
		return fmt.Errorf("privateer initial PKI sync did not complete: %w", err)
	}

	dnsPort := parseDNSPort(config.Metadata["dns_port"])

	// 5. Configure Host DNS after Privateer is ready.
	if err := p.configureDNS(ctx, host, dnsPort); err != nil {
		return fmt.Errorf("failed to configure DNS: %w", err)
	}

	fmt.Printf("✓ Privateer provisioned on %s\n", host.ExternalIP)
	return nil
}

func (p *PrivateerProvisioner) installDependencies(ctx context.Context, host inventory.Host) error {
	// Install WireGuard userspace tools with the host's package manager.
	script := `#!/bin/bash
set -e

if command -v apt-get >/dev/null; then
    apt-get update && apt-get install -y wireguard-tools
elif command -v dnf >/dev/null; then
    dnf install -y wireguard-tools
elif command -v yum >/dev/null; then
    yum install -y wireguard-tools
elif command -v pacman >/dev/null; then
    pacman -Syu --noconfirm --needed wireguard-tools
else
    echo "Unsupported package manager"
    exit 1
fi
`
	result, err := p.ExecuteScript(ctx, host, script)
	if err != nil || result.ExitCode != 0 {
		return fmt.Errorf("dependency install failed: %v", result.Stderr)
	}
	return nil
}

func (p *PrivateerProvisioner) installBinary(ctx context.Context, host inventory.Host, version string) error {
	// Fetch from GitOps
	channel, resolved := gitops.ResolveVersion(version)
	fetcher, err := gitops.NewFetcher(gitops.FetchOptions{})
	if err != nil {
		return err
	}
	manifest, err := fetcher.Fetch(channel, resolved)
	if err != nil {
		return err
	}
	svcInfo, err := manifest.GetServiceInfo("privateer")
	if err != nil {
		return err
	}
	remoteOS, remoteArch, archErr := p.DetectRemoteArch(ctx, host)
	if archErr != nil {
		return fmt.Errorf("failed to detect remote architecture: %w", archErr)
	}
	url, err := svcInfo.GetBinaryURL(remoteOS, remoteArch)
	if err != nil {
		return err
	}

	script := fmt.Sprintf(`#!/bin/bash
set -e
wget -q -O /tmp/privateer.tar.gz "%s"
mkdir -p /opt/frameworks/privateer
tar -xzf /tmp/privateer.tar.gz -C /tmp/
mv /tmp/frameworks-privateer-* /opt/frameworks/privateer/privateer
chmod +x /opt/frameworks/privateer/privateer
rm /tmp/privateer.tar.gz
`, url)

	result, err := p.ExecuteScript(ctx, host, script)
	if err != nil || result.ExitCode != 0 {
		return fmt.Errorf("binary install failed: %v", result.Stderr)
	}
	return nil
}

func (p *PrivateerProvisioner) configureSystemd(ctx context.Context, host inventory.Host, config ServiceConfig) error {
	// Extract secrets — prefer per-cluster token from EnvVars (resolved by buildServiceEnvVars)
	qmGRPCAddr, _ := config.Metadata["quartermaster_grpc_addr"].(string)
	token := config.EnvVars["ENROLLMENT_TOKEN"]
	if token == "" {
		token, _ = config.Metadata["enrollment_token"].(string)
	}
	serviceToken, _ := config.Metadata["service_token"].(string)
	certIssueToken, _ := config.Metadata["cert_issue_token"].(string)
	nodeType, _ := config.Metadata["mesh_node_type"].(string)
	if nodeType == "" {
		nodeType = infra.NodeTypeCore
	}
	nodeName, _ := config.Metadata["mesh_node_name"].(string)
	navigatorGRPCAddr := config.EnvVars["NAVIGATOR_GRPC_ADDR"]
	grpcAllowInsecure := config.EnvVars["GRPC_ALLOW_INSECURE"]
	if grpcAllowInsecure == "" {
		grpcAllowInsecure = "true"
	}
	buildEnv := config.EnvVars["BUILD_ENV"]
	expectedServices := metadataStringSlice(config.Metadata["expected_internal_grpc_services"])

	dnsPort := strconv.Itoa(parseDNSPort(config.Metadata["dns_port"]))

	// Capture the host's current upstream nameservers before we overwrite resolv.conf,
	// so Privateer can forward non-.internal queries to them.
	var upstreamDNS string
	captureResult, captureErr := p.RunCommand(ctx, host, system.CaptureUpstreamNameservers())
	if captureErr == nil && captureResult.ExitCode == 0 {
		upstreamDNS = strings.TrimSpace(captureResult.Stdout)
	}

	nodeID, _ := config.Metadata["node_id"].(string) //nolint:errcheck // type assertion, not error

	envContent := fmt.Sprintf(`QUARTERMASTER_GRPC_ADDR=%s
NAVIGATOR_GRPC_ADDR=%s
SERVICE_TOKEN=%s
ENROLLMENT_TOKEN=%s
CERT_ISSUANCE_TOKEN=%s
DNS_PORT=%s
MESH_INTERFACE=wg0
MESH_NODE_TYPE=%s
MESH_NODE_NAME=%s
MESH_EXTERNAL_IP=%s
NODE_ID=%s
GRPC_TLS_PKI_DIR=/etc/frameworks/pki
GRPC_TLS_CA_PATH=/etc/frameworks/pki/ca.crt
GRPC_ALLOW_INSECURE=%s
BUILD_ENV=%s
`, qmGRPCAddr, navigatorGRPCAddr, serviceToken, token, certIssueToken, dnsPort, nodeType, nodeName, host.ExternalIP, nodeID, grpcAllowInsecure, buildEnv)

	if upstreamDNS != "" {
		envContent += fmt.Sprintf("UPSTREAM_DNS=%s\n", upstreamDNS)
	}
	if len(expectedServices) > 0 {
		envContent += fmt.Sprintf("EXPECTED_INTERNAL_GRPC_SERVICES=%s\n", strings.Join(expectedServices, ","))
	}

	// Upload Env File
	tmpEnv := filepath.Join(os.TempDir(), "privateer.env")
	if err := os.WriteFile(tmpEnv, []byte(envContent), 0600); err != nil {
		return err
	}
	if err := p.UploadFile(ctx, host, ssh.UploadOptions{
		LocalPath: tmpEnv, RemotePath: "/etc/frameworks/privateer.env", Mode: 0600,
	}); err != nil {
		return err
	}

	// Generate Unit
	unitData := SystemdUnitData{
		ServiceName: "privateer",
		Description: "FrameWorks Privateer Mesh Agent",
		WorkingDir:  "/opt/frameworks/privateer",
		ExecStart:   "/opt/frameworks/privateer/privateer",
		User:        "root", // Needs root for WireGuard
		EnvFile:     "/etc/frameworks/privateer.env",
		Restart:     "always",
	}
	unitContent, err := GenerateSystemdUnit(unitData)
	if err != nil {
		return err
	}

	// Upload Unit
	tmpUnit := filepath.Join(os.TempDir(), "privateer.service")
	if err := os.WriteFile(tmpUnit, []byte(unitContent), 0644); err != nil {
		return err
	}
	return p.UploadFile(ctx, host, ssh.UploadOptions{
		LocalPath: tmpUnit, RemotePath: "/etc/systemd/system/frameworks-privateer.service", Mode: 0644,
	})
}

func parseDNSPort(raw any) int {
	const defaultPort = 53
	var port int

	switch v := raw.(type) {
	case string:
		if parsed, err := strconv.Atoi(v); err == nil {
			port = parsed
		}
	case int:
		port = v
	case int32:
		port = int(v)
	case int64:
		port = int(v)
	}

	if port < 1 || port > 65535 {
		return defaultPort
	}

	return port
}

func (p *PrivateerProvisioner) waitForInitialPKISync(ctx context.Context, host inventory.Host, config ServiceConfig) error {
	certIssueToken, _ := config.Metadata["cert_issue_token"].(string)
	if strings.TrimSpace(certIssueToken) == "" {
		return nil
	}

	paths := initialPKIPaths()

	script := "#!/bin/bash\nset -e\npaths=(\n"
	for _, path := range paths {
		script += fmt.Sprintf("  %q\n", path)
	}
	script += ")\nfor _ in $(seq 1 60); do\n  ready=1\n  for path in \"${paths[@]}\"; do\n    if [ ! -s \"$path\" ]; then\n      ready=0\n      break\n    fi\n  done\n  if [ \"$ready\" -eq 1 ]; then\n    exit 0\n  fi\n  sleep 2\n done\nprintf 'timed out waiting for initial PKI files\\n' >&2\nexit 1\n"

	result, err := p.ExecuteScript(ctx, host, script)
	if err != nil {
		return err
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("%s", strings.TrimSpace(result.Stderr))
	}
	return nil
}

func initialPKIPaths() []string {
	return []string{"/etc/frameworks/pki/ca.crt"}
}

func metadataStringSlice(raw interface{}) []string {
	switch values := raw.(type) {
	case []string:
		out := make([]string, 0, len(values))
		for _, value := range values {
			value = strings.TrimSpace(value)
			if value != "" {
				out = append(out, value)
			}
		}
		return out
	case []interface{}:
		out := make([]string, 0, len(values))
		for _, value := range values {
			if str, ok := value.(string); ok {
				str = strings.TrimSpace(str)
				if str != "" {
					out = append(out, str)
				}
			}
		}
		return out
	default:
		return nil
	}
}

func (p *PrivateerProvisioner) configureDNS(ctx context.Context, host inventory.Host, port int) error {
	// Generate config content
	conf, err := system.GenerateSystemdResolvedConfig(port)
	if err != nil {
		return err
	}

	// Check for systemd-resolved
	checkCmd := system.DetectSystemdResolved()
	result, _ := p.RunCommand(ctx, host, checkCmd)

	var script string
	if result.ExitCode == 0 {
		// Systemd-resolved active
		fmt.Println("    Configuring systemd-resolved...")
		script = system.ConfigureSystemdResolved(conf)
	} else {
		// Fallback
		fmt.Println("    Configuring /etc/resolv.conf (fallback).")
		script = system.ConfigureResolvConf()
	}

	res, err := p.ExecuteScript(ctx, host, script)
	if err != nil || res.ExitCode != 0 {
		return fmt.Errorf("DNS configuration failed: %s", res.Stderr)
	}
	return nil
}

// Validate checks health
func (p *PrivateerProvisioner) Validate(ctx context.Context, host inventory.Host, config ServiceConfig) error {
	// Check process
	return p.WaitForService(ctx, host, "privateer", 10)
}

// Initialize - no op
func (p *PrivateerProvisioner) Initialize(ctx context.Context, host inventory.Host, config ServiceConfig) error {
	return nil
}

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
	state, detectErr := p.Detect(ctx, host)
	if detectErr != nil {
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
	if err := p.installBinary(ctx, host, config.Version, config.Metadata); err != nil {
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
    apt-get -o DPkg::Lock::Timeout=300 update && apt-get -o DPkg::Lock::Timeout=300 install -y wireguard-tools
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

func (p *PrivateerProvisioner) installBinary(ctx context.Context, host inventory.Host, version string, metadata map[string]any) error {
	// Fetch from GitOps
	channel, resolved := gitops.ResolveVersion(version)
	manifest, err := fetchGitopsManifest(channel, resolved, metadata)
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
ASSET_URL=%q
ASSET_PATH=/tmp/privateer.asset
EXTRACT_DIR="$(mktemp -d)"
trap 'rm -rf "$EXTRACT_DIR" "$ASSET_PATH"' EXIT

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
mkdir -p /opt/frameworks/privateer
if [[ "$ASSET_URL" == *.zip ]]; then
  extract_zip "$ASSET_PATH" "$EXTRACT_DIR"
  mv "$EXTRACT_DIR"/frameworks-privateer-* /opt/frameworks/privateer/privateer 2>/dev/null || mv "$EXTRACT_DIR"/privateer /opt/frameworks/privateer/privateer
else
  tar -xzf "$ASSET_PATH" -C "$EXTRACT_DIR"
  mv "$EXTRACT_DIR"/frameworks-privateer-* /opt/frameworks/privateer/privateer
fi
chmod +x /opt/frameworks/privateer/privateer
`, url)

	result, err := p.ExecuteScript(ctx, host, script)
	if err != nil || result.ExitCode != 0 {
		return fmt.Errorf("binary install failed: %v", result.Stderr)
	}
	return nil
}

// PrivateerEnvInputs bundles everything BuildPrivateerEnv needs. UpstreamDNS
// is host-captured at apply time; drift passes "" and IgnoreKeys handles it.
type PrivateerEnvInputs struct {
	QMGRPCAddr        string
	NavigatorGRPCAddr string
	ServiceToken      string
	EnrollmentToken   string
	CertIssueToken    string
	DNSPort           string
	NodeType          string
	NodeName          string
	ExternalIP        string
	NodeID            string
	GRPCAllowInsecure string
	BuildEnv          string
	UpstreamDNS       string
	ExpectedServices  []string
}

// BuildPrivateerEnv returns the /etc/frameworks/privateer.env bytes.
func BuildPrivateerEnv(in PrivateerEnvInputs) []byte {
	nodeType := in.NodeType
	if nodeType == "" {
		nodeType = infra.NodeTypeCore
	}
	allowInsecure := in.GRPCAllowInsecure
	if allowInsecure == "" {
		allowInsecure = "true"
	}
	content := fmt.Sprintf(`QUARTERMASTER_GRPC_ADDR=%s
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
`, in.QMGRPCAddr, in.NavigatorGRPCAddr, in.ServiceToken, in.EnrollmentToken, in.CertIssueToken, in.DNSPort, nodeType, in.NodeName, in.ExternalIP, in.NodeID, allowInsecure, in.BuildEnv)
	if in.UpstreamDNS != "" {
		content += fmt.Sprintf("UPSTREAM_DNS=%s\n", in.UpstreamDNS)
	}
	if len(in.ExpectedServices) > 0 {
		content += fmt.Sprintf("EXPECTED_INTERNAL_GRPC_SERVICES=%s\n", strings.Join(in.ExpectedServices, ","))
	}
	return []byte(content)
}

// BuildPrivateerSystemdUnit returns the frameworks-privateer.service bytes.
// Runs as root because WireGuard requires it.
func BuildPrivateerSystemdUnit() ([]byte, error) {
	unit, err := GenerateSystemdUnit(SystemdUnitData{
		ServiceName: "privateer",
		Description: "FrameWorks Privateer Mesh Agent",
		WorkingDir:  "/opt/frameworks/privateer",
		ExecStart:   "/opt/frameworks/privateer/privateer",
		User:        "root",
		EnvFile:     "/etc/frameworks/privateer.env",
		Restart:     "always",
	})
	if err != nil {
		return nil, err
	}
	return []byte(unit), nil
}

// privateerInputsFromConfig extracts the BuildPrivateerEnv inputs from a
// ServiceConfig. UpstreamDNS is left empty — callers that want the apply-
// time value fill it after capturing it on the host.
func privateerInputsFromConfig(host inventory.Host, config ServiceConfig) PrivateerEnvInputs {
	token := config.EnvVars["ENROLLMENT_TOKEN"]
	if token == "" {
		if v, ok := config.Metadata["enrollment_token"].(string); ok {
			token = v
		}
	}
	qmGRPCAddr, _ := config.Metadata["quartermaster_grpc_addr"].(string) //nolint:errcheck // zero value acceptable
	serviceToken, _ := config.Metadata["service_token"].(string)         //nolint:errcheck
	certIssueToken, _ := config.Metadata["cert_issue_token"].(string)    //nolint:errcheck
	nodeType, _ := config.Metadata["mesh_node_type"].(string)            //nolint:errcheck
	nodeName, _ := config.Metadata["mesh_node_name"].(string)            //nolint:errcheck
	nodeID, _ := config.Metadata["node_id"].(string)                     //nolint:errcheck
	return PrivateerEnvInputs{
		QMGRPCAddr:        qmGRPCAddr,
		NavigatorGRPCAddr: config.EnvVars["NAVIGATOR_GRPC_ADDR"],
		ServiceToken:      serviceToken,
		EnrollmentToken:   token,
		CertIssueToken:    certIssueToken,
		DNSPort:           strconv.Itoa(parseDNSPort(config.Metadata["dns_port"])),
		NodeType:          nodeType,
		NodeName:          nodeName,
		ExternalIP:        host.ExternalIP,
		NodeID:            nodeID,
		GRPCAllowInsecure: config.EnvVars["GRPC_ALLOW_INSECURE"],
		BuildEnv:          config.EnvVars["BUILD_ENV"],
		ExpectedServices:  metadataStringSlice(config.Metadata["expected_internal_grpc_services"]),
	}
}

func (p *PrivateerProvisioner) configureSystemd(ctx context.Context, host inventory.Host, config ServiceConfig) error {
	inputs := privateerInputsFromConfig(host, config)

	// Capture the host's current upstream nameservers before we overwrite resolv.conf,
	// so Privateer can forward non-.internal queries to them.
	captureResult, captureErr := p.RunCommand(ctx, host, system.CaptureUpstreamNameservers())
	if captureErr == nil && captureResult.ExitCode == 0 {
		inputs.UpstreamDNS = strings.TrimSpace(captureResult.Stdout)
	}

	envContent := string(BuildPrivateerEnv(inputs))

	tmpEnv := filepath.Join(os.TempDir(), "privateer.env")
	if err := os.WriteFile(tmpEnv, []byte(envContent), 0600); err != nil {
		return err
	}
	if err := p.UploadFile(ctx, host, ssh.UploadOptions{
		LocalPath: tmpEnv, RemotePath: "/etc/frameworks/privateer.env", Mode: 0600,
	}); err != nil {
		return err
	}

	unitContent, err := BuildPrivateerSystemdUnit()
	if err != nil {
		return err
	}
	tmpUnit := filepath.Join(os.TempDir(), "privateer.service")
	if err := os.WriteFile(tmpUnit, unitContent, 0644); err != nil {
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
	certIssueToken, ok := config.Metadata["cert_issue_token"].(string)
	if !ok || strings.TrimSpace(certIssueToken) == "" {
		return nil
	}

	expectedServices := metadataStringSlice(config.Metadata["expected_internal_grpc_services"])
	paths := initialPKIPaths(expectedServices)

	var b strings.Builder
	b.WriteString("#!/bin/bash\nset -e\npaths=(\n")
	for _, path := range paths {
		fmt.Fprintf(&b, "  %q\n", path)
	}
	b.WriteString(")\nfor _ in $(seq 1 60); do\n  ready=1\n  for path in \"${paths[@]}\"; do\n    if [ ! -s \"$path\" ]; then\n      ready=0\n      break\n    fi\n  done\n  if [ \"$ready\" -eq 1 ]; then\n    exit 0\n  fi\n  sleep 2\n done\nprintf 'timed out waiting for initial PKI files\\n' >&2\nexit 1\n")
	script := b.String()

	result, err := p.ExecuteScript(ctx, host, script)
	if err != nil {
		return err
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("%s", strings.TrimSpace(result.Stderr))
	}
	return nil
}

func initialPKIPaths(expectedServices []string) []string {
	paths := []string{"/etc/frameworks/pki/ca.crt"}
	for _, svc := range expectedServices {
		base := fmt.Sprintf("/etc/frameworks/pki/services/%s", svc)
		paths = append(paths, base+"/tls.crt", base+"/tls.key")
	}
	return paths
}

func metadataStringSlice(raw any) []string {
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
	case []any:
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

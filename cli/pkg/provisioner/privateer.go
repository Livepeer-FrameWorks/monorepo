package provisioner

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"frameworks/cli/pkg/ansible"
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
	executor *ansible.Executor
}

// NewPrivateerProvisioner creates a new Privateer provisioner
func NewPrivateerProvisioner(pool *ssh.Pool) *PrivateerProvisioner {
	executor, err := ansible.NewExecutor("")
	if err != nil {
		// NewExecutor failure is fatal at startup; surface via panic so the
		// CLI fails fast at provisioner construction rather than on first use.
		panic(fmt.Sprintf("create ansible executor: %v", err))
	}
	return &PrivateerProvisioner{
		BaseProvisioner: NewBaseProvisioner("privateer", pool),
		executor:        executor,
	}
}

// Detect checks if Privateer is installed
func (p *PrivateerProvisioner) Detect(ctx context.Context, host inventory.Host) (*detect.ServiceState, error) {
	return p.CheckExists(ctx, host, "privateer")
}

// Provision installs and configures Privateer. Runs every apply; each task
// is idempotent via its own gate (package state=present, version-keyed
// install sentinel, systemd state=started).
func (p *PrivateerProvisioner) Provision(ctx context.Context, host inventory.Host, config ServiceConfig) error {
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
	// wireguard-tools is the same package name across apt/dnf/yum/pacman, so
	// `package` module's auto-detect routes correctly; no DistroPackageSpec needed.
	playbook := &ansible.Playbook{
		Name:  "Install Privateer dependencies",
		Hosts: host.ExternalIP,
		Plays: []ansible.Play{
			{
				Name:        "Install WireGuard userspace tools",
				Hosts:       host.ExternalIP,
				Become:      true,
				GatherFacts: true,
				Tasks:       []ansible.Task{ansible.TaskPackage("wireguard-tools", ansible.PackagePresent)},
			},
		},
	}

	inv := ansible.NewInventory()
	inv.AddHost(&ansible.InventoryHost{
		Name:    host.ExternalIP,
		Address: host.ExternalIP,
		Vars: map[string]string{
			"ansible_user":                 host.User,
			"ansible_ssh_private_key_file": p.sshPool.DefaultKeyPath(),
		},
	})

	result, err := p.executor.ExecutePlaybook(ctx, playbook, inv, ansible.ExecuteOptions{Verbose: true})
	if err != nil {
		return fmt.Errorf("ansible execution failed: %w\nOutput: %s", err, result.Output)
	}
	if !result.Success {
		return fmt.Errorf("ansible playbook failed\nOutput: %s", result.Output)
	}
	return nil
}

func (p *PrivateerProvisioner) installBinary(ctx context.Context, host inventory.Host, version string, metadata map[string]any) error {
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
	bin, err := svcInfo.GetBinary(remoteOS, remoteArch)
	if err != nil {
		return err
	}
	url, checksum := bin.URL, bin.Checksum

	// The privateer release asset is tar.gz on linux, zip on darwin. Ansible's
	// unarchive handles both but needs `unzip` present for zip archives. A
	// post-extract move picks up whichever filename variant shipped.
	// Version-keyed sentinel rotates when checksum or URL changes.
	isZip := strings.HasSuffix(url, ".zip")
	installSentinel := ansible.ArtifactSentinel("/opt/frameworks/privateer", checksum+url)
	tasks := []ansible.Task{
		mkdirTask("/opt/frameworks/privateer", "root", "root", "0755"),
	}
	if isZip {
		tasks = append(tasks, ansible.TaskPackage("unzip", ansible.PackagePresent))
	}
	tasks = append(tasks,
		ansible.TaskGetURL(url, "/tmp/privateer.asset", checksum),
		ansible.TaskUnarchive("/tmp/privateer.asset", "/opt/frameworks/privateer",
			installSentinel, ansible.UnarchiveOpts{}),
		ansible.TaskShell(
			"mv /opt/frameworks/privateer/frameworks-privateer-* /opt/frameworks/privateer/privateer 2>/dev/null || true; "+
				"chmod +x /opt/frameworks/privateer/privateer; touch "+installSentinel,
			ansible.ShellOpts{Creates: installSentinel},
		),
	)

	playbook := &ansible.Playbook{
		Name:  "Install Privateer binary",
		Hosts: host.ExternalIP,
		Plays: []ansible.Play{
			{
				Name:        "Install Privateer binary",
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
			"ansible_ssh_private_key_file": p.sshPool.DefaultKeyPath(),
		},
	})
	result, execErr := p.executor.ExecutePlaybook(ctx, playbook, inv, ansible.ExecuteOptions{Verbose: true})
	if execErr != nil {
		return fmt.Errorf("privateer binary install failed: %w\nOutput: %s", execErr, result.Output)
	}
	if !result.Success {
		return fmt.Errorf("privateer binary install playbook failed\nOutput: %s", result.Output)
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

// Validate asserts the privateer systemd unit is running via the standard
// ansible.builtin.service_facts lookup, then goss structural backstop.
func (p *PrivateerProvisioner) Validate(ctx context.Context, host inventory.Host, config ServiceConfig) error {
	tasks := []ansible.Task{
		{
			Name:   "gather service facts",
			Module: "ansible.builtin.service_facts",
		},
		{
			Name:   "assert privateer service running",
			Module: "ansible.builtin.assert",
			Args: map[string]any{
				"that":     []string{"ansible_facts.services['privateer.service'].state == 'running'"},
				"fail_msg": "privateer.service not running on host",
				"quiet":    true,
			},
		},
	}
	if err := runValidatePlaybook(ctx, p.executor, p.sshPool.DefaultKeyPath(), host, "privateer", tasks); err != nil {
		return err
	}
	if _, remoteArch, err := p.DetectRemoteArch(ctx, host); err == nil {
		spec := ansible.RenderGossYAML(ansible.GossSpec{
			Services: map[string]ansible.GossService{
				"privateer": {Running: true, Enabled: true},
			},
			Files: map[string]ansible.GossFile{
				"/opt/frameworks/privateer/privateer": {Exists: true},
			},
		})
		if gossErr := runGossValidate(ctx, p.executor, p.sshPool.DefaultKeyPath(), host,
			"privateer", platformChannelFromMetadata(config.Metadata), config.Metadata, remoteArch, spec); gossErr != nil {
			return fmt.Errorf("privateer goss validate failed: %w", gossErr)
		}
	}
	return nil
}

// Initialize - no op
func (p *PrivateerProvisioner) Initialize(ctx context.Context, host inventory.Host, config ServiceConfig) error {
	return nil
}

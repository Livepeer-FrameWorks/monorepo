package provisioner

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"time"

	"frameworks/cli/pkg/detect"
	"frameworks/cli/pkg/gitops"
	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/ssh"
	"frameworks/cli/pkg/system"
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

	dnsPort := 5353
	if value, ok := config.Metadata["dns_port"]; ok {
		switch v := value.(type) {
		case string:
			if parsed, err := strconv.Atoi(v); err == nil {
				dnsPort = parsed
			}
		case int:
			dnsPort = v
		case int32:
			dnsPort = int(v)
		case int64:
			dnsPort = int(v)
		}
	}

	// 5. Configure Host DNS after Privateer is ready.
	if err := p.configureDNS(ctx, host, dnsPort); err != nil {
		return fmt.Errorf("failed to configure DNS: %w", err)
	}

	fmt.Printf("✓ Privateer provisioned on %s\n", host.Address)
	return nil
}

func (p *PrivateerProvisioner) installDependencies(ctx context.Context, host inventory.Host) error {
	// Simple detection for apt vs yum
	script := `#!/bin/bash
if command -v apt-get >/dev/null;
    then
    apt-get update && apt-get install -y wireguard-tools
elif command -v yum >/dev/null;
    then
    yum install -y wireguard-tools
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
	fetcher, err := gitops.NewFetcher(gitops.FetchOptions{})
	if err != nil {
		return err
	}
	manifest, err := fetcher.Fetch("stable", version)
	if err != nil {
		return err
	}
	svcInfo, err := manifest.GetServiceInfo("privateer")
	if err != nil {
		return err
	}
	url, err := svcInfo.GetBinaryURL(runtime.GOOS, runtime.GOARCH)
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
	// Extract secrets from Metadata
	qmGRPCAddr, _ := config.Metadata["quartermaster_grpc_addr"].(string)
	token, _ := config.Metadata["enrollment_token"].(string)
	serviceToken, _ := config.Metadata["service_token"].(string)
	dnsPort, _ := config.Metadata["dns_port"].(string)
	if dnsPort == "" {
		dnsPort = "5353"
	}

	envContent := fmt.Sprintf(`QUARTERMASTER_GRPC_ADDR=%s
SERVICE_TOKEN=%s
ENROLLMENT_TOKEN=%s
DNS_PORT=%s
MESH_INTERFACE=wg0
`, qmGRPCAddr, serviceToken, token, dnsPort)

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

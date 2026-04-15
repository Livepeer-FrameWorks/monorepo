package provisioner

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"frameworks/cli/pkg/detect"
	"frameworks/cli/pkg/gitops"
	"frameworks/cli/pkg/health"
	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/ssh"
)

// LivepeerSignerProvisioner provisions go-livepeer in remote signer mode.
// The signer holds the ETH keystore and signs transactions on behalf of gateway nodes.
// One signer per cluster; multiple gateways point at it via -remoteSignerUrl.
type LivepeerSignerProvisioner struct {
	*BaseProvisioner
}

func NewLivepeerSignerProvisioner(pool *ssh.Pool) *LivepeerSignerProvisioner {
	return &LivepeerSignerProvisioner{
		BaseProvisioner: NewBaseProvisioner("livepeer-signer", pool),
	}
}

func (p *LivepeerSignerProvisioner) Detect(ctx context.Context, host inventory.Host) (*detect.ServiceState, error) {
	return p.CheckExists(ctx, host, "livepeer-signer")
}

func (p *LivepeerSignerProvisioner) Provision(ctx context.Context, host inventory.Host, config ServiceConfig) error {
	state, err := p.Detect(ctx, host)
	if err != nil {
		state = nil
	}

	switch config.Mode {
	case "native":
		desiredVersion := ""
		if config.Version != "" && config.Version != "stable" {
			desiredVersion = config.Version
		}
		if skip, reason := shouldSkipProvision(state, config, desiredVersion, ""); skip {
			fmt.Printf("Service %s already running (%s), skipping...\n", p.name, reason)
			return nil
		}
		return p.provisionNative(ctx, host, config)
	case "docker":
		image := config.Image
		if image == "" {
			image = p.resolveImageFromManifest(config.Version)
		}
		if image == "" {
			image = "ghcr.io/livepeer-frameworks/go-livepeer:latest"
		}
		if skip, reason := shouldSkipProvision(state, config, "", image); skip {
			fmt.Printf("Service %s already running (%s), skipping...\n", p.name, reason)
			return nil
		}
		return p.provisionDocker(ctx, host, config)
	default:
		return fmt.Errorf("unsupported mode %q for livepeer-signer (native or docker)", config.Mode)
	}
}

func (p *LivepeerSignerProvisioner) provisionNative(ctx context.Context, host inventory.Host, config ServiceConfig) error {
	fmt.Println("Provisioning livepeer-signer in native mode...")

	if err := p.installBinary(ctx, host, config); err != nil {
		return fmt.Errorf("failed to install binary: %w", err)
	}

	keystorePath, err := p.ensureKeystore(ctx, host, config)
	if err != nil {
		return fmt.Errorf("failed to ensure keystore: %w", err)
	}

	flags := p.buildFlags(config, keystorePath)

	// Build CLI args for ExecStart
	var args []string
	for k, v := range flags {
		args = append(args, fmt.Sprintf("-%s=%s", k, v))
	}

	envFile := "/etc/frameworks/livepeer-signer.env"
	if err = p.writeFlagsEnv(ctx, host, envFile, flags); err != nil {
		return fmt.Errorf("failed to write env file: %w", err)
	}

	unitData := SystemdUnitData{
		ServiceName: "livepeer-signer",
		Description: "Livepeer Remote Transaction Signer",
		WorkingDir:  "/opt/frameworks/livepeer-signer",
		ExecStart:   "/opt/frameworks/livepeer-signer/livepeer " + strings.Join(args, " "),
		User:        "root",
		EnvFile:     envFile,
		Restart:     "always",
	}

	unitContent, err := GenerateSystemdUnit(unitData)
	if err != nil {
		return err
	}

	tmpUnit := filepath.Join(os.TempDir(), "livepeer-signer.service")
	if err := os.WriteFile(tmpUnit, []byte(unitContent), 0644); err != nil {
		return err
	}

	unitPath := "/etc/systemd/system/frameworks-livepeer-signer.service"
	if err := p.UploadFile(ctx, host, ssh.UploadOptions{
		LocalPath: tmpUnit, RemotePath: unitPath, Mode: 0644,
	}); err != nil {
		return err
	}

	if result, err := p.RunCommand(ctx, host, "systemctl daemon-reload"); err != nil || result.ExitCode != 0 {
		return fmt.Errorf("failed to reload systemd: %w", err)
	}

	if config.DeferStart {
		fmt.Println("⏸ livepeer-signer deployed but NOT started")
	} else {
		startCmd := "systemctl enable frameworks-livepeer-signer && systemctl restart frameworks-livepeer-signer"
		if result, err := p.RunCommand(ctx, host, startCmd); err != nil || result.ExitCode != 0 {
			return fmt.Errorf("failed to start: %w\nStderr: %s", err, result.Stderr)
		}
		fmt.Println("✓ livepeer-signer provisioned in native mode")
	}
	return nil
}

func (p *LivepeerSignerProvisioner) provisionDocker(ctx context.Context, host inventory.Host, config ServiceConfig) error {
	fmt.Println("Provisioning livepeer-signer in Docker mode...")

	image := config.Image
	if image == "" {
		image = p.resolveImageFromManifest(config.Version)
	}
	if image == "" {
		image = "ghcr.io/livepeer-frameworks/go-livepeer:latest"
	}

	keystorePath, err := p.ensureKeystore(ctx, host, config)
	if err != nil {
		return fmt.Errorf("failed to ensure keystore: %w", err)
	}

	flags := p.buildFlags(config, keystorePath)

	port := config.Port
	if port == 0 {
		port = 18016
	}

	envFile := fmt.Sprintf("/etc/frameworks/%s.env", p.name)
	if err = p.writeFlagsEnv(ctx, host, envFile, flags); err != nil {
		return fmt.Errorf("failed to write env file: %w", err)
	}

	composeData := DockerComposeData{
		ServiceName: "livepeer-signer",
		Image:       image,
		Port:        port,
		EnvFile:     envFile,
		HealthCheck: &HealthCheckConfig{
			Test:     []string{"CMD", "curl", "-f", fmt.Sprintf("http://localhost:%d/status", port)},
			Interval: "30s",
			Timeout:  "10s",
			Retries:  3,
		},
		Networks: []string{"frameworks"},
		Volumes: []string{
			fmt.Sprintf("%s:%s:ro", keystorePath, keystorePath),
		},
	}

	composeYAML, err := GenerateDockerCompose(composeData)
	if err != nil {
		return err
	}

	tmpDir, err := os.MkdirTemp("", "livepeer-signer-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmpDir)

	composePath := filepath.Join(tmpDir, "docker-compose.yml")
	if err := os.WriteFile(composePath, []byte(composeYAML), 0644); err != nil {
		return err
	}

	remotePath := "/opt/frameworks/livepeer-signer/docker-compose.yml"
	if err := p.UploadFile(ctx, host, ssh.UploadOptions{
		LocalPath: composePath, RemotePath: remotePath, Mode: 0644,
	}); err != nil {
		return err
	}

	composeCmd := "cd /opt/frameworks/livepeer-signer && docker compose pull"
	if !config.DeferStart {
		composeCmd += " && docker compose up -d"
	}
	result, err := p.RunCommand(ctx, host, composeCmd)
	if err != nil || result.ExitCode != 0 {
		return fmt.Errorf("docker compose failed: %s\nStderr: %s", composeCmd, result.Stderr)
	}

	if config.DeferStart {
		fmt.Println("⏸ livepeer-signer deployed but NOT started")
	} else {
		fmt.Println("✓ livepeer-signer provisioned in Docker mode")
	}
	return nil
}

// buildFlags maps manifest config to go-livepeer remote signer CLI flags.
func (p *LivepeerSignerProvisioner) buildFlags(config ServiceConfig, keystorePath string) map[string]string {
	flags := map[string]string{
		"remoteSigner": "true",
	}

	p.setFlag(flags, config, "network", "network", "arbitrum-one-mainnet")
	p.setFlag(flags, config, "eth_url", "ethUrl", "")

	// The signer's HTTP address — where gateways connect to it
	port := config.Port
	if port == 0 {
		port = 18016
	}
	flags["httpAddr"] = fmt.Sprintf(":%d", port)

	// Keystore
	flags["ethKeystorePath"] = keystorePath
	p.setFlag(flags, config, "eth_password", "ethPassword", "/etc/frameworks/.livepeer_keystore_password")
	p.setFlag(flags, config, "eth_acct_addr", "ethAcctAddr", "")

	// Orchestrator discovery (signer can discover and cache orchestrators for gateways)
	p.setFlag(flags, config, "remote_discovery", "remoteDiscovery", "true")
	p.setFlag(flags, config, "orch_webhook_url", "orchWebhookUrl", "")
	p.setFlag(flags, config, "orch_addr", "orchAddr", "")

	for k, v := range flags {
		if v == "" {
			delete(flags, k)
		}
	}

	return flags
}

func (p *LivepeerSignerProvisioner) setFlag(flags map[string]string, config ServiceConfig, configKey, flagName, defaultVal string) {
	val := config.EnvVars[configKey]
	if val == "" {
		val = defaultVal
	}
	flags[flagName] = val
}

// ensureKeystore creates the keystore directory and password file on the remote host.
// If a keystore already exists, it's reused. Password is generated once and persisted.
// Follows the ChatwootProvisioner.ensureSecretKey pattern.
func (p *LivepeerSignerProvisioner) ensureKeystore(ctx context.Context, host inventory.Host, config ServiceConfig) (string, error) {
	keystorePath := config.EnvVars["keystore_path"]
	if keystorePath == "" {
		keystorePath = "/etc/frameworks/livepeer-signer-keystore"
	}
	passwordFile := "/etc/frameworks/.livepeer_keystore_password"

	// Ensure keystore directory exists
	mkdirCmd := fmt.Sprintf("mkdir -p %s && chmod 700 %s", keystorePath, keystorePath)
	if result, err := p.RunCommand(ctx, host, mkdirCmd); err != nil || result.ExitCode != 0 {
		return "", fmt.Errorf("failed to create keystore dir: %w", err)
	}

	// Check if password file exists; generate if not
	result, err := p.RunCommand(ctx, host, fmt.Sprintf("cat %s 2>/dev/null", passwordFile))
	if err != nil || result.ExitCode != 0 || strings.TrimSpace(result.Stdout) == "" {
		result, err = p.RunCommand(ctx, host, fmt.Sprintf(
			"openssl rand -hex 32 | tee %s && chmod 600 %s", passwordFile, passwordFile))
		if err != nil || result.ExitCode != 0 {
			return "", fmt.Errorf("failed to generate keystore password: %w", err)
		}
		fmt.Println("    Generated new keystore password")
	}

	// Check if keystore has any key files
	result, err = p.RunCommand(ctx, host, fmt.Sprintf("ls %s/UTC--* 2>/dev/null | head -1", keystorePath))
	if err == nil && result.ExitCode == 0 && strings.TrimSpace(result.Stdout) != "" {
		fmt.Println("    Existing keystore found, reusing")
		return keystorePath, nil
	}

	// No keystore exists — generate a new ETH account using geth-style keystore.
	// We use the livepeer binary itself if available, or openssl + python as fallback.
	// The go-livepeer binary creates a keystore on first run if none exists,
	// so we can also just let it create one on startup.
	fmt.Println("    No existing keystore found — will be created on first signer startup")

	return keystorePath, nil
}

func (p *LivepeerSignerProvisioner) resolveImageFromManifest(version string) string {
	channel, resolved := gitops.ResolveVersion(version)
	fetcher, err := gitops.NewFetcher(gitops.FetchOptions{})
	if err != nil {
		return ""
	}
	manifest, err := fetcher.Fetch(channel, resolved)
	if err != nil {
		return ""
	}
	dep := manifest.GetExternalDependency("go-livepeer")
	if dep == nil {
		return ""
	}
	if dep.Digest != "" {
		return dep.Image + "@" + dep.Digest
	}
	return dep.Image
}

func (p *LivepeerSignerProvisioner) installBinary(ctx context.Context, host inventory.Host, config ServiceConfig) error {
	binaryURL := config.BinaryURL
	if binaryURL == "" {
		channel, version := gitops.ResolveVersion(config.Version)
		fetcher, err := gitops.NewFetcher(gitops.FetchOptions{})
		if err != nil {
			return err
		}
		manifest, err := fetcher.Fetch(channel, version)
		if err != nil {
			return err
		}
		dep := manifest.GetExternalDependency("go-livepeer")
		if dep == nil {
			return fmt.Errorf("go-livepeer not found in manifest external_dependencies")
		}
		remoteOS, remoteArch, err := p.DetectRemoteArch(ctx, host)
		if err != nil {
			return err
		}
		archKey := remoteOS + "-" + remoteArch
		binaryURL = dep.GetBinaryURL(archKey)
		if binaryURL == "" {
			return fmt.Errorf("no go-livepeer binary for %s", archKey)
		}
	}

	script := fmt.Sprintf(`#!/bin/bash
set -e
mkdir -p /opt/frameworks/livepeer-signer
wget -q -O /tmp/go-livepeer-signer.tar.gz "%s"
tar -xzf /tmp/go-livepeer-signer.tar.gz -C /opt/frameworks/livepeer-signer/
chmod +x /opt/frameworks/livepeer-signer/livepeer
rm -f /tmp/go-livepeer-signer.tar.gz
`, binaryURL)

	result, err := p.ExecuteScript(ctx, host, script)
	if err != nil || result.ExitCode != 0 {
		return fmt.Errorf("binary install failed: %v", result.Stderr)
	}
	return nil
}

func (p *LivepeerSignerProvisioner) writeFlagsEnv(ctx context.Context, host inventory.Host, envFilePath string, flags map[string]string) error {
	var lines []string
	var flagParts []string
	for k, v := range flags {
		lines = append(lines, fmt.Sprintf("LP_%s=%s", strings.ToUpper(k), v))
		flagParts = append(flagParts, fmt.Sprintf("-%s=%s", k, v))
	}
	lines = append(lines, fmt.Sprintf("LIVEPEER_CLI_FLAGS=%s", strings.Join(flagParts, " ")))

	content := strings.Join(lines, "\n") + "\n"
	writeCmd := fmt.Sprintf("mkdir -p /etc/frameworks && cat > %s << 'ENVEOF'\n%sENVEOF", envFilePath, content)
	result, err := p.RunCommand(ctx, host, writeCmd)
	if err != nil {
		return fmt.Errorf("failed to write env file: %w", err)
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("failed to write env file: %s", result.Stderr)
	}
	return nil
}

func (p *LivepeerSignerProvisioner) Validate(ctx context.Context, host inventory.Host, config ServiceConfig) error {
	port := config.Port
	if port == 0 {
		port = 18016
	}
	checker := &health.HTTPChecker{
		Path:    "/status",
		Timeout: 10,
	}
	result := checker.Check(host.ExternalIP, port)
	if !result.OK {
		return fmt.Errorf("livepeer-signer health check failed: %s", result.Error)
	}
	return nil
}

func (p *LivepeerSignerProvisioner) Initialize(ctx context.Context, host inventory.Host, config ServiceConfig) error {
	return nil
}

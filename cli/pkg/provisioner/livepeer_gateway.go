package provisioner

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"frameworks/cli/pkg/detect"
	"frameworks/cli/pkg/gitops"
	"frameworks/cli/pkg/health"
	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/ssh"
)

// LivepeerGatewayProvisioner provisions the Livepeer gateway (go-livepeer in gateway mode).
// Supports both Docker and native binary deployment. Reads config from the manifest's
// config: block (merged into EnvVars by buildServiceEnvVars).
type LivepeerGatewayProvisioner struct {
	*BaseProvisioner
}

func NewLivepeerGatewayProvisioner(pool *ssh.Pool) *LivepeerGatewayProvisioner {
	return &LivepeerGatewayProvisioner{
		BaseProvisioner: NewBaseProvisioner("livepeer-gateway", pool),
	}
}

func (p *LivepeerGatewayProvisioner) Detect(ctx context.Context, host inventory.Host) (*detect.ServiceState, error) {
	return p.CheckExists(ctx, host, "livepeer-gateway")
}

func (p *LivepeerGatewayProvisioner) Provision(ctx context.Context, host inventory.Host, config ServiceConfig) error {
	switch config.Mode {
	case "docker":
		return p.provisionDocker(ctx, host, config)
	case "native":
		return p.provisionNative(ctx, host, config)
	default:
		return fmt.Errorf("unsupported mode %q for livepeer-gateway (docker or native)", config.Mode)
	}
}

func (p *LivepeerGatewayProvisioner) provisionDocker(ctx context.Context, host inventory.Host, config ServiceConfig) error {
	fmt.Println("Provisioning livepeer-gateway in Docker mode...")

	image := config.Image
	if image == "" {
		image = p.resolveImageFromManifest(config.Version)
	}
	if image == "" {
		image = "ghcr.io/livepeer-frameworks/go-livepeer:latest"
	}

	port := config.Port
	if port == 0 {
		port = 8935
	}

	flags := p.buildFlags(config)

	// Write env file with mapped flags
	envFile := fmt.Sprintf("/etc/frameworks/%s.env", p.name)
	if err := p.writeFlagsEnv(ctx, host, envFile, flags); err != nil {
		return fmt.Errorf("failed to write env file: %w", err)
	}

	// CLI port for management API
	cliPort := p.cfgStr(config, "cli_addr", ":7935")
	cliPortNum := 7935
	if parts := strings.SplitN(cliPort, ":", 2); len(parts) == 2 {
		if n, err := strconv.Atoi(parts[1]); err == nil {
			cliPortNum = n
		}
	}

	composeData := DockerComposeData{
		ServiceName: "livepeer-gateway",
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
		Ports: []string{
			fmt.Sprintf("%d:%d", cliPortNum, cliPortNum),
		},
	}

	composeYAML, err := GenerateDockerCompose(composeData)
	if err != nil {
		return fmt.Errorf("failed to generate docker-compose: %w", err)
	}

	tmpDir, err := os.MkdirTemp("", "livepeer-gateway-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmpDir)

	composePath := filepath.Join(tmpDir, "docker-compose.yml")
	if err := os.WriteFile(composePath, []byte(composeYAML), 0644); err != nil {
		return err
	}

	remotePath := "/opt/frameworks/livepeer-gateway/docker-compose.yml"
	if err := p.UploadFile(ctx, host, ssh.UploadOptions{
		LocalPath: composePath, RemotePath: remotePath, Mode: 0644,
	}); err != nil {
		return err
	}

	commands := []string{
		"cd /opt/frameworks/livepeer-gateway",
		"docker compose pull",
	}
	if !config.DeferStart {
		commands = append(commands, "docker compose up -d")
	}

	for _, cmd := range commands {
		result, err := p.RunCommand(ctx, host, cmd)
		if err != nil || result.ExitCode != 0 {
			return fmt.Errorf("docker compose failed: %s\nStderr: %s", cmd, result.Stderr)
		}
	}

	if config.DeferStart {
		fmt.Println("⏸ livepeer-gateway deployed but NOT started (missing required config)")
	} else {
		fmt.Println("✓ livepeer-gateway provisioned in Docker mode")
	}
	return nil
}

func (p *LivepeerGatewayProvisioner) provisionNative(ctx context.Context, host inventory.Host, config ServiceConfig) error {
	fmt.Println("Provisioning livepeer-gateway in native mode...")

	if err := p.installBinary(ctx, host, config); err != nil {
		return fmt.Errorf("failed to install binary: %w", err)
	}

	flags := p.buildFlags(config)

	envFile := "/etc/frameworks/livepeer-gateway.env"
	if err := p.writeFlagsEnv(ctx, host, envFile, flags); err != nil {
		return fmt.Errorf("failed to write env file: %w", err)
	}

	// Build the command line from flags
	var args []string
	for k, v := range flags {
		args = append(args, fmt.Sprintf("-%s %s", k, v))
	}

	unitData := SystemdUnitData{
		ServiceName: "livepeer-gateway",
		Description: "Livepeer Gateway (Transcoding)",
		WorkingDir:  "/opt/frameworks/livepeer-gateway",
		ExecStart:   "/opt/frameworks/livepeer-gateway/livepeer " + strings.Join(args, " "),
		User:        "frameworks",
		EnvFile:     envFile,
		Restart:     "always",
	}

	unitContent, err := GenerateSystemdUnit(unitData)
	if err != nil {
		return err
	}

	tmpUnit := filepath.Join(os.TempDir(), "livepeer-gateway.service")
	if err := os.WriteFile(tmpUnit, []byte(unitContent), 0644); err != nil {
		return err
	}

	unitPath := "/etc/systemd/system/frameworks-livepeer-gateway.service"
	if err := p.UploadFile(ctx, host, ssh.UploadOptions{
		LocalPath: tmpUnit, RemotePath: unitPath, Mode: 0644,
	}); err != nil {
		return err
	}

	reloadCmd := "systemctl daemon-reload"
	if result, err := p.RunCommand(ctx, host, reloadCmd); err != nil || result.ExitCode != 0 {
		return fmt.Errorf("failed to reload systemd: %w", err)
	}

	if config.DeferStart {
		fmt.Println("⏸ livepeer-gateway deployed but NOT started")
	} else {
		startCmd := "systemctl enable frameworks-livepeer-gateway && systemctl restart frameworks-livepeer-gateway"
		if result, err := p.RunCommand(ctx, host, startCmd); err != nil || result.ExitCode != 0 {
			return fmt.Errorf("failed to start: %w\nStderr: %s", err, result.Stderr)
		}
		fmt.Println("✓ livepeer-gateway provisioned in native mode")
	}
	return nil
}

// buildFlags maps manifest config keys to go-livepeer CLI flags.
func (p *LivepeerGatewayProvisioner) buildFlags(config ServiceConfig) map[string]string {
	flags := map[string]string{
		"gateway":    "true",
		"httpIngest": "true",
	}

	// Network & addresses
	p.setFlag(flags, config, "network", "network", "arbitrum-one-mainnet")
	p.setFlag(flags, config, "http_addr", "httpAddr", ":8935")
	p.setFlag(flags, config, "cli_addr", "cliAddr", ":7935")
	p.setFlag(flags, config, "rtmp_addr", "rtmpAddr", "")
	p.setFlag(flags, config, "eth_url", "ethUrl", "")
	p.setFlag(flags, config, "gateway_host", "gatewayHost", "")

	// Remote signer
	p.setFlag(flags, config, "remote_signer_url", "remoteSignerUrl", "")

	// Auth
	p.setFlag(flags, config, "auth_webhook_url", "authWebhookUrl", "")

	// Capacity & pricing — these are critical operational params
	p.setFlag(flags, config, "max_sessions", "maxSessions", "50")
	p.setFlag(flags, config, "max_price_per_unit", "maxPricePerUnit", "1200")
	p.setFlag(flags, config, "pixels_per_unit", "pixelsPerUnit", "1")
	p.setFlag(flags, config, "max_ticket_ev", "maxTicketEV", "3000000000000")
	p.setFlag(flags, config, "deposit_multiplier", "depositMultiplier", "1")

	// Orchestrator selection
	p.setFlag(flags, config, "orch_addr", "orchAddr", "")
	p.setFlag(flags, config, "orch_webhook_url", "orchWebhookUrl", "")
	p.setFlag(flags, config, "region", "region", "")
	p.setFlag(flags, config, "min_perf_score", "minPerfScore", "")

	// Data directory
	p.setFlag(flags, config, "data_dir", "dataDir", "/var/lib/frameworks/livepeer-gateway")

	// Remove empty-value flags (go-livepeer treats empty string differently than unset)
	for k, v := range flags {
		if v == "" {
			delete(flags, k)
		}
	}

	return flags
}

// setFlag reads a config key from EnvVars, falling back to defaultVal, and sets the go-livepeer flag.
func (p *LivepeerGatewayProvisioner) setFlag(flags map[string]string, config ServiceConfig, configKey, flagName, defaultVal string) {
	val := config.EnvVars[configKey]
	if val == "" {
		val = defaultVal
	}
	flags[flagName] = val
}

// cfgStr reads a config key with a default.
func (p *LivepeerGatewayProvisioner) cfgStr(config ServiceConfig, key, defaultVal string) string {
	if v := config.EnvVars[key]; v != "" {
		return v
	}
	return defaultVal
}

func (p *LivepeerGatewayProvisioner) resolveImageFromManifest(version string) string {
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

func (p *LivepeerGatewayProvisioner) installBinary(ctx context.Context, host inventory.Host, config ServiceConfig) error {
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
mkdir -p /opt/frameworks/livepeer-gateway
wget -q -O /tmp/go-livepeer.tar.gz "%s"
tar -xzf /tmp/go-livepeer.tar.gz -C /opt/frameworks/livepeer-gateway/
chmod +x /opt/frameworks/livepeer-gateway/livepeer
rm -f /tmp/go-livepeer.tar.gz
`, binaryURL)

	result, err := p.ExecuteScript(ctx, host, script)
	if err != nil || result.ExitCode != 0 {
		return fmt.Errorf("binary install failed: %v", result.Stderr)
	}
	return nil
}

// writeFlagsEnv writes go-livepeer flags as LP_<FLAG>=<value> environment variables.
// go-livepeer reads flags from env vars prefixed with LP_ (undocumented but works via pflag).
// We also write a LIVEPEER_CLI_FLAGS var with the full flag string for systemd ExecStart.
func (p *LivepeerGatewayProvisioner) writeFlagsEnv(ctx context.Context, host inventory.Host, envFilePath string, flags map[string]string) error {
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

func (p *LivepeerGatewayProvisioner) Validate(ctx context.Context, host inventory.Host, config ServiceConfig) error {
	port := config.Port
	if port == 0 {
		port = 8935
	}
	checker := &health.HTTPChecker{
		Path:    "/status",
		Timeout: 10,
	}
	result := checker.Check(host.ExternalIP, port)
	if !result.OK {
		return fmt.Errorf("livepeer-gateway health check failed: %s", result.Error)
	}
	return nil
}

func (p *LivepeerGatewayProvisioner) Initialize(ctx context.Context, host inventory.Host, config ServiceConfig) error {
	return nil
}

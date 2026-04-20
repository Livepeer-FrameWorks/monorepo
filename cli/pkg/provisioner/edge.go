package provisioner

import (
	"context"
	"crypto/tls"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"crypto/rand"
	"encoding/hex"

	"frameworks/cli/internal/preflight"
	"frameworks/cli/internal/templates"
	"frameworks/cli/pkg/detect"
	"frameworks/cli/pkg/gitops"
	"frameworks/cli/pkg/health"
	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/ssh"
	"frameworks/pkg/mist"
)

// DarwinDomain selects the launchd domain for macOS service management.
type DarwinDomain string

const (
	DomainSystem DarwinDomain = "system" // /Library/LaunchDaemons — root, survives logout
	DomainUser   DarwinDomain = "user"   // ~/Library/LaunchAgents — no admin, dies on logout
)

// EdgeProvisioner provisions the 3-service edge stack (Caddy, MistServer, Helmsman)
// in Docker (docker-compose), native Linux (systemd), or native macOS (launchd) mode.
type EdgeProvisioner struct {
	*BaseProvisioner
}

// NewEdgeProvisioner creates a new edge provisioner.
func NewEdgeProvisioner(pool *ssh.Pool) *EdgeProvisioner {
	return &EdgeProvisioner{
		BaseProvisioner: NewBaseProvisioner("edge", pool),
	}
}

// EdgeProvisionConfig carries all parameters for the edge 7-step pipeline.
type EdgeProvisionConfig struct {
	Mode string // "docker" | "native"

	NodeName        string
	NodeDomain      string
	PoolDomain      string
	ClusterID       string
	Region          string
	Email           string
	EnrollmentToken string
	FoghornGRPCAddr string
	NodeID          string // From PreRegisterEdge
	CertPEM         string // Pre-staged wildcard cert
	KeyPEM          string
	CABundlePEM     string
	TelemetryURL    string
	TelemetryToken  string

	// Step toggles
	SkipPreflight bool
	ApplyTuning   bool
	FetchCert     bool

	Timeout      time.Duration
	Force        bool
	Version      string       // Gitops version for binary resolution
	DarwinDomain DarwinDomain // "system" (root) or "user" (no admin)
}

// generateEdgePassword returns a random 32-char hex string for edge-local auth.
func generateEdgePassword() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate edge password: %w", err)
	}
	return hex.EncodeToString(b), nil
}

func (c *EdgeProvisionConfig) primaryDomain() string {
	if c.PoolDomain != "" {
		return c.PoolDomain
	}
	return c.NodeDomain
}

func (c *EdgeProvisionConfig) resolvedMode() string {
	if c.Mode == "" {
		return "docker"
	}
	return c.Mode
}

func (c *EdgeProvisionConfig) helmsmanCAPath(remoteOS string) string {
	if strings.TrimSpace(c.CABundlePEM) == "" {
		return ""
	}
	if c.resolvedMode() == "docker" {
		return "/etc/frameworks/pki/ca.crt"
	}
	if remoteOS == "darwin" {
		return filepath.Join(darwinPaths(c.DarwinDomain).confDir, "pki", "ca.crt")
	}
	return "/etc/frameworks/pki/ca.crt"
}

// parseUnameOutput parses "uname -sm" output (e.g. "Linux x86_64") into Go-style
// os and arch values (e.g. "linux", "amd64").
// detectRemoteArch delegates to BaseProvisioner.DetectRemoteArch.
func (e *EdgeProvisioner) detectRemoteArch(ctx context.Context, host inventory.Host) (osName, goArch string, err error) {
	return e.DetectRemoteArch(ctx, host)
}

// sudoPrefix returns "sudo " for non-root SSH users, empty string for root.
func (e *EdgeProvisioner) sudoPrefix(host inventory.Host) string {
	if host.User == "root" || host.User == "" {
		return ""
	}
	return "sudo "
}

// RunSudoCommand executes a command on a host, prepending sudo when the SSH user is not root.
func (e *EdgeProvisioner) RunSudoCommand(ctx context.Context, host inventory.Host, command string) (*ssh.CommandResult, error) {
	return e.RunCommand(ctx, host, e.sudoPrefix(host)+command)
}

// ExecuteSudoScript uploads and runs a shell script with sudo when the SSH user is not root.
func (e *EdgeProvisioner) ExecuteSudoScript(ctx context.Context, host inventory.Host, script string) (*ssh.CommandResult, error) {
	if host.User == "root" || host.User == "" {
		return e.ExecuteScript(ctx, host, script)
	}
	// Upload script to temp file, then execute with sudo bash
	tmpFile, err := os.CreateTemp("", "edge-sudo-*.sh")
	if err != nil {
		return nil, err
	}
	defer os.Remove(tmpFile.Name())
	if _, err = tmpFile.WriteString(script); err != nil {
		tmpFile.Close()
		return nil, err
	}
	tmpFile.Close()

	remotePath := fmt.Sprintf("/tmp/frameworks-script-%d.sh", time.Now().UnixNano())
	if err = e.UploadFile(ctx, host, ssh.UploadOptions{
		LocalPath: tmpFile.Name(), RemotePath: remotePath, Mode: 0700,
	}); err != nil {
		return nil, fmt.Errorf("failed to upload script: %w", err)
	}

	result, err := e.RunCommand(ctx, host, "sudo bash "+remotePath)
	_, _ = e.RunCommand(ctx, host, "rm -f "+remotePath)
	return result, err
}

// uploadFileWithSudo uploads a file to a root-owned remote path by first uploading
// to /tmp, then using sudo to move it into place with correct permissions.
func (e *EdgeProvisioner) uploadFileWithSudo(ctx context.Context, host inventory.Host, opts ssh.UploadOptions) error {
	if host.User == "root" || host.User == "" {
		return e.UploadFile(ctx, host, opts)
	}

	tempRemote := fmt.Sprintf("/tmp/frameworks-upload-%d-%s", time.Now().UnixNano(), filepath.Base(opts.RemotePath))
	if err := e.UploadFile(ctx, host, ssh.UploadOptions{
		LocalPath: opts.LocalPath, RemotePath: tempRemote, Mode: 0600,
	}); err != nil {
		return fmt.Errorf("failed to upload to temp path: %w", err)
	}

	dir := filepath.Dir(opts.RemotePath)
	if _, err := e.RunCommand(ctx, host, fmt.Sprintf("sudo mkdir -p %s", dir)); err != nil {
		return fmt.Errorf("failed to create directory %s: %w", dir, err)
	}
	if _, err := e.RunCommand(ctx, host, fmt.Sprintf("sudo mv %s %s", tempRemote, opts.RemotePath)); err != nil {
		return fmt.Errorf("failed to move file to %s: %w", opts.RemotePath, err)
	}
	if _, err := e.RunCommand(ctx, host, fmt.Sprintf("sudo chmod %04o %s", opts.Mode, opts.RemotePath)); err != nil {
		return fmt.Errorf("failed to set permissions on %s: %w", opts.RemotePath, err)
	}
	if opts.Owner != "" {
		owner := opts.Owner
		if opts.Group != "" {
			owner = opts.Owner + ":" + opts.Group
		}
		if _, err := e.RunCommand(ctx, host, fmt.Sprintf("sudo chown %s %s", owner, opts.RemotePath)); err != nil {
			return fmt.Errorf("failed to chown %s: %w", opts.RemotePath, err)
		}
	}
	return nil
}

// Provision runs the full 7-step edge pipeline on a remote host.
func (e *EdgeProvisioner) Provision(ctx context.Context, host inventory.Host, config EdgeProvisionConfig) error {
	mode := config.resolvedMode()

	// Detect remote OS early so we can use OS-appropriate paths throughout
	remoteOS, _, err := e.detectRemoteArch(ctx, host)
	if err != nil {
		return fmt.Errorf("failed to detect remote OS: %w", err)
	}

	// [1/7] Preflight
	if !config.SkipPreflight {
		fmt.Printf("[1/7] Running preflight checks on %s...\n", host.ExternalIP)
		if err := e.runPreflight(ctx, host, mode); err != nil {
			return fmt.Errorf("preflight failed: %w", err)
		}
	} else {
		fmt.Println("[1/7] Skipping preflight checks")
	}

	// [2/7] Tuning (Linux-only; macOS has different tuning mechanisms)
	if config.ApplyTuning && remoteOS == "linux" {
		fmt.Println("[2/7] Applying sysctl/limits tuning...")
		if err := e.applyTuning(ctx, host); err != nil {
			return fmt.Errorf("tuning failed: %w", err)
		}
	} else if config.ApplyTuning && remoteOS == "darwin" {
		fmt.Println("[2/7] Skipping sysctl tuning (macOS)")
	} else {
		fmt.Println("[2/7] Skipping sysctl tuning")
	}

	// [3/7] Registration (caller handles QM registration externally — same as before)
	fmt.Println("[3/7] Registration handled by caller")

	// [4/7] TLS certs are now delivered via ConfigSeed after enrollment
	fmt.Println("[4/7] TLS certificates will be delivered after enrollment via ConfigSeed")

	// [5-6/7] Install + start (mode-dependent)
	switch mode {
	case "docker":
		fmt.Println("[5-6/7] Installing edge stack (Docker)...")
		if err := e.installDocker(ctx, host, config); err != nil {
			return fmt.Errorf("docker install failed: %w", err)
		}
	case "native":
		modeDesc := "native/systemd"
		if remoteOS == "darwin" {
			modeDesc = "native/launchd"
		}
		fmt.Printf("[5-6/7] Installing edge stack (%s)...\n", modeDesc)
		if err := e.installNative(ctx, host, config); err != nil {
			return fmt.Errorf("native install failed: %w", err)
		}
	default:
		return fmt.Errorf("unsupported mode: %s (must be docker or native)", mode)
	}

	// [7/7] Verify HTTPS
	domain := config.primaryDomain()
	if domain != "" {
		fmt.Printf("[7/7] Verifying HTTPS readiness at %s...\n", domain)
		timeout := config.Timeout
		if timeout == 0 {
			timeout = 3 * time.Minute
		}
		if err := e.verifyHTTPS(domain, timeout); err != nil {
			return fmt.Errorf("HTTPS verification failed: %w", err)
		}
	} else {
		fmt.Println("[7/7] No domain set, skipping HTTPS verification")
	}

	fmt.Printf("Edge node provisioned successfully on %s (%s mode)\n", host.ExternalIP, mode)
	return nil
}

// runPreflight checks host readiness based on mode.
func (e *EdgeProvisioner) runPreflight(ctx context.Context, host inventory.Host, mode string) error {
	remoteOS, _, err := e.detectRemoteArch(ctx, host)
	if err != nil {
		return fmt.Errorf("failed to detect remote OS: %w", err)
	}

	if mode == "docker" {
		result, dockerErr := e.RunCommand(ctx, host, "docker --version")
		if dockerErr != nil || result.ExitCode != 0 {
			return fmt.Errorf("docker not installed")
		}
		fmt.Printf("  docker: %s\n", strings.TrimSpace(result.Stdout))

		result, dockerErr = e.RunCommand(ctx, host, "docker compose version")
		if dockerErr != nil || result.ExitCode != 0 {
			return fmt.Errorf("docker compose not available")
		}
		fmt.Printf("  compose: %s\n", strings.TrimSpace(result.Stdout))
	} else if remoteOS == "darwin" {
		result, launchErr := e.RunCommand(ctx, host, "launchctl version")
		if launchErr != nil || result.ExitCode != 0 {
			return fmt.Errorf("launchctl not available")
		}
		fmt.Printf("  launchctl: %s\n", strings.TrimSpace(result.Stdout))
	} else {
		result, sysErr := e.RunCommand(ctx, host, "systemctl --version")
		if sysErr != nil || result.ExitCode != 0 {
			return fmt.Errorf("systemd not available")
		}
		fmt.Printf("  systemd: %s\n", strings.TrimSpace(strings.Split(result.Stdout, "\n")[0]))
	}

	// Check ports 80/443 — use lsof on macOS, ss on Linux
	var portCheckCmd string
	if remoteOS == "darwin" {
		portCheckCmd = "lsof -iTCP:80 -iTCP:443 -sTCP:LISTEN -P -n 2>/dev/null"
	} else {
		portCheckCmd = "ss -tlnp | grep -E ':80 |:443 '"
	}
	result, err := e.RunCommand(ctx, host, portCheckCmd)
	if err == nil && result.ExitCode == 0 && strings.TrimSpace(result.Stdout) != "" {
		return fmt.Errorf("ports 80/443 already in use:\n%s", result.Stdout)
	}

	// Disk space — check OS-appropriate paths
	const minDiskFreeBytes = 20 * 1024 * 1024 * 1024
	const minDiskFreePercent = 10.0
	diskPaths := []string{"/", "/var/lib"}
	if remoteOS == "darwin" {
		diskPaths = []string{"/", "/usr/local"}
	}
	for _, path := range diskPaths {
		result, err = e.RunCommand(ctx, host, fmt.Sprintf("df -Pk %s", path))
		if err != nil || result.ExitCode != 0 {
			return fmt.Errorf("disk check failed for %s", path)
		}
		check := preflight.DiskSpaceFromDF(result.Stdout, path, minDiskFreeBytes, minDiskFreePercent)
		if !check.OK {
			return fmt.Errorf("insufficient disk space on %s: %s", path, check.Detail)
		}
		fmt.Printf("  disk %s: %s\n", path, check.Detail)
	}

	// /dev/shm check (Linux only — macOS uses POSIX shared memory via shm_open)
	if remoteOS != "darwin" {
		result, err = e.RunCommand(ctx, host, "df -h /dev/shm")
		if err == nil && result.ExitCode == 0 {
			fmt.Println("  /dev/shm: available")
		} else {
			fmt.Println("  /dev/shm: not mounted (MistServer may need --shm-size)")
		}
	}

	return nil
}

// applyTuning uploads sysctl and limits config.
func (e *EdgeProvisioner) applyTuning(ctx context.Context, host inventory.Host) error {
	tuningScript := `#!/bin/bash
set -e
cat > /etc/sysctl.d/frameworks-edge.conf << 'SYSCTL'
net.core.rmem_max = 16777216
net.core.wmem_max = 16777216
net.core.somaxconn = 8192
net.ipv4.ip_local_port_range = 16384 65535
SYSCTL

cat > /etc/security/limits.d/frameworks-edge.conf << 'LIMITS'
* soft nofile 1048576
* hard nofile 1048576
LIMITS

sysctl --system > /dev/null 2>&1 || true
echo "tuning applied"
`
	result, err := e.ExecuteSudoScript(ctx, host, tuningScript)
	if err != nil || result.ExitCode != 0 {
		stderr := ""
		if result != nil {
			stderr = result.Stderr
		}
		return fmt.Errorf("tuning script failed: %w (%s)", err, stderr)
	}
	fmt.Println("  sysctl + limits applied")
	return nil
}

func (e *EdgeProvisioner) stageCertificatesAt(ctx context.Context, host inventory.Host, certPEM, keyPEM, certDir string) error {
	_, err := e.RunSudoCommand(ctx, host, "mkdir -p "+certDir)
	if err != nil {
		return fmt.Errorf("failed to create cert directory: %w", err)
	}

	tmpDir, err := os.MkdirTemp("", "edge-certs-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmpDir)

	certPath := filepath.Join(tmpDir, "cert.pem")
	keyPath := filepath.Join(tmpDir, "key.pem")
	if err := os.WriteFile(certPath, []byte(certPEM), 0644); err != nil {
		return err
	}
	if err := os.WriteFile(keyPath, []byte(keyPEM), 0600); err != nil {
		return err
	}

	if err := e.uploadFileWithSudo(ctx, host, ssh.UploadOptions{
		LocalPath: certPath, RemotePath: certDir + "/cert.pem", Mode: 0644,
	}); err != nil {
		return err
	}
	if err := e.uploadFileWithSudo(ctx, host, ssh.UploadOptions{
		LocalPath: keyPath, RemotePath: certDir + "/key.pem", Mode: 0600,
	}); err != nil {
		return err
	}

	fmt.Printf("  certificates staged at %s/\n", certDir)
	return nil
}

func (e *EdgeProvisioner) stageCABundleAt(ctx context.Context, host inventory.Host, caBundlePEM, caPath string) error {
	if strings.TrimSpace(caBundlePEM) == "" || strings.TrimSpace(caPath) == "" {
		return nil
	}
	if _, err := e.RunSudoCommand(ctx, host, "mkdir -p "+filepath.Dir(caPath)); err != nil {
		return fmt.Errorf("failed to create CA bundle directory: %w", err)
	}

	tmpFile, err := os.CreateTemp("", "edge-ca-*.crt")
	if err != nil {
		return err
	}
	defer os.Remove(tmpFile.Name())

	if _, err = tmpFile.WriteString(caBundlePEM); err != nil {
		tmpFile.Close()
		return err
	}
	if err = tmpFile.Close(); err != nil {
		return err
	}

	if err = e.uploadFileWithSudo(ctx, host, ssh.UploadOptions{
		LocalPath:  tmpFile.Name(),
		RemotePath: caPath,
		Mode:       0o644,
	}); err != nil {
		return err
	}

	fmt.Printf("  gRPC CA bundle staged at %s\n", caPath)
	return nil
}

// installDocker generates edge templates, uploads them, and runs docker compose up.
func (e *EdgeProvisioner) installDocker(ctx context.Context, host inventory.Host, config EdgeProvisionConfig) error {
	vars := e.buildEdgeVars(config, "linux") // Docker containers are always Linux
	vars.Mode = "docker"
	mistPass, err := generateEdgePassword()
	if err != nil {
		return err
	}
	vars.MistAPIPassword = mistPass
	vars.SetModeDefaults()

	// Write templates to local temp dir
	tmpDir, err := os.MkdirTemp("", "edge-docker-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmpDir)

	if err = templates.WriteEdgeTemplates(tmpDir, vars, true); err != nil {
		return fmt.Errorf("failed to write templates: %w", err)
	}

	// Create remote directory
	remoteDir := "/opt/frameworks/edge"
	if _, err = e.RunSudoCommand(ctx, host, "mkdir -p "+remoteDir); err != nil {
		return fmt.Errorf("failed to create remote directory: %w", err)
	}
	if err = e.stageCABundleAt(ctx, host, config.CABundlePEM, remoteDir+"/pki/ca.crt"); err != nil {
		return fmt.Errorf("failed to stage gRPC CA bundle: %w", err)
	}

	// Upload each template file
	files := []string{"docker-compose.edge.yml", "Caddyfile", ".edge.env"}
	for _, f := range files {
		localPath := filepath.Join(tmpDir, f)
		remotePath := remoteDir + "/" + f
		if err = e.uploadFileWithSudo(ctx, host, ssh.UploadOptions{
			LocalPath: localPath, RemotePath: remotePath, Mode: 0600,
		}); err != nil {
			return fmt.Errorf("failed to upload %s: %w", f, err)
		}
		fmt.Printf("  uploaded %s\n", f)
	}

	// Upload certs directory if present
	if config.CertPEM != "" && config.KeyPEM != "" {
		certDir := remoteDir + "/certs"
		if _, err = e.RunSudoCommand(ctx, host, "mkdir -p "+certDir); err != nil {
			return fmt.Errorf("failed to create remote certs directory: %w", err)
		}
		// Certs already staged at /etc/frameworks/certs; symlink or copy for compose mount
		_, _ = e.RunSudoCommand(ctx, host, fmt.Sprintf("cp /etc/frameworks/certs/cert.pem %s/cert.pem && cp /etc/frameworks/certs/key.pem %s/key.pem", certDir, certDir))
	}

	// docker compose up
	result, err := e.RunSudoCommand(ctx, host,
		fmt.Sprintf("cd %s && docker compose -f docker-compose.edge.yml --env-file .edge.env up -d", remoteDir))
	if err != nil || result.ExitCode != 0 {
		stderr := ""
		if result != nil {
			stderr = result.Stderr
		}
		return fmt.Errorf("docker compose up failed: %w (%s)", err, stderr)
	}

	fmt.Println("  edge stack started (caddy, mistserver, helmsman)")
	if strings.TrimSpace(config.TelemetryURL) != "" {
		if err := e.installEdgeTelemetryDocker(ctx, host, config); err != nil {
			return fmt.Errorf("failed to install edge telemetry agent: %w", err)
		}
		fmt.Println("  edge telemetry agent started (vmagent)")
	}
	return nil
}

func (e *EdgeProvisioner) installEdgeTelemetryDocker(ctx context.Context, host inventory.Host, config EdgeProvisionConfig) error {
	if _, err := e.RunSudoCommand(ctx, host, "mkdir -p /etc/frameworks /etc/frameworks/telemetry"); err != nil {
		return err
	}

	scrapeConfig, err := buildEdgeTelemetryScrapeConfig("docker", config.NodeID)
	if err != nil {
		return err
	}
	err = e.writeRemoteFile(ctx, host, "/etc/frameworks/vmagent-edge.yml", scrapeConfig, 0o644)
	if err != nil {
		return err
	}
	image, err := resolveObservabilityImage(config.Version, "", "vmagent", defaultVMAgentImage)
	if err != nil {
		return err
	}
	networkName, err := e.edgeTelemetryDockerNetwork(ctx, host)
	if err != nil {
		return err
	}

	cmdParts := []string{
		"docker rm -f frameworks-edge-vmagent >/dev/null 2>&1 || true",
	}
	if strings.TrimSpace(config.TelemetryToken) != "" {
		err = e.writeRemoteFile(ctx, host, "/etc/frameworks/telemetry/token", config.TelemetryToken+"\n", 0o600)
		if err != nil {
			return err
		}
	}

	runArgs := []string{
		fmt.Sprintf("docker run -d --name frameworks-edge-vmagent --restart unless-stopped --network %s", networkName),
		"-v /etc/frameworks/vmagent-edge.yml:/etc/frameworks/vmagent-edge.yml:ro",
	}
	if strings.TrimSpace(config.TelemetryToken) != "" {
		runArgs = append(runArgs, "-v /etc/frameworks/telemetry:/etc/frameworks/telemetry:ro")
	}
	runArgs = append(runArgs,
		image,
		fmt.Sprintf("-promscrape.config=%s", "/etc/frameworks/vmagent-edge.yml"),
		"-httpListenAddr=:8430",
		fmt.Sprintf("-remoteWrite.url=%s", config.TelemetryURL),
	)
	if strings.TrimSpace(config.TelemetryToken) != "" {
		runArgs = append(runArgs, "-remoteWrite.bearerTokenFile=/etc/frameworks/telemetry/token")
	}
	cmdParts = append(cmdParts, strings.Join(runArgs, " "))

	result, err := e.RunSudoCommand(ctx, host, strings.Join(cmdParts, " && "))
	if err != nil || result.ExitCode != 0 {
		stderr := ""
		if result != nil {
			stderr = result.Stderr
		}
		return fmt.Errorf("edge vmagent startup failed: %w (%s)", err, stderr)
	}
	return nil
}

func buildEdgeTelemetryScrapeConfig(mode, nodeID string) (string, error) {
	mistTarget := "127.0.0.1:8080"
	helmsmanTarget := "127.0.0.1:18007"
	if mode == "docker" {
		mistTarget = "mistserver:8080"
		helmsmanTarget = "helmsman:18007"
	}

	return buildVMAgentScrapeConfig([]map[string]interface{}{
		{
			"job_name": "edge-mist",
			"targets":  []string{mistTarget},
			"path":     mist.MetricsPath,
			"labels": map[string]string{
				"frameworks_mode":    "edge",
				"frameworks_node":    nodeID,
				"frameworks_service": "mistserver",
			},
		},
		{
			"job_name": "edge-helmsman",
			"targets":  []string{helmsmanTarget},
			"path":     "/metrics",
			"labels": map[string]string{
				"frameworks_mode":    "edge",
				"frameworks_node":    nodeID,
				"frameworks_service": "helmsman",
			},
		},
	}, "30s")
}

func (e *EdgeProvisioner) edgeTelemetryDockerNetwork(ctx context.Context, host inventory.Host) (string, error) {
	result, err := e.RunSudoCommand(ctx, host, "docker inspect -f '{{range $name, $_ := .NetworkSettings.Networks}}{{println $name}}{{end}}' helmsman 2>/dev/null | head -n 1")
	if err != nil || result.ExitCode != 0 {
		stderr := ""
		if result != nil {
			stderr = result.Stderr
		}
		return "", fmt.Errorf("failed to detect edge docker network: %w (%s)", err, stderr)
	}
	networkName := strings.TrimSpace(result.Stdout)
	if networkName == "" {
		return "", fmt.Errorf("failed to detect edge docker network: helmsman has no attached networks")
	}
	return networkName, nil
}

func (e *EdgeProvisioner) installEdgeTelemetryLinux(ctx context.Context, host inventory.Host, config EdgeProvisionConfig, remoteArch string) error {
	if _, err := e.RunSudoCommand(ctx, host, "mkdir -p /etc/frameworks /etc/frameworks/telemetry /opt/frameworks/vmagent-edge /var/log/frameworks"); err != nil {
		return err
	}

	scrapeConfig, err := buildEdgeTelemetryScrapeConfig("native", config.NodeID)
	if err != nil {
		return err
	}
	err = e.writeRemoteFile(ctx, host, "/etc/frameworks/vmagent-edge.yml", scrapeConfig, 0o644)
	if err != nil {
		return err
	}
	if _, chownErr := e.RunSudoCommand(ctx, host, "chown frameworks:frameworks /etc/frameworks/vmagent-edge.yml"); chownErr != nil {
		return chownErr
	}

	tokenArg := ""
	if strings.TrimSpace(config.TelemetryToken) != "" {
		err = e.writeRemoteFile(ctx, host, "/etc/frameworks/telemetry/token", config.TelemetryToken+"\n", 0o600)
		if err != nil {
			return err
		}
		if _, chownErr := e.RunSudoCommand(ctx, host, "chown frameworks:frameworks /etc/frameworks/telemetry/token"); chownErr != nil {
			return chownErr
		}
		tokenArg = " -remoteWrite.bearerTokenFile=/etc/frameworks/telemetry/token"
	}

	binaryURL, err := resolveVMAgentBinaryURL(config.Version, "linux", remoteArch)
	if err != nil {
		return err
	}
	installScript := fmt.Sprintf(`#!/bin/bash
set -e
tmpdir=$(mktemp -d)
trap 'rm -rf "$tmpdir" /tmp/vmagent-edge.tar.gz' EXIT
wget -q -O /tmp/vmagent-edge.tar.gz "%s"
tar -xzf /tmp/vmagent-edge.tar.gz -C "$tmpdir"
bin=$(find "$tmpdir" -type f \( -name vmagent-prod -o -name vmagent \) | head -n 1)
test -n "$bin"
install -m 0755 "$bin" /opt/frameworks/vmagent-edge/vmagent
chown -R frameworks:frameworks /opt/frameworks/vmagent-edge /var/log/frameworks
`, binaryURL)
	if result, err := e.ExecuteSudoScript(ctx, host, installScript); err != nil || result.ExitCode != 0 {
		stderr := ""
		if result != nil {
			stderr = result.Stderr
		}
		return fmt.Errorf("vmagent install failed: %w (%s)", err, stderr)
	}

	execStart := fmt.Sprintf(
		"/opt/frameworks/vmagent-edge/vmagent -httpListenAddr=:8430 -promscrape.config=/etc/frameworks/vmagent-edge.yml -remoteWrite.url=%s",
		config.TelemetryURL,
	)
	if tokenArg != "" {
		execStart += tokenArg
	}

	return e.uploadSystemdUnit(ctx, host, SystemdUnitData{
		ServiceName: "frameworks-vmagent-edge",
		Description: "FrameWorks vmagent (edge telemetry)",
		WorkingDir:  "/opt/frameworks/vmagent-edge",
		ExecStart:   execStart,
		User:        "frameworks",
		After:       []string{"network-online", "frameworks-mistserver", "frameworks-helmsman"},
		LimitNOFILE: "1048576",
	})
}

func (e *EdgeProvisioner) installEdgeTelemetryDarwin(ctx context.Context, host inventory.Host, config EdgeProvisionConfig, dirs darwinDirSet, runScript scriptRunner, uploadFile fileUploader, remoteArch string) error {
	scrapeConfig, err := buildEdgeTelemetryScrapeConfig("native", config.NodeID)
	if err != nil {
		return err
	}
	err = e.writeRemoteFile(ctx, host, dirs.confDir+"/vmagent-edge.yml", scrapeConfig, 0o644)
	if err != nil {
		return err
	}

	tokenPath := ""
	if strings.TrimSpace(config.TelemetryToken) != "" {
		tokenPath = dirs.confDir + "/telemetry/token"
		err = e.writeRemoteFile(ctx, host, tokenPath, config.TelemetryToken+"\n", 0o600)
		if err != nil {
			return err
		}
	}

	binaryURL, err := resolveVMAgentBinaryURL(config.Version, "darwin", remoteArch)
	if err != nil {
		return err
	}
	installScript := fmt.Sprintf(`#!/bin/bash
set -e
mkdir -p %[1]s/vmagent-edge %[2]s
tmpdir=$(mktemp -d)
trap 'rm -rf "$tmpdir" /tmp/vmagent-edge.tar.gz' EXIT
curl -sSfL -o /tmp/vmagent-edge.tar.gz "%[3]s"
tar -xzf /tmp/vmagent-edge.tar.gz -C "$tmpdir"
bin=$(find "$tmpdir" -type f \( -name vmagent-prod -o -name vmagent \) | head -n 1)
test -n "$bin"
install -m 0755 "$bin" %[1]s/vmagent-edge/vmagent
`, dirs.baseDir, dirs.logDir, binaryURL)
	if result, err := runScript(installScript); err != nil || result.ExitCode != 0 {
		stderr := ""
		if result != nil {
			stderr = result.Stderr
		}
		return fmt.Errorf("vmagent install failed: %w (%s)", err, stderr)
	}

	args := []string{
		"-httpListenAddr=:8430",
		"-promscrape.config=" + dirs.confDir + "/vmagent-edge.yml",
		"-remoteWrite.url=" + config.TelemetryURL,
	}
	if tokenPath != "" {
		args = append(args, "-remoteWrite.bearerTokenFile="+tokenPath)
	}

	return e.uploadLaunchdPlistTo(ctx, host, dirs, LaunchdPlistData{
		Label:       "com.livepeer.frameworks.vmagent-edge",
		Description: "FrameWorks vmagent (edge telemetry)",
		Program:     dirs.baseDir + "/vmagent-edge/vmagent",
		ProgramArgs: args,
		WorkingDir:  dirs.baseDir + "/vmagent-edge",
		RunAtLoad:   true,
		KeepAlive:   true,
		LogPath:     dirs.logDir + "/com.livepeer.frameworks.vmagent-edge.log",
		ErrorPath:   dirs.logDir + "/com.livepeer.frameworks.vmagent-edge.err",
	})
}

func linuxTelemetryEnableSuffix(config EdgeProvisionConfig) string {
	if strings.TrimSpace(config.TelemetryURL) == "" {
		return ""
	}
	return " frameworks-vmagent-edge"
}

func linuxTelemetryStartCommands(config EdgeProvisionConfig) string {
	if strings.TrimSpace(config.TelemetryURL) == "" {
		return ""
	}
	return "systemctl start frameworks-vmagent-edge\nsleep 1"
}

func darwinTelemetryBootstrapCommand(plistDir string, config EdgeProvisionConfig, isUser bool) string {
	if strings.TrimSpace(config.TelemetryURL) == "" {
		return ""
	}
	if isUser {
		return fmt.Sprintf(`launchctl bootstrap "gui/${uid}" %s/com.livepeer.frameworks.vmagent-edge.plist 2>/dev/null || launchctl kickstart "gui/${uid}/com.livepeer.frameworks.vmagent-edge"
sleep 1`, plistDir)
	}
	return fmt.Sprintf(`launchctl bootstrap system %s/com.livepeer.frameworks.vmagent-edge.plist 2>/dev/null || launchctl kickstart system/com.livepeer.frameworks.vmagent-edge
sleep 1`, plistDir)
}

// installNative installs MistServer, Helmsman, and Caddy as systemd services.
func (e *EdgeProvisioner) installNative(ctx context.Context, host inventory.Host, config EdgeProvisionConfig) error {
	remoteOS, remoteArch, err := e.detectRemoteArch(ctx, host)
	if err != nil {
		return fmt.Errorf("failed to detect remote architecture: %w", err)
	}

	vars := e.buildEdgeVars(config, remoteOS)
	vars.Mode = "native"
	vars.SetModeDefaults()

	// Resolve versions from gitops manifest
	var manifest *gitops.Manifest
	if config.Version != "" {
		channel, version := gitops.ResolveVersion(config.Version)
		fetcher, err := gitops.NewFetcher(gitops.FetchOptions{})
		if err != nil {
			return fmt.Errorf("failed to create gitops fetcher: %w", err)
		}
		manifest, err = fetcher.Fetch(channel, version)
		if err != nil {
			return fmt.Errorf("failed to fetch gitops manifest: %w", err)
		}
	}

	arch := fmt.Sprintf("%s-%s", remoteOS, remoteArch)
	fmt.Printf("  remote architecture: %s\n", arch)

	switch remoteOS {
	case "darwin":
		return e.installNativeDarwin(ctx, host, config, vars, manifest, arch, remoteOS, remoteArch)
	case "linux":
		return e.installNativeLinux(ctx, host, config, vars, manifest, arch, remoteOS, remoteArch)
	default:
		return fmt.Errorf("unsupported OS for native mode: %s", remoteOS)
	}
}

// macOS system-domain paths (root-owned, /Library/LaunchDaemons).
const (
	darwinBaseDir  = "/usr/local/opt/frameworks"
	darwinConfDir  = "/usr/local/etc/frameworks"
	darwinLogDir   = "/usr/local/var/log/frameworks"
	darwinPlistDir = "/Library/LaunchDaemons"
)

// darwinPaths returns the appropriate base/conf/log/plist directories for the given domain.
// System domain uses /usr/local paths; user domain uses ~/.local and ~/Library paths.
type darwinDirSet struct {
	baseDir  string
	confDir  string
	logDir   string
	plistDir string
}

func darwinPaths(domain DarwinDomain) darwinDirSet {
	if domain == DomainUser {
		home := os.Getenv("HOME")
		return darwinDirSet{
			baseDir:  filepath.Join(home, ".local/opt/frameworks"),
			confDir:  filepath.Join(home, ".config/frameworks"),
			logDir:   filepath.Join(home, ".local/var/log/frameworks"),
			plistDir: filepath.Join(home, "Library/LaunchAgents"),
		}
	}
	return darwinDirSet{
		baseDir:  darwinBaseDir,
		confDir:  darwinConfDir,
		logDir:   darwinLogDir,
		plistDir: darwinPlistDir,
	}
}

func (e *EdgeProvisioner) installNativeDarwin(ctx context.Context, host inventory.Host, config EdgeProvisionConfig, vars templates.EdgeVars, manifest *gitops.Manifest, arch, remoteOS, remoteArch string) error {
	dirs := darwinPaths(config.DarwinDomain)
	isUser := config.DarwinDomain == DomainUser

	// runScript picks sudo vs direct execution based on domain
	runScript := func(script string) (*ssh.CommandResult, error) {
		if isUser {
			return e.ExecuteScript(ctx, host, script)
		}
		return e.ExecuteSudoScript(ctx, host, script)
	}
	uploadFile := func(opts ssh.UploadOptions) error {
		if isUser {
			return e.UploadFile(ctx, host, opts)
		}
		return e.uploadFileWithSudo(ctx, host, opts)
	}

	domainLabel := "system"
	if isUser {
		domainLabel = "user"
	}
	fmt.Printf("  launchd domain: %s\n", domainLabel)

	// (a) Create directories
	fmt.Println("  creating macOS directories...")
	mkdirScript := fmt.Sprintf(`#!/bin/bash
set -e
mkdir -p %s/mistserver %s/helmsman %s/caddy %s %s/certs %s %s/pki
`, dirs.baseDir, dirs.baseDir, dirs.baseDir, dirs.logDir, dirs.confDir, dirs.confDir, dirs.confDir)
	if _, err := runScript(mkdirScript); err != nil {
		return fmt.Errorf("failed to create directories: %w", err)
	}
	if caPath := config.helmsmanCAPath(remoteOS); strings.TrimSpace(config.CABundlePEM) != "" && caPath != "" {
		tmpFile, tmpErr := os.CreateTemp("", "edge-ca-*.crt")
		if tmpErr != nil {
			return tmpErr
		}
		defer os.Remove(tmpFile.Name())
		if _, tmpErr = tmpFile.WriteString(config.CABundlePEM); tmpErr != nil {
			tmpFile.Close()
			return tmpErr
		}
		if tmpErr = tmpFile.Close(); tmpErr != nil {
			return tmpErr
		}
		if err := uploadFile(ssh.UploadOptions{
			LocalPath:  tmpFile.Name(),
			RemotePath: caPath,
			Mode:       0o644,
		}); err != nil {
			return fmt.Errorf("failed to stage gRPC CA bundle: %w", err)
		}
		fmt.Printf("  gRPC CA bundle staged at %s\n", caPath)
	}

	// Ensure plist directory exists (~/Library/LaunchAgents may not exist)
	if isUser {
		mkPlistDir := fmt.Sprintf("#!/bin/bash\nmkdir -p %s\n", dirs.plistDir)
		if _, err := runScript(mkPlistDir); err != nil {
			return fmt.Errorf("failed to create plist directory: %w", err)
		}
	}

	// (b) MistServer
	fmt.Println("  installing MistServer...")
	mistPass, err := e.installDarwinMistServer(ctx, host, manifest, arch, dirs, runScript, uploadFile)
	if err != nil {
		return fmt.Errorf("mistserver install failed: %w", err)
	}

	// (c) Helmsman
	fmt.Println("  installing Helmsman...")
	if err = e.installDarwinHelmsman(ctx, host, config, vars, manifest, arch, remoteOS, remoteArch, dirs, runScript, uploadFile, mistPass); err != nil {
		return fmt.Errorf("helmsman install failed: %w", err)
	}

	// (d) Caddy
	fmt.Println("  installing Caddy...")
	if err = e.installDarwinCaddy(ctx, host, vars, manifest, arch, remoteOS, remoteArch, dirs, runScript, uploadFile); err != nil {
		return fmt.Errorf("caddy install failed: %w", err)
	}

	if strings.TrimSpace(config.TelemetryURL) != "" {
		fmt.Println("  installing edge telemetry agent...")
		if err = e.installEdgeTelemetryDarwin(ctx, host, config, dirs, runScript, uploadFile, remoteArch); err != nil {
			return fmt.Errorf("edge telemetry install failed: %w", err)
		}
	}

	// (e) Write .edge.env
	fmt.Println("  writing .edge.env for mode detection...")
	envTmpDir, err := os.MkdirTemp("", "edge-native-env-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(envTmpDir)

	if err = templates.WriteEdgeTemplates(envTmpDir, vars, true); err != nil {
		return fmt.Errorf("failed to render .edge.env: %w", err)
	}

	remoteDir := dirs.baseDir + "/edge"
	mkEdgeDir := fmt.Sprintf("#!/bin/bash\nmkdir -p %s\n", remoteDir)
	if _, err = runScript(mkEdgeDir); err != nil {
		return fmt.Errorf("failed to create %s: %w", remoteDir, err)
	}
	if err = uploadFile(ssh.UploadOptions{
		LocalPath: filepath.Join(envTmpDir, ".edge.env"), RemotePath: remoteDir + "/.edge.env", Mode: 0600,
	}); err != nil {
		return fmt.Errorf("failed to upload .edge.env: %w", err)
	}

	// (f) Start all services via launchctl
	fmt.Println("  starting services...")
	var startScript string
	if isUser {
		startScript = fmt.Sprintf(`#!/bin/bash
set -e
uid=$(id -u)
launchctl bootstrap "gui/${uid}" %[1]s/com.livepeer.frameworks.mistserver.plist 2>/dev/null || launchctl kickstart "gui/${uid}/com.livepeer.frameworks.mistserver"
sleep 2
launchctl bootstrap "gui/${uid}" %[1]s/com.livepeer.frameworks.helmsman.plist 2>/dev/null || launchctl kickstart "gui/${uid}/com.livepeer.frameworks.helmsman"
sleep 1
%[2]s
launchctl bootstrap "gui/${uid}" %[1]s/com.livepeer.frameworks.caddy.plist 2>/dev/null || launchctl kickstart "gui/${uid}/com.livepeer.frameworks.caddy"
echo "all services started (user domain)"
`, dirs.plistDir, darwinTelemetryBootstrapCommand(dirs.plistDir, config, true))
	} else {
		startScript = fmt.Sprintf(`#!/bin/bash
set -e
launchctl bootstrap system %[1]s/com.livepeer.frameworks.mistserver.plist 2>/dev/null || launchctl kickstart system/com.livepeer.frameworks.mistserver
sleep 2
launchctl bootstrap system %[1]s/com.livepeer.frameworks.helmsman.plist 2>/dev/null || launchctl kickstart system/com.livepeer.frameworks.helmsman
sleep 1
%[2]s
launchctl bootstrap system %[1]s/com.livepeer.frameworks.caddy.plist 2>/dev/null || launchctl kickstart system/com.livepeer.frameworks.caddy
echo "all services started (system domain)"
`, dirs.plistDir, darwinTelemetryBootstrapCommand(dirs.plistDir, config, false))
	}
	result, err := runScript(startScript)
	if err != nil || result.ExitCode != 0 {
		stderr := ""
		if result != nil {
			stderr = result.Stderr
		}
		return fmt.Errorf("service start failed: %w (%s)", err, stderr)
	}

	fmt.Println("  edge stack running on macOS (launchd)")
	return nil
}

type scriptRunner func(string) (*ssh.CommandResult, error)
type fileUploader func(ssh.UploadOptions) error

func (e *EdgeProvisioner) installDarwinMistServer(ctx context.Context, host inventory.Host, manifest *gitops.Manifest, arch string, dirs darwinDirSet, runScript scriptRunner, uploadFile fileUploader) (string, error) {
	mistPass, err := generateEdgePassword()
	if err != nil {
		return "", err
	}

	var binaryURL string
	if manifest != nil {
		dep := manifest.GetExternalDependency("mistserver")
		if dep != nil {
			binaryURL = dep.GetBinaryURL(arch)
		}
	}
	if binaryURL == "" {
		return "", fmt.Errorf("MistServer binary URL not available for %s (ensure darwin builds exist in mistserver releases)", arch)
	}

	installScript := fmt.Sprintf(`#!/bin/bash
set -e
curl -sSfL -o /tmp/mistserver.tar.gz "%s"
tar -xzf /tmp/mistserver.tar.gz -C %s/mistserver/
rm -f /tmp/mistserver.tar.gz
chmod +x %s/mistserver/MistServer
echo "MistServer installed"
`, binaryURL, dirs.baseDir, dirs.baseDir)

	result, err := runScript(installScript)
	if err != nil || result.ExitCode != 0 {
		stderr := ""
		if result != nil {
			stderr = result.Stderr
		}
		return "", fmt.Errorf("install script failed: %w (%s)", err, stderr)
	}

	envContent := "# MistServer environment\nMIST_DEBUG=3\n"
	if err = e.writeRemoteFile(ctx, host, dirs.confDir+"/mistserver.env", envContent, 0644); err != nil {
		return "", err
	}

	err = e.uploadLaunchdPlistTo(ctx, host, dirs, LaunchdPlistData{
		Label:       "com.livepeer.frameworks.mistserver",
		Description: "FrameWorks MistServer (edge media server)",
		Program:     dirs.baseDir + "/mistserver/MistServer",
		ProgramArgs: []string{"-a", fmt.Sprintf("frameworks:%s", mistPass)},
		WorkingDir:  dirs.baseDir + "/mistserver",
		EnvFile:     dirs.confDir + "/mistserver.env",
		RunAtLoad:   true,
		KeepAlive:   true,
		LogPath:     dirs.logDir + "/com.livepeer.frameworks.mistserver.log",
		ErrorPath:   dirs.logDir + "/com.livepeer.frameworks.mistserver.err",
	})
	return mistPass, err
}

func (e *EdgeProvisioner) installDarwinHelmsman(ctx context.Context, host inventory.Host, config EdgeProvisionConfig, vars templates.EdgeVars, manifest *gitops.Manifest, arch, remoteOS, remoteArch string, dirs darwinDirSet, runScript scriptRunner, uploadFile fileUploader, mistPass string) error {
	var binaryURL string
	if manifest != nil {
		svcInfo, err := manifest.GetServiceInfo("helmsman")
		if err == nil {
			binaryURL, _ = svcInfo.GetBinaryURL(remoteOS, remoteArch)
		}
	}
	if binaryURL == "" {
		return fmt.Errorf("helmsman binary URL not available for %s/%s", remoteOS, remoteArch)
	}

	installScript := fmt.Sprintf(`#!/bin/bash
set -e
ASSET_URL=%[1]q
ASSET_PATH=/tmp/helmsman.asset
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

curl -sSfL -o "$ASSET_PATH" "$ASSET_URL"
if [[ "$ASSET_URL" == *.zip ]]; then
  extract_zip "$ASSET_PATH" "$EXTRACT_DIR"
else
  tar -xzf "$ASSET_PATH" -C "$EXTRACT_DIR"
fi
mv "$EXTRACT_DIR"/frameworks-helmsman-* %[2]s/helmsman/helmsman 2>/dev/null || mv "$EXTRACT_DIR"/helmsman %[2]s/helmsman/helmsman 2>/dev/null || mv "$EXTRACT_DIR"/frameworks %[2]s/helmsman/helmsman 2>/dev/null || true
chmod +x %[2]s/helmsman/helmsman
echo "Helmsman installed"
`, binaryURL, dirs.baseDir)

	result, err := runScript(installScript)
	if err != nil || result.ExitCode != 0 {
		stderr := ""
		if result != nil {
			stderr = result.Stderr
		}
		return fmt.Errorf("install script failed: %w (%s)", err, stderr)
	}

	domain := config.primaryDomain()
	envLines := []string{
		"# Helmsman edge environment",
		fmt.Sprintf("NODE_ID=%s", vars.NodeID),
		fmt.Sprintf("EDGE_PUBLIC_URL=https://%s/view", domain),
		fmt.Sprintf("FOGHORN_CONTROL_ADDR=%s", vars.FoghornGRPCAddr),
		fmt.Sprintf("EDGE_ENROLLMENT_TOKEN=%s", vars.EnrollmentToken),
		fmt.Sprintf("EDGE_DOMAIN=%s", domain),
		fmt.Sprintf("ACME_EMAIL=%s", vars.AcmeEmail),
		fmt.Sprintf("DEPLOY_MODE=%s", vars.Mode),
		fmt.Sprintf("MISTSERVER_URL=http://%s", vars.MistUpstream),
		fmt.Sprintf("HELMSMAN_WEBHOOK_URL=http://%s", vars.HelmsmanUpstream),
		"CADDY_ADMIN_URL=http://localhost:2019",
		fmt.Sprintf("MIST_API_USERNAME=%s", "frameworks"),
		fmt.Sprintf("MIST_API_PASSWORD=%s", mistPass),
	}
	if vars.GRPCTLSCAPath != "" {
		envLines = append(envLines, fmt.Sprintf("GRPC_TLS_CA_PATH=%s", vars.GRPCTLSCAPath))
	}
	envContent := strings.Join(envLines, "\n") + "\n"
	if err := e.writeRemoteFile(ctx, host, dirs.confDir+"/helmsman.env", envContent, 0600); err != nil {
		return err
	}

	return e.uploadLaunchdPlistTo(ctx, host, dirs, LaunchdPlistData{
		Label:       "com.livepeer.frameworks.helmsman",
		Description: "FrameWorks Helmsman (edge sidecar)",
		Program:     dirs.baseDir + "/helmsman/helmsman",
		WorkingDir:  dirs.baseDir + "/helmsman",
		EnvFile:     dirs.confDir + "/helmsman.env",
		RunAtLoad:   true,
		KeepAlive:   true,
		LogPath:     dirs.logDir + "/com.livepeer.frameworks.helmsman.log",
		ErrorPath:   dirs.logDir + "/com.livepeer.frameworks.helmsman.err",
	})
}

func (e *EdgeProvisioner) installDarwinCaddy(ctx context.Context, host inventory.Host, vars templates.EdgeVars, manifest *gitops.Manifest, arch, remoteOS, remoteArch string, dirs darwinDirSet, runScript scriptRunner, uploadFile fileUploader) error {
	var binaryURL string
	if manifest != nil {
		dep := manifest.GetExternalDependency("caddy")
		if dep != nil {
			binaryURL = dep.GetBinaryURL(arch)
		}
		if binaryURL == "" {
			svcInfo, err := manifest.GetServiceInfo("caddy")
			if err == nil {
				binaryURL, _ = svcInfo.GetBinaryURL(remoteOS, remoteArch)
			}
		}
	}
	if binaryURL == "" {
		return fmt.Errorf("caddy binary URL not available for %s", arch)
	}

	// Caddy data dir: system → /usr/local/var/lib/caddy, user → ~/.local/var/lib/caddy
	caddyDataDir := "/usr/local/var/lib/caddy"
	if dirs.baseDir != darwinBaseDir {
		caddyDataDir = filepath.Join(os.Getenv("HOME"), ".local/var/lib/caddy")
	}

	installScript := fmt.Sprintf(`#!/bin/bash
set -e
curl -sSfL -o /tmp/caddy.tar.gz "%s"
tar -xzf /tmp/caddy.tar.gz -C /tmp/
mv /tmp/caddy %[2]s/caddy/caddy 2>/dev/null || true
chmod +x %[2]s/caddy/caddy
mkdir -p %[3]s
echo "Caddy installed"
`, binaryURL, dirs.baseDir, caddyDataDir)

	result, err := runScript(installScript)
	if err != nil || result.ExitCode != 0 {
		stderr := ""
		if result != nil {
			stderr = result.Stderr
		}
		return fmt.Errorf("install script failed: %w (%s)", err, stderr)
	}

	tmpDir, err := os.MkdirTemp("", "edge-caddy-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmpDir)

	if err := templates.WriteEdgeTemplates(tmpDir, vars, true); err != nil {
		return fmt.Errorf("failed to write templates: %w", err)
	}

	caddyfilePath := filepath.Join(tmpDir, "Caddyfile")
	if err := uploadFile(ssh.UploadOptions{
		LocalPath: caddyfilePath, RemotePath: dirs.confDir + "/Caddyfile", Mode: 0644,
	}); err != nil {
		return fmt.Errorf("failed to upload Caddyfile: %w", err)
	}

	envContent := fmt.Sprintf("# Caddy edge environment\nCADDY_EMAIL=%s\n", vars.AcmeEmail)
	if err := e.writeRemoteFile(ctx, host, dirs.confDir+"/caddy.env", envContent, 0600); err != nil {
		return err
	}

	return e.uploadLaunchdPlistTo(ctx, host, dirs, LaunchdPlistData{
		Label:       "com.livepeer.frameworks.caddy",
		Description: "FrameWorks Caddy Reverse Proxy (edge)",
		Program:     dirs.baseDir + "/caddy/caddy",
		ProgramArgs: []string{"run", "--config", dirs.confDir + "/Caddyfile"},
		WorkingDir:  dirs.confDir,
		EnvFile:     dirs.confDir + "/caddy.env",
		RunAtLoad:   true,
		KeepAlive:   true,
		LogPath:     dirs.logDir + "/com.livepeer.frameworks.caddy.log",
		ErrorPath:   dirs.logDir + "/com.livepeer.frameworks.caddy.err",
	})
}

// installNativeLinux is the original Linux systemd installation path.
func (e *EdgeProvisioner) installNativeLinux(ctx context.Context, host inventory.Host, config EdgeProvisionConfig, vars templates.EdgeVars, manifest *gitops.Manifest, arch, remoteOS, remoteArch string) error {
	// (0) Create frameworks user/group for MistServer and Helmsman systemd units
	fmt.Println("  creating frameworks user/group...")
	userScript := `#!/bin/bash
set -e
shell=/usr/bin/nologin
[ ! -x "$shell" ] && shell=/sbin/nologin
[ ! -x "$shell" ] && shell=/bin/false
getent group frameworks >/dev/null || groupadd --system frameworks
id -u frameworks >/dev/null 2>&1 || useradd -r -g frameworks -s "$shell" frameworks
mkdir -p /opt/frameworks/mistserver /opt/frameworks/helmsman /etc/frameworks /var/log/frameworks
mkdir -p /etc/frameworks/pki
chown -R frameworks:frameworks /opt/frameworks/mistserver /opt/frameworks/helmsman /etc/frameworks /var/log/frameworks
`
	if result, err := e.ExecuteSudoScript(ctx, host, userScript); err != nil || result.ExitCode != 0 {
		stderr := ""
		if result != nil {
			stderr = result.Stderr
		}
		return fmt.Errorf("failed to create frameworks user: %w (%s)", err, stderr)
	}
	if err := e.stageCABundleAt(ctx, host, config.CABundlePEM, config.helmsmanCAPath(remoteOS)); err != nil {
		return fmt.Errorf("failed to stage gRPC CA bundle: %w", err)
	}

	// (a) MistServer
	fmt.Println("  installing MistServer...")
	mistPass, err := e.installNativeMistServer(ctx, host, manifest, arch)
	if err != nil {
		return fmt.Errorf("mistserver install failed: %w", err)
	}

	// (b) Helmsman
	fmt.Println("  installing Helmsman...")
	if err = e.installNativeHelmsman(ctx, host, config, vars, manifest, arch, remoteOS, remoteArch, mistPass); err != nil {
		return fmt.Errorf("helmsman install failed: %w", err)
	}

	// (c) Caddy
	fmt.Println("  installing Caddy...")
	if err = e.installNativeCaddy(ctx, host, vars, manifest, arch, remoteOS, remoteArch); err != nil {
		return fmt.Errorf("caddy install failed: %w", err)
	}

	if strings.TrimSpace(config.TelemetryURL) != "" {
		fmt.Println("  installing edge telemetry agent...")
		if err = e.installEdgeTelemetryLinux(ctx, host, config, remoteArch); err != nil {
			return fmt.Errorf("edge telemetry install failed: %w", err)
		}
	}

	// (d) Write .edge.env for mode detection by status/update/cert/logs commands
	fmt.Println("  writing .edge.env for mode detection...")
	envTmpDir, err := os.MkdirTemp("", "edge-native-env-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(envTmpDir)

	if err = templates.WriteEdgeTemplates(envTmpDir, vars, true); err != nil {
		return fmt.Errorf("failed to render .edge.env: %w", err)
	}

	remoteDir := "/opt/frameworks/edge"
	if _, err = e.RunSudoCommand(ctx, host, "mkdir -p "+remoteDir); err != nil {
		return fmt.Errorf("failed to create %s: %w", remoteDir, err)
	}
	if err = e.uploadFileWithSudo(ctx, host, ssh.UploadOptions{
		LocalPath: filepath.Join(envTmpDir, ".edge.env"), RemotePath: remoteDir + "/.edge.env", Mode: 0600,
	}); err != nil {
		return fmt.Errorf("failed to upload .edge.env: %w", err)
	}

	// (e) Start all services in order
	fmt.Println("  starting services...")
	startScript := fmt.Sprintf(`#!/bin/bash
set -e
systemctl daemon-reload
systemctl enable frameworks-mistserver frameworks-helmsman frameworks-caddy%s
systemctl start frameworks-mistserver
sleep 2
systemctl start frameworks-helmsman
	sleep 1
	%s
	systemctl start frameworks-caddy
	echo "all services started"
	`, linuxTelemetryEnableSuffix(config), linuxTelemetryStartCommands(config))
	result, err := e.ExecuteSudoScript(ctx, host, startScript)
	if err != nil || result.ExitCode != 0 {
		stderr := ""
		if result != nil {
			stderr = result.Stderr
		}
		return fmt.Errorf("service start failed: %w (%s)", err, stderr)
	}

	fmt.Println("  edge stack running (frameworks-mistserver, frameworks-helmsman, frameworks-caddy)")
	return nil
}

func (e *EdgeProvisioner) installNativeMistServer(ctx context.Context, host inventory.Host, manifest *gitops.Manifest, arch string) (string, error) {
	mistPass, err := generateEdgePassword()
	if err != nil {
		return "", err
	}

	var binaryURL string
	if manifest != nil {
		dep := manifest.GetExternalDependency("mistserver")
		if dep != nil {
			binaryURL = dep.GetBinaryURL(arch)
		}
	}
	if binaryURL == "" {
		return "", fmt.Errorf("MistServer binary URL not available (set --version to resolve from gitops, or provide binary URL in manifest)")
	}

	installScript := fmt.Sprintf(`#!/bin/bash
set -e
mkdir -p /opt/frameworks/mistserver
wget -q -O /tmp/mistserver.tar.gz "%s"
tar -xzf /tmp/mistserver.tar.gz -C /opt/frameworks/mistserver/
rm -f /tmp/mistserver.tar.gz
chmod +x /opt/frameworks/mistserver/MistServer
echo "MistServer installed"
`, binaryURL)

	result, err := e.ExecuteSudoScript(ctx, host, installScript)
	if err != nil || result.ExitCode != 0 {
		stderr := ""
		if result != nil {
			stderr = result.Stderr
		}
		return "", fmt.Errorf("install script failed: %w (%s)", err, stderr)
	}

	envContent := "# MistServer environment\nMIST_DEBUG=3\n"
	if err := e.writeRemoteFile(ctx, host, "/etc/frameworks/mistserver.env", envContent, 0644); err != nil {
		return "", err
	}

	unitData := SystemdUnitData{
		ServiceName: "frameworks-mistserver",
		Description: "FrameWorks MistServer (edge media server)",
		WorkingDir:  "/opt/frameworks/mistserver",
		ExecStart:   fmt.Sprintf("/opt/frameworks/mistserver/MistServer -a frameworks:%s", mistPass),
		User:        "frameworks",
		EnvFile:     "/etc/frameworks/mistserver.env",
		After:       []string{"network-online"},
		LimitNOFILE: "1048576",
	}

	return mistPass, e.uploadSystemdUnit(ctx, host, unitData)
}

func (e *EdgeProvisioner) installNativeHelmsman(ctx context.Context, host inventory.Host, config EdgeProvisionConfig, vars templates.EdgeVars, manifest *gitops.Manifest, arch, remoteOS, remoteArch, mistPass string) error {
	var binaryURL string
	if manifest != nil {
		svcInfo, err := manifest.GetServiceInfo("helmsman")
		if err == nil {
			binaryURL, _ = svcInfo.GetBinaryURL(remoteOS, remoteArch)
		}
	}
	if binaryURL == "" {
		return fmt.Errorf("helmsman binary URL not available (set --version to resolve from gitops)")
	}

	installScript := fmt.Sprintf(`#!/bin/bash
set -e
ASSET_URL=%[1]q
ASSET_PATH=/tmp/helmsman.asset
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

mkdir -p /opt/frameworks/helmsman
wget -q -O "$ASSET_PATH" "$ASSET_URL"
if [[ "$ASSET_URL" == *.zip ]]; then
  extract_zip "$ASSET_PATH" "$EXTRACT_DIR"
else
  tar -xzf "$ASSET_PATH" -C "$EXTRACT_DIR"
fi
mv "$EXTRACT_DIR"/frameworks-helmsman-* /opt/frameworks/helmsman/helmsman 2>/dev/null || mv "$EXTRACT_DIR"/helmsman /opt/frameworks/helmsman/helmsman 2>/dev/null || mv "$EXTRACT_DIR"/frameworks /opt/frameworks/helmsman/helmsman 2>/dev/null || true
chmod +x /opt/frameworks/helmsman/helmsman
echo "Helmsman installed"
`, binaryURL)

	result, err := e.ExecuteSudoScript(ctx, host, installScript)
	if err != nil || result.ExitCode != 0 {
		stderr := ""
		if result != nil {
			stderr = result.Stderr
		}
		return fmt.Errorf("install script failed: %w (%s)", err, stderr)
	}

	// Generate env file with all edge vars + native-specific additions
	domain := config.primaryDomain()
	envLines := []string{
		"# Helmsman edge environment",
		fmt.Sprintf("NODE_ID=%s", vars.NodeID),
		fmt.Sprintf("EDGE_PUBLIC_URL=https://%s/view", domain),
		fmt.Sprintf("FOGHORN_CONTROL_ADDR=%s", vars.FoghornGRPCAddr),
		fmt.Sprintf("EDGE_ENROLLMENT_TOKEN=%s", vars.EnrollmentToken),
		fmt.Sprintf("EDGE_DOMAIN=%s", domain),
		fmt.Sprintf("ACME_EMAIL=%s", vars.AcmeEmail),
		fmt.Sprintf("DEPLOY_MODE=%s", vars.Mode),
		// Native-specific: services on localhost
		fmt.Sprintf("MISTSERVER_URL=http://%s", vars.MistUpstream),
		fmt.Sprintf("HELMSMAN_WEBHOOK_URL=http://%s", vars.HelmsmanUpstream),
		"CADDY_ADMIN_URL=http://localhost:2019",
		fmt.Sprintf("MIST_API_USERNAME=%s", "frameworks"),
		fmt.Sprintf("MIST_API_PASSWORD=%s", mistPass),
	}
	if vars.GRPCTLSCAPath != "" {
		envLines = append(envLines, fmt.Sprintf("GRPC_TLS_CA_PATH=%s", vars.GRPCTLSCAPath))
	}
	envContent := strings.Join(envLines, "\n") + "\n"
	if err := e.writeRemoteFile(ctx, host, "/etc/frameworks/helmsman.env", envContent, 0600); err != nil {
		return err
	}

	// Generate systemd unit
	unitData := SystemdUnitData{
		ServiceName: "frameworks-helmsman",
		Description: "FrameWorks Helmsman (edge sidecar)",
		WorkingDir:  "/opt/frameworks/helmsman",
		ExecStart:   "/opt/frameworks/helmsman/helmsman",
		User:        "frameworks",
		EnvFile:     "/etc/frameworks/helmsman.env",
		After:       []string{"network-online", "frameworks-mistserver"},
	}

	return e.uploadSystemdUnit(ctx, host, unitData)
}

func (e *EdgeProvisioner) installNativeCaddy(ctx context.Context, host inventory.Host, vars templates.EdgeVars, manifest *gitops.Manifest, arch, remoteOS, remoteArch string) error {
	var binaryURL string
	if manifest != nil {
		dep := manifest.GetExternalDependency("caddy")
		if dep != nil {
			binaryURL = dep.GetBinaryURL(arch)
		}
		// Fallback: try service info
		if binaryURL == "" {
			svcInfo, err := manifest.GetServiceInfo("caddy")
			if err == nil {
				binaryURL, _ = svcInfo.GetBinaryURL(remoteOS, remoteArch)
			}
		}
	}
	if binaryURL == "" {
		return fmt.Errorf("caddy binary URL not available (set --version to resolve from gitops)")
	}

	installScript := fmt.Sprintf(`#!/bin/bash
set -e
mkdir -p /opt/frameworks/caddy
wget -q -O /tmp/caddy.tar.gz "%s"
tar -xzf /tmp/caddy.tar.gz -C /tmp/
mv /tmp/caddy /opt/frameworks/caddy/caddy 2>/dev/null || true
chmod +x /opt/frameworks/caddy/caddy
rm -f /tmp/caddy.tar.gz

# Create caddy user if needed
shell=/usr/bin/nologin
[ ! -x "$shell" ] && shell=/sbin/nologin
[ ! -x "$shell" ] && shell=/bin/false
getent group caddy >/dev/null || groupadd --system caddy
id -u caddy &>/dev/null || useradd -r -g caddy -s "$shell" caddy
mkdir -p /etc/caddy /var/lib/caddy
chown -R caddy:caddy /etc/caddy /var/lib/caddy

echo "Caddy installed"
`, binaryURL)

	result, err := e.ExecuteSudoScript(ctx, host, installScript)
	if err != nil || result.ExitCode != 0 {
		stderr := ""
		if result != nil {
			stderr = result.Stderr
		}
		return fmt.Errorf("install script failed: %w (%s)", err, stderr)
	}

	// Write the Caddyfile using the edge template system
	tmpDir, err := os.MkdirTemp("", "edge-caddy-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmpDir)

	if err := templates.WriteEdgeTemplates(tmpDir, vars, true); err != nil {
		return fmt.Errorf("failed to write templates: %w", err)
	}

	caddyfilePath := filepath.Join(tmpDir, "Caddyfile")
	if err := e.uploadFileWithSudo(ctx, host, ssh.UploadOptions{
		LocalPath: caddyfilePath, RemotePath: "/etc/caddy/Caddyfile", Mode: 0644,
		Owner: "caddy", Group: "caddy",
	}); err != nil {
		return fmt.Errorf("failed to upload Caddyfile: %w", err)
	}

	// Ensure caddy can read cert files
	if vars.CertPath != "" {
		_, _ = e.RunSudoCommand(ctx, host, "chown caddy:caddy /etc/frameworks/certs/cert.pem /etc/frameworks/certs/key.pem 2>/dev/null || true")
	}

	// Generate caddy env file
	envContent := fmt.Sprintf("# Caddy edge environment\nCADDY_EMAIL=%s\n", vars.AcmeEmail)
	if err := e.writeRemoteFile(ctx, host, "/etc/frameworks/caddy.env", envContent, 0600); err != nil {
		return err
	}

	// Generate systemd unit
	unitData := SystemdUnitData{
		ServiceName: "frameworks-caddy",
		Description: "FrameWorks Caddy Reverse Proxy (edge)",
		WorkingDir:  "/etc/caddy",
		ExecStart:   "/opt/frameworks/caddy/caddy run --config /etc/caddy/Caddyfile",
		User:        "caddy",
		EnvFile:     "/etc/frameworks/caddy.env",
		After:       []string{"network-online", "frameworks-mistserver", "frameworks-helmsman"},
	}

	return e.uploadSystemdUnit(ctx, host, unitData)
}

// verifyHTTPS polls the edge domain for HTTPS readiness.
func (e *EdgeProvisioner) verifyHTTPS(domain string, timeout time.Duration) error {
	url := "https://" + domain + "/health"
	httpClient := &http.Client{
		Timeout:   5 * time.Second,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}},
	}
	deadline := time.Now().Add(timeout)

	for {
		ctx, cancel := context.WithTimeout(context.Background(), httpClient.Timeout)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			cancel()
			return err
		}
		resp, err := httpClient.Do(req)
		cancel()
		if err == nil && resp != nil && resp.StatusCode == 200 {
			if resp.Body != nil {
				_ = resp.Body.Close()
			}
			fmt.Printf("  HTTPS ready at %s\n", url)
			return nil
		}
		if resp != nil && resp.Body != nil {
			_ = resp.Body.Close()
		}
		if time.Now().After(deadline) {
			if err != nil {
				return fmt.Errorf("HTTPS check failed: %w", err)
			}
			return fmt.Errorf("HTTPS not ready before timeout")
		}
		time.Sleep(5 * time.Second)
	}
}

// Detect checks if an edge stack is running on the host.
func (e *EdgeProvisioner) Detect(ctx context.Context, host inventory.Host) (*detect.ServiceState, error) {
	// Try docker first
	result, err := e.RunCommand(ctx, host, "docker compose -f /opt/frameworks/edge/docker-compose.edge.yml ps --format json 2>/dev/null")
	if err == nil && result.ExitCode == 0 && strings.TrimSpace(result.Stdout) != "" {
		return &detect.ServiceState{
			Exists:  true,
			Running: true,
			Metadata: map[string]string{
				"mode": "docker",
			},
		}, nil
	}

	// Try native (systemd)
	result, err = e.RunCommand(ctx, host, "systemctl is-active frameworks-caddy frameworks-helmsman frameworks-mistserver 2>/dev/null")
	if err == nil && result.ExitCode == 0 {
		return &detect.ServiceState{
			Exists:  true,
			Running: true,
			Metadata: map[string]string{
				"mode": "native",
			},
		}, nil
	}

	// Try native (macOS launchd — system domain)
	result, err = e.RunCommand(ctx, host, "launchctl print system/com.livepeer.frameworks.caddy 2>/dev/null")
	if err == nil && result.ExitCode == 0 {
		return &detect.ServiceState{
			Exists:  true,
			Running: true,
			Metadata: map[string]string{
				"mode": "native",
			},
		}, nil
	}

	// Try native (macOS launchd — user domain)
	result, err = e.RunCommand(ctx, host, "launchctl print gui/$(id -u)/com.livepeer.frameworks.caddy 2>/dev/null")
	if err == nil && result.ExitCode == 0 {
		return &detect.ServiceState{
			Exists:  true,
			Running: true,
			Metadata: map[string]string{
				"mode": "native",
			},
		}, nil
	}

	return &detect.ServiceState{Exists: false, Running: false}, nil
}

// Validate checks if the edge stack is healthy.
func (e *EdgeProvisioner) Validate(ctx context.Context, host inventory.Host, config ServiceConfig) error {
	// Check Caddy on port 443
	checker := &health.TCPChecker{Timeout: 5 * time.Second}
	result := checker.Check(host.ExternalIP, 443)
	if !result.OK {
		return fmt.Errorf("edge HTTPS port check failed: %s", result.Error)
	}
	return nil
}

// Initialize is a no-op for edge nodes.
func (e *EdgeProvisioner) Initialize(ctx context.Context, host inventory.Host, config ServiceConfig) error {
	return nil
}

// buildEdgeVars converts EdgeProvisionConfig into templates.EdgeVars.
// remoteOS should be "linux" or "darwin" to set OS-appropriate paths.
func (e *EdgeProvisioner) buildEdgeVars(config EdgeProvisionConfig, remoteOS string) templates.EdgeVars {
	domain := config.primaryDomain()
	vars := templates.EdgeVars{
		NodeID:          config.NodeID,
		EdgeDomain:      domain,
		AcmeEmail:       config.Email,
		FoghornGRPCAddr: config.FoghornGRPCAddr,
		EnrollmentToken: config.EnrollmentToken,
		GRPCTLSCAPath:   config.helmsmanCAPath(remoteOS),
		Mode:            config.resolvedMode(),
		TelemetryURL:    config.TelemetryURL,
		TelemetryToken:  config.TelemetryToken,
	}
	// Bootstrap Caddyfile: no wildcard site address needed.
	// Helmsman renders the production Caddyfile after enrollment via ConfigSeed.
	vars.SiteAddress = domain
	return vars
}

// writeRemoteFile writes content to a remote file via temp file + upload.
func (e *EdgeProvisioner) writeRemoteFile(ctx context.Context, host inventory.Host, remotePath, content string, mode uint32) error {
	tmpFile, err := os.CreateTemp("", "edge-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmpFile.Name())

	if _, err := tmpFile.WriteString(content); err != nil {
		tmpFile.Close()
		return err
	}
	tmpFile.Close()

	return e.uploadFileWithSudo(ctx, host, ssh.UploadOptions{
		LocalPath: tmpFile.Name(), RemotePath: remotePath, Mode: mode,
	})
}

// uploadLaunchdPlist generates a launchd plist and uploads it to /Library/LaunchDaemons (system domain).
// Kept for backward compatibility; new code should use uploadLaunchdPlistTo.
// uploadLaunchdPlistTo generates a launchd plist and uploads it to the given directory set.
// Since launchd doesn't support EnvironmentFile natively, we use a wrapper shell script
// that sources the env file before exec-ing the program.
func (e *EdgeProvisioner) uploadLaunchdPlistTo(ctx context.Context, host inventory.Host, dirs darwinDirSet, data LaunchdPlistData) error {
	wrapperPath := dirs.baseDir + "/" + strings.TrimPrefix(data.Label, "com.livepeer.frameworks.") + "/run.sh"
	program := data.Program
	args := strings.Join(data.ProgramArgs, " ")

	// Read env file line-by-line and export only valid KEY=VALUE pairs.
	wrapperContent := fmt.Sprintf(`#!/bin/bash
while IFS= read -r line || [ -n "$line" ]; do
  line="${line%%#*}"
  line="${line#"${line%%%%[! ]*}"}"
  [ -z "$line" ] && continue
  case "$line" in
    *=*) export "$line" ;;
  esac
done < %s
exec %s %s
`, data.EnvFile, program, args)

	if err := e.writeRemoteFile(ctx, host, wrapperPath, wrapperContent, 0755); err != nil {
		return fmt.Errorf("failed to write wrapper script for %s: %w", data.Label, err)
	}

	data.Program = "/bin/bash"
	data.ProgramArgs = []string{wrapperPath}
	data.EnvFile = "" // Handled by wrapper

	plistContent, err := GenerateLaunchdPlist(data)
	if err != nil {
		return fmt.Errorf("failed to generate launchd plist for %s: %w", data.Label, err)
	}

	plistPath := fmt.Sprintf("%s/%s.plist", dirs.plistDir, data.Label)
	return e.writeRemoteFile(ctx, host, plistPath, plistContent, 0644)
}

// uploadSystemdUnit generates a unit file and uploads it.
func (e *EdgeProvisioner) uploadSystemdUnit(ctx context.Context, host inventory.Host, data SystemdUnitData) error {
	unitContent, err := GenerateSystemdUnit(data)
	if err != nil {
		return fmt.Errorf("failed to generate systemd unit for %s: %w", data.ServiceName, err)
	}

	unitPath := fmt.Sprintf("/etc/systemd/system/%s.service", data.ServiceName)
	return e.writeRemoteFile(ctx, host, unitPath, unitContent, 0644)
}

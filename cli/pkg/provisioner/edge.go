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
	"frameworks/cli/pkg/ansible"
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
	executor *ansible.Executor
}

// NewEdgeProvisioner creates a new edge provisioner.
func NewEdgeProvisioner(pool *ssh.Pool) *EdgeProvisioner {
	executor, err := ansible.NewExecutor("")
	if err != nil {
		panic(fmt.Sprintf("create ansible executor for edge: %v", err))
	}
	return &EdgeProvisioner{
		BaseProvisioner: NewBaseProvisioner("edge", pool),
		executor:        executor,
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

	// Remote OS determines OS-appropriate paths (systemd vs launchd, etc.).
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
	sysctlContent := "net.core.rmem_max = 16777216\n" +
		"net.core.wmem_max = 16777216\n" +
		"net.core.somaxconn = 8192\n" +
		"net.ipv4.ip_local_port_range = 16384 65535\n"
	limitsContent := "* soft nofile 1048576\n* hard nofile 1048576\n"

	tasks := []ansible.Task{
		ansible.TaskCopy("/etc/sysctl.d/frameworks-edge.conf", sysctlContent, ansible.CopyOpts{Mode: "0644"}),
		ansible.TaskCopy("/etc/security/limits.d/frameworks-edge.conf", limitsContent, ansible.CopyOpts{Mode: "0644"}),
		// sysctl --system is always-safe to re-run; changed_when: false keeps
		// Ansible from reporting this task as a change on every apply.
		ansible.TaskShell("sysctl --system", ansible.ShellOpts{ChangedWhen: "false"}),
	}

	playbook := &ansible.Playbook{
		Name:  "Apply edge kernel/fs tuning",
		Hosts: host.ExternalIP,
		Plays: []ansible.Play{
			{
				Name:        "Edge tuning",
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
			"ansible_ssh_private_key_file": e.sshPool.DefaultKeyPath(),
		},
	})
	result, execErr := e.executor.ExecutePlaybook(ctx, playbook, inv, ansible.ExecuteOptions{Verbose: false})
	if execErr != nil {
		return fmt.Errorf("tuning failed: %w\nOutput: %s", execErr, result.Output)
	}
	if !result.Success {
		return fmt.Errorf("tuning playbook failed\nOutput: %s", result.Output)
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
	image, err := resolveObservabilityImage(config.Version, "", "vmagent", defaultVMAgentImage, nil)
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

	return buildVMAgentScrapeConfig([]map[string]any{
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
	scrapeConfig, err := buildEdgeTelemetryScrapeConfig("native", config.NodeID)
	if err != nil {
		return err
	}

	artifact, err := resolveVMAgentArtifact(config.Version, "linux", remoteArch, nil)
	if err != nil {
		return err
	}

	execStart := fmt.Sprintf(
		"/opt/frameworks/vmagent-edge/vmagent -httpListenAddr=:8430 -promscrape.config=/etc/frameworks/vmagent-edge.yml -remoteWrite.url=%s",
		config.TelemetryURL,
	)
	if strings.TrimSpace(config.TelemetryToken) != "" {
		execStart += " -remoteWrite.bearerTokenFile=/etc/frameworks/telemetry/token"
	}

	unitContent := ansible.RenderSystemdUnit(ansible.SystemdUnitSpec{
		Description: "FrameWorks vmagent (edge telemetry)",
		After:       []string{"network-online.target", "frameworks-mistserver.service", "frameworks-helmsman.service"},
		Wants:       []string{"network-online.target"},
		User:        "frameworks",
		Group:       "frameworks",
		WorkingDir:  "/opt/frameworks/vmagent-edge",
		ExecStart:   execStart,
		Restart:     "always",
		RestartSec:  5,
		LimitNOFILE: "1048576",
	})

	tasks := []ansible.Task{
		// Directories. owner=frameworks so the service user can write logs/state.
		mkdirTask("/etc/frameworks", "frameworks", "frameworks", "0755"),
		mkdirTask("/etc/frameworks/telemetry", "frameworks", "frameworks", "0755"),
		mkdirTask("/opt/frameworks/vmagent-edge", "frameworks", "frameworks", "0755"),
		mkdirTask("/var/log/frameworks", "frameworks", "frameworks", "0755"),
		mkdirTask("/tmp/frameworks-vmutils", "root", "root", "0755"),

		// Scrape config.
		ansible.TaskCopy("/etc/frameworks/vmagent-edge.yml", scrapeConfig, ansible.CopyOpts{Owner: "frameworks", Group: "frameworks", Mode: "0644"}),
	}

	// Optional bearer-token file for remote-write auth.
	if strings.TrimSpace(config.TelemetryToken) != "" {
		tasks = append(tasks, ansible.TaskCopy(
			"/etc/frameworks/telemetry/token",
			config.TelemetryToken+"\n",
			ansible.CopyOpts{Owner: "frameworks", Group: "frameworks", Mode: "0600"},
		))
	}

	extractSentinel := ansible.ArtifactSentinel("/tmp/frameworks-vmutils", artifact.Checksum+artifact.URL)
	installSentinel := ansible.ArtifactSentinel("/opt/frameworks/vmagent-edge", artifact.Checksum+artifact.URL)
	tasks = append(tasks,
		// Pinned vmutils tarball. Flat layout, no top-level wrapper dir.
		// Tarball stays in /tmp for get_url cache-hit; version-keyed sentinels
		// rotate both the extract skip and the install skip on a pin bump.
		ansible.TaskGetURL(artifact.URL, "/tmp/vmagent-edge.tar.gz", artifact.Checksum),
		ansible.TaskUnarchive("/tmp/vmagent-edge.tar.gz", "/tmp/frameworks-vmutils",
			extractSentinel, ansible.UnarchiveOpts{}),
		ansible.TaskShell("touch "+extractSentinel, ansible.ShellOpts{Creates: extractSentinel}),
		ansible.TaskShell(
			"install -m 0755 -o frameworks -g frameworks /tmp/frameworks-vmutils/vmagent-prod /opt/frameworks/vmagent-edge/vmagent && "+
				"touch "+installSentinel+" && chown frameworks:frameworks "+installSentinel,
			ansible.ShellOpts{Creates: installSentinel},
		),

		// Systemd unit + start.
		ansible.TaskCopy("/etc/systemd/system/frameworks-vmagent-edge.service", unitContent, ansible.CopyOpts{Mode: "0644"}),
		ansible.TaskSystemdService("frameworks-vmagent-edge", ansible.SystemdOpts{
			State:        "started",
			Enabled:      ansible.BoolPtr(true),
			DaemonReload: true,
		}),
	)

	playbook := &ansible.Playbook{
		Name:  "Install vmagent edge telemetry (linux)",
		Hosts: host.ExternalIP,
		Plays: []ansible.Play{
			{
				Name:        "Install vmagent edge telemetry",
				Hosts:       host.ExternalIP,
				Become:      true,
				GatherFacts: true,
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
			"ansible_ssh_private_key_file": e.sshPool.DefaultKeyPath(),
		},
	})

	result, execErr := e.executor.ExecutePlaybook(ctx, playbook, inv, ansible.ExecuteOptions{Verbose: true})
	if execErr != nil {
		return fmt.Errorf("vmagent install failed: %w\nOutput: %s", execErr, result.Output)
	}
	if !result.Success {
		return fmt.Errorf("vmagent install playbook failed\nOutput: %s", result.Output)
	}
	return nil
}

// mkdirTask emits an ansible.builtin.file task that creates path as a directory.
// Used by edge install paths where the same directory pattern recurs across
// telemetry, mistserver, helmsman, and caddy installs.
func mkdirTask(path, owner, group, mode string) ansible.Task {
	return ansible.Task{
		Name:   "create " + path,
		Module: "ansible.builtin.file",
		Args:   map[string]any{"path": path, "state": "directory", "owner": owner, "group": group, "mode": mode},
	}
}

// darwinLaunchdTasks returns the task sequence that makes a launchd service
// running: write the wrapper script that sources the env file, write the
// plist, and hand off to ansible.builtin.launchd to load/start it. isUser
// selects the launchd domain (user vs system) — callers match this to the
// Play's Become setting (Become=false for user domain).
//
// launchd natively does not support EnvironmentFile, so we use a shell
// wrapper that sources the env file before exec-ing the binary.
func darwinLaunchdTasks(dirs darwinDirSet, data LaunchdPlistData) []ansible.Task {
	wrapperPath := dirs.baseDir + "/" + strings.TrimPrefix(data.Label, "com.livepeer.frameworks.") + "/run.sh"
	args := strings.Join(data.ProgramArgs, " ")
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
`, data.EnvFile, data.Program, args)

	plistData := data
	plistData.Program = "/bin/bash"
	plistData.ProgramArgs = []string{wrapperPath}
	plistData.EnvFile = ""
	plistContent, err := GenerateLaunchdPlist(plistData)
	if err != nil {
		// GenerateLaunchdPlist fails only on malformed LaunchdPlistData; the
		// callers pass static struct literals so this is effectively unreachable.
		plistContent = ""
	}
	plistPath := fmt.Sprintf("%s/%s.plist", dirs.plistDir, data.Label)

	return []ansible.Task{
		ansible.TaskCopy(wrapperPath, wrapperContent, ansible.CopyOpts{Mode: "0755"}),
		ansible.TaskCopy(plistPath, plistContent, ansible.CopyOpts{Mode: "0644"}),
		{
			Name:   "launchd load " + data.Label,
			Module: "ansible.builtin.launchd",
			Args: map[string]any{
				"name":    data.Label,
				"state":   "started",
				"enabled": true,
			},
		},
	}
}

// darwinPlaybook wraps the standard edge-darwin playbook-execution boilerplate.
// isUser=false selects Become=true (system domain / /Library/LaunchDaemons).
// isUser=true selects Become=false so Ansible runs as the SSH user, letting
// the launchd module address the user's GUI domain.
func (e *EdgeProvisioner) darwinPlaybook(ctx context.Context, host inventory.Host, name string, isUser bool, tasks []ansible.Task) error {
	playbook := &ansible.Playbook{
		Name:  name,
		Hosts: host.ExternalIP,
		Plays: []ansible.Play{
			{
				Name:        name,
				Hosts:       host.ExternalIP,
				Become:      !isUser,
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
			"ansible_ssh_private_key_file": e.sshPool.DefaultKeyPath(),
		},
	})
	result, execErr := e.executor.ExecutePlaybook(ctx, playbook, inv, ansible.ExecuteOptions{Verbose: true})
	if execErr != nil {
		return fmt.Errorf("%s failed: %w\nOutput: %s", name, execErr, result.Output)
	}
	if !result.Success {
		return fmt.Errorf("%s playbook failed\nOutput: %s", name, result.Output)
	}
	return nil
}

func (e *EdgeProvisioner) installEdgeTelemetryDarwin(ctx context.Context, host inventory.Host, config EdgeProvisionConfig, dirs darwinDirSet, isUser bool, remoteArch string) error {
	scrapeConfig, err := buildEdgeTelemetryScrapeConfig("native", config.NodeID)
	if err != nil {
		return err
	}
	artifact, err := resolveVMAgentArtifact(config.Version, "darwin", remoteArch, nil)
	if err != nil {
		return err
	}

	tokenPath := ""
	args := []string{
		"-httpListenAddr=:8430",
		"-promscrape.config=" + dirs.confDir + "/vmagent-edge.yml",
		"-remoteWrite.url=" + config.TelemetryURL,
	}

	tasks := []ansible.Task{
		mkdirTask(dirs.baseDir+"/vmagent-edge", "", "", "0755"),
		mkdirTask(dirs.logDir, "", "", "0755"),
		mkdirTask(dirs.confDir, "", "", "0755"),
		ansible.TaskCopy(dirs.confDir+"/vmagent-edge.yml", scrapeConfig, ansible.CopyOpts{Mode: "0644"}),
	}
	if strings.TrimSpace(config.TelemetryToken) != "" {
		tokenPath = dirs.confDir + "/telemetry/token"
		tasks = append(tasks,
			mkdirTask(dirs.confDir+"/telemetry", "", "", "0755"),
			ansible.TaskCopy(tokenPath, config.TelemetryToken+"\n", ansible.CopyOpts{Mode: "0600"}),
		)
		args = append(args, "-remoteWrite.bearerTokenFile="+tokenPath)
	}
	extractSentinel := ansible.ArtifactSentinel("/tmp", artifact.Checksum+artifact.URL+"-vmagent")
	installSentinel := ansible.ArtifactSentinel(dirs.baseDir+"/vmagent-edge", artifact.Checksum+artifact.URL)
	tasks = append(tasks,
		// Tarball left in /tmp; version-keyed sentinels rotate on a pin bump
		// so both extraction and install rerun.
		ansible.TaskGetURL(artifact.URL, "/tmp/vmagent-edge.tar.gz", artifact.Checksum),
		ansible.TaskUnarchive("/tmp/vmagent-edge.tar.gz", "/tmp", extractSentinel, ansible.UnarchiveOpts{}),
		ansible.TaskShell("touch "+extractSentinel, ansible.ShellOpts{Creates: extractSentinel}),
		// vmutils tarball unpacks vmagent-prod (and peers) directly at the
		// extraction root. Copy the single binary we need to its run path.
		ansible.TaskShell(
			fmt.Sprintf("install -m 0755 /tmp/vmagent-prod %s/vmagent-edge/vmagent && touch %s",
				dirs.baseDir, installSentinel),
			ansible.ShellOpts{Creates: installSentinel},
		),
	)
	tasks = append(tasks, darwinLaunchdTasks(dirs, LaunchdPlistData{
		Label:       "com.livepeer.frameworks.vmagent-edge",
		Description: "FrameWorks vmagent (edge telemetry)",
		Program:     dirs.baseDir + "/vmagent-edge/vmagent",
		ProgramArgs: args,
		WorkingDir:  dirs.baseDir + "/vmagent-edge",
		RunAtLoad:   true,
		KeepAlive:   true,
		LogPath:     dirs.logDir + "/com.livepeer.frameworks.vmagent-edge.log",
		ErrorPath:   dirs.logDir + "/com.livepeer.frameworks.vmagent-edge.err",
	})...)
	return e.darwinPlaybook(ctx, host, "Install edge telemetry (darwin)", isUser, tasks)
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
	domainLabel := "system"
	if isUser {
		domainLabel = "user"
	}
	fmt.Printf("  launchd domain: %s\n", domainLabel)

	// (a) Base directory prep + optional CA bundle + plist dir in one playbook.
	fmt.Println("  creating macOS directories...")
	prepTasks := []ansible.Task{
		mkdirTask(dirs.baseDir+"/mistserver", "", "", "0755"),
		mkdirTask(dirs.baseDir+"/helmsman", "", "", "0755"),
		mkdirTask(dirs.baseDir+"/caddy", "", "", "0755"),
		mkdirTask(dirs.baseDir+"/edge", "", "", "0755"),
		mkdirTask(dirs.logDir, "", "", "0755"),
		mkdirTask(dirs.confDir, "", "", "0755"),
		mkdirTask(dirs.confDir+"/certs", "", "", "0755"),
		mkdirTask(dirs.confDir+"/pki", "", "", "0755"),
		mkdirTask(dirs.plistDir, "", "", "0755"),
	}
	if caPath := config.helmsmanCAPath(remoteOS); strings.TrimSpace(config.CABundlePEM) != "" && caPath != "" {
		prepTasks = append(prepTasks,
			ansible.TaskCopy(caPath, config.CABundlePEM, ansible.CopyOpts{Mode: "0644"}),
		)
		fmt.Printf("  gRPC CA bundle will be staged at %s\n", caPath)
	}
	if err := e.darwinPlaybook(ctx, host, "Edge macOS prep (dirs + CA)", isUser, prepTasks); err != nil {
		return fmt.Errorf("failed to prep macOS host: %w", err)
	}

	// (b) MistServer
	fmt.Println("  installing MistServer...")
	mistPass, err := e.installDarwinMistServer(ctx, host, manifest, arch, dirs, isUser)
	if err != nil {
		return fmt.Errorf("mistserver install failed: %w", err)
	}

	// (c) Helmsman
	fmt.Println("  installing Helmsman...")
	if err = e.installDarwinHelmsman(ctx, host, config, vars, manifest, remoteOS, remoteArch, dirs, isUser, mistPass); err != nil {
		return fmt.Errorf("helmsman install failed: %w", err)
	}

	// (d) Caddy
	fmt.Println("  installing Caddy...")
	if err = e.installDarwinCaddy(ctx, host, vars, manifest, arch, remoteOS, remoteArch, dirs, isUser); err != nil {
		return fmt.Errorf("caddy install failed: %w", err)
	}

	if strings.TrimSpace(config.TelemetryURL) != "" {
		fmt.Println("  installing edge telemetry agent...")
		if err = e.installEdgeTelemetryDarwin(ctx, host, config, dirs, isUser, remoteArch); err != nil {
			return fmt.Errorf("edge telemetry install failed: %w", err)
		}
	}

	// (e) Write .edge.env for mode detection.
	fmt.Println("  writing .edge.env for mode detection...")
	envTmpDir, err := os.MkdirTemp("", "edge-native-env-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(envTmpDir)
	if err = templates.WriteEdgeTemplates(envTmpDir, vars, true); err != nil {
		return fmt.Errorf("failed to render .edge.env: %w", err)
	}
	edgeEnvBytes, err := os.ReadFile(filepath.Join(envTmpDir, ".edge.env"))
	if err != nil {
		return fmt.Errorf("failed to read rendered .edge.env: %w", err)
	}
	envTasks := []ansible.Task{
		ansible.TaskCopy(dirs.baseDir+"/edge/.edge.env", string(edgeEnvBytes), ansible.CopyOpts{Mode: "0600"}),
	}
	if err := e.darwinPlaybook(ctx, host, "Write edge .edge.env (darwin)", isUser, envTasks); err != nil {
		return fmt.Errorf("failed to write .edge.env: %w", err)
	}

	// Each install* step above used ansible.builtin.launchd to load+start its
	// own service, so no separate launchctl orchestration is needed here.
	fmt.Println("  edge stack running on macOS (launchd)")
	return nil
}

func (e *EdgeProvisioner) installDarwinMistServer(ctx context.Context, host inventory.Host, manifest *gitops.Manifest, arch string, dirs darwinDirSet, isUser bool) (string, error) {
	mistPass, err := generateEdgePassword()
	if err != nil {
		return "", err
	}

	var (
		binaryURL string
		checksum  string
	)
	if manifest != nil {
		if dep := manifest.GetExternalDependency("mistserver"); dep != nil {
			if bin := dep.GetBinary(arch); bin != nil {
				binaryURL = bin.URL
				checksum = bin.Checksum
			}
		}
	}
	if binaryURL == "" {
		return "", fmt.Errorf("MistServer binary URL not available for %s (ensure darwin builds exist in mistserver releases)", arch)
	}

	envContent := "# MistServer environment\nMIST_DEBUG=3\n"
	// Tarball stays in /tmp so get_url cache-hits via checksum on rerun;
	// version-keyed sentinel triggers re-extract on a pinned-version bump.
	installSentinel := ansible.ArtifactSentinel(dirs.baseDir+"/mistserver", checksum+binaryURL)
	tasks := []ansible.Task{
		mkdirTask(dirs.baseDir+"/mistserver", "", "", "0755"),
		mkdirTask(dirs.confDir, "", "", "0755"),
		mkdirTask(dirs.logDir, "", "", "0755"),
		ansible.TaskCopy(dirs.confDir+"/mistserver.env", envContent, ansible.CopyOpts{Mode: "0644"}),
		ansible.TaskGetURL(binaryURL, "/tmp/mistserver.tar.gz", checksum),
		ansible.TaskUnarchive("/tmp/mistserver.tar.gz", dirs.baseDir+"/mistserver",
			installSentinel, ansible.UnarchiveOpts{}),
		ansible.TaskShell("touch "+installSentinel, ansible.ShellOpts{Creates: installSentinel}),
		{
			Name:   "ensure MistServer is executable",
			Module: "ansible.builtin.file",
			Args:   map[string]any{"path": dirs.baseDir + "/mistserver/MistServer", "mode": "0755"},
		},
	}
	tasks = append(tasks, darwinLaunchdTasks(dirs, LaunchdPlistData{
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
	})...)
	if err := e.darwinPlaybook(ctx, host, "Install MistServer (darwin)", isUser, tasks); err != nil {
		return "", err
	}
	return mistPass, nil
}

func (e *EdgeProvisioner) installDarwinHelmsman(ctx context.Context, host inventory.Host, config EdgeProvisionConfig, vars templates.EdgeVars, manifest *gitops.Manifest, remoteOS, remoteArch string, dirs darwinDirSet, isUser bool, mistPass string) error {
	var (
		binaryURL string
		checksum  string
	)
	if manifest != nil {
		svcInfo, err := manifest.GetServiceInfo("helmsman")
		if err == nil {
			if bin, binErr := svcInfo.GetBinary(remoteOS, remoteArch); binErr == nil {
				binaryURL = bin.URL
				checksum = bin.Checksum
			}
		}
	}
	if binaryURL == "" {
		return fmt.Errorf("helmsman binary URL not available for %s/%s", remoteOS, remoteArch)
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
		"MIST_API_USERNAME=frameworks",
		fmt.Sprintf("MIST_API_PASSWORD=%s", mistPass),
	}
	if vars.GRPCTLSCAPath != "" {
		envLines = append(envLines, fmt.Sprintf("GRPC_TLS_CA_PATH=%s", vars.GRPCTLSCAPath))
	}
	envContent := strings.Join(envLines, "\n") + "\n"

	// macOS ships ditto and unzip out of the box, so Ansible's unarchive can
	// handle both .tar.gz and .zip release assets without a prereq install.
	// Version-keyed sentinel rotates when checksum or URL changes.
	installSentinel := ansible.ArtifactSentinel(dirs.baseDir+"/helmsman", checksum+binaryURL)
	tasks := []ansible.Task{
		mkdirTask(dirs.baseDir+"/helmsman", "", "", "0755"),
		mkdirTask(dirs.confDir, "", "", "0755"),
		mkdirTask(dirs.logDir, "", "", "0755"),
		ansible.TaskCopy(dirs.confDir+"/helmsman.env", envContent, ansible.CopyOpts{Mode: "0600"}),
	}
	tasks = append(tasks,
		ansible.TaskGetURL(binaryURL, "/tmp/helmsman.asset", checksum),
		ansible.TaskUnarchive("/tmp/helmsman.asset", dirs.baseDir+"/helmsman",
			installSentinel, ansible.UnarchiveOpts{}),
		ansible.TaskShell(
			fmt.Sprintf("mv %[1]s/helmsman/frameworks-helmsman-* %[1]s/helmsman/helmsman 2>/dev/null || "+
				"mv %[1]s/helmsman/frameworks %[1]s/helmsman/helmsman 2>/dev/null || true; "+
				"chmod +x %[1]s/helmsman/helmsman; touch %[2]s", dirs.baseDir, installSentinel),
			ansible.ShellOpts{Creates: installSentinel},
		),
	)
	tasks = append(tasks, darwinLaunchdTasks(dirs, LaunchdPlistData{
		Label:       "com.livepeer.frameworks.helmsman",
		Description: "FrameWorks Helmsman (edge sidecar)",
		Program:     dirs.baseDir + "/helmsman/helmsman",
		WorkingDir:  dirs.baseDir + "/helmsman",
		EnvFile:     dirs.confDir + "/helmsman.env",
		RunAtLoad:   true,
		KeepAlive:   true,
		LogPath:     dirs.logDir + "/com.livepeer.frameworks.helmsman.log",
		ErrorPath:   dirs.logDir + "/com.livepeer.frameworks.helmsman.err",
	})...)
	return e.darwinPlaybook(ctx, host, "Install Helmsman (darwin)", isUser, tasks)
}

func (e *EdgeProvisioner) installDarwinCaddy(ctx context.Context, host inventory.Host, vars templates.EdgeVars, manifest *gitops.Manifest, arch, remoteOS, remoteArch string, dirs darwinDirSet, isUser bool) error {
	var binaryURL string
	var checksum string
	if manifest != nil {
		if dep := manifest.GetExternalDependency("caddy"); dep != nil {
			if bin := dep.GetBinary(arch); bin != nil {
				binaryURL = bin.URL
				checksum = bin.Checksum
			}
		}
		if binaryURL == "" {
			svcInfo, err := manifest.GetServiceInfo("caddy")
			if err == nil {
				if bin, binErr := svcInfo.GetBinary(remoteOS, remoteArch); binErr == nil {
					binaryURL = bin.URL
					checksum = bin.Checksum
				}
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

	tmpDir, err := os.MkdirTemp("", "edge-caddy-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmpDir)
	if err := templates.WriteEdgeTemplates(tmpDir, vars, true); err != nil {
		return fmt.Errorf("failed to render caddyfile: %w", err)
	}
	caddyfileBytes, err := os.ReadFile(filepath.Join(tmpDir, "Caddyfile"))
	if err != nil {
		return fmt.Errorf("failed to read rendered caddyfile: %w", err)
	}

	envContent := fmt.Sprintf("# Caddy edge environment\nCADDY_EMAIL=%s\n", vars.AcmeEmail)
	installSentinel := ansible.ArtifactSentinel(dirs.baseDir+"/caddy", checksum+binaryURL)
	tasks := []ansible.Task{
		mkdirTask(dirs.baseDir+"/caddy", "", "", "0755"),
		mkdirTask(dirs.confDir, "", "", "0755"),
		mkdirTask(dirs.logDir, "", "", "0755"),
		mkdirTask(caddyDataDir, "", "", "0755"),
		ansible.TaskCopy(dirs.confDir+"/Caddyfile", string(caddyfileBytes), ansible.CopyOpts{Mode: "0644"}),
		ansible.TaskCopy(dirs.confDir+"/caddy.env", envContent, ansible.CopyOpts{Mode: "0600"}),
		// Tarball stays in /tmp so get_url cache-hits on rerun via checksum;
		// version-keyed sentinel triggers re-extract on a pin bump.
		ansible.TaskGetURL(binaryURL, "/tmp/caddy.tar.gz", checksum),
		ansible.TaskUnarchive("/tmp/caddy.tar.gz", dirs.baseDir+"/caddy",
			installSentinel, ansible.UnarchiveOpts{}),
		ansible.TaskShell("touch "+installSentinel, ansible.ShellOpts{Creates: installSentinel}),
		{
			Name:   "ensure Caddy binary is executable",
			Module: "ansible.builtin.file",
			Args:   map[string]any{"path": dirs.baseDir + "/caddy/caddy", "mode": "0755"},
		},
	}
	tasks = append(tasks, darwinLaunchdTasks(dirs, LaunchdPlistData{
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
	})...)
	return e.darwinPlaybook(ctx, host, "Install Caddy (darwin)", isUser, tasks)
}

// installNativeLinux is the original Linux systemd installation path.
func (e *EdgeProvisioner) installNativeLinux(ctx context.Context, host inventory.Host, config EdgeProvisionConfig, vars templates.EdgeVars, manifest *gitops.Manifest, arch, remoteOS, remoteArch string) error {
	// (0) Create frameworks user/group for MistServer and Helmsman systemd units
	fmt.Println("  creating frameworks user/group...")
	userPrepTasks := []ansible.Task{
		{
			Name:   "ensure frameworks group exists",
			Module: "ansible.builtin.group",
			Args:   map[string]any{"name": "frameworks", "system": true, "state": "present"},
		},
		{
			Name:   "ensure frameworks user exists",
			Module: "ansible.builtin.user",
			Args: map[string]any{
				"name":   "frameworks",
				"group":  "frameworks",
				"system": true,
				"shell":  "/usr/sbin/nologin",
				"state":  "present",
			},
		},
		mkdirTask("/opt/frameworks/mistserver", "frameworks", "frameworks", "0755"),
		mkdirTask("/opt/frameworks/helmsman", "frameworks", "frameworks", "0755"),
		mkdirTask("/etc/frameworks", "frameworks", "frameworks", "0755"),
		mkdirTask("/etc/frameworks/pki", "frameworks", "frameworks", "0755"),
		mkdirTask("/var/log/frameworks", "frameworks", "frameworks", "0755"),
	}
	prepPlaybook := &ansible.Playbook{
		Name:  "Prepare edge native (user, group, dirs)",
		Hosts: host.ExternalIP,
		Plays: []ansible.Play{
			{
				Name:        "Edge user prep",
				Hosts:       host.ExternalIP,
				Become:      true,
				GatherFacts: false,
				Tasks:       userPrepTasks,
			},
		},
	}
	prepInv := ansible.NewInventory()
	prepInv.AddHost(&ansible.InventoryHost{
		Name:    host.ExternalIP,
		Address: host.ExternalIP,
		Vars: map[string]string{
			"ansible_user":                 host.User,
			"ansible_ssh_private_key_file": e.sshPool.DefaultKeyPath(),
		},
	})
	prepResult, prepErr := e.executor.ExecutePlaybook(ctx, prepPlaybook, prepInv, ansible.ExecuteOptions{Verbose: false})
	if prepErr != nil {
		return fmt.Errorf("failed to create frameworks user: %w\nOutput: %s", prepErr, prepResult.Output)
	}
	if !prepResult.Success {
		return fmt.Errorf("frameworks user prep playbook failed\nOutput: %s", prepResult.Output)
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
	if err = e.installNativeHelmsman(ctx, host, config, vars, manifest, remoteOS, remoteArch, mistPass); err != nil {
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

	// Each install_*Native* call above already runs daemon-reload, enable, and
	// start for its own systemd unit (and for the optional vmagent telemetry
	// agent), so no separate orchestration step is needed here.
	fmt.Println("  edge stack running (frameworks-mistserver, frameworks-helmsman, frameworks-caddy)")
	return nil
}

func (e *EdgeProvisioner) installNativeMistServer(ctx context.Context, host inventory.Host, manifest *gitops.Manifest, arch string) (string, error) {
	mistPass, err := generateEdgePassword()
	if err != nil {
		return "", err
	}

	var (
		binaryURL string
		checksum  string
	)
	if manifest != nil {
		if dep := manifest.GetExternalDependency("mistserver"); dep != nil {
			if bin := dep.GetBinary(arch); bin != nil {
				binaryURL = bin.URL
				checksum = bin.Checksum
			}
		}
	}
	if binaryURL == "" {
		return "", fmt.Errorf("MistServer binary URL not available (set --version to resolve from gitops, or provide binary URL in manifest)")
	}

	envContent := "# MistServer environment\nMIST_DEBUG=3\n"
	unitContent := ansible.RenderSystemdUnit(ansible.SystemdUnitSpec{
		Description:     "FrameWorks MistServer (edge media server)",
		After:           []string{"network-online.target"},
		Wants:           []string{"network-online.target"},
		User:            "frameworks",
		WorkingDir:      "/opt/frameworks/mistserver",
		EnvironmentFile: "/etc/frameworks/mistserver.env",
		ExecStart:       fmt.Sprintf("/opt/frameworks/mistserver/MistServer -a frameworks:%s", mistPass),
		Restart:         "always",
		RestartSec:      5,
		LimitNOFILE:     "1048576",
	})

	installSentinel := ansible.ArtifactSentinel("/opt/frameworks/mistserver", checksum+binaryURL)
	tasks := []ansible.Task{
		mkdirTask("/etc/frameworks", "frameworks", "frameworks", "0755"),
		mkdirTask("/opt/frameworks/mistserver", "frameworks", "frameworks", "0755"),
		ansible.TaskCopy("/etc/frameworks/mistserver.env", envContent, ansible.CopyOpts{Owner: "frameworks", Group: "frameworks", Mode: "0644"}),
		// Tarball stays in /tmp for get_url cache-hit; version-keyed sentinel
		// triggers re-extract on a pinned-version bump. MistServer tarball lays
		// out files directly (no top-level wrapper), so no --strip-components.
		ansible.TaskGetURL(binaryURL, "/tmp/mistserver.tar.gz", checksum),
		ansible.TaskUnarchive("/tmp/mistserver.tar.gz", "/opt/frameworks/mistserver",
			installSentinel,
			ansible.UnarchiveOpts{Owner: "frameworks", Group: "frameworks"}),
		ansible.TaskShell("touch "+installSentinel+" && chown frameworks:frameworks "+installSentinel,
			ansible.ShellOpts{Creates: installSentinel}),
		{
			Name:   "ensure MistServer is executable",
			Module: "ansible.builtin.file",
			Args:   map[string]any{"path": "/opt/frameworks/mistserver/MistServer", "mode": "0755", "owner": "frameworks", "group": "frameworks"},
		},
		ansible.TaskCopy("/etc/systemd/system/frameworks-mistserver.service", unitContent, ansible.CopyOpts{Mode: "0644"}),
		ansible.TaskSystemdService("frameworks-mistserver", ansible.SystemdOpts{
			State:        "started",
			Enabled:      ansible.BoolPtr(true),
			DaemonReload: true,
		}),
	}

	playbook := &ansible.Playbook{
		Name:  "Install MistServer (edge linux)",
		Hosts: host.ExternalIP,
		Plays: []ansible.Play{
			{
				Name:        "Install MistServer",
				Hosts:       host.ExternalIP,
				Become:      true,
				GatherFacts: true,
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
			"ansible_ssh_private_key_file": e.sshPool.DefaultKeyPath(),
		},
	})

	result, execErr := e.executor.ExecutePlaybook(ctx, playbook, inv, ansible.ExecuteOptions{Verbose: true})
	if execErr != nil {
		return "", fmt.Errorf("MistServer install failed: %w\nOutput: %s", execErr, result.Output)
	}
	if !result.Success {
		return "", fmt.Errorf("MistServer install playbook failed\nOutput: %s", result.Output)
	}
	return mistPass, nil
}

func (e *EdgeProvisioner) installNativeHelmsman(ctx context.Context, host inventory.Host, config EdgeProvisionConfig, vars templates.EdgeVars, manifest *gitops.Manifest, remoteOS, remoteArch, mistPass string) error {
	var (
		binaryURL string
		checksum  string
	)
	if manifest != nil {
		svcInfo, err := manifest.GetServiceInfo("helmsman")
		if err == nil {
			if bin, binErr := svcInfo.GetBinary(remoteOS, remoteArch); binErr == nil {
				binaryURL = bin.URL
				checksum = bin.Checksum
			}
		}
	}
	if binaryURL == "" {
		return fmt.Errorf("helmsman binary URL not available (set --version to resolve from gitops)")
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
		"MIST_API_USERNAME=frameworks",
		fmt.Sprintf("MIST_API_PASSWORD=%s", mistPass),
	}
	if vars.GRPCTLSCAPath != "" {
		envLines = append(envLines, fmt.Sprintf("GRPC_TLS_CA_PATH=%s", vars.GRPCTLSCAPath))
	}
	envContent := strings.Join(envLines, "\n") + "\n"

	unitContent := ansible.RenderSystemdUnit(ansible.SystemdUnitSpec{
		Description:     "FrameWorks Helmsman (edge sidecar)",
		After:           []string{"network-online.target", "frameworks-mistserver.service"},
		Wants:           []string{"network-online.target"},
		User:            "frameworks",
		WorkingDir:      "/opt/frameworks/helmsman",
		EnvironmentFile: "/etc/frameworks/helmsman.env",
		ExecStart:       "/opt/frameworks/helmsman/helmsman",
		Restart:         "always",
		RestartSec:      5,
	})

	// Helmsman's release asset is a tar.gz or zip; Ansible's unarchive auto-
	// detects both but needs `unzip` for zip archives. The filename pattern
	// inside the archive varies (frameworks-helmsman-*, helmsman, frameworks),
	// so a post-extract move picks whichever shipped. Version-keyed sentinel
	// rotates when either checksum or URL changes.
	isZip := strings.HasSuffix(binaryURL, ".zip")
	installSentinel := ansible.ArtifactSentinel("/opt/frameworks/helmsman", checksum+binaryURL)
	tasks := []ansible.Task{
		mkdirTask("/etc/frameworks", "frameworks", "frameworks", "0755"),
		mkdirTask("/opt/frameworks/helmsman", "frameworks", "frameworks", "0755"),
		ansible.TaskCopy("/etc/frameworks/helmsman.env", envContent, ansible.CopyOpts{Owner: "frameworks", Group: "frameworks", Mode: "0600"}),
	}
	if isZip {
		tasks = append(tasks, ansible.TaskPackage("unzip", ansible.PackagePresent))
	}
	tasks = append(tasks,
		ansible.TaskGetURL(binaryURL, "/tmp/helmsman.asset", checksum),
		ansible.TaskUnarchive("/tmp/helmsman.asset", "/opt/frameworks/helmsman",
			installSentinel,
			ansible.UnarchiveOpts{Owner: "frameworks", Group: "frameworks"}),
		ansible.TaskShell(
			"mv /opt/frameworks/helmsman/frameworks-helmsman-* /opt/frameworks/helmsman/helmsman 2>/dev/null || "+
				"mv /opt/frameworks/helmsman/frameworks /opt/frameworks/helmsman/helmsman 2>/dev/null || true; "+
				"chmod +x /opt/frameworks/helmsman/helmsman; touch "+installSentinel,
			ansible.ShellOpts{Creates: installSentinel},
		),
		ansible.TaskCopy("/etc/systemd/system/frameworks-helmsman.service", unitContent, ansible.CopyOpts{Mode: "0644"}),
		ansible.TaskSystemdService("frameworks-helmsman", ansible.SystemdOpts{
			State:        "started",
			Enabled:      ansible.BoolPtr(true),
			DaemonReload: true,
		}),
	)

	playbook := &ansible.Playbook{
		Name:  "Install Helmsman (edge linux)",
		Hosts: host.ExternalIP,
		Plays: []ansible.Play{
			{
				Name:        "Install Helmsman",
				Hosts:       host.ExternalIP,
				Become:      true,
				GatherFacts: true,
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
			"ansible_ssh_private_key_file": e.sshPool.DefaultKeyPath(),
		},
	})
	result, execErr := e.executor.ExecutePlaybook(ctx, playbook, inv, ansible.ExecuteOptions{Verbose: true})
	if execErr != nil {
		return fmt.Errorf("helmsman install failed: %w\nOutput: %s", execErr, result.Output)
	}
	if !result.Success {
		return fmt.Errorf("helmsman install playbook failed\nOutput: %s", result.Output)
	}
	return nil
}

func (e *EdgeProvisioner) installNativeCaddy(ctx context.Context, host inventory.Host, vars templates.EdgeVars, manifest *gitops.Manifest, arch, remoteOS, remoteArch string) error {
	var (
		binaryURL string
		checksum  string
	)
	if manifest != nil {
		if dep := manifest.GetExternalDependency("caddy"); dep != nil {
			if bin := dep.GetBinary(arch); bin != nil {
				binaryURL = bin.URL
				checksum = bin.Checksum
			}
		}
		if binaryURL == "" {
			svcInfo, err := manifest.GetServiceInfo("caddy")
			if err == nil {
				if bin, binErr := svcInfo.GetBinary(remoteOS, remoteArch); binErr == nil {
					binaryURL = bin.URL
					checksum = bin.Checksum
				}
			}
		}
	}
	if binaryURL == "" {
		return fmt.Errorf("caddy binary URL not available (set --version to resolve from gitops)")
	}

	// Caddyfile is rendered locally (owned by the templates package), then
	// shipped to the host inline via TaskCopy.
	tmpDir, err := os.MkdirTemp("", "edge-caddy-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmpDir)
	if err := templates.WriteEdgeTemplates(tmpDir, vars, true); err != nil {
		return fmt.Errorf("failed to render caddyfile: %w", err)
	}
	caddyfileBytes, err := os.ReadFile(filepath.Join(tmpDir, "Caddyfile"))
	if err != nil {
		return fmt.Errorf("failed to read rendered caddyfile: %w", err)
	}

	envContent := fmt.Sprintf("# Caddy edge environment\nCADDY_EMAIL=%s\n", vars.AcmeEmail)
	unitContent := ansible.RenderSystemdUnit(ansible.SystemdUnitSpec{
		Description:     "FrameWorks Caddy Reverse Proxy (edge)",
		After:           []string{"network-online.target", "frameworks-mistserver.service", "frameworks-helmsman.service"},
		Wants:           []string{"network-online.target"},
		User:            "caddy",
		WorkingDir:      "/etc/caddy",
		EnvironmentFile: "/etc/frameworks/caddy.env",
		ExecStart:       "/opt/frameworks/caddy/caddy run --config /etc/caddy/Caddyfile",
		Restart:         "always",
		RestartSec:      5,
	})

	installSentinel := ansible.ArtifactSentinel("/opt/frameworks/caddy", checksum+binaryURL)
	tasks := []ansible.Task{
		mkdirTask("/opt/frameworks/caddy", "root", "root", "0755"),
		mkdirTask("/etc/frameworks", "frameworks", "frameworks", "0755"),
		{
			Name:   "ensure caddy group exists",
			Module: "ansible.builtin.group",
			Args:   map[string]any{"name": "caddy", "system": true, "state": "present"},
		},
		{
			Name:   "ensure caddy user exists",
			Module: "ansible.builtin.user",
			Args: map[string]any{
				"name":   "caddy",
				"group":  "caddy",
				"system": true,
				"shell":  "/usr/sbin/nologin",
				"state":  "present",
			},
		},
		mkdirTask("/etc/caddy", "caddy", "caddy", "0755"),
		mkdirTask("/var/lib/caddy", "caddy", "caddy", "0755"),
		// Tarball stays in /tmp for get_url cache-hit; version-keyed sentinel
		// triggers re-extract on a pinned-version bump.
		ansible.TaskGetURL(binaryURL, "/tmp/caddy.tar.gz", checksum),
		ansible.TaskUnarchive("/tmp/caddy.tar.gz", "/opt/frameworks/caddy",
			installSentinel, ansible.UnarchiveOpts{}),
		ansible.TaskShell("touch "+installSentinel, ansible.ShellOpts{Creates: installSentinel}),
		{
			Name:   "ensure Caddy binary is executable",
			Module: "ansible.builtin.file",
			Args:   map[string]any{"path": "/opt/frameworks/caddy/caddy", "mode": "0755"},
		},
		ansible.TaskCopy("/etc/caddy/Caddyfile", string(caddyfileBytes), ansible.CopyOpts{Owner: "caddy", Group: "caddy", Mode: "0644"}),
		ansible.TaskCopy("/etc/frameworks/caddy.env", envContent, ansible.CopyOpts{Owner: "caddy", Group: "caddy", Mode: "0600"}),
		ansible.TaskCopy("/etc/systemd/system/frameworks-caddy.service", unitContent, ansible.CopyOpts{Mode: "0644"}),
		ansible.TaskSystemdService("frameworks-caddy", ansible.SystemdOpts{
			State:        "started",
			Enabled:      ansible.BoolPtr(true),
			DaemonReload: true,
		}),
	}
	// Edge cert/key are written to /etc/frameworks/certs earlier in the pipeline
	// as root:root; caddy needs read access to them.
	if vars.CertPath != "" {
		tasks = append(tasks,
			ansible.Task{
				Name:   "chown edge cert for caddy",
				Module: "ansible.builtin.file",
				Args:   map[string]any{"path": "/etc/frameworks/certs/cert.pem", "owner": "caddy", "group": "caddy"},
			},
			ansible.Task{
				Name:   "chown edge key for caddy",
				Module: "ansible.builtin.file",
				Args:   map[string]any{"path": "/etc/frameworks/certs/key.pem", "owner": "caddy", "group": "caddy"},
			},
		)
	}

	playbook := &ansible.Playbook{
		Name:  "Install Caddy (edge linux)",
		Hosts: host.ExternalIP,
		Plays: []ansible.Play{
			{
				Name:        "Install Caddy",
				Hosts:       host.ExternalIP,
				Become:      true,
				GatherFacts: true,
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
			"ansible_ssh_private_key_file": e.sshPool.DefaultKeyPath(),
		},
	})
	result, execErr := e.executor.ExecutePlaybook(ctx, playbook, inv, ansible.ExecuteOptions{Verbose: true})
	if execErr != nil {
		return fmt.Errorf("caddy install failed: %w\nOutput: %s", execErr, result.Output)
	}
	if !result.Success {
		return fmt.Errorf("caddy install playbook failed\nOutput: %s", result.Output)
	}
	return nil
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

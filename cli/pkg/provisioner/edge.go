package provisioner

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"fmt"
	"net/http"
	"strings"
	"time"

	"frameworks/cli/internal/preflight"
	"frameworks/cli/pkg/detect"
	"frameworks/cli/pkg/health"
	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/ssh"
)

// DarwinDomain selects the launchd domain for macOS service management.
type DarwinDomain string

const (
	DomainSystem DarwinDomain = "system" // /Library/LaunchDaemons — root, survives logout
	DomainUser   DarwinDomain = "user"   // ~/Library/LaunchAgents — no admin, dies on logout
)

// EdgeProvisioner drives the edge provisioning pipeline. Install / configure /
// service / validate are delegated to the frameworks.infra.edge Ansible role
// (see edge_role.go). Preflight and post-apply HTTPS verification stay
// Go-side so operators see them in the same command output as the role run.
type EdgeProvisioner struct {
	*BaseProvisioner
}

func NewEdgeProvisioner(pool *ssh.Pool) *EdgeProvisioner {
	return &EdgeProvisioner{BaseProvisioner: NewBaseProvisioner("edge", pool)}
}

// EdgeProvisionConfig carries everything the edge role needs plus the Go-side
// pipeline controls (preflight skip, HTTPS verify timeout, darwin domain).
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
	NodeID          string
	CertPEM         string
	KeyPEM          string
	CABundlePEM     string
	TelemetryURL    string
	TelemetryToken  string

	SkipPreflight bool
	ApplyTuning   bool
	FetchCert     bool

	Timeout      time.Duration
	Force        bool
	Version      string
	DarwinDomain DarwinDomain

	// mistPassword is lazily populated by mistAPIPassword() so a single
	// Provision invocation sees one consistent MIST_API_PASSWORD across
	// mistserver (-a) and helmsman (env var).
	mistPassword string
}

// generateEdgePassword returns a random 32-char hex string used as the
// MistServer API password shared between mistserver and helmsman.
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

// Provision runs the edge pipeline. Steps:
//
//	[1] preflight (direct SSH probes — kept Go-side for fast-fail messages)
//	[2] tuning   (routed into the role's configure tag via edge_apply_tuning)
//	[3] registration (no-op — caller handles via Bridge/Foghorn)
//	[4] certs   (post-enrollment via ConfigSeed — no-op here)
//	[5-6] install + start (frameworks.infra.edge role, mode + OS aware)
//	[7] HTTPS verify (direct HTTP probe of /health)
func (e *EdgeProvisioner) Provision(ctx context.Context, host inventory.Host, config EdgeProvisionConfig) error {
	mode := config.resolvedMode()

	remoteOS, remoteArch, err := e.DetectRemoteArch(ctx, host)
	if err != nil {
		return fmt.Errorf("failed to detect remote OS: %w", err)
	}

	if !config.SkipPreflight {
		fmt.Printf("[1/7] Running preflight checks on %s...\n", host.ExternalIP)
		if err := e.runPreflight(ctx, host, mode); err != nil {
			return fmt.Errorf("preflight failed: %w", err)
		}
	} else {
		fmt.Println("[1/7] Skipping preflight checks")
	}

	switch {
	case remoteOS == "darwin" && config.ApplyTuning:
		fmt.Println("[2/7] Skipping sysctl tuning (macOS)")
	case config.ApplyTuning:
		fmt.Println("[2/7] Tuning will be applied by the edge role")
	default:
		fmt.Println("[2/7] Skipping sysctl tuning")
	}

	fmt.Println("[3/7] Registration handled by caller")
	fmt.Println("[4/7] TLS certificates will be delivered after enrollment via ConfigSeed")

	if remoteOS == "darwin" && mode != "native" {
		return fmt.Errorf("unsupported mode for darwin: %s (only native)", mode)
	}
	fmt.Printf("[5-6/7] Installing edge stack (%s, %s)...\n", mode, remoteOS)
	if err := runEdgeRole(ctx, e.sshPool, host, &config, remoteOS, remoteArch); err != nil {
		return fmt.Errorf("edge role apply failed: %w", err)
	}

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

// runPreflight does host-readiness checks over SSH before any playbook runs.
// Direct SSH probes answer "is docker available / is the port free / is there
// disk space?" faster and with clearer operator-facing messages than a full
// Ansible play would.
func (e *EdgeProvisioner) runPreflight(ctx context.Context, host inventory.Host, mode string) error {
	remoteOS, _, err := e.DetectRemoteArch(ctx, host)
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

// verifyHTTPS polls the edge domain's /health endpoint until it returns 200
// or the timeout elapses. The endpoint is self-signed during bootstrap so
// InsecureSkipVerify is intentional.
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

// Detect reports whether an edge stack is running on the host. Checks docker
// compose first, then systemd (Linux), then launchd (macOS, both domains).
// Stays Go-side because it's observed-state only and needs to answer quickly
// without bringing up an Ansible subprocess.
func (e *EdgeProvisioner) Detect(ctx context.Context, host inventory.Host) (*detect.ServiceState, error) {
	result, err := e.RunCommand(ctx, host, "docker compose -f /opt/frameworks/edge/docker-compose.yml ps --format json 2>/dev/null")
	if err == nil && result.ExitCode == 0 && strings.TrimSpace(result.Stdout) != "" {
		return &detect.ServiceState{
			Exists:   true,
			Running:  true,
			Metadata: map[string]string{"mode": "docker"},
		}, nil
	}

	result, err = e.RunCommand(ctx, host, "systemctl is-active frameworks-caddy frameworks-helmsman frameworks-mistserver 2>/dev/null")
	if err == nil && result.ExitCode == 0 {
		return &detect.ServiceState{
			Exists:   true,
			Running:  true,
			Metadata: map[string]string{"mode": "native"},
		}, nil
	}

	result, err = e.RunCommand(ctx, host, "launchctl print system/com.livepeer.frameworks.caddy 2>/dev/null")
	if err == nil && result.ExitCode == 0 {
		return &detect.ServiceState{
			Exists:   true,
			Running:  true,
			Metadata: map[string]string{"mode": "native"},
		}, nil
	}

	result, err = e.RunCommand(ctx, host, "launchctl print gui/$(id -u)/com.livepeer.frameworks.caddy 2>/dev/null")
	if err == nil && result.ExitCode == 0 {
		return &detect.ServiceState{
			Exists:   true,
			Running:  true,
			Metadata: map[string]string{"mode": "native"},
		}, nil
	}

	return &detect.ServiceState{Exists: false, Running: false}, nil
}

// Validate is a TCP probe of the edge's HTTPS listener. The full role-side
// validate (port wait for :443) runs as part of Provision via the role's
// validate tag; this method is what `edge status` / `edge doctor` use for
// a fast observed-state check.
func (e *EdgeProvisioner) Validate(ctx context.Context, host inventory.Host, config ServiceConfig) error {
	checker := &health.TCPChecker{Timeout: 5 * time.Second}
	result := checker.Check(host.ExternalIP, 443)
	if !result.OK {
		return fmt.Errorf("edge HTTPS port check failed: %s", result.Error)
	}
	return nil
}

// Initialize is a no-op for edge nodes — no one-shot bootstrap data
// (equivalent to databases/topics) is needed; Helmsman negotiates all
// runtime state post-enrollment.
func (e *EdgeProvisioner) Initialize(ctx context.Context, host inventory.Host, config ServiceConfig) error {
	return nil
}

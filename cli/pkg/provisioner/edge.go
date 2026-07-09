package provisioner

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"fmt"
	"net"
	"regexp"
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
// (see edge_role.go). Preflight stays Go-side so operators see fast-fail
// messages before the role runs. Public HTTPS verification is Go-side because
// the final readiness signal must cover the full Helmsman/Foghorn ConfigSeed
// bootstrap, not just local service startup.
type EdgeProvisioner struct {
	*BaseProvisioner
}

func NewEdgeProvisioner(pool *ssh.Pool) *EdgeProvisioner {
	return &EdgeProvisioner{BaseProvisioner: NewBaseProvisioner("edge", pool)}
}

// EdgeProvisionConfig carries everything the edge role needs plus the Go-side
// pipeline controls (preflight skip, HTTPS verify timeout, darwin domain).
type EdgeProvisionConfig struct {
	Mode string // "container" | "native" ("docker" is a deprecated alias for container)

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
	Capabilities    []string
	BandwidthMbps   int
	MaxTranscodes   int
	StorageBytes    uint64

	SkipPreflight bool
	ApplyTuning   bool
	FetchCert     bool
	DryRun        bool

	// AlreadyEnrolled marks a node whose prior provision completed: identity
	// comes from the remote install and no enrollment token is needed —
	// Foghorn resolves reconnecting nodes by fingerprint and ignores tokens.
	// ForceReenroll overrides that (wiped control-plane state) and also
	// re-renders the write-once bootstrap files on the host.
	AlreadyEnrolled bool
	ForceReenroll   bool

	Timeout       time.Duration
	Force         bool
	Version       string
	DarwinDomain  DarwinDomain
	BeforeInstall func(context.Context, *EdgeProvisionConfig) error

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
	if domain := strings.TrimSpace(c.NodeDomain); domain != "" {
		return domain
	}
	return strings.TrimSpace(c.PoolDomain)
}

func (c *EdgeProvisionConfig) verificationDomain() string {
	return c.primaryDomain()
}

func (c *EdgeProvisionConfig) resolvedMode() string {
	switch strings.ToLower(strings.TrimSpace(c.Mode)) {
	case "", "docker", "container":
		// "docker" is the deprecated name for the single-image container mode.
		return "container"
	case "native":
		return "native"
	default:
		return strings.ToLower(strings.TrimSpace(c.Mode))
	}
}

func (c *EdgeProvisionConfig) requireEnrollmentToken() error {
	if c.AlreadyEnrolled && !c.ForceReenroll {
		return nil
	}
	if strings.TrimSpace(c.EnrollmentToken) != "" {
		return nil
	}
	return fmt.Errorf("edge enrollment token is required before installing Helmsman; Foghorn rejects first boot without one")
}

// Provision runs the edge pipeline. Steps:
//
//	[1] preflight (direct SSH probes — kept Go-side for fast-fail messages)
//	[2] tuning   (frameworks.infra.node_tuning role, profile=edge)
//	[3] registration / enrollment hook (after host readiness, before install)
//	[4] certs   (post-enrollment via ConfigSeed — no-op here)
//	[5-6] install + start (frameworks.infra.edge role, mode + OS aware)
//	[7] public HTTPS verify after ConfigSeed activation
func (e *EdgeProvisioner) Provision(ctx context.Context, host inventory.Host, config EdgeProvisionConfig) error {
	mode := config.resolvedMode()
	if mode != "native" && mode != "container" {
		return fmt.Errorf("invalid edge mode %q (valid: container, native; 'docker' is a deprecated alias for container)", config.Mode)
	}
	if strings.TrimSpace(config.FoghornGRPCAddr) == "" {
		return fmt.Errorf("edge Foghorn gRPC address is required; use Bridge enrollment or a cluster manifest that can derive the target Foghorn endpoint")
	}
	// Both modes install pinned release artifacts (native tarballs or the
	// edge image digest), so an unpinned run gets the stable channel rather
	// than failing the happy path.
	if strings.TrimSpace(config.Version) == "" {
		config.Version = "stable"
		fmt.Println("No --version specified; installing from the stable release channel")
	}
	if config.DryRun {
		fmt.Println("Dry-run mode: remote preflight plus Ansible --check --diff; registration, ConfigSeed delivery, service start, and HTTPS verification are skipped")
	}

	fmt.Printf("Preparing remote host %s...\n", host.ExternalIP)
	remoteOS, remoteArch, err := e.DetectRemoteArch(ctx, host)
	if err != nil {
		return fmt.Errorf("failed to detect remote OS: %w", err)
	}
	fmt.Printf("  platform: %s/%s\n", remoteOS, remoteArch)

	fmt.Println("  ensuring Python for Ansible")
	if err := ensureRemoteAnsiblePython(ctx, e.sshPool, host, config.DryRun); err != nil {
		return err
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
		fmt.Println("[2/7] Skipping OS tuning (macOS)")
	case config.ApplyTuning:
		if config.DryRun {
			fmt.Println("[2/7] Checking OS tuning (node_tuning role, profile=edge)...")
		} else {
			fmt.Println("[2/7] Applying OS tuning (node_tuning role, profile=edge)...")
		}
		if err := runNodeTuningRole(ctx, e.sshPool, host, "edge", config.DryRun); err != nil {
			return fmt.Errorf("node tuning failed: %w", err)
		}
	default:
		fmt.Println("[2/7] Skipping OS tuning")
	}

	if config.BeforeInstall != nil {
		fmt.Println("[3/7] Running control-plane registration/enrollment")
		if err := config.BeforeInstall(ctx, &config); err != nil {
			return err
		}
	} else {
		fmt.Println("[3/7] No control-plane registration step")
	}
	fmt.Println("[4/7] TLS certificates will be delivered after enrollment via ConfigSeed")
	if !config.DryRun {
		if err := config.requireEnrollmentToken(); err != nil {
			return err
		}
	}

	if config.DryRun {
		fmt.Printf("[5-6/7] Checking edge stack (%s, %s)...\n", mode, remoteOS)
	} else {
		fmt.Printf("[5-6/7] Installing edge stack (%s, %s)...\n", mode, remoteOS)
	}
	if err := runEdgeRole(ctx, e.sshPool, host, &config, remoteOS, remoteArch, config.DryRun); err != nil {
		return fmt.Errorf("edge role apply failed: %w", err)
	}
	if config.DryRun {
		fmt.Println("[7/7] Skipping HTTPS verification in dry-run mode")
		return nil
	}

	domain := config.verificationDomain()
	if domain != "" {
		fmt.Printf("[7/7] Verifying HTTPS readiness at %s...\n", domain)
		timeout := config.Timeout
		if timeout == 0 {
			timeout = 3 * time.Minute
		}
		if err := e.verifyHTTPS(domain, host.ExternalIP, timeout); err != nil {
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

	if mode != "native" {
		result, dockerErr := e.RunCommand(ctx, host, "docker --version")
		if dockerErr != nil || result.ExitCode != 0 {
			if remoteOS == "darwin" {
				return fmt.Errorf("docker not installed (container mode on macOS requires Docker Desktop or OrbStack)")
			}
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
		if ownershipErr := e.checkEdgePortOwnership(ctx, host, remoteOS, mode, result.Stdout); ownershipErr != nil {
			return ownershipErr
		}
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

func (e *EdgeProvisioner) checkEdgePortOwnership(ctx context.Context, host inventory.Host, remoteOS, mode, listenerOutput string) error {
	listenerOutput = strings.TrimSpace(listenerOutput)
	if listenerOutput == "" {
		return nil
	}
	if remoteOS == "darwin" {
		// Docker Desktop's/OrbStack's port proxy holds published ports —
		// acceptable only when EVERY listener is docker-managed (mixed
		// ownership means some other process squats one of the ports) AND
		// the managed edge container is what published them. An unrelated
		// compose project on 80/443 must fail here, not later at compose up.
		if darwinListenersAllDockerManaged(listenerOutput) && e.edgeContainerStackRunning(ctx, host) {
			if mode == "native" {
				// The native install path migrates the managed container
				// stack (compose down + DEPLOY_MODE flip) — not a conflict.
				fmt.Println("  ports 80/443: held by the managed edge container stack; native install will migrate it")
			} else {
				fmt.Println("  ports 80/443: already held by the managed edge container via Docker Desktop/OrbStack; continuing")
			}
			return nil
		}
		if mode != "native" && darwinListenersAllDockerManaged(listenerOutput) {
			return fmt.Errorf("ports 80/443 are docker-published by a container that is not the managed edge stack:\n%s", listenerOutput)
		}
		return fmt.Errorf("ports 80/443 already in use:\n%s", listenerOutput)
	}

	nativePIDs := e.frameworksCaddyPIDs(ctx, host)
	nativeOwnsPorts := len(nativePIDs) > 0 && listenerPIDsSubset(listenerOutput, nativePIDs)
	containerRunning := e.edgeContainerStackRunning(ctx, host)

	if mode == "native" {
		if nativeOwnsPorts {
			fmt.Println("  ports 80/443: already held by managed frameworks-caddy; continuing")
			return nil
		}
		if containerRunning {
			// Not a conflict: the native install path migrates the managed
			// container stack (compose down + DEPLOY_MODE flip) before the
			// native services start. Failing here would make that
			// migration unreachable through normal provisioning.
			fmt.Println("  ports 80/443: held by the managed edge container stack; native install will migrate it")
			return nil
		}
	} else {
		// The single edge container uses host networking on Linux, so the
		// listener is caddy inside the container (no docker-proxy). Any
		// running managed edge container claims the ports legitimately;
		// legacy 3-container stacks show docker-proxy listeners.
		if containerRunning {
			fmt.Println("  ports 80/443: already held by managed edge container stack; continuing")
			return nil
		}
		if len(nativePIDs) > 0 {
			return fmt.Errorf("ports 80/443 are held while frameworks-caddy is running; requested container mode. Stop frameworks-caddy or provision with --mode native")
		}
		if listenerLooksDockerManaged(listenerOutput) {
			return fmt.Errorf("ports 80/443 are held by a docker-published container that is not the managed edge stack:\n%s", listenerOutput)
		}
	}

	return fmt.Errorf("ports 80/443 already in use by unmanaged process:\n%s", listenerOutput)
}

func (e *EdgeProvisioner) frameworksCaddyPIDs(ctx context.Context, host inventory.Host) map[string]struct{} {
	result, err := e.RunCommand(ctx, host, "systemctl show -p MainPID --value frameworks-caddy 2>/dev/null")
	if err != nil || result.ExitCode != 0 {
		return nil
	}
	pid := strings.TrimSpace(result.Stdout)
	if pid == "" || pid == "0" {
		return nil
	}
	return map[string]struct{}{pid: {}}
}

// darwinListenersAllDockerManaged reports whether every listening entry in
// lsof output belongs to a docker-desktop-class port proxy. lsof truncates
// COMMAND to 9 characters, so Docker Desktop's com.docker.backend shows as
// "com.docke".
func darwinListenersAllDockerManaged(listenerOutput string) bool {
	sawListener := false
	for line := range strings.SplitSeq(strings.TrimSpace(listenerOutput), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "COMMAND") {
			continue
		}
		sawListener = true
		lower := strings.ToLower(line)
		if !strings.HasPrefix(lower, "com.dock") && !strings.HasPrefix(lower, "docker") && !strings.HasPrefix(lower, "orbstack") && !strings.HasPrefix(lower, "vpnkit") {
			return false
		}
	}
	return sawListener
}

// edgeContainerStackRunning detects a managed edge container: the current
// single-image stack (frameworks-edge) or the retired 3-container proxy
// (edge-proxy), which the container install path migrates away. Names are
// matched exactly — sibling containers (frameworks-edge-vmagent) or
// similarly-named strangers must never stand in for the edge container.
// The sudo retry mirrors runEdgeDocker: on Ansible-provisioned hosts the
// SSH user often has passwordless sudo but no docker group, and a false
// negative here blocks the container→native migration in preflight.
func (e *EdgeProvisioner) edgeContainerStackRunning(ctx context.Context, host inventory.Host) bool {
	result, err := e.RunCommand(ctx, host, "docker ps --format '{{.Names}}' 2>/dev/null || sudo -n docker ps --format '{{.Names}}' 2>/dev/null")
	if err != nil || result.ExitCode != 0 {
		return false
	}
	for line := range strings.SplitSeq(result.Stdout, "\n") {
		switch strings.TrimSpace(line) {
		case "frameworks-edge", "edge-proxy":
			return true
		}
	}
	return false
}

var listenerPIDPattern = regexp.MustCompile(`pid=([0-9]+)`)

func listenerPIDsSubset(listenerOutput string, allowed map[string]struct{}) bool {
	pids := listenerPIDPattern.FindAllStringSubmatch(listenerOutput, -1)
	if len(pids) == 0 || len(allowed) == 0 {
		return false
	}
	for _, match := range pids {
		if len(match) < 2 {
			return false
		}
		if _, ok := allowed[match[1]]; !ok {
			return false
		}
	}
	return true
}

func listenerLooksDockerManaged(listenerOutput string) bool {
	output := strings.ToLower(listenerOutput)
	return strings.Contains(output, "docker-proxy") || strings.Contains(output, "com.docker")
}

// verifyHTTPS polls the edge domain until it serves a publicly trusted TLS
// certificate for that domain. The provisioner dials the target host directly
// when it knows the host IP, so this readiness gate is not blocked by DNS
// propagation. Application routes are intentionally not checked here: the edge
// Caddyfile ultimately proxies most paths to MistServer, which does not expose
// a conventional /health endpoint.
func (e *EdgeProvisioner) verifyHTTPS(domain, dialAddress string, timeout time.Duration) error {
	return VerifyEdgeTLS(domain, dialAddress, timeout, nil)
}

func VerifyEdgeTLS(domain, dialAddress string, timeout time.Duration, rootCAs *x509.CertPool) error {
	serverName := edgeHTTPSDialHost(domain)
	if serverName == "" {
		return fmt.Errorf("HTTPS check failed: empty domain")
	}
	dialHost := edgeHTTPSDialHost(dialAddress)
	dialPort := edgeHTTPSDialPort(dialAddress)
	if dialHost == "" {
		dialHost = serverName
	}
	if dialPort == "" {
		dialPort = "443"
	}
	endpoint := net.JoinHostPort(dialHost, dialPort)
	displayURL := "https://" + serverName
	tlsConfig := &tls.Config{
		MinVersion: tls.VersionTLS12,
		RootCAs:    rootCAs,
		ServerName: serverName,
	}
	deadline := time.Now().Add(timeout)
	var lastErr error
	var lastTLS string

	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			if lastErr != nil {
				return fmt.Errorf("HTTPS TLS check failed for %s via %s: %w", displayURL, dialHost, lastErr)
			}
			return fmt.Errorf("HTTPS TLS check failed for %s via %s: not ready before timeout (%s)", displayURL, dialHost, lastTLS)
		}
		dialTimeout := remaining
		if dialTimeout > 5*time.Second {
			dialTimeout = 5 * time.Second
		}
		ctx, cancel := context.WithTimeout(context.Background(), dialTimeout)
		dialer := &tls.Dialer{
			NetDialer: &net.Dialer{},
			Config:    tlsConfig,
		}
		conn, err := dialer.DialContext(ctx, "tcp", endpoint)
		cancel()
		if err == nil {
			tlsConn, ok := conn.(*tls.Conn)
			if !ok {
				_ = conn.Close()
				return fmt.Errorf("HTTPS TLS check failed for %s via %s: unexpected connection type %T", displayURL, dialHost, conn)
			}
			state := tlsConn.ConnectionState()
			tlsSummary := tlsConnectionSummary(&state)
			_ = conn.Close()
			if dialHost != serverName {
				fmt.Printf("  HTTPS TLS ready for %s via %s (%s)\n", displayURL, dialHost, tlsSummary)
			} else {
				fmt.Printf("  HTTPS TLS ready for %s (%s)\n", displayURL, tlsSummary)
			}
			return nil
		}
		lastErr = err
		lastTLS = tlsErrorSummary(err)
		sleepFor := time.Until(deadline)
		if sleepFor > 5*time.Second {
			sleepFor = 5 * time.Second
		}
		if sleepFor > 0 {
			time.Sleep(sleepFor)
		}
	}
}

func edgeHTTPSDialHost(dialAddress string) string {
	dialAddress = strings.TrimSpace(dialAddress)
	if dialAddress == "" {
		return ""
	}
	if host, _, err := net.SplitHostPort(dialAddress); err == nil {
		return strings.Trim(host, "[]")
	}
	return strings.Trim(dialAddress, "[]")
}

func edgeHTTPSDialPort(dialAddress string) string {
	dialAddress = strings.TrimSpace(dialAddress)
	if dialAddress == "" {
		return ""
	}
	_, port, err := net.SplitHostPort(dialAddress)
	if err != nil {
		return ""
	}
	return port
}

func tlsConnectionSummary(state *tls.ConnectionState) string {
	if state == nil {
		return "tls: unavailable"
	}
	parts := []string{"tls=" + tlsVersionName(state.Version)}
	if len(state.PeerCertificates) == 0 {
		return strings.Join(parts, " ")
	}
	cert := state.PeerCertificates[0]
	if cn := strings.TrimSpace(cert.Subject.CommonName); cn != "" {
		parts = append(parts, "subject="+cn)
	}
	if issuer := strings.TrimSpace(cert.Issuer.CommonName); issuer != "" {
		parts = append(parts, "issuer="+issuer)
	}
	if len(cert.DNSNames) > 0 {
		parts = append(parts, "dns="+strings.Join(cert.DNSNames, ","))
	}
	return strings.Join(parts, " ")
}

func tlsErrorSummary(err error) string {
	if err == nil {
		return "tls: unavailable"
	}
	return "tls: " + err.Error()
}

func tlsVersionName(version uint16) string {
	switch version {
	case tls.VersionTLS13:
		return "1.3"
	case tls.VersionTLS12:
		return "1.2"
	case tls.VersionTLS11:
		return "1.1"
	case tls.VersionTLS10:
		return "1.0"
	default:
		return fmt.Sprintf("0x%x", version)
	}
}

// Detect reports whether an edge stack is running on the host. Checks docker
// compose first, then systemd (Linux), then launchd (macOS, both domains).
// Stays Go-side because it's observed-state only and needs to answer quickly
// without bringing up an Ansible subprocess.
func (e *EdgeProvisioner) Detect(ctx context.Context, host inventory.Host) (*detect.ServiceState, error) {
	result, err := e.RunCommand(ctx, host, "docker compose -f /opt/frameworks/edge/docker-compose.yml ps --format json 2>/dev/null")
	if err == nil && result.ExitCode == 0 && strings.TrimSpace(result.Stdout) != "" {
		// Detected state uses the canonical mode name; "docker" survives
		// only as an input alias, never as persisted/reported truth.
		return &detect.ServiceState{
			Exists:   true,
			Running:  true,
			Metadata: map[string]string{"mode": "container"},
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

// EdgeEnrollment is the identity a completed edge install left on the host.
type EdgeEnrollment struct {
	NodeID      string
	EdgeDomain  string
	FoghornAddr string
	ClusterID   string
	Mode        string // "container" | "native" (best-effort, from Detect)
	Running     bool
}

// Enrolled reports whether the host carries a completed enrollment.
func (e *EdgeEnrollment) Enrolled() bool {
	return e != nil && e.NodeID != ""
}

// edgeEnrollmentEnvCandidates are the env files a completed provision renders,
// in probe order: linux native, darwin system, darwin user, docker compose.
// The env file is the enrollment marker — it is written only by a completed
// install, survives helmsman downtime, and carries the identity to reuse.
var edgeEnrollmentEnvCandidates = []string{
	"/etc/frameworks/helmsman.env",
	"/usr/local/etc/frameworks/helmsman.env",
	"$HOME/.config/frameworks/helmsman.env",
	"/opt/frameworks/edge/.edge.env",
}

// DetectEnrollment reports whether the host already carries an enrolled edge
// install and, if so, the node identity to reuse. Provision uses this to skip
// PreRegisterEdge and the enrollment-token requirement on re-runs — Foghorn
// resolves reconnecting nodes by fingerprint, so re-presenting a token only
// churns config files.
//
// The marker files are secret-bearing (0600, owned by frameworks/root), so a
// plain SSH user cannot read them directly; the probe falls back to
// passwordless sudo — the same privilege Ansible's become path requires. A
// marker that exists but is unreadable is an error, never "fresh": treating
// an enrolled node as fresh causes exactly the identity churn this detection
// prevents.
func (e *EdgeProvisioner) DetectEnrollment(ctx context.Context, host inventory.Host) (*EdgeEnrollment, error) {
	// Two readability paths, both fail closed (exit 4) when the marker
	// exists but its contents can't be trusted — never silently "fresh",
	// which would re-pre-register and churn the node identity.
	//
	// Direct read ([ -r ]): plain grep. rc 0 (keys found) and 1 (no keys in
	// a readable file) are both genuine — print and exit 0; rc >=2 is a read
	// failure (I/O error, file vanished) — exit 4.
	//
	// Privileged read: sudoers is per-command, so probing `sudo -n true`
	// proves nothing about `sudo -n grep` (policy may allow one and deny the
	// other — and a denied sudo also exits 1, indistinguishable from grep's
	// no-match). Instead the read runs under one `sudo -n sh -c` that emits a
	// sentinel only after the grep actually executes; if sudo refuses
	// (policy or auth), the sentinel is absent and we exit 4. With the
	// sentinel present, grep's own rc (0 or 1) is trustworthy.
	result, err := e.RunCommand(ctx, host, edgeEnrollmentProbeScript(edgeEnrollmentEnvCandidates))
	enrollment, err := classifyEnrollmentProbe(result, err, host.User)
	if err != nil {
		return nil, err
	}
	if !enrollment.Enrolled() {
		return enrollment, nil
	}
	if state, detectErr := e.Detect(ctx, host); detectErr == nil && state != nil {
		enrollment.Running = state.Running
		enrollment.Mode = state.Metadata["mode"]
	}
	return enrollment, nil
}

// edgeEnrollmentProbeScript builds the POSIX-sh probe that reads the
// enrollment identity from the first marker file that exists, over the given
// candidate paths.
//
// Two readability paths, both fail closed (exit 4) when the marker exists but
// its contents can't be trusted — never silently "fresh", which would
// re-pre-register and churn the node identity.
//
// Direct read ([ -r ]): plain grep. rc 0 (keys found) and 1 (no keys in a
// readable file) are both genuine — print and exit 0; rc >=2 is a read
// failure (I/O error, file vanished) — exit 4.
//
// Privileged read: sudoers is per-command, so probing `sudo -n true` proves
// nothing about `sudo -n grep` (policy may allow one and deny the other — and
// a denied sudo also exits 1, indistinguishable from grep's no-match).
// Instead the read runs under one `sudo -n sh -c` that emits a sentinel only
// when grep actually ran AND returned a trustworthy rc (0 or 1). Sentinel
// absent ⇒ exit 4, covering both sudo refusal (policy or auth) and a
// post-sudo read failure (grep rc >=2: file vanished, I/O error) — the same
// fail-closed rule as the direct branch.
//
// Exit codes consumed by classifyEnrollmentProbe: 0 = identity printed (may
// be empty → fresh), 3 = no marker file exists (fresh), 4 = marker exists but
// unreadable (error, never fresh).
func edgeEnrollmentProbeScript(candidates []string) string {
	return `for f in ` + strings.Join(candidates, " ") + `; do
  [ -f "$f" ] || continue
  if [ -r "$f" ]; then
    out=$(grep -E '^(NODE_ID|EDGE_DOMAIN|FOGHORN_CONTROL_ADDR|CLUSTER_ID)=' "$f" 2>/dev/null)
    [ $? -le 1 ] || exit 4
    printf '%s\n' "$out"
    exit 0
  fi
  out=$(sudo -n sh -c 'grep -E "^(NODE_ID|EDGE_DOMAIN|FOGHORN_CONTROL_ADDR|CLUSTER_ID)=" "$1" 2>/dev/null; [ $? -le 1 ] && printf __PROBE_OK__' _ "$f" 2>/dev/null)
  case "$out" in
    *__PROBE_OK__) printf '%s' "${out%__PROBE_OK__}"; exit 0 ;;
    *) exit 4 ;;
  esac
done
exit 3`
}

// classifyEnrollmentProbe maps the probe script's outcome to an enrollment
// state. The runners return a non-nil error for ANY non-zero exit (see
// ssh.Client.Run), so the script's deliberate exit codes (3 = fresh host,
// 4 = marker unreadable) arrive alongside an error and must be classified
// from result.ExitCode, never from err.
func classifyEnrollmentProbe(result *ssh.CommandResult, runErr error, sshUser string) (*EdgeEnrollment, error) {
	if result == nil {
		if runErr == nil {
			runErr = fmt.Errorf("no command result")
		}
		return nil, fmt.Errorf("edge: detect enrollment: %w", runErr)
	}
	switch result.ExitCode {
	case 0:
		return parseEdgeEnrollmentEnv(result.Stdout), nil
	case 3:
		return &EdgeEnrollment{}, nil
	case 4:
		return nil, fmt.Errorf("edge: existing edge install detected but its identity file could not be read (passwordless sudo missing for %s, or the privileged read failed); fix sudo or pass --force-reenroll", sshUser)
	default:
		// -1 = runner/process failure, 255 = ssh transport failure; anything
		// else is an unexpected probe outcome. All are errors, never "fresh".
		detail := strings.TrimSpace(result.Stderr)
		if detail == "" && runErr != nil {
			detail = runErr.Error()
		}
		return nil, fmt.Errorf("edge: detect enrollment probe failed (exit %d): %s", result.ExitCode, detail)
	}
}

// parseEdgeEnrollmentEnv extracts the enrollment identity from KEY=VALUE
// lines of a rendered helmsman/edge env file.
func parseEdgeEnrollmentEnv(content string) *EdgeEnrollment {
	enrollment := &EdgeEnrollment{}
	for line := range strings.SplitSeq(content, "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		value = strings.TrimSpace(value)
		switch key {
		case "NODE_ID":
			enrollment.NodeID = value
		case "EDGE_DOMAIN":
			enrollment.EdgeDomain = value
		case "FOGHORN_CONTROL_ADDR":
			enrollment.FoghornAddr = value
		case "CLUSTER_ID":
			enrollment.ClusterID = value
		}
	}
	return enrollment
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

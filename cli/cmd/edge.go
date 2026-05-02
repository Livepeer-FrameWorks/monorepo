package cmd

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"

	fwcfg "frameworks/cli/internal/config"
	fwcredentials "frameworks/cli/internal/credentials"
	"frameworks/cli/internal/platformauth"
	"frameworks/cli/internal/preflight"
	"frameworks/cli/internal/readiness"
	"frameworks/cli/internal/templates"
	"frameworks/cli/internal/ux"
	"frameworks/cli/internal/xexec"
	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/mistdiag"
	"frameworks/cli/pkg/provisioner"
	fwssh "frameworks/cli/pkg/ssh"
	"frameworks/pkg/clients/foghorn"
	"frameworks/pkg/clients/navigator"
	"frameworks/pkg/clients/quartermaster"
	"frameworks/pkg/ctxkeys"
	pkgdns "frameworks/pkg/dns"
	infra "frameworks/pkg/models"
	pb "frameworks/pkg/proto"
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
)

const (
	minDiskFreeBytes   = 20 * 1024 * 1024 * 1024
	minDiskFreePercent = 10.0
	darwinLogDir       = "/usr/local/var/log/frameworks"
)

var edgeNodeIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,99}$`)

func newEdgeCmd() *cobra.Command {
	edge := &cobra.Command{
		Use:   "edge",
		Short: "Edge node lifecycle operations",
	}
	edge.AddCommand(newEdgePreflightCmd())
	edge.AddCommand(newEdgeTuneCmd())
	edge.AddCommand(newEdgeInitCmd())
	edge.AddCommand(newEdgeEnrollCmd())
	edge.AddCommand(newEdgeProvisionCmd())
	edge.AddCommand(newEdgeStatusCmd())
	edge.AddCommand(newEdgeUpdateCmd())
	edge.AddCommand(newEdgeCertCmd())
	edge.AddCommand(newEdgeLogsCmd())
	edge.AddCommand(newEdgeDoctorCmd())
	edge.AddCommand(newEdgeDriftCmd())
	edge.AddCommand(newEdgeDiagnoseCmd())
	edge.AddCommand(newEdgeModeCmd())
	edge.AddCommand(newEdgeDeployCmd())
	return edge
}

func newEdgePreflightCmd() *cobra.Command {
	var domain string
	cmd := &cobra.Command{Use: "preflight", Short: "Check host readiness (DNS/ports/sysctl/limits)", RunE: func(cmd *cobra.Command, args []string) error {
		ctx := cmd.Context()
		out := cmd.OutOrStdout()
		ux.Heading(out, "Edge host readiness checks")

		results := []preflight.Check{}
		if domain != "" {
			results = append(results, preflight.DNSResolution(ctx, domain))
		}
		results = append(results, preflight.HasDocker(ctx)...)
		results = append(results, preflight.HasServiceManager())
		if runtime.GOOS == "linux" {
			results = append(results, preflight.LinuxSysctlChecks()...)
			results = append(results, preflight.ShmSize())
		}
		results = append(results, preflight.UlimitNoFile())
		results = append(results, preflight.PortChecks(ctx)...)
		results = append(results, preflight.DiskSpace("/", minDiskFreeBytes, minDiskFreePercent))
		if runtime.GOOS == "linux" {
			results = append(results, preflight.DiskSpace("/var/lib", minDiskFreeBytes, minDiskFreePercent))
		} else {
			results = append(results, preflight.DiskSpace("/usr/local", minDiskFreeBytes, minDiskFreePercent))
		}

		okCount := 0
		for _, r := range results {
			label := r.Name + ":"
			line := fmt.Sprintf("%-18s %s", label, r.Detail)
			if r.Error != "" {
				line = fmt.Sprintf("%-18s %-40s (%s)", label, r.Detail, r.Error)
			}
			if r.OK {
				ux.Success(out, line)
				okCount++
			} else {
				ux.Fail(out, line)
			}
		}
		fmt.Fprintf(out, "\nSummary: %d/%d checks passed\n", okCount, len(results))
		if okCount < len(results) {
			ux.PrintNextSteps(out, []ux.NextStep{
				{Cmd: "frameworks edge tune --write", Why: "Apply recommended sysctl/limits (reboot may be required)."},
			})
			return fmt.Errorf("%d preflight check(s) failed", len(results)-okCount)
		}
		ux.PrintNextSteps(out, []ux.NextStep{
			{Cmd: "frameworks edge deploy --ssh <user>@<host>", Why: "Host is ready — deploy an edge node."},
		})
		return nil
	}}
	cmd.Flags().StringVar(&domain, "domain", "", "Edge domain to validate (DNS)")
	return cmd
}

func newEdgeTuneCmd() *cobra.Command {
	var write bool
	var sysctlPath string
	var limitsPath string
	cmd := &cobra.Command{Use: "tune", Short: "Apply recommended sysctl/limits (requires sudo)", RunE: func(cmd *cobra.Command, args []string) error {
		out := cmd.OutOrStdout()
		if runtime.GOOS == "darwin" {
			ux.Heading(out, "Network tuning on macOS")
			fmt.Fprintln(out, "  - File descriptors: launchctl limit maxfiles 1048576 1048576")
			fmt.Fprintln(out, "  - Socket buffers: sysctl -w kern.ipc.maxsockbuf=16777216")
			fmt.Fprintln(out, "  - Listen backlog: sysctl -w kern.ipc.somaxconn=8192")
			ux.Warn(out, "These require sudo and reset on reboot unless added to /etc/sysctl.conf.")
			return nil
		}
		ux.Heading(out, "Applying edge tuning (sysctl + limits)")

		sysctl := `# Frameworks Edge recommended network tuning
net.core.rmem_max = 16777216
net.core.wmem_max = 16777216
net.core.somaxconn = 8192
net.ipv4.ip_local_port_range = 16384 65535
`
		limits := `# Frameworks Edge recommended file limits
* soft nofile 1048576
* hard nofile 1048576
`
		if write {
			if err := os.WriteFile(sysctlPath, []byte(sysctl), 0o644); err != nil {
				ux.FormatError(cmd.OutOrStdout(), fmt.Errorf("write %s: %w", sysctlPath, err), "re-run with sudo or pick a writable path via --sysctl-path")
			} else {
				ux.Success(cmd.OutOrStdout(), fmt.Sprintf("Wrote %s", sysctlPath))
			}
			if err := os.WriteFile(limitsPath, []byte(limits), 0o644); err != nil {
				ux.FormatError(cmd.OutOrStdout(), fmt.Errorf("write %s: %w", limitsPath, err), "re-run with sudo or pick a writable path via --limits-path")
			} else {
				ux.Success(cmd.OutOrStdout(), fmt.Sprintf("Wrote %s", limitsPath))
			}
			fmt.Fprintln(cmd.OutOrStdout(), "Note: run 'sysctl --system' or reboot to apply sysctl. Relogin to apply limits.")
			return nil
		}
		// Dry run: write to local files for review
		if err := os.WriteFile("frameworks-edge.sysctl", []byte(sysctl), 0o644); err == nil {
			fmt.Fprintln(cmd.OutOrStdout(), "Wrote frameworks-edge.sysctl (preview)")
		}
		if err := os.WriteFile("frameworks-edge.limits", []byte(limits), 0o644); err == nil {
			fmt.Fprintln(cmd.OutOrStdout(), "Wrote frameworks-edge.limits (preview)")
		}
		fmt.Fprintf(cmd.OutOrStdout(), "To apply system-wide, run with --write (sudo), or manually place files at %s and %s.\n", sysctlPath, limitsPath)
		return nil
	}}
	cmd.Flags().BoolVar(&write, "write", false, "write to system paths (requires sudo)")
	cmd.Flags().StringVar(&sysctlPath, "sysctl-path", "/etc/sysctl.d/frameworks-edge.conf", "target sysctl path")
	cmd.Flags().StringVar(&limitsPath, "limits-path", "/etc/security/limits.d/frameworks-edge.conf", "target limits path")
	return cmd
}

func newEdgeInitCmd() *cobra.Command {
	var target string
	var domain string
	var email string
	var enrollmentToken string
	var foghornAddr string
	var overwrite bool
	var initMode string
	var telemetryURL string
	var telemetryToken string
	cmd := &cobra.Command{Use: "init", Short: ".edge.env + templates (compose, Caddyfile)", RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := fwcfg.Load()
		if err != nil {
			return err
		}
		rt := fwcfg.GetRuntimeOverrides()
		cliCtx, err := fwcfg.MaybeActiveContext(rt, fwcfg.OSEnv{}, cfg)
		if err != nil {
			return err
		}
		if target == "" {
			target = "."
		}
		ux.Heading(cmd.OutOrStdout(), fmt.Sprintf("Generating edge templates in %s", target))

		// PreRegisterEdge: if enrollment token is provided but domain is not,
		// call Foghorn to get an assigned domain.
		var preRegNodeID string
		var preRegFoghornAddr string
		var preRegCABundle string
		if enrollmentToken != "" {
			fmt.Fprintln(cmd.OutOrStdout(), "Pre-registering edge via enrollment token...")
			var (
				resp      *pb.PreRegisterEdgeResponse
				preRegErr error
			)
			if foghornAddr != "" {
				// Explicit override: dial Foghorn directly. For admin debug only.
				resp, preRegErr = preRegisterEdgeLocal(cmd.Context(), foghornAddr, enrollmentToken, deriveEdgeNodeName("", "", "", true))
			} else {
				resp, preRegErr = bootstrapEdgeViaBridge(cmd.Context(), cliCtx, enrollmentToken, "", "", deriveEdgeNodeName("", "", "", true))
			}
			if preRegErr != nil {
				return fmt.Errorf("pre-registration failed: %w", preRegErr)
			}
			if domain == "" {
				domain = resp.GetEdgeDomain()
			}
			preRegNodeID = resp.GetNodeId()
			preRegFoghornAddr = resp.GetFoghornGrpcAddr()
			preRegCABundle = string(resp.GetInternalCaBundle())
			if telemetry := resp.GetTelemetry(); telemetry != nil && telemetry.GetEnabled() {
				telemetryURL = telemetry.GetWriteUrl()
				telemetryToken = telemetry.GetBearerToken()
			}
			fmt.Fprintf(cmd.OutOrStdout(), "  Assigned domain: %s\n", domain)
			fmt.Fprintf(cmd.OutOrStdout(), "  Node ID: %s\n", preRegNodeID)
		}
		if preRegNodeID == "" {
			preRegNodeID = canonicalEdgeNodeID(deriveEdgeNodeName("", domain, "", true))
		}
		if preRegNodeID == "" {
			b := make([]byte, 6)
			if _, randErr := rand.Read(b); randErr != nil {
				return randErr
			}
			preRegNodeID = hex.EncodeToString(b)
		}

		foghornGRPC := cliCtx.Endpoints.FoghornGRPCAddr
		if preRegFoghornAddr != "" {
			foghornGRPC = preRegFoghornAddr
		}

		vars := templates.EdgeVars{
			NodeID:          preRegNodeID,
			EdgeDomain:      domain,
			AcmeEmail:       email,
			FoghornGRPCAddr: foghornGRPC,
			EnrollmentToken: enrollmentToken,
			GRPCTLSCAPath:   "/etc/frameworks/pki/ca.crt",
			CABundlePEM:     preRegCABundle,
			Mode:            initMode,
			TelemetryURL:    telemetryURL,
			TelemetryToken:  telemetryToken,
		}

		if err := templates.WriteEdgeTemplates(target, vars, overwrite); err != nil {
			return err
		}
		ux.Success(cmd.OutOrStdout(), fmt.Sprintf("Wrote edge templates to %s", target))
		ux.PrintNextSteps(cmd.OutOrStdout(), []ux.NextStep{
			{Cmd: fmt.Sprintf("frameworks edge enroll --dir %s", target), Why: "Start the edge stack and verify HTTPS."},
		})
		return nil
	}}
	cmd.Flags().StringVar(&target, "dir", ".", "target directory for templates")
	cmd.Flags().StringVar(&domain, "domain", "", "EDGE_DOMAIN to configure (manual DNS)")
	cmd.Flags().StringVar(&email, "email", "", "ACME email for certificate issuance")
	cmd.Flags().StringVar(&enrollmentToken, "enrollment-token", "", "enrollment token issued by FrameWorks for node bootstrap")
	cmd.Flags().StringVar(&foghornAddr, "foghorn-addr", "", "Foghorn gRPC address for PreRegisterEdge (host:port)")
	cmd.Flags().BoolVar(&overwrite, "overwrite", false, "overwrite existing files")
	cmd.Flags().StringVar(&initMode, "mode", "docker", "Deployment mode: docker (compose) or native (systemd)")
	return cmd
}

func newEdgeEnrollCmd() *cobra.Command {
	var dir string
	var timeout time.Duration
	var sshTarget string
	var sshKey string
	cmd := &cobra.Command{Use: "enroll", Short: "Start edge stack and enroll with control-plane", RunE: func(cmd *cobra.Command, args []string) error {
		if dir == "" {
			dir = "."
		}
		out := cmd.OutOrStdout()
		target := sshTarget
		if target == "" {
			target = "local"
		}
		ux.Heading(out, fmt.Sprintf("Enrolling edge node on %s", target))

		compose := "docker-compose.edge.yml"
		envFile := ".edge.env"
		var outStr, errOut string
		var err error
		if strings.TrimSpace(sshTarget) != "" {
			_, outStr, errOut, err = xexec.RunSSHWithKey(cmd.Context(), sshTarget, sshKey, "docker", []string{"compose", "-f", compose, "--env-file", envFile, "up", "-d"}, dir)
		} else {
			_, outStr, errOut, err = xexec.Run(cmd.Context(), "docker", []string{"compose", "-f", compose, "--env-file", envFile, "up", "-d"}, dir)
		}
		if err != nil {
			ux.ErrorWithOutput(cmd.ErrOrStderr(), fmt.Errorf("docker compose up: %w", err), "docker daemon rejected the stack — inspect the output for the specific service error", outStr, errOut)
			return err
		}
		ux.Success(out, "Edge stack started (caddy, mistserver, helmsman)")
		domain := readRemoteEnvFileKey(cmd.Context(), sshTarget, sshKey, dir, envFile, "EDGE_DOMAIN")
		if strings.TrimSpace(domain) == "" {
			ux.Warn(out, "EDGE_DOMAIN not set in .edge.env; skipping HTTPS check")
			return nil
		}
		url := "https://" + domain + "/health"
		httpClient := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}}
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
				ux.Success(cmd.OutOrStdout(), fmt.Sprintf("HTTPS ready at %s", url))
				break
			}
			if resp != nil && resp.Body != nil {
				_ = resp.Body.Close()
			}
			if time.Now().After(deadline) {
				if err != nil {
					fmt.Fprintf(cmd.ErrOrStderr(), "HTTPS check failed: %v\n", err)
				} else if resp != nil {
					fmt.Fprintf(cmd.ErrOrStderr(), "HTTPS not ready: status %d\n", resp.StatusCode)
				}
				return fmt.Errorf("edge HTTPS not ready before timeout")
			}
			time.Sleep(2 * time.Second)
		}
		return nil
	}}
	cmd.Flags().StringVar(&dir, "dir", ".", "directory with docker-compose.edge.yml and .edge.env")
	cmd.Flags().DurationVar(&timeout, "timeout", 2*time.Minute, "maximum time to wait for HTTPS readiness")
	cmd.Flags().StringVar(&sshTarget, "ssh", "", "run remotely on user@host via SSH")
	cmd.Flags().StringVar(&sshKey, "ssh-key", "", "SSH private key path")
	return cmd
}

// parseEdgeServiceStatus extracts per-service up/down state from docker
// compose ps or systemctl status output. Service names returned match
// `frameworks edge logs <name>` handles. On parse uncertainty the service
// is omitted rather than reported as down.
func parseEdgeServiceStatus(raw, mode string) []readiness.EdgeCheck {
	services := []string{"caddy", "mistserver", "helmsman"}
	out := make([]readiness.EdgeCheck, 0, len(services))
	lowered := strings.ToLower(raw)
	for _, svc := range services {
		idx := strings.Index(lowered, svc)
		if idx < 0 {
			continue
		}
		lineStart := strings.LastIndex(lowered[:idx], "\n") + 1
		lineEnd := strings.Index(lowered[idx:], "\n")
		if lineEnd < 0 {
			lineEnd = len(lowered) - idx
		}
		nameLine := lowered[lineStart : idx+lineEnd]

		// Docker ps is one line per service; systemctl status spans multiple
		// lines per block — native mode expands scope to the next block boundary.
		scope := nameLine
		if mode == "native" {
			blockEnd := len(lowered)
			if dot := strings.Index(lowered[idx+len(svc):], "●"); dot >= 0 {
				blockEnd = idx + len(svc) + dot
			}
			if lineBreak := strings.Index(lowered[idx+len(svc):], "\n\n"); lineBreak >= 0 && idx+len(svc)+lineBreak < blockEnd {
				blockEnd = idx + len(svc) + lineBreak
			}
			scope = lowered[lineStart:blockEnd]
		}

		healthy := false
		switch mode {
		case "native":
			healthy = strings.Contains(scope, "active (running)") || strings.Contains(scope, "com.livepeer.frameworks")
			if strings.Contains(scope, "failed") || strings.Contains(scope, "inactive") {
				healthy = false
			}
		case "docker":
			healthy = strings.Contains(scope, "up ") || strings.Contains(scope, "running")
			if strings.Contains(scope, "unhealthy") || strings.Contains(scope, "exited") || strings.Contains(scope, "restarting") {
				healthy = false
			}
		}
		out = append(out, readiness.EdgeCheck{
			Name:   svc,
			OK:     healthy,
			Detail: strings.TrimSpace(nameLine),
		})
	}
	return out
}

// detectEdgeMode reads DEPLOY_MODE from <dir>/.edge.env to determine if the
// edge stack is running in docker or native mode. Honors --dir both locally
// and over SSH. Falls back to "docker" if unset.
func detectEdgeMode(ctx context.Context, dir, envFile, sshTarget, sshKey string) string {
	if readRemoteEnvFileKey(ctx, sshTarget, sshKey, dir, envFile, "DEPLOY_MODE") == "native" {
		return "native"
	}
	return "docker"
}

// detectEdgeOS returns "darwin" or "linux" for the target host.
// For remote SSH targets, runs `uname -s`. For local, uses runtime.GOOS.
func detectEdgeOS(ctx context.Context, sshTarget, sshKey string) string {
	if strings.TrimSpace(sshTarget) != "" {
		_, out, _, err := xexec.RunSSHWithKey(ctx, sshTarget, sshKey, "uname", []string{"-s"}, "")
		if err == nil && strings.TrimSpace(strings.ToLower(out)) == "darwin" {
			return "darwin"
		}
		return "linux"
	}
	return runtime.GOOS
}

func readEnvFileKey(path, key string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	lines := strings.Split(string(b), "\n")
	prefix := key + "="
	for _, ln := range lines {
		ln = strings.TrimSpace(ln)
		if v, ok := strings.CutPrefix(ln, prefix); ok {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// probeEdgeEnvFile reports whether the .edge.env file at <dir>/<envFile> is
// readable. Callers use it to distinguish "file missing / unreachable" from
// "key absent" before attempting per-key reads.
func probeEdgeEnvFile(ctx context.Context, sshTarget, sshKey, dir, envFile string) error {
	path := dir + string(os.PathSeparator) + envFile
	if strings.TrimSpace(sshTarget) != "" {
		path = strings.TrimRight(dir, "/") + "/" + envFile
		if strings.TrimSpace(dir) == "" {
			path = envFile
		}
		script := fmt.Sprintf("test -r %s", fwssh.ShellQuote(path))
		code, _, stderr, err := xexec.RunSSHWithKey(ctx, sshTarget, sshKey, "sh", []string{"-c", script}, "")
		if err != nil {
			return fmt.Errorf("probe %s: %w", path, err)
		}
		switch code {
		case 0:
			return nil
		case 1:
			return fmt.Errorf("probe %s: not readable", path)
		default:
			return fmt.Errorf("probe %s: test -r exited %d: %s", path, code, stderr)
		}
	}
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("probe %s: %w", path, err)
	}
	if info.IsDir() {
		return fmt.Errorf("probe %s: is a directory", path)
	}
	return nil
}

// readRemoteEnvFileKey reads a single key from <dir>/.edge.env, honoring --dir
// both locally and over SSH. When sshTarget is empty, delegates to
// readEnvFileKey. Returns "" on any read error or missing key — callers that
// need to distinguish the two call probeEdgeEnvFile() first.
func readRemoteEnvFileKey(ctx context.Context, sshTarget, sshKey, dir, envFile, key string) string {
	if strings.TrimSpace(sshTarget) == "" {
		return readEnvFileKey(dir+string(os.PathSeparator)+envFile, key)
	}
	remoteDir := dir
	if strings.TrimSpace(remoteDir) == "" {
		remoteDir = "."
	}
	remotePath := strings.TrimRight(remoteDir, "/") + "/" + envFile
	script := fmt.Sprintf("grep ^%s= %s 2>/dev/null", fwssh.ShellQuote(key), fwssh.ShellQuote(remotePath))
	_, out, _, err := xexec.RunSSHWithKey(ctx, sshTarget, sshKey, "sh", []string{"-c", script}, "")
	if err != nil {
		return ""
	}
	prefix := key + "="
	for ln := range strings.SplitSeq(out, "\n") {
		ln = strings.TrimSpace(ln)
		if v, ok := strings.CutPrefix(ln, prefix); ok {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func newEdgeProvisionCmd() *cobra.Command {
	var sshTarget string
	var sshKey string
	var nodeDomain string
	var poolDomain string
	var nodeName string
	var clusterID string
	var region string
	var email string
	var enrollmentToken string
	var foghornAddr string
	var timeout time.Duration
	var skipPreflight bool
	var applyTuning bool
	var registerNode bool
	var fetchCert bool
	var manifestPath string
	var parallel int
	var mode string
	var version string
	var local bool
	var ageKeyFile string
	var dryRun bool
	var telemetryURL string
	var telemetryToken string

	cmd := &cobra.Command{
		Use:   "provision",
		Short: "Provision edge node(s) via SSH or locally (preflight, tune, init, enroll)",
		Long: `Provision edge node(s) by SSHing (or locally with --local) and running the full edge setup:
  1. Run preflight checks (Docker, ports, sysctl, limits)
  2. Apply sysctl/limits tuning (optional)
  3. Register node in Quartermaster (triggers DNS sync via Navigator)
  4. Fetch TLS certificate from Navigator (optional, uses centralized DNS-01 challenge)
  5. Generate and upload edge templates (docker-compose, Caddyfile, .env)
  6. Start edge stack (docker compose up)
  7. Wait for HTTPS readiness

Single node example:
  frameworks edge provision --ssh ubuntu@edge-1.example.com \
    --pool-domain edge.media-eu.example.com \
    --node-domain edge-1.media-eu.example.com \
    --node-name edge-us-east-1 \
    --email ops@example.com \
    --fetch-cert

Local (user LaunchAgent, no admin required):
  frameworks edge provision --local --enrollment-token <tok>

Multi-node manifest example:
  frameworks edge provision --manifest edges.yaml --parallel 4`,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := fwcfg.Load()
			if err != nil {
				return fmt.Errorf("failed to load config: %w", err)
			}
			rt := fwcfg.GetRuntimeOverrides()
			cliCtx, err := fwcfg.MaybeActiveContext(rt, fwcfg.OSEnv{}, cfg)
			if err != nil {
				return err
			}

			// Check if using manifest mode
			if manifestPath != "" {
				return runEdgeProvisionFromManifest(cmd, cliCtx, manifestPath, sshKey, enrollmentToken, parallel, timeout, mode, version, ageKeyFile, dryRun)
			}

			// Default --cluster-id from context. --foghorn-addr is intentionally
			// NOT defaulted here; the bootstrap branch below must distinguish
			// "operator explicitly passed --foghorn-addr" (debug override → direct
			// dial) from "context happens to have a Foghorn addr" (still → Bridge).
			if clusterID == "" {
				clusterID = cliCtx.ClusterID
			}
			foghornAddrExplicit := cmd.Flags().Changed("foghorn-addr")

			// --register belongs to the manual/admin path. The token path
			// already registers the node via Foghorn's PreRegisterEdge, and
			// the edge persona doesn't have Quartermaster reachability.
			if registerNode && enrollmentToken != "" {
				return fmt.Errorf("--register is for the manual provisioning path; the token path already registers the node via Foghorn")
			}
			if registerNode && cliCtx.Persona == fwcfg.PersonaEdge {
				return fmt.Errorf("--register requires Quartermaster access; the edge persona only has Bridge — use a platform or self-hosted context for manual node registration")
			}

			// Single node mode - require ssh target or --local
			isLocal := local || sshTarget == "localhost" || sshTarget == "127.0.0.1"
			if sshTarget == "" && !isLocal {
				return fmt.Errorf("--ssh is required (user@host), --local for this machine, or --manifest for multi-node")
			}

			// PreRegisterEdge: if enrollment token is provided but domain is not,
			// call Foghorn to validate the token and get an assigned domain.
			var preRegNodeID string
			var preRegFoghornAddr string
			var preRegCABundle string
			if enrollmentToken != "" {
				fmt.Fprintln(cmd.OutOrStdout(), "Pre-registering edge via enrollment token...")
				preRegTarget := sshTarget
				if isLocal {
					preRegTarget = "localhost"
				}
				preferredNodeID := deriveEdgeNodeName(nodeName, "", sshTarget, isLocal)
				var (
					preRegResp *pb.PreRegisterEdgeResponse
					preRegErr  error
				)
				if foghornAddrExplicit && foghornAddr != "" {
					preRegResp, preRegErr = preRegisterEdge(cmd.Context(), foghornAddr, enrollmentToken, preRegTarget, sshKey, preferredNodeID)
				} else {
					preRegResp, preRegErr = bootstrapEdgeViaBridge(cmd.Context(), cliCtx, enrollmentToken, preRegTarget, sshKey, preferredNodeID)
				}
				if preRegErr != nil {
					return fmt.Errorf("pre-registration failed: %w", preRegErr)
				}
				if nodeDomain == "" {
					nodeDomain = preRegResp.GetEdgeDomain()
				}
				if poolDomain == "" {
					poolDomain = preRegResp.GetPoolDomain()
				}
				preRegNodeID = preRegResp.GetNodeId()
				preRegFoghornAddr = preRegResp.GetFoghornGrpcAddr()
				preRegCABundle = string(preRegResp.GetInternalCaBundle())
				if telemetry := preRegResp.GetTelemetry(); telemetry != nil && telemetry.GetEnabled() {
					telemetryURL = telemetry.GetWriteUrl()
					telemetryToken = telemetry.GetBearerToken()
				}
				if nodeName == "" {
					nodeName = preRegNodeID
				}
				if clusterID == "" {
					clusterID = preRegResp.GetClusterId()
				}
				if clusterID == "" {
					clusterID = preRegResp.GetClusterSlug()
				}
				fmt.Fprintf(cmd.OutOrStdout(), "  Assigned domain: %s\n", nodeDomain)
				fmt.Fprintf(cmd.OutOrStdout(), "  Pool domain: %s\n", poolDomain)
				fmt.Fprintf(cmd.OutOrStdout(), "  Node ID: %s\n", preRegNodeID)
				fmt.Fprintf(cmd.OutOrStdout(), "  Cluster: %s\n", preRegResp.GetClusterSlug())
			}

			// Generate a node ID if pre-registration didn't provide one
			if preRegNodeID == "" {
				b := make([]byte, 6)
				_, _ = rand.Read(b)
				preRegNodeID = hex.EncodeToString(b)
			}

			if poolDomain == "" && nodeDomain == "" {
				return fmt.Errorf("at least one of --pool-domain or --node-domain is required (or use --enrollment-token for auto-assignment)")
			}

			// Use poolDomain for the main domain if set, otherwise nodeDomain
			primaryDomain := poolDomain
			if primaryDomain == "" {
				primaryDomain = nodeDomain
			}

			// Default node name from SSH target, domain, or hostname
			if nodeName == "" {
				nodeName = deriveEdgeNodeName("", nodeDomain, sshTarget, isLocal)
				if nodeName == "" {
					nodeName = preRegNodeID
				}
			}

			targetLabel := sshTarget
			if isLocal {
				targetLabel = "localhost"
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Provisioning edge node: %s (%s mode)\n", targetLabel, mode)
			fmt.Fprintf(cmd.OutOrStdout(), "  Node name: %s\n", nodeName)
			fmt.Fprintf(cmd.OutOrStdout(), "  Pool domain: %s\n", poolDomain)
			fmt.Fprintf(cmd.OutOrStdout(), "  Node domain: %s\n", nodeDomain)

			// Fetch cert from Navigator if requested (before calling EdgeProvisioner)
			var certPEM, keyPEM string
			if fetchCert {
				fmt.Fprintln(cmd.OutOrStdout(), "\nFetching TLS certificate from Navigator...")
				if email == "" {
					return fmt.Errorf("--email is required when using --fetch-cert")
				}
				certPEM, keyPEM, err = fetchCertFromNavigator(cmd, cliCtx, primaryDomain, email)
				if err != nil {
					return fmt.Errorf("certificate fetch failed: %w", err)
				}
			}

			// Build EdgeProvisionConfig and delegate to EdgeProvisioner.
			// Order: pre-reg response (authoritative when token bootstrap ran) →
			// explicit --foghorn-addr → context default. The provisioner needs an
			// addr to plant in FOGHORN_CONTROL_ADDR for Helmsman's first dial.
			foghornGRPC := preRegFoghornAddr
			if foghornGRPC == "" {
				foghornGRPC = foghornAddr
			}
			if foghornGRPC == "" {
				foghornGRPC = cliCtx.Endpoints.FoghornGRPCAddr
			}

			var host inventory.Host
			var darwinDomain provisioner.DarwinDomain
			if isLocal {
				host = inventory.Host{
					ExternalIP: "localhost",
					User:       os.Getenv("USER"),
				}
				darwinDomain = provisioner.DomainUser
				if mode == "docker" {
					mode = "native" // local install always uses native launchd
				}
				fmt.Fprintln(cmd.OutOrStdout(), "  launchd domain: user (no admin required)")
			} else {
				host = sshTargetToHost(sshTarget)
				darwinDomain = provisioner.DomainSystem
			}

			epConfig := provisioner.EdgeProvisionConfig{
				Mode:            mode,
				NodeName:        nodeName,
				NodeDomain:      nodeDomain,
				PoolDomain:      poolDomain,
				ClusterID:       clusterID,
				Region:          region,
				Email:           email,
				EnrollmentToken: enrollmentToken,
				FoghornGRPCAddr: foghornGRPC,
				NodeID:          preRegNodeID,
				CertPEM:         certPEM,
				KeyPEM:          keyPEM,
				CABundlePEM:     preRegCABundle,
				TelemetryURL:    telemetryURL,
				TelemetryToken:  telemetryToken,
				SkipPreflight:   skipPreflight,
				ApplyTuning:     applyTuning,
				Timeout:         timeout,
				Version:         version,
				DarwinDomain:    darwinDomain,
			}

			pool := fwssh.NewPool(30*time.Second, sshKey)
			ep := provisioner.NewEdgeProvisioner(pool)

			// Registration handled before EdgeProvisioner (needs QM client)
			if registerNode {
				fmt.Fprintln(cmd.OutOrStdout(), "\nRegistering node in Quartermaster...")
				externalIP, _ := getRemoteExternalIP(cmd.Context(), sshTarget, sshKey)
				if errRegister := registerEdgeNode(cmd, cliCtx, nodeName, clusterID, externalIP, region); errRegister != nil {
					return fmt.Errorf("node registration failed: %w", errRegister)
				}
			}

			if err := ep.Provision(cmd.Context(), host, epConfig); err != nil {
				return err
			}

			ux.Success(cmd.OutOrStdout(), fmt.Sprintf("Edge node provisioned at https://%s/health", primaryDomain))
			ux.PrintNextSteps(cmd.OutOrStdout(), []ux.NextStep{
				{Cmd: "frameworks edge status", Why: "Confirm services are running and HTTPS is healthy."},
				{Cmd: "frameworks edge doctor", Why: "Run diagnostics if anything looks off."},
			})
			return nil
		},
	}

	cmd.Flags().StringVar(&sshTarget, "ssh", "", "SSH target (user@host, required)")
	cmd.Flags().StringVar(&sshKey, "ssh-key", "", "SSH private key path")
	cmd.Flags().StringVar(&poolDomain, "pool-domain", "", "Edge pool domain (e.g., edge.media-eu.example.com)")
	cmd.Flags().StringVar(&nodeDomain, "node-domain", "", "Individual node domain (e.g., edge-1.media-eu.example.com)")
	cmd.Flags().StringVar(&nodeName, "node-name", "", "Node name for registration (default: derived from domain/ssh)")
	cmd.Flags().StringVar(&clusterID, "cluster-id", "", "Cluster ID for node registration")
	cmd.Flags().StringVar(&region, "region", "", "Region for node registration (e.g., us-east-1)")
	cmd.Flags().StringVar(&email, "email", "", "ACME email for certificate issuance")
	cmd.Flags().StringVar(&enrollmentToken, "enrollment-token", "", "enrollment token issued by FrameWorks for node bootstrap")
	cmd.Flags().StringVar(&foghornAddr, "foghorn-addr", "", "Foghorn gRPC address for PreRegisterEdge (host:port)")
	cmd.Flags().DurationVar(&timeout, "timeout", 3*time.Minute, "Timeout for HTTPS readiness")
	cmd.Flags().BoolVar(&skipPreflight, "skip-preflight", false, "Skip preflight checks")
	cmd.Flags().BoolVar(&applyTuning, "tune", false, "Apply sysctl/limits tuning")
	cmd.Flags().BoolVar(&registerNode, "register", false, "Register node in Quartermaster (DNS synced by Navigator reconciler)")
	cmd.Flags().BoolVar(&fetchCert, "fetch-cert", false, "Fetch TLS certificate from Navigator (DNS-01 challenge)")
	cmd.Flags().StringVar(&manifestPath, "manifest", "", "Path to edge manifest file (edges.yaml) for multi-node provisioning")
	cmd.Flags().IntVar(&parallel, "parallel", 1, "Number of nodes to provision in parallel (for manifest mode)")
	cmd.Flags().StringVar(&mode, "mode", "docker", "Deployment mode: docker (compose) or native (systemd)")
	cmd.Flags().StringVar(&version, "version", "", "Platform version for binary resolution (e.g., stable, v1.2.3)")
	cmd.Flags().BoolVar(&local, "local", false, "Provision this machine as a user LaunchAgent (no admin required, macOS only)")
	cmd.Flags().StringVar(&ageKeyFile, "age-key", "", "Path to age private key for SOPS-encrypted host files (default: $SOPS_AGE_KEY_FILE)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Load and validate manifest, show provision plan, but do not execute")

	return cmd
}

// EdgeProvisionResult holds the result of provisioning a single edge node
type EdgeProvisionResult struct {
	NodeName string
	SSHAddr  string
	Success  bool
	Error    error
}

// runEdgeProvisionFromManifest provisions multiple edge nodes from a manifest file
func runEdgeProvisionFromManifest(cmd *cobra.Command, cliCtx fwcfg.Context, manifestPath, defaultSSHKey, enrollmentToken string, parallel int, timeout time.Duration, cliMode, cliVersion, ageKeyFile string, dryRun bool) error {
	// Load manifest (with host inventory merge if hosts_file is set)
	manifest, err := inventory.LoadEdgeWithHosts(manifestPath, ageKeyFile)
	if err != nil {
		return fmt.Errorf("failed to load manifest: %w", err)
	}

	fmt.Fprintf(cmd.OutOrStdout(), "Loaded edge manifest with %d nodes\n", len(manifest.Nodes))
	fmt.Fprintf(cmd.OutOrStdout(), "  Pool domain: %s\n", manifest.PoolDomain)
	fmt.Fprintf(cmd.OutOrStdout(), "  Root domain: %s\n", manifest.RootDomain)
	fmt.Fprintf(cmd.OutOrStdout(), "  Parallelism: %d\n", parallel)

	// Dry-run: show plan and exit without provisioning
	if dryRun {
		effectiveVersion := manifest.Channel
		if cmd.Flags().Changed("version") {
			effectiveVersion = cliVersion
		}
		fmt.Fprintf(cmd.OutOrStdout(), "\nDry-run mode — no changes will be made.\n")
		if effectiveVersion == "" {
			fmt.Fprintf(cmd.OutOrStdout(), "  Release selector: (none — native mode will fail without --version or channel)\n")
		} else {
			fmt.Fprintf(cmd.OutOrStdout(), "  Release selector: %s\n", effectiveVersion)
		}
		for _, node := range manifest.Nodes {
			nodeMode := node.ResolvedMode(manifest.Mode)
			if cmd.Flags().Changed("mode") {
				nodeMode = cliMode
			}
			fmt.Fprintf(cmd.OutOrStdout(), "  Node: %-20s SSH: %-30s Region: %-12s Mode: %s\n",
				node.Name, node.SSH, node.Region, nodeMode)
		}
		return nil
	}

	if parallel < 1 {
		parallel = 1
	}
	if parallel > len(manifest.Nodes) {
		parallel = len(manifest.Nodes)
	}

	// Semaphore for parallelism control
	sem := make(chan struct{}, parallel)
	var wg sync.WaitGroup
	results := make(chan EdgeProvisionResult, len(manifest.Nodes))

	// Provision each node
	for _, node := range manifest.Nodes {
		wg.Add(1)
		go func(n inventory.EdgeNode) {
			defer wg.Done()
			sem <- struct{}{}        // Acquire semaphore
			defer func() { <-sem }() // Release semaphore

			result := EdgeProvisionResult{
				NodeName: n.Name,
				SSHAddr:  n.SSH,
			}

			// Build node domain
			nodeDomain := ""
			if n.Subdomain != "" {
				if manifest.RootDomain != "" {
					nodeDomain = n.Subdomain + "." + manifest.RootDomain
				}
			}

			// Use pool domain from manifest
			poolDomain := manifest.PoolDomain

			// Primary domain for Caddy config
			primaryDomain := poolDomain
			if primaryDomain == "" {
				primaryDomain = nodeDomain
			}

			fmt.Fprintf(cmd.OutOrStdout(), "\n[%s] Starting provisioning...\n", n.Name)

			sshKey := defaultSSHKey
			token := enrollmentToken
			if token == "" {
				token = manifest.EnrollmentToken
			}
			nodeMode := n.ResolvedMode(manifest.Mode)
			if cmd.Flags().Changed("mode") {
				nodeMode = cliMode
			}
			nodeVersion := manifest.Channel
			if cmd.Flags().Changed("version") {
				nodeVersion = cliVersion
			}
			err := provisionSingleEdgeNode(cmd, cliCtx, n.SSH, sshKey, n.Name, nodeDomain, poolDomain, manifest.ClusterID, n.Region, manifest.Email, token, manifest.FetchCert, n.ApplyTune, n.RegisterQM, timeout, nodeMode, nodeVersion, "", "")
			if err != nil {
				result.Error = err
				result.Success = false
				fmt.Fprintf(cmd.OutOrStdout(), "[%s] FAILED: %v\n", n.Name, err)
			} else {
				result.Success = true
				fmt.Fprintf(cmd.OutOrStdout(), "[%s] SUCCESS: https://%s/health\n", n.Name, primaryDomain)
			}

			results <- result
		}(node)
	}

	// Wait for all goroutines to finish
	go func() {
		wg.Wait()
		close(results)
	}()

	// Collect results
	var succeeded, failed int
	var failedNodes []string
	for result := range results {
		if result.Success {
			succeeded++
		} else {
			failed++
			failedNodes = append(failedNodes, result.NodeName)
		}
	}

	// Summary
	fmt.Fprintf(cmd.OutOrStdout(), "\n=== Provisioning Summary ===\n")
	fmt.Fprintf(cmd.OutOrStdout(), "  Succeeded: %d/%d\n", succeeded, len(manifest.Nodes))
	fmt.Fprintf(cmd.OutOrStdout(), "  Failed: %d/%d\n", failed, len(manifest.Nodes))
	if len(failedNodes) > 0 {
		fmt.Fprintf(cmd.OutOrStdout(), "  Failed nodes: %v\n", failedNodes)
		return fmt.Errorf("%d nodes failed to provision", failed)
	}

	return nil
}

// provisionSingleEdgeNode provisions a single edge node using EdgeProvisioner.
func provisionSingleEdgeNode(cmd *cobra.Command, cliCtx fwcfg.Context, sshTarget, sshKey, nodeName, nodeDomain, poolDomain, clusterID, region, email, enrollmentToken string, fetchCert, applyTuning, registerNode bool, timeout time.Duration, mode, version, telemetryURL, telemetryToken string) error {
	// Same --register guards the single-node provision RunE applies. Manifest
	// mode reaches this helper without going through that RunE, so duplicate
	// the contract here to keep the rule single-source.
	if registerNode && enrollmentToken != "" {
		return fmt.Errorf("register_qm is for the manual provisioning path; the token path already registers the node via Foghorn")
	}
	if registerNode && cliCtx.Persona == fwcfg.PersonaEdge {
		return fmt.Errorf("register_qm requires Quartermaster access; the edge persona only has Bridge — use a platform or self-hosted context for manual node registration")
	}

	var preRegFoghornAddr string
	var preRegCABundle string
	if enrollmentToken != "" {
		preferredNodeID := deriveEdgeNodeName(nodeName, nodeDomain, sshTarget, false)
		preRegResp, err := bootstrapEdgeViaBridge(cmd.Context(), cliCtx, enrollmentToken, sshTarget, sshKey, preferredNodeID)
		if err != nil {
			return fmt.Errorf("pre-registration failed: %w", err)
		}
		preRegFoghornAddr = preRegResp.GetFoghornGrpcAddr()
		preRegCABundle = string(preRegResp.GetInternalCaBundle())
		if nodeDomain == "" {
			nodeDomain = preRegResp.GetEdgeDomain()
		}
		if poolDomain == "" {
			poolDomain = preRegResp.GetPoolDomain()
		}
		if clusterID == "" {
			clusterID = preRegResp.GetClusterId()
		}
		if telemetry := preRegResp.GetTelemetry(); telemetry != nil && telemetry.GetEnabled() {
			telemetryURL = telemetry.GetWriteUrl()
			telemetryToken = telemetry.GetBearerToken()
		}
	}
	primaryDomain := poolDomain
	if primaryDomain == "" {
		primaryDomain = nodeDomain
	}

	// Registration is done before calling EdgeProvisioner (needs QM client)
	if registerNode {
		externalIP, _ := getRemoteExternalIP(cmd.Context(), sshTarget, sshKey)
		if err := registerEdgeNode(cmd, cliCtx, nodeName, clusterID, externalIP, region); err != nil {
			return fmt.Errorf("node registration failed: %w", err)
		}
	}

	// Fetch certs from Navigator if requested
	var certPEM, keyPEM string
	if fetchCert && email != "" {
		var err error
		certPEM, keyPEM, err = fetchCertFromNavigator(cmd, cliCtx, primaryDomain, email)
		if err != nil {
			return fmt.Errorf("certificate fetch failed: %w", err)
		}
	}

	// Parse SSH target (user@host) into inventory.Host
	host := sshTargetToHost(sshTarget)

	// Build EdgeProvisionConfig
	config := provisioner.EdgeProvisionConfig{
		Mode:            mode,
		NodeName:        nodeName,
		NodeDomain:      nodeDomain,
		PoolDomain:      poolDomain,
		ClusterID:       clusterID,
		Region:          region,
		Email:           email,
		EnrollmentToken: enrollmentToken,
		FoghornGRPCAddr: firstNonEmpty(preRegFoghornAddr, cliCtx.Endpoints.FoghornGRPCAddr),
		CertPEM:         certPEM,
		KeyPEM:          keyPEM,
		CABundlePEM:     preRegCABundle,
		TelemetryURL:    telemetryURL,
		TelemetryToken:  telemetryToken,
		SkipPreflight:   false, // Preflight always runs for manifest mode
		ApplyTuning:     applyTuning,
		Timeout:         timeout,
		Version:         version,
	}

	pool := fwssh.NewPool(30*time.Second, sshKey)
	ep := provisioner.NewEdgeProvisioner(pool)
	return ep.Provision(cmd.Context(), host, config)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

// sshTargetToHost converts a "user@host" string into an inventory.Host.
func sshTargetToHost(sshTarget string) inventory.Host {
	parts := strings.SplitN(sshTarget, "@", 2)
	user := "root"
	address := sshTarget
	if len(parts) == 2 {
		user = parts[0]
		address = parts[1]
	}
	return inventory.Host{
		ExternalIP: address,
		User:       user,
	}
}

// getRemoteExternalIP detects the external IP of the remote host
func preRegisterEdgeLocal(ctx context.Context, foghornAddr, enrollmentToken, preferredNodeID string) (*pb.PreRegisterEdgeResponse, error) {
	return preRegisterEdge(ctx, foghornAddr, enrollmentToken, "", "", preferredNodeID)
}

func preRegisterEdge(ctx context.Context, foghornAddr, enrollmentToken, sshTarget, sshKey, preferredNodeID string) (*pb.PreRegisterEdgeResponse, error) {
	externalIP, _ := getRemoteExternalIP(ctx, sshTarget, sshKey)

	logger := logrus.New()
	logger.SetLevel(logrus.WarnLevel)
	client, err := foghorn.NewGRPCClient(foghorn.GRPCConfig{
		GRPCAddr: foghornAddr,
		Timeout:  15 * time.Second,
		Logger:   logger,
		UseTLS:   true,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to connect to Foghorn at %s: %w", foghornAddr, err)
	}
	defer client.Close()

	return client.PreRegisterEdge(ctx, &pb.PreRegisterEdgeRequest{
		EnrollmentToken: enrollmentToken,
		ExternalIp:      externalIP,
		PreferredNodeId: preferredNodeID,
	})
}

func deriveEdgeNodeName(nodeName, nodeDomain, sshTarget string, isLocal bool) string {
	if trimmed := strings.TrimSpace(nodeName); trimmed != "" {
		return trimmed
	}
	if host := strings.TrimSpace(nodeDomain); host != "" {
		if idx := strings.Index(host, "."); idx > 0 {
			return host[:idx]
		}
		return host
	}
	if isLocal {
		hostname, hostErr := os.Hostname()
		if hostErr != nil {
			return ""
		}
		return strings.TrimSpace(hostname)
	}

	target := strings.TrimSpace(sshTarget)
	if target == "" {
		return ""
	}
	if at := strings.LastIndex(target, "@"); at >= 0 {
		target = target[at+1:]
	}
	if host, port, err := net.SplitHostPort(target); err == nil && host != "" && port != "" {
		target = host
	}
	target = strings.Trim(target, "[]")
	if net.ParseIP(target) != nil {
		return ""
	}
	return target
}

func canonicalEdgeNodeID(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ""
	}
	if net.ParseIP(strings.Trim(trimmed, "[]")) != nil {
		return ""
	}
	candidate := strings.ToLower(trimmed)
	if idx := strings.Index(candidate, "."); idx > 0 {
		candidate = candidate[:idx]
	}
	candidate = pkgdns.SanitizeLabel(candidate)
	if candidate == "default" && !strings.EqualFold(trimmed, "default") {
		return ""
	}
	if !edgeNodeIDPattern.MatchString(candidate) {
		return ""
	}
	return candidate
}

func getRemoteExternalIP(ctx context.Context, sshTarget, sshKey string) (string, error) {
	// Try multiple methods to detect external IP
	methods := []struct {
		cmd  string
		args []string
	}{
		{"curl", []string{"-s", "-4", "ifconfig.me"}},
		{"curl", []string{"-s", "-4", "icanhazip.com"}},
		{"curl", []string{"-s", "-4", "api.ipify.org"}},
	}

	for _, m := range methods {
		_, out, _, err := xexec.RunSSHWithKey(ctx, sshTarget, sshKey, m.cmd, m.args, "")
		if err == nil {
			ip := strings.TrimSpace(out)
			if ip != "" && !strings.Contains(ip, "<") { // Basic sanity check
				return ip, nil
			}
		}
	}

	return "", fmt.Errorf("could not detect external IP via any method")
}

// registerEdgeNode registers an edge node in Quartermaster.
// Platform-admin direct path — uses the gitops-sourced SERVICE_TOKEN for
// Quartermaster auth. The normal edge bootstrap flow (Bridge +
// enrollment token) does not need this function.
func registerEdgeNode(cmd *cobra.Command, cliCtx fwcfg.Context, nodeName, clusterID, externalIP, region string) error {
	cfg, err := fwcfg.Load()
	if err != nil {
		return err
	}
	jwt, err := fwcredentials.ResolveUserAuth(fwcfg.OSEnv{}, fwcredentials.DefaultStore())
	if err != nil {
		return err
	}
	token, err := platformauth.ResolveManifestServiceToken(cmd.Context(), cliCtx, cfg)
	if err != nil {
		return err
	}
	cliCtx.Auth = fwcfg.Auth{JWT: jwt, ServiceToken: token}
	// Create Quartermaster gRPC client
	qmClient, err := quartermaster.NewGRPCClient(quartermaster.GRPCConfig{
		GRPCAddr:      cliCtx.Endpoints.QuartermasterGRPCAddr,
		Timeout:       30 * time.Second,
		ServiceToken:  cliCtx.Auth.ServiceToken,
		AllowInsecure: true,
	})
	if err != nil {
		return fmt.Errorf("failed to connect to Quartermaster: %w", err)
	}
	defer qmClient.Close()

	nodeID := canonicalEdgeNodeID(nodeName)
	if nodeID == "" {
		nodeID = uuid.New().String()
	}

	// Create node request
	req := &pb.CreateNodeRequest{
		NodeId:    nodeID,
		ClusterId: clusterID,
		NodeName:  nodeName,
		NodeType:  infra.NodeTypeEdge, // This triggers DNS sync for "edge" service type
	}

	// Set optional fields
	if externalIP != "" {
		req.ExternalIp = &externalIP
	}
	if region != "" {
		req.Region = &region
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if cliCtx.Auth.JWT != "" {
		ctx = context.WithValue(ctx, ctxkeys.KeyJWTToken, cliCtx.Auth.JWT)
	}

	resp, err := qmClient.CreateNode(ctx, req)
	if err != nil {
		return fmt.Errorf("failed to create node: %w", err)
	}

	ux.Success(cmd.OutOrStdout(), fmt.Sprintf("Node registered: %s (ID: %s)", nodeName, resp.GetNode().GetNodeId()))
	ux.Success(cmd.OutOrStdout(), "Node registered (DNS will be synced by Navigator reconciler)")

	return nil
}

// fetchCertFromNavigator fetches a TLS certificate from Navigator service
func fetchCertFromNavigator(cmd *cobra.Command, cliCtx fwcfg.Context, domain, email string) (certPEM, keyPEM string, err error) {
	// Create Navigator gRPC client
	navClient, err := navigator.NewClient(navigator.Config{
		Addr:          cliCtx.Endpoints.NavigatorGRPCAddr,
		Timeout:       120 * time.Second, // ACME can take a while
		ServiceToken:  cliCtx.Auth.ServiceToken,
		AllowInsecure: os.Getenv("GRPC_ALLOW_INSECURE") != "false",
		CACertFile:    os.Getenv("GRPC_TLS_CA_PATH"),
		ServerName:    os.Getenv("GRPC_TLS_SERVER_NAME"),
	})
	if err != nil {
		return "", "", fmt.Errorf("failed to connect to Navigator: %w", err)
	}
	defer navClient.Close()

	// Create context with timeout
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	// Request certificate issuance
	fmt.Fprintf(cmd.OutOrStdout(), "  - Requesting certificate for %s (this may take a minute)...\n", domain)
	resp, err := navClient.IssueCertificate(ctx, &pb.IssueCertificateRequest{
		Domain: domain,
		Email:  email,
	})
	if err != nil {
		return "", "", fmt.Errorf("certificate issuance failed: %w", err)
	}

	if !resp.GetSuccess() {
		errMsg := resp.GetError()
		if errMsg == "" {
			errMsg = resp.GetMessage()
		}
		return "", "", fmt.Errorf("certificate issuance failed: %s", errMsg)
	}

	ux.Success(cmd.OutOrStdout(), fmt.Sprintf("Certificate issued for %s", domain))
	return resp.GetCertPem(), resp.GetKeyPem(), nil
}

func newEdgeStatusCmd() *cobra.Command {
	var dir string
	var domain string
	var sshTarget string
	var sshKey string
	cmd := &cobra.Command{Use: "status", Short: "Show local and registry health", RunE: func(cmd *cobra.Command, args []string) error {
		if dir == "" {
			dir = "."
		}
		envFile := ".edge.env"
		deployMode := detectEdgeMode(cmd.Context(), dir, envFile, sshTarget, sshKey)

		// Multi-line state summary sourced from .edge.env + runtime probes.
		if envDomain := readRemoteEnvFileKey(cmd.Context(), sshTarget, sshKey, dir, envFile, "EDGE_DOMAIN"); envDomain != "" && domain == "" {
			domain = envDomain
		}
		nodeID := readRemoteEnvFileKey(cmd.Context(), sshTarget, sshKey, dir, envFile, "NODE_ID")
		telemetryURL := readRemoteEnvFileKey(cmd.Context(), sshTarget, sshKey, dir, envFile, "TELEMETRY_URL")
		ux.Subheading(cmd.OutOrStdout(), "Edge node state:")
		fmt.Fprintf(cmd.OutOrStdout(), "  mode:      %s\n", deployMode)
		if nodeID != "" {
			fmt.Fprintf(cmd.OutOrStdout(), "  node id:   %s\n", nodeID)
		}
		if domain != "" {
			fmt.Fprintf(cmd.OutOrStdout(), "  domain:    %s\n", domain)
		}
		if telemetryURL != "" {
			fmt.Fprintf(cmd.OutOrStdout(), "  telemetry: %s\n", telemetryURL)
		}
		fmt.Fprintln(cmd.OutOrStdout(), "")

		var out, errOut string
		var err error
		if deployMode == "native" {
			edgeOS := detectEdgeOS(cmd.Context(), sshTarget, sshKey)
			var statusCmd string
			if edgeOS == "darwin" {
				statusCmd = "launchctl print system/com.livepeer.frameworks.caddy system/com.livepeer.frameworks.helmsman system/com.livepeer.frameworks.mistserver 2>&1 || launchctl list | grep com.livepeer.frameworks"
			} else {
				statusCmd = "systemctl status frameworks-caddy frameworks-helmsman frameworks-mistserver --no-pager"
			}
			if strings.TrimSpace(sshTarget) != "" {
				_, out, errOut, err = xexec.RunSSHWithKey(cmd.Context(), sshTarget, sshKey, "sh", []string{"-c", statusCmd}, "")
			} else {
				_, out, errOut, err = xexec.Run(cmd.Context(), "sh", []string{"-c", statusCmd}, "")
			}
			if err != nil {
				tool := "systemctl"
				if edgeOS == "darwin" {
					tool = "launchctl"
				}
				ux.ErrorWithOutput(cmd.ErrOrStderr(), fmt.Errorf("%s status: %w", tool, err), "confirm unit names match the deployed stack (frameworks-caddy / frameworks-helmsman / frameworks-mistserver)", out, errOut)
			} else {
				fmt.Fprint(cmd.OutOrStdout(), out)
			}
		} else {
			// Docker: compose ps
			compose := "docker-compose.edge.yml"
			if strings.TrimSpace(sshTarget) != "" {
				_, out, errOut, err = xexec.RunSSHWithKey(cmd.Context(), sshTarget, sshKey, "docker", []string{"compose", "-f", compose, "--env-file", envFile, "ps"}, dir)
			} else {
				_, out, errOut, err = xexec.Run(cmd.Context(), "docker", []string{"compose", "-f", compose, "--env-file", envFile, "ps"}, dir)
			}
			if err != nil {
				ux.ErrorWithOutput(cmd.ErrOrStderr(), fmt.Errorf("docker compose ps: %w", err), "run 'docker info' to confirm the daemon is reachable, or re-run with --dir pointing at the deployment directory", out, errOut)
			} else {
				fmt.Fprint(cmd.OutOrStdout(), out)
			}
		}
		// HTTPS health
		if strings.TrimSpace(domain) != "" {
			url := "https://" + domain + "/health"
			httpClient := &http.Client{Timeout: 3 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}}
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
			if err != nil {
				cancel()
				ux.Fail(cmd.OutOrStdout(), fmt.Sprintf("HTTPS %s: %v", url, err))
			} else {
				resp, err := httpClient.Do(req)
				cancel()
				switch {
				case err != nil:
					ux.Fail(cmd.OutOrStdout(), fmt.Sprintf("HTTPS %s: %v", url, err))
				case resp.StatusCode == 200:
					if resp.Body != nil {
						_ = resp.Body.Close()
					}
					ux.Success(cmd.OutOrStdout(), fmt.Sprintf("HTTPS %s (http 200)", url))
				default:
					if resp.Body != nil {
						_ = resp.Body.Close()
					}
					ux.Fail(cmd.OutOrStdout(), fmt.Sprintf("HTTPS %s (http %d)", url, resp.StatusCode))
				}
			}
		}
		return nil
	}}
	cmd.Flags().StringVar(&dir, "dir", ".", "directory with docker-compose.edge.yml and .edge.env")
	cmd.Flags().StringVar(&domain, "domain", "", "override EDGE_DOMAIN for HTTPS check")
	cmd.Flags().StringVar(&sshTarget, "ssh", "", "run remotely on user@host via SSH")
	cmd.Flags().StringVar(&sshKey, "ssh-key", "", "SSH private key path")
	return cmd
}

func newEdgeUpdateCmd() *cobra.Command {
	var dir string
	var sshTarget string
	var sshKey string
	cmd := &cobra.Command{Use: "update", Short: "Pull and restart edge services", RunE: func(cmd *cobra.Command, args []string) error {
		if dir == "" {
			dir = "."
		}
		out := cmd.OutOrStdout()
		target := sshTarget
		if target == "" {
			target = "local"
		}
		ux.Heading(out, fmt.Sprintf("Updating edge services on %s", target))
		envFile := ".edge.env"
		deployMode := detectEdgeMode(cmd.Context(), dir, envFile, sshTarget, sshKey)

		if deployMode == "native" {
			edgeOS := detectEdgeOS(cmd.Context(), sshTarget, sshKey)
			var restartCmd string
			if edgeOS == "darwin" {
				restartCmd = "launchctl kickstart -k system/com.livepeer.frameworks.mistserver && launchctl kickstart -k system/com.livepeer.frameworks.helmsman && launchctl kickstart -k system/com.livepeer.frameworks.caddy"
			} else {
				restartCmd = "systemctl restart frameworks-mistserver frameworks-helmsman frameworks-caddy"
			}
			if strings.TrimSpace(sshTarget) != "" {
				if _, out, errOut, err := xexec.RunSSHWithKey(cmd.Context(), sshTarget, sshKey, "sh", []string{"-c", restartCmd}, ""); err != nil {
					ux.ErrorWithOutput(cmd.ErrOrStderr(), fmt.Errorf("restart: %w", err), "check service state with 'frameworks edge status' and unit/compose file permissions", out, errOut)
					return err
				}
			} else if _, out, errOut, err := xexec.Run(cmd.Context(), "sh", []string{"-c", restartCmd}, ""); err != nil {
				ux.ErrorWithOutput(cmd.ErrOrStderr(), fmt.Errorf("restart: %w", err), "check service state with 'frameworks edge status' and unit/compose file permissions", out, errOut)
				return err
			}
			ux.Success(out, "Edge services restarted (native)")
		} else {
			compose := "docker-compose.edge.yml"
			// pull
			if strings.TrimSpace(sshTarget) != "" {
				if _, out, errOut, err := xexec.RunSSHWithKey(cmd.Context(), sshTarget, sshKey, "docker", []string{"compose", "-f", compose, "--env-file", envFile, "pull"}, dir); err != nil {
					ux.ErrorWithOutput(cmd.ErrOrStderr(), fmt.Errorf("compose pull: %w", err), "confirm the registry is reachable from this host and the image tags in docker-compose.edge.yml are valid", out, errOut)
					return err
				}
			} else if _, out, errOut, err := xexec.Run(cmd.Context(), "docker", []string{"compose", "-f", compose, "--env-file", envFile, "pull"}, dir); err != nil {
				ux.ErrorWithOutput(cmd.ErrOrStderr(), fmt.Errorf("compose pull: %w", err), "confirm the registry is reachable from this host and the image tags in docker-compose.edge.yml are valid", out, errOut)
				return err
			}
			// up -d
			if strings.TrimSpace(sshTarget) != "" {
				if _, out, errOut, err := xexec.RunSSHWithKey(cmd.Context(), sshTarget, sshKey, "docker", []string{"compose", "-f", compose, "--env-file", envFile, "up", "-d"}, dir); err != nil {
					ux.ErrorWithOutput(cmd.ErrOrStderr(), fmt.Errorf("compose up: %w", err), "docker daemon rejected the stack — inspect the output for a specific service error", out, errOut)
					return err
				}
			} else if _, out, errOut, err := xexec.Run(cmd.Context(), "docker", []string{"compose", "-f", compose, "--env-file", envFile, "up", "-d"}, dir); err != nil {
				ux.ErrorWithOutput(cmd.ErrOrStderr(), fmt.Errorf("compose up: %w", err), "docker daemon rejected the stack — inspect the output for a specific service error", out, errOut)
				return err
			}
			ux.Success(out, "Edge containers updated")
		}
		return nil
	}}
	cmd.Flags().StringVar(&dir, "dir", ".", "directory with docker-compose.edge.yml and .edge.env")
	cmd.Flags().StringVar(&sshTarget, "ssh", "", "run remotely on user@host via SSH")
	cmd.Flags().StringVar(&sshKey, "ssh-key", "", "SSH private key path")
	return cmd
}

func newEdgeCertCmd() *cobra.Command {
	var dir string
	var domain string
	var sshTarget string
	var sshKey string
	var reload bool
	cmd := &cobra.Command{Use: "cert", Short: "Show TLS expiry and optionally reload Caddy", RunE: func(cmd *cobra.Command, args []string) error {
		out := cmd.OutOrStdout()
		if strings.TrimSpace(domain) == "" {
			if dir == "" {
				dir = "."
			}
			domain = readRemoteEnvFileKey(cmd.Context(), sshTarget, sshKey, dir, ".edge.env", "EDGE_DOMAIN")
		}
		if strings.TrimSpace(domain) == "" {
			ux.Warn(out, "No domain provided and EDGE_DOMAIN not set in .edge.env")
		} else {
			ux.Heading(out, fmt.Sprintf("TLS certificate for %s", domain))
			exp, issuer, err := tlsExpiry(domain)
			if err != nil {
				ux.FormatError(out, fmt.Errorf("TLS check %s: %w", domain, err), "ensure ports 443 is reachable and DNS resolves")
			} else {
				days := int(time.Until(exp).Hours() / 24)
				detail := fmt.Sprintf("expires %s (%d days); issuer=%s", exp.Format(time.RFC3339), days, issuer)
				if days < 30 {
					ux.Warn(out, detail+" — expiring within 30 days")
				} else {
					ux.Success(out, detail)
				}
			}
		}
		if reload {
			deployMode := detectEdgeMode(cmd.Context(), dir, ".edge.env", sshTarget, sshKey)
			var outStr, errOut string
			var err error
			if deployMode == "native" {
				edgeOS := detectEdgeOS(cmd.Context(), sshTarget, sshKey)
				if edgeOS == "darwin" {
					reloadCmd := "launchctl kickstart -k system/com.livepeer.frameworks.caddy"
					if strings.TrimSpace(sshTarget) != "" {
						_, outStr, errOut, err = xexec.RunSSHWithKey(cmd.Context(), sshTarget, sshKey, "sh", []string{"-c", reloadCmd}, "")
					} else {
						_, outStr, errOut, err = xexec.Run(cmd.Context(), "sh", []string{"-c", reloadCmd}, "")
					}
				} else if strings.TrimSpace(sshTarget) != "" {
					_, outStr, errOut, err = xexec.RunSSHWithKey(cmd.Context(), sshTarget, sshKey, "systemctl", []string{"reload", "frameworks-caddy"}, "")
				} else {
					_, outStr, errOut, err = xexec.Run(cmd.Context(), "systemctl", []string{"reload", "frameworks-caddy"}, "")
				}
			} else {
				if strings.TrimSpace(sshTarget) != "" {
					_, outStr, errOut, err = xexec.RunSSHWithKey(cmd.Context(), sshTarget, sshKey, "docker", []string{"exec", "edge-proxy", "caddy", "reload", "--config", "/etc/caddy/Caddyfile"}, dir)
				} else {
					_, outStr, errOut, err = xexec.Run(cmd.Context(), "docker", []string{"exec", "edge-proxy", "caddy", "reload", "--config", "/etc/caddy/Caddyfile"}, dir)
				}
			}
			if err != nil {
				ux.ErrorWithOutput(cmd.ErrOrStderr(), fmt.Errorf("caddy reload: %w", err), "confirm the edge-proxy container is running and /etc/caddy/Caddyfile is valid", outStr, errOut)
				return err
			}
			ux.Success(out, "Caddy reloaded")
		}
		return nil
	}}
	cmd.Flags().StringVar(&dir, "dir", ".", "directory with .edge.env")
	cmd.Flags().StringVar(&domain, "domain", "", "edge domain to check")
	cmd.Flags().StringVar(&sshTarget, "ssh", "", "run remotely on user@host via SSH for reload")
	cmd.Flags().StringVar(&sshKey, "ssh-key", "", "SSH private key path")
	cmd.Flags().BoolVar(&reload, "reload", false, "reload Caddy inside edge-proxy container")
	return cmd
}

func newEdgeLogsCmd() *cobra.Command {
	var dir string
	var follow bool
	var tail int
	var sshTarget string
	var sshKey string
	cmd := &cobra.Command{Use: "logs [service]", Short: "Tail logs for proxy/mist/helmsman", Args: cobra.RangeArgs(0, 1), RunE: func(cmd *cobra.Command, args []string) error {
		if dir == "" {
			dir = "."
		}
		envFile := ".edge.env"
		deployMode := detectEdgeMode(cmd.Context(), dir, envFile, sshTarget, sshKey)
		svc := ""
		if len(args) == 1 {
			svc = args[0]
		}

		var out, errOut string
		var err error

		if deployMode == "native" {
			edgeOS := detectEdgeOS(cmd.Context(), sshTarget, sshKey)
			var logArgs []string

			if edgeOS == "darwin" {
				// macOS: read from launchd log files
				var logFiles []string
				if svc != "" {
					label := "com.livepeer.frameworks." + svc
					logFiles = append(logFiles, darwinLogDir+"/"+label+".log", darwinLogDir+"/"+label+".err")
				} else {
					for _, s := range []string{"caddy", "helmsman", "mistserver"} {
						label := "com.livepeer.frameworks." + s
						logFiles = append(logFiles, darwinLogDir+"/"+label+".log")
					}
				}
				tailFlag := fmt.Sprintf("-n %d", tail)
				if follow {
					logArgs = []string{"-c", fmt.Sprintf("tail %s -f %s", tailFlag, strings.Join(logFiles, " "))}
				} else {
					logArgs = []string{"-c", fmt.Sprintf("tail %s %s", tailFlag, strings.Join(logFiles, " "))}
				}
			} else {
				// Linux: use journalctl
				unit := ""
				if svc != "" {
					unit = "frameworks-" + svc
				} else {
					unit = "frameworks-caddy frameworks-helmsman frameworks-mistserver"
				}
				followFlag := ""
				if follow {
					followFlag = "-f"
				}
				logArgs = []string{"-c", fmt.Sprintf("journalctl --no-pager -n %d %s -u %s", tail, followFlag, unit)}
			}

			if strings.TrimSpace(sshTarget) != "" {
				_, out, errOut, err = xexec.RunSSHWithKey(cmd.Context(), sshTarget, sshKey, "sh", logArgs, "")
			} else {
				_, out, errOut, err = xexec.Run(cmd.Context(), "sh", logArgs, "")
			}
		} else {
			compose := "docker-compose.edge.yml"
			arg := []string{"compose", "-f", compose, "--env-file", envFile, "logs"}
			if follow {
				arg = append(arg, "-f")
			}
			if tail > 0 {
				arg = append(arg, "--tail", fmt.Sprintf("%d", tail))
			}
			if svc != "" {
				arg = append(arg, svc)
			}
			if strings.TrimSpace(sshTarget) != "" {
				_, out, errOut, err = xexec.RunSSHWithKey(cmd.Context(), sshTarget, sshKey, "docker", arg, dir)
			} else {
				_, out, errOut, err = xexec.Run(cmd.Context(), "docker", arg, dir)
			}
		}

		if err != nil {
			ux.ErrorWithOutput(cmd.ErrOrStderr(), fmt.Errorf("fetch logs: %w", err), "confirm the service is running via 'frameworks edge status'", out, errOut)
			return err
		}
		fmt.Fprint(cmd.OutOrStdout(), out)
		return nil
	}}
	cmd.Flags().StringVar(&dir, "dir", ".", "directory with docker-compose.edge.yml and .edge.env")
	cmd.Flags().BoolVar(&follow, "follow", false, "follow logs (tail)")
	cmd.Flags().IntVar(&tail, "tail", 200, "number of lines to show")
	cmd.Flags().StringVar(&sshTarget, "ssh", "", "run remotely on user@host via SSH")
	cmd.Flags().StringVar(&sshKey, "ssh-key", "", "SSH private key path")
	return cmd
}

// edgeDoctorHTTPS carries the /health probe result. Status == 0 with
// Error == "" means the probe was not attempted (no EDGE_DOMAIN).
type edgeDoctorHTTPS struct {
	URL    string `json:"url,omitempty"`
	Status int    `json:"status,omitempty"`
	Error  string `json:"error,omitempty"`
}

// edgeDoctorJSONReport is the --output json shape for `edge doctor`.
type edgeDoctorJSONReport struct {
	Mode            string                `json:"mode"`
	Domain          string                `json:"domain,omitempty"`
	HostChecks      []preflight.Check     `json:"host_checks"`
	ServiceChecks   []readiness.EdgeCheck `json:"service_checks"`
	ServiceProbeErr string                `json:"service_probe_error,omitempty"`
	HTTPS           edgeDoctorHTTPS       `json:"https"`
	StreamChecks    []readiness.EdgeCheck `json:"stream_checks"`
	Warnings        []readiness.Warning   `json:"warnings"`
	NextSteps       []ux.NextStep         `json:"next_steps"`
	OK              bool                  `json:"ok"`
}

func newEdgeDoctorCmd() *cobra.Command {
	var domain string
	var dir string
	cmd := &cobra.Command{Use: "doctor", Short: "Run diagnostics and remediation hints", RunE: func(cmd *cobra.Command, args []string) error {
		ctx := cmd.Context()
		jsonMode := output == "json"
		// JSON mode discards section output and encodes the full payload
		// at the end — the diagnostic pipeline must stay identical.
		var textOut = cmd.OutOrStdout()
		if jsonMode {
			textOut = io.Discard
		}

		results := []preflight.Check{}
		if domain != "" {
			results = append(results, preflight.DNSResolution(ctx, domain))
		}
		results = append(results, preflight.HasDocker(ctx)...)
		results = append(results, preflight.HasServiceManager())
		if runtime.GOOS == "linux" {
			results = append(results, preflight.LinuxSysctlChecks()...)
			results = append(results, preflight.ShmSize())
		}
		results = append(results, preflight.UlimitNoFile())
		results = append(results, preflight.PortChecks(ctx)...)

		okCount := 0
		ux.Subheading(textOut, "Host Checks:")
		for _, r := range results {
			label := r.Name + ":"
			line := fmt.Sprintf("%-18s %s", label, r.Detail)
			if r.Error != "" {
				line = fmt.Sprintf("%-18s %-40s (%s)", label, r.Detail, r.Error)
			}
			if r.OK {
				ux.Success(textOut, line)
				okCount++
			} else {
				ux.Fail(textOut, line)
			}
		}
		fmt.Fprintf(textOut, "Summary: %d/%d checks passed\n\n", okCount, len(results))

		// Service status
		if dir == "" {
			dir = "."
		}
		envFile := ".edge.env"
		deployMode := detectEdgeMode(ctx, dir, envFile, "", "")
		var (
			serviceChecks []readiness.EdgeCheck
			probeErr      string
		)
		if deployMode == "native" {
			fmt.Fprintln(textOut, "Native Services:")
			var statusCmd string
			if runtime.GOOS == "darwin" {
				statusCmd = "launchctl list | grep com.livepeer.frameworks"
			} else {
				statusCmd = "systemctl status frameworks-caddy frameworks-helmsman frameworks-mistserver --no-pager 2>&1 | head -30"
			}
			_, out, _, err := xexec.Run(ctx, "sh", []string{"-c", statusCmd}, "")
			if err != nil {
				fmt.Fprintf(textOut, " service status error: %v\n", err)
				probeErr = err.Error()
			} else {
				fmt.Fprint(textOut, out)
				serviceChecks = parseEdgeServiceStatus(out, "native")
			}
		} else {
			compose := "docker-compose.edge.yml"
			_, out, errOut, err := xexec.Run(ctx, "docker", []string{"compose", "-f", compose, "--env-file", envFile, "ps"}, dir)
			fmt.Fprintln(textOut, "Compose Services:")
			if err != nil {
				fmt.Fprintf(textOut, " compose ps error: %v\n%s\n", err, errOut)
				probeErr = err.Error()
			} else {
				fmt.Fprint(textOut, out)
				serviceChecks = parseEdgeServiceStatus(out, "docker")
			}
		}

		// HTTPS health
		if domain == "" {
			domain = readRemoteEnvFileKey(ctx, "", "", dir, envFile, "EDGE_DOMAIN")
		}
		httpsStatus := 0
		httpsError := ""
		httpsURL := ""
		if domain != "" {
			httpsURL = "https://" + domain + "/health"
			httpClient := &http.Client{Timeout: 3 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}}
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, httpsURL, nil)
			if err != nil {
				cancel()
				httpsError = err.Error()
				fmt.Fprintln(textOut, "HTTPS Health:")
				fmt.Fprintf(textOut, " %s error: %v\n", httpsURL, err)
			} else {
				resp, err := httpClient.Do(req)
				cancel()
				fmt.Fprintln(textOut, "HTTPS Health:")
				if err != nil {
					httpsError = err.Error()
					fmt.Fprintf(textOut, " %s error: %v\n", httpsURL, err)
				} else {
					if resp.Body != nil {
						_ = resp.Body.Close()
					}
					httpsStatus = resp.StatusCode
					fmt.Fprintf(textOut, " %s http %d\n", httpsURL, resp.StatusCode)
				}
			}
		}
		// Stream health quick-check via MistServer analyzers
		fmt.Fprintln(textOut, "\nStream Health:")
		var streamChecks []readiness.EdgeCheck
		func() {
			diagCtx, diagCancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer diagCancel()
			deployMode := detectEdgeMode(diagCtx, dir, ".edge.env", "", "")
			localRunner := fwssh.NewLocalRunner("")
			ar := mistdiag.NewAnalyzerRunner(localRunner, deployMode)

			streams, err := mistdiag.DiscoverStreams(diagCtx, localRunner, deployMode)
			if err != nil {
				fmt.Fprintf(textOut, " - Could not query MistServer: %v\n", err)
				return
			}
			if len(streams) == 0 {
				fmt.Fprintln(textOut, " - No active streams (skipped)")
				return
			}
			for _, s := range streams {
				result, err := ar.Validate(diagCtx, "HLS", s.HLSURL, 5)
				label := fmt.Sprintf("%-24s", s.Name+":")
				if err != nil {
					ux.Warn(textOut, fmt.Sprintf("%s error: %v", label, err))
					streamChecks = append(streamChecks, readiness.EdgeCheck{Name: s.Name, OK: false, Detail: err.Error()})
					continue
				}
				if result.OK {
					ux.Success(textOut, fmt.Sprintf("%s HLS OK", label))
					streamChecks = append(streamChecks, readiness.EdgeCheck{Name: s.Name, OK: true, Detail: "HLS OK"})
				} else {
					msg := result.Summary()
					ux.Fail(textOut, fmt.Sprintf("%s HLS FAIL (%s)", label, msg))
					streamChecks = append(streamChecks, readiness.EdgeCheck{Name: s.Name, OK: false, Detail: msg})
				}
			}
		}()

		// Adaptive hints from readiness — only the remediations relevant to
		// what actually failed, replacing the previous four static hints.
		hostChecks := make([]readiness.EdgeCheck, 0, len(results))
		for _, r := range results {
			detail := r.Detail
			if r.Error != "" {
				detail = r.Detail + " (" + r.Error + ")"
			}
			hostChecks = append(hostChecks, readiness.EdgeCheck{Name: r.Name, OK: r.OK, Detail: detail})
		}
		hasEnv := true
		if _, statErr := os.Stat(dir + string(os.PathSeparator) + envFile); statErr != nil {
			hasEnv = false
		}
		report := readiness.EdgeReadiness(readiness.EdgeInputs{
			HasEnv:          hasEnv,
			Domain:          domain,
			Mode:            deployMode,
			HostChecks:      hostChecks,
			ServiceChecks:   serviceChecks,
			StreamChecks:    streamChecks,
			ServiceProbeErr: probeErr,
			HTTPSStatus:     httpsStatus,
			HTTPSError:      httpsError,
		})
		var steps []ux.NextStep
		for _, w := range report.Warnings {
			if w.Remediation.Cmd == "" && w.Remediation.Why == "" {
				continue
			}
			steps = append(steps, ux.NextStep{Cmd: w.Remediation.Cmd, Why: w.Remediation.Why})
		}

		if jsonMode {
			payload := edgeDoctorJSONReport{
				Mode:          deployMode,
				Domain:        domain,
				HostChecks:    results,
				ServiceChecks: serviceChecks,
				StreamChecks:  streamChecks,
				HTTPS: edgeDoctorHTTPS{
					URL:    httpsURL,
					Status: httpsStatus,
					Error:  httpsError,
				},
				ServiceProbeErr: probeErr,
				Warnings:        report.Warnings,
				NextSteps:       steps,
				OK:              report.OK(),
			}
			enc := json.NewEncoder(cmd.OutOrStdout())
			enc.SetIndent("", "  ")
			return enc.Encode(payload)
		}

		out := cmd.OutOrStdout()
		if report.OK() && len(steps) == 0 {
			ux.Success(out, "Edge node healthy — all checks passed")
			return nil
		}
		ux.PrintNextSteps(out, steps)
		return nil
	}}
	cmd.Flags().StringVar(&domain, "domain", "", "edge domain to validate (DNS and HTTPS)")
	cmd.Flags().StringVar(&dir, "dir", ".", "directory with edge templates")
	return cmd
}

func newEdgeModeCmd() *cobra.Command {
	var sshTarget string
	var sshKey string
	var reason string
	cmd := &cobra.Command{Use: "mode [normal|draining|maintenance]", Short: "Get or set node operational mode", Long: `Query or change the edge node's operational mode via Helmsman.

Without arguments, prints the current mode. With an argument, requests a mode change:
  normal      - accept new viewers and ingest
  draining    - finish existing sessions, reject new ones
  maintenance - fully isolated, no traffic

The mode change is sent upstream to Foghorn for validation. Foghorn applies
the change and pushes an updated ConfigSeed back to the node.`, Args: cobra.RangeArgs(0, 1), RunE: func(cmd *cobra.Command, args []string) error {
		helmsmanBase := "http://localhost:18007"

		if len(args) == 0 {
			curlArgs := []string{"-s", "-f", helmsmanBase + "/node/mode"}
			var outStr, errOut string
			var err error
			if strings.TrimSpace(sshTarget) != "" {
				_, outStr, errOut, err = xexec.RunSSHWithKey(cmd.Context(), sshTarget, sshKey, "curl", curlArgs, "")
			} else {
				_, outStr, errOut, err = xexec.Run(cmd.Context(), "curl", curlArgs, "")
			}
			if err != nil {
				ux.ErrorWithOutput(cmd.ErrOrStderr(), fmt.Errorf("get node mode: %w", err), "helmsman at "+helmsmanBase+" unreachable — run 'frameworks edge status' to confirm the node is up", "", errOut)
				return err
			}
			fmt.Fprint(cmd.OutOrStdout(), outStr+"\n")
			return nil
		}

		mode := strings.ToLower(args[0])
		switch mode {
		case "normal", "draining", "maintenance":
		default:
			return fmt.Errorf("invalid mode %q: must be normal, draining, or maintenance", mode)
		}

		ux.Heading(cmd.OutOrStdout(), fmt.Sprintf("Setting node mode to %s", mode))
		body := fmt.Sprintf(`{"mode":%q,"reason":%q}`, mode, reason)
		curlArgs := []string{"-s", "-f", "-X", "POST", "-H", "Content-Type: application/json", "-d", body, helmsmanBase + "/node/mode"}
		var outStr, errOut string
		var err error
		if strings.TrimSpace(sshTarget) != "" {
			_, outStr, errOut, err = xexec.RunSSHWithKey(cmd.Context(), sshTarget, sshKey, "curl", curlArgs, "")
		} else {
			_, outStr, errOut, err = xexec.Run(cmd.Context(), "curl", curlArgs, "")
		}
		if err != nil {
			ux.ErrorWithOutput(cmd.ErrOrStderr(), fmt.Errorf("set node mode: %w", err), "helmsman at "+helmsmanBase+" refused the mode change — check 'frameworks edge logs helmsman'", "", errOut)
			return err
		}
		fmt.Fprint(cmd.OutOrStdout(), outStr+"\n")
		ux.Success(cmd.OutOrStdout(), fmt.Sprintf("Node mode set to %s", mode))
		return nil
	}}
	cmd.Flags().StringVar(&reason, "reason", "cli_request", "reason for mode change (for audit)")
	cmd.Flags().StringVar(&sshTarget, "ssh", "", "run on remote edge node via SSH (user@host)")
	cmd.Flags().StringVar(&sshKey, "ssh-key", "", "SSH private key path")
	return cmd
}

// tlsExpiry fetches TLS certificate NotAfter for a domain.
func tlsExpiry(domain string) (time.Time, string, error) {
	dialer := &tls.Dialer{Config: &tls.Config{ServerName: domain}}
	conn, err := dialer.Dial("tcp", domain+":443")
	if err != nil {
		return time.Time{}, "", err
	}
	defer conn.Close()
	tlsConn, ok := conn.(*tls.Conn)
	if !ok {
		return time.Time{}, "", fmt.Errorf("not a TLS connection")
	}
	state := tlsConn.ConnectionState()
	if len(state.PeerCertificates) == 0 {
		return time.Time{}, "", fmt.Errorf("no peer certificates")
	}
	cert := state.PeerCertificates[0]
	issuer := ""
	if cert.Issuer.CommonName != "" {
		issuer = cert.Issuer.CommonName
	} else {
		issuer = cert.Issuer.String()
	}
	return cert.NotAfter, issuer, nil
}

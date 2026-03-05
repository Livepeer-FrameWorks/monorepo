package cmd

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	fwcfg "frameworks/cli/internal/config"
	"frameworks/cli/internal/preflight"
	"frameworks/cli/internal/templates"
	"frameworks/cli/internal/xexec"
	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/mistdiag"
	"frameworks/cli/pkg/provisioner"
	fwssh "frameworks/cli/pkg/ssh"
	"frameworks/pkg/clients/foghorn"
	"frameworks/pkg/clients/navigator"
	"frameworks/pkg/clients/quartermaster"
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
	edge.AddCommand(newEdgeDiagnoseCmd())
	edge.AddCommand(newEdgeModeCmd())
	edge.AddCommand(newEdgeDeployCmd())
	return edge
}

func newEdgePreflightCmd() *cobra.Command {
	var domain string
	cmd := &cobra.Command{Use: "preflight", Short: "Check host readiness (DNS/ports/sysctl/limits)", RunE: func(cmd *cobra.Command, args []string) error {
		ctx := cmd.Context()
		// Gather checks
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

		// Print
		okCount := 0
		for _, r := range results {
			mark := "✗"
			if r.OK {
				mark = "✓"
				okCount++
			}
			if r.Error != "" {
				fmt.Fprintf(cmd.OutOrStdout(), " %s %-18s %-40s (%s)\n", mark, r.Name+":", r.Detail, r.Error)
			} else {
				fmt.Fprintf(cmd.OutOrStdout(), " %s %-18s %s\n", mark, r.Name+":", r.Detail)
			}
		}
		fmt.Fprintf(cmd.OutOrStdout(), "\nSummary: %d/%d checks passed\n", okCount, len(results))
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
		if runtime.GOOS == "darwin" {
			fmt.Fprintln(cmd.OutOrStdout(), "macOS detected. Network tuning uses different mechanisms:")
			fmt.Fprintln(cmd.OutOrStdout(), "  - File descriptors: launchctl limit maxfiles 1048576 1048576")
			fmt.Fprintln(cmd.OutOrStdout(), "  - Socket buffers: sysctl -w kern.ipc.maxsockbuf=16777216")
			fmt.Fprintln(cmd.OutOrStdout(), "  - Listen backlog: sysctl -w kern.ipc.somaxconn=8192")
			fmt.Fprintln(cmd.OutOrStdout(), "\nThese require sudo and reset on reboot unless added to /etc/sysctl.conf.")
			return nil
		}

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
				fmt.Fprintf(cmd.OutOrStdout(), "Failed to write %s: %v\n", sysctlPath, err)
			} else {
				fmt.Fprintf(cmd.OutOrStdout(), "Wrote %s\n", sysctlPath)
			}
			if err := os.WriteFile(limitsPath, []byte(limits), 0o644); err != nil {
				fmt.Fprintf(cmd.OutOrStdout(), "Failed to write %s: %v\n", limitsPath, err)
			} else {
				fmt.Fprintf(cmd.OutOrStdout(), "Wrote %s\n", limitsPath)
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
	cmd := &cobra.Command{Use: "init", Short: ".edge.env + templates (compose, Caddyfile)", RunE: func(cmd *cobra.Command, args []string) error {
		cfg, _, err := fwcfg.Load()
		if err != nil {
			return err
		}
		cliCtx := fwcfg.GetCurrent(cfg)
		if target == "" {
			target = "."
		}

		// PreRegisterEdge: if enrollment token is provided but domain is not,
		// call Foghorn to get an assigned domain.
		var preRegNodeID string
		var preRegFoghornAddr string
		var preRegCertPEM, preRegKeyPEM string
		if enrollmentToken != "" && domain == "" {
			addr := foghornAddr
			if addr == "" {
				addr = cliCtx.Endpoints.FoghornGRPCAddr
			}
			if addr == "" {
				return fmt.Errorf("--foghorn-addr is required when using --enrollment-token without --domain")
			}
			fmt.Fprintln(cmd.OutOrStdout(), "Pre-registering edge via enrollment token...")
			resp, preRegErr := preRegisterEdgeLocal(cmd.Context(), addr, enrollmentToken)
			if preRegErr != nil {
				return fmt.Errorf("pre-registration failed: %w", preRegErr)
			}
			domain = resp.GetEdgeDomain()
			preRegNodeID = resp.GetNodeId()
			preRegFoghornAddr = resp.GetFoghornGrpcAddr()
			preRegCertPEM = resp.GetCertPem()
			preRegKeyPEM = resp.GetKeyPem()
			fmt.Fprintf(cmd.OutOrStdout(), "  Assigned domain: %s\n", domain)
			fmt.Fprintf(cmd.OutOrStdout(), "  Node ID: %s\n", preRegNodeID)
		}

		foghornGRPC := cliCtx.Endpoints.FoghornGRPCAddr
		if preRegFoghornAddr != "" {
			foghornGRPC = preRegFoghornAddr
		}

		vars := templates.EdgeVars{
			NodeID:          preRegNodeID,
			EdgeDomain:      domain,
			AcmeEmail:       email,
			FoghornHTTPBase: cliCtx.Endpoints.FoghornHTTPURL,
			FoghornGRPCAddr: foghornGRPC,
			EnrollmentToken: enrollmentToken,
			Mode:            initMode,
		}

		// Stage TLS certs from PreRegisterEdge so Caddy starts with valid TLS
		if preRegCertPEM != "" && preRegKeyPEM != "" {
			certDir := filepath.Join(target, "certs")
			if err := os.MkdirAll(certDir, 0o755); err != nil {
				return fmt.Errorf("creating cert directory: %w", err)
			}
			certPath := filepath.Join(certDir, "cert.pem")
			keyPath := filepath.Join(certDir, "key.pem")
			if err := os.WriteFile(certPath, []byte(preRegCertPEM), 0o600); err != nil {
				return fmt.Errorf("writing cert.pem: %w", err)
			}
			if err := os.WriteFile(keyPath, []byte(preRegKeyPEM), 0o600); err != nil {
				return fmt.Errorf("writing key.pem: %w", err)
			}
			// Container path — docker-compose mounts ./certs -> /etc/frameworks/certs
			vars.CertPath = "/etc/frameworks/certs/cert.pem"
			vars.KeyPath = "/etc/frameworks/certs/key.pem"
			fmt.Fprintln(cmd.OutOrStdout(), "  TLS certificate staged for Caddy")
		}
		if err := templates.WriteEdgeTemplates(target, vars, overwrite); err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Wrote edge templates to %s\n", target)
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
		compose := "docker-compose.edge.yml"
		envFile := ".edge.env"
		// Start stack
		var out, errOut string
		var err error
		if strings.TrimSpace(sshTarget) != "" {
			_, out, errOut, err = xexec.RunSSHWithKey(cmd.Context(), sshTarget, sshKey, "docker", []string{"compose", "-f", compose, "--env-file", envFile, "up", "-d"}, dir)
		} else {
			_, out, errOut, err = xexec.Run(cmd.Context(), "docker", []string{"compose", "-f", compose, "--env-file", envFile, "up", "-d"}, dir)
		}
		if err != nil {
			fmt.Fprintf(cmd.ErrOrStderr(), "docker compose up failed: %v\n%s\n%s\n", err, out, errOut)
			return err
		}
		fmt.Fprintln(cmd.OutOrStdout(), "Edge stack started (caddy, mistserver, helmsman)")
		// Verify HTTPS readiness
		domain := readEnvFileKey(dir+string(os.PathSeparator)+envFile, "EDGE_DOMAIN")
		if strings.TrimSpace(domain) == "" {
			fmt.Fprintln(cmd.OutOrStdout(), "EDGE_DOMAIN not set in .edge.env; skipping HTTPS check")
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
				fmt.Fprintf(cmd.OutOrStdout(), "HTTPS ready at %s\n", url)
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

// detectEdgeMode reads DEPLOY_MODE from .edge.env to determine if the edge
// stack is running in docker or native mode. Falls back to "docker" if unset.
func detectEdgeMode(ctx context.Context, dir, envFile, sshTarget, sshKey string) string {
	// For remote hosts, read .edge.env via SSH
	if strings.TrimSpace(sshTarget) != "" {
		remoteEnv := "/opt/frameworks/edge/" + envFile
		_, out, _, err := xexec.RunSSHWithKey(ctx, sshTarget, sshKey, "sh", []string{"-c", fmt.Sprintf("grep ^DEPLOY_MODE= %s 2>/dev/null", remoteEnv)}, "")
		if err == nil {
			val := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(out), "DEPLOY_MODE="))
			if val == "native" {
				return "native"
			}
		}
		return "docker"
	}
	// Local: read from file
	val := readEnvFileKey(dir+string(os.PathSeparator)+envFile, "DEPLOY_MODE")
	if val == "native" {
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
		if strings.HasPrefix(ln, prefix) {
			return strings.TrimSpace(strings.TrimPrefix(ln, prefix))
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
    --pool-domain edge.example.com \
    --node-domain edge-1.example.com \
    --node-name edge-us-east-1 \
    --email ops@example.com \
    --fetch-cert

Local (user LaunchAgent, no admin required):
  frameworks edge provision --local --enrollment-token <tok>

Multi-node manifest example:
  frameworks edge provision --manifest edges.yaml --parallel 4`,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Load config for control plane endpoints
			cfg, _, err := fwcfg.Load()
			if err != nil {
				return fmt.Errorf("failed to load config: %w", err)
			}
			cliCtx := fwcfg.GetCurrent(cfg)

			// Check if using manifest mode
			if manifestPath != "" {
				return runEdgeProvisionFromManifest(cmd, cliCtx, manifestPath, sshKey, enrollmentToken, parallel, timeout, mode, version)
			}

			// Default --cluster-id and --foghorn-addr from context
			if clusterID == "" {
				clusterID = cliCtx.ClusterID
			}
			if foghornAddr == "" {
				foghornAddr = cliCtx.Endpoints.FoghornGRPCAddr
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
			var preRegCertPEM, preRegKeyPEM string
			if enrollmentToken != "" && nodeDomain == "" && poolDomain == "" {
				addr := foghornAddr
				if addr == "" {
					addr = cliCtx.Endpoints.FoghornGRPCAddr
				}
				if addr == "" {
					return fmt.Errorf("--foghorn-addr is required when using --enrollment-token without --node-domain")
				}

				fmt.Fprintln(cmd.OutOrStdout(), "Pre-registering edge via enrollment token...")
				preRegTarget := sshTarget
				if isLocal {
					preRegTarget = "localhost"
				}
				preRegResp, preRegErr := preRegisterEdge(cmd.Context(), addr, enrollmentToken, preRegTarget, sshKey)
				if preRegErr != nil {
					return fmt.Errorf("pre-registration failed: %w", preRegErr)
				}
				nodeDomain = preRegResp.GetEdgeDomain()
				poolDomain = preRegResp.GetPoolDomain()
				preRegNodeID = preRegResp.GetNodeId()
				preRegFoghornAddr = preRegResp.GetFoghornGrpcAddr()
				preRegCertPEM = preRegResp.GetCertPem()
				preRegKeyPEM = preRegResp.GetKeyPem()
				if nodeName == "" {
					nodeName = "edge-" + preRegNodeID
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
				if nodeDomain != "" {
					nodeName = strings.Split(nodeDomain, ".")[0]
				} else if isLocal {
					hostname, _ := os.Hostname()
					if hostname != "" {
						nodeName = hostname
					} else {
						nodeName = "localhost"
					}
				} else {
					parts := strings.Split(sshTarget, "@")
					if len(parts) > 1 {
						nodeName = parts[1]
					} else {
						nodeName = sshTarget
					}
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
			} else if preRegCertPEM != "" && preRegKeyPEM != "" {
				certPEM = preRegCertPEM
				keyPEM = preRegKeyPEM
			}

			// Build EdgeProvisionConfig and delegate to EdgeProvisioner
			foghornGRPC := foghornAddr // user flag, already defaulted from context at line 400-402
			if preRegFoghornAddr != "" {
				foghornGRPC = preRegFoghornAddr
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
				host = sshTargetToHost(sshTarget, sshKey)
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
				FoghornHTTPBase: cliCtx.Endpoints.FoghornHTTPURL,
				NodeID:          preRegNodeID,
				CertPEM:         certPEM,
				KeyPEM:          keyPEM,
				SkipPreflight:   skipPreflight,
				ApplyTuning:     applyTuning,
				RegisterNode:    registerNode,
				Timeout:         timeout,
				Version:         version,
				DarwinDomain:    darwinDomain,
			}

			pool := fwssh.NewPool(30 * time.Second)
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

			fmt.Fprintf(cmd.OutOrStdout(), "\nEdge node provisioned successfully!\n")
			fmt.Fprintf(cmd.OutOrStdout(), "  HTTPS: https://%s/health\n", primaryDomain)
			return nil
		},
	}

	cmd.Flags().StringVar(&sshTarget, "ssh", "", "SSH target (user@host, required)")
	cmd.Flags().StringVar(&sshKey, "ssh-key", "", "SSH private key path")
	cmd.Flags().StringVar(&poolDomain, "pool-domain", "", "Load balancer pool domain (e.g., edge.example.com)")
	cmd.Flags().StringVar(&nodeDomain, "node-domain", "", "Individual node domain (e.g., edge-1.example.com)")
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
func runEdgeProvisionFromManifest(cmd *cobra.Command, cliCtx fwcfg.Context, manifestPath, defaultSSHKey, enrollmentToken string, parallel int, timeout time.Duration, cliMode, cliVersion string) error {
	// Load manifest
	manifest, err := inventory.LoadEdgeManifest(manifestPath)
	if err != nil {
		return fmt.Errorf("failed to load manifest: %w", err)
	}

	fmt.Fprintf(cmd.OutOrStdout(), "Loaded edge manifest with %d nodes\n", len(manifest.Nodes))
	fmt.Fprintf(cmd.OutOrStdout(), "  Pool domain: %s\n", manifest.PoolDomain)
	fmt.Fprintf(cmd.OutOrStdout(), "  Root domain: %s\n", manifest.RootDomain)
	fmt.Fprintf(cmd.OutOrStdout(), "  Parallelism: %d\n", parallel)

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

			// Run provisioning steps
			sshKey := n.SSHKey
			if sshKey == "" {
				sshKey = defaultSSHKey
			}
			token := enrollmentToken
			if token == "" {
				token = manifest.EnrollmentToken
			}
			nodeMode := n.ResolvedMode(manifest.Mode)
			if cmd.Flags().Changed("mode") {
				nodeMode = cliMode
			}
			nodeVersion := manifest.Version
			if cmd.Flags().Changed("version") {
				nodeVersion = cliVersion
			}
			err := provisionSingleEdgeNode(cmd, cliCtx, n.SSH, sshKey, n.Name, nodeDomain, poolDomain, manifest.ClusterID, n.Region, manifest.Email, token, manifest.FetchCert, n.ApplyTune, n.RegisterQM, timeout, nodeMode, nodeVersion)
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
func provisionSingleEdgeNode(cmd *cobra.Command, cliCtx fwcfg.Context, sshTarget, sshKey, nodeName, nodeDomain, poolDomain, clusterID, region, email, enrollmentToken string, fetchCert, applyTuning, registerNode bool, timeout time.Duration, mode, version string) error {
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
	host := sshTargetToHost(sshTarget, sshKey)

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
		FoghornGRPCAddr: cliCtx.Endpoints.FoghornGRPCAddr,
		FoghornHTTPBase: cliCtx.Endpoints.FoghornHTTPURL,
		CertPEM:         certPEM,
		KeyPEM:          keyPEM,
		SkipPreflight:   false, // Preflight always runs for manifest mode
		ApplyTuning:     applyTuning,
		RegisterNode:    registerNode,
		Timeout:         timeout,
		Version:         version,
	}

	pool := fwssh.NewPool(30 * time.Second)
	ep := provisioner.NewEdgeProvisioner(pool)
	return ep.Provision(cmd.Context(), host, config)
}

// sshTargetToHost converts a "user@host" string into an inventory.Host.
func sshTargetToHost(sshTarget, sshKey string) inventory.Host {
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
		SSHKey:     sshKey,
	}
}

// getRemoteExternalIP detects the external IP of the remote host
func preRegisterEdgeLocal(ctx context.Context, foghornAddr, enrollmentToken string) (*pb.PreRegisterEdgeResponse, error) {
	return preRegisterEdge(ctx, foghornAddr, enrollmentToken, "", "")
}

func preRegisterEdge(ctx context.Context, foghornAddr, enrollmentToken, sshTarget, sshKey string) (*pb.PreRegisterEdgeResponse, error) {
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
	})
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

// registerEdgeNode registers an edge node in Quartermaster
func registerEdgeNode(cmd *cobra.Command, cliCtx fwcfg.Context, nodeName, clusterID, externalIP, region string) error {
	cliCtx.Auth = fwcfg.ResolveAuth(cliCtx)
	// Create Quartermaster gRPC client
	qmClient, err := quartermaster.NewGRPCClient(quartermaster.GRPCConfig{
		GRPCAddr:     cliCtx.Endpoints.QuartermasterGRPCAddr,
		Timeout:      30 * time.Second,
		ServiceToken: cliCtx.Auth.ServiceToken,
	})
	if err != nil {
		return fmt.Errorf("failed to connect to Quartermaster: %w", err)
	}
	defer qmClient.Close()

	// Generate a node ID
	nodeID := uuid.New().String()

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

	// Create context with timeout
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Register the node
	resp, err := qmClient.CreateNode(ctx, req)
	if err != nil {
		return fmt.Errorf("failed to create node: %w", err)
	}

	fmt.Fprintf(cmd.OutOrStdout(), "  ✓ Node registered: %s (ID: %s)\n", nodeName, resp.GetNode().GetNodeId())
	fmt.Fprintf(cmd.OutOrStdout(), "  ✓ Node registered (DNS will be synced by Navigator reconciler)\n")

	return nil
}

// fetchCertFromNavigator fetches a TLS certificate from Navigator service
func fetchCertFromNavigator(cmd *cobra.Command, cliCtx fwcfg.Context, domain, email string) (certPEM, keyPEM string, err error) {
	// Create Navigator gRPC client
	navClient, err := navigator.NewClient(navigator.Config{
		Addr:         cliCtx.Endpoints.NavigatorGRPCAddr,
		Timeout:      120 * time.Second, // ACME can take a while
		ServiceToken: cliCtx.Auth.ServiceToken,
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

	fmt.Fprintf(cmd.OutOrStdout(), "  ✓ Certificate issued for %s\n", domain)
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
				fmt.Fprintf(cmd.ErrOrStderr(), "%s status error: %v\n%s\n%s\n", tool, err, out, errOut)
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
				fmt.Fprintf(cmd.ErrOrStderr(), "docker compose ps error: %v\n%s\n%s\n", err, out, errOut)
			} else {
				fmt.Fprint(cmd.OutOrStdout(), out)
			}
		}
		// HTTPS health
		if strings.TrimSpace(domain) == "" {
			domain = readEnvFileKey(dir+string(os.PathSeparator)+envFile, "EDGE_DOMAIN")
		}
		if strings.TrimSpace(domain) != "" {
			url := "https://" + domain + "/health"
			httpClient := &http.Client{Timeout: 3 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}}
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
			if err != nil {
				cancel()
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "HTTPS: %s -> error: %v\n", url, err)
			} else {
				resp, err := httpClient.Do(req)
				cancel()
				if err != nil {
					_, _ = fmt.Fprintf(cmd.OutOrStdout(), "HTTPS: %s -> error: %v\n", url, err)
				} else {
					if resp.Body != nil {
						_ = resp.Body.Close()
					}
					ok := resp.StatusCode == 200
					mark := "✗"
					if ok {
						mark = "✓"
					}
					_, _ = fmt.Fprintf(cmd.OutOrStdout(), "HTTPS: %s -> %s (http %d)\n", url, mark, resp.StatusCode)
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
					fmt.Fprintf(cmd.ErrOrStderr(), "restart failed: %v\n%s\n%s\n", err, out, errOut)
					return err
				}
			} else if _, out, errOut, err := xexec.Run(cmd.Context(), "sh", []string{"-c", restartCmd}, ""); err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "restart failed: %v\n%s\n%s\n", err, out, errOut)
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), "Edge services restarted (native)")
		} else {
			compose := "docker-compose.edge.yml"
			// pull
			if strings.TrimSpace(sshTarget) != "" {
				if _, out, errOut, err := xexec.RunSSHWithKey(cmd.Context(), sshTarget, sshKey, "docker", []string{"compose", "-f", compose, "--env-file", envFile, "pull"}, dir); err != nil {
					fmt.Fprintf(cmd.ErrOrStderr(), "compose pull failed: %v\n%s\n%s\n", err, out, errOut)
					return err
				}
			} else if _, out, errOut, err := xexec.Run(cmd.Context(), "docker", []string{"compose", "-f", compose, "--env-file", envFile, "pull"}, dir); err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "compose pull failed: %v\n%s\n%s\n", err, out, errOut)
				return err
			}
			// up -d
			if strings.TrimSpace(sshTarget) != "" {
				if _, out, errOut, err := xexec.RunSSHWithKey(cmd.Context(), sshTarget, sshKey, "docker", []string{"compose", "-f", compose, "--env-file", envFile, "up", "-d"}, dir); err != nil {
					fmt.Fprintf(cmd.ErrOrStderr(), "compose up failed: %v\n%s\n%s\n", err, out, errOut)
					return err
				}
			} else if _, out, errOut, err := xexec.Run(cmd.Context(), "docker", []string{"compose", "-f", compose, "--env-file", envFile, "up", "-d"}, dir); err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "compose up failed: %v\n%s\n%s\n", err, out, errOut)
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), "Edge containers updated")
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
		if strings.TrimSpace(domain) == "" {
			// try to read from .edge.env
			if dir == "" {
				dir = "."
			}
			envFile := dir + string(os.PathSeparator) + ".edge.env"
			domain = readEnvFileKey(envFile, "EDGE_DOMAIN")
		}
		if strings.TrimSpace(domain) == "" {
			fmt.Fprintln(cmd.OutOrStdout(), "No domain provided and EDGE_DOMAIN not set in .edge.env")
		} else {
			// Check TLS expiry
			exp, issuer, err := tlsExpiry(domain)
			if err != nil {
				fmt.Fprintf(cmd.OutOrStdout(), "TLS check: %s -> error: %v\n", domain, err)
			} else {
				days := int(time.Until(exp).Hours() / 24)
				warn := ""
				if days < 30 {
					warn = " (warning: <30 days)"
				}
				fmt.Fprintf(cmd.OutOrStdout(), "TLS: %s -> expires %s (%d days)%s; issuer=%s\n", domain, exp.Format(time.RFC3339), days, warn, issuer)
			}
		}
		if reload {
			deployMode := detectEdgeMode(cmd.Context(), dir, ".edge.env", sshTarget, sshKey)
			var out, errOut string
			var err error
			if deployMode == "native" {
				edgeOS := detectEdgeOS(cmd.Context(), sshTarget, sshKey)
				if edgeOS == "darwin" {
					// macOS: kickstart caddy to reload
					reloadCmd := "launchctl kickstart -k system/com.livepeer.frameworks.caddy"
					if strings.TrimSpace(sshTarget) != "" {
						_, out, errOut, err = xexec.RunSSHWithKey(cmd.Context(), sshTarget, sshKey, "sh", []string{"-c", reloadCmd}, "")
					} else {
						_, out, errOut, err = xexec.Run(cmd.Context(), "sh", []string{"-c", reloadCmd}, "")
					}
				} else if strings.TrimSpace(sshTarget) != "" {
					_, out, errOut, err = xexec.RunSSHWithKey(cmd.Context(), sshTarget, sshKey, "systemctl", []string{"reload", "frameworks-caddy"}, "")
				} else {
					_, out, errOut, err = xexec.Run(cmd.Context(), "systemctl", []string{"reload", "frameworks-caddy"}, "")
				}
			} else {
				// Docker: exec into container
				if strings.TrimSpace(sshTarget) != "" {
					_, out, errOut, err = xexec.RunSSHWithKey(cmd.Context(), sshTarget, sshKey, "docker", []string{"exec", "edge-proxy", "caddy", "reload", "--config", "/etc/caddy/Caddyfile"}, dir)
				} else {
					_, out, errOut, err = xexec.Run(cmd.Context(), "docker", []string{"exec", "edge-proxy", "caddy", "reload", "--config", "/etc/caddy/Caddyfile"}, dir)
				}
			}
			if err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "caddy reload failed: %v\n%s\n%s\n", err, out, errOut)
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), "Caddy reloaded")
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
			fmt.Fprintf(cmd.ErrOrStderr(), "logs error: %v\n%s\n%s\n", err, out, errOut)
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

func newEdgeDoctorCmd() *cobra.Command {
	var domain string
	var dir string
	cmd := &cobra.Command{Use: "doctor", Short: "Run diagnostics and remediation hints", RunE: func(cmd *cobra.Command, args []string) error {
		ctx := cmd.Context()
		// Combine preflight + service status + https
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

		// Print checks
		okCount := 0
		fmt.Fprintln(cmd.OutOrStdout(), "Host Checks:")
		for _, r := range results {
			mark := "✗"
			if r.OK {
				mark = "✓"
				okCount++
			}
			if r.Error != "" {
				fmt.Fprintf(cmd.OutOrStdout(), " %s %-18s %-40s (%s)\n", mark, r.Name+":", r.Detail, r.Error)
			} else {
				fmt.Fprintf(cmd.OutOrStdout(), " %s %-18s %s\n", mark, r.Name+":", r.Detail)
			}
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Summary: %d/%d checks passed\n\n", okCount, len(results))

		// Service status
		if dir == "" {
			dir = "."
		}
		envFile := ".edge.env"
		deployMode := detectEdgeMode(ctx, dir, envFile, "", "")
		if deployMode == "native" {
			fmt.Fprintln(cmd.OutOrStdout(), "Native Services:")
			var statusCmd string
			if runtime.GOOS == "darwin" {
				statusCmd = "launchctl list | grep com.livepeer.frameworks"
			} else {
				statusCmd = "systemctl status frameworks-caddy frameworks-helmsman frameworks-mistserver --no-pager 2>&1 | head -30"
			}
			_, out, _, err := xexec.Run(ctx, "sh", []string{"-c", statusCmd}, "")
			if err != nil {
				fmt.Fprintf(cmd.OutOrStdout(), " service status error: %v\n", err)
			} else {
				fmt.Fprint(cmd.OutOrStdout(), out)
			}
		} else {
			compose := "docker-compose.edge.yml"
			_, out, errOut, err := xexec.Run(ctx, "docker", []string{"compose", "-f", compose, "--env-file", envFile, "ps"}, dir)
			fmt.Fprintln(cmd.OutOrStdout(), "Compose Services:")
			if err != nil {
				fmt.Fprintf(cmd.OutOrStdout(), " compose ps error: %v\n%s\n", err, errOut)
			} else {
				fmt.Fprint(cmd.OutOrStdout(), out)
			}
		}

		// HTTPS health
		if domain == "" {
			domain = readEnvFileKey(dir+string(os.PathSeparator)+envFile, "EDGE_DOMAIN")
		}
		if domain != "" {
			url := "https://" + domain + "/health"
			httpClient := &http.Client{Timeout: 3 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}}
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
			if err != nil {
				cancel()
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), "HTTPS Health:")
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), " %s error: %v\n", url, err)
			} else {
				resp, err := httpClient.Do(req)
				cancel()
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), "HTTPS Health:")
				if err != nil {
					_, _ = fmt.Fprintf(cmd.OutOrStdout(), " %s error: %v\n", url, err)
				} else {
					if resp.Body != nil {
						_ = resp.Body.Close()
					}
					_, _ = fmt.Fprintf(cmd.OutOrStdout(), " %s http %d\n", url, resp.StatusCode)
				}
			}
		}
		// Stream health quick-check via MistServer analyzers
		fmt.Fprintln(cmd.OutOrStdout(), "\nStream Health:")
		func() {
			diagCtx, diagCancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer diagCancel()
			deployMode := detectEdgeMode(diagCtx, dir, ".edge.env", "", "")
			localRunner := fwssh.NewLocalRunner("")
			ar := mistdiag.NewAnalyzerRunner(localRunner, deployMode)

			streams, err := mistdiag.DiscoverStreams(diagCtx, localRunner, deployMode)
			if err != nil {
				fmt.Fprintf(cmd.OutOrStdout(), " - Could not query MistServer: %v\n", err)
				return
			}
			if len(streams) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), " - No active streams (skipped)")
				return
			}
			for _, s := range streams {
				result, err := ar.Validate(diagCtx, "HLS", s.HLSURL, 5)
				if err != nil {
					fmt.Fprintf(cmd.OutOrStdout(), " ⚠ %-24s error: %v\n", s.Name+":", err)
					continue
				}
				if result.OK {
					fmt.Fprintf(cmd.OutOrStdout(), " ✓ %-24s HLS OK\n", s.Name+":")
				} else {
					msg := result.Summary()
					fmt.Fprintf(cmd.OutOrStdout(), " ✗ %-24s HLS FAIL (%s)\n", s.Name+":", msg)
				}
			}
		}()

		// Hints minimal
		fmt.Fprintln(cmd.OutOrStdout(), "\nHints:")
		fmt.Fprintln(cmd.OutOrStdout(), " - Ensure DNS A/AAAA records point to this host before enrollment.")
		fmt.Fprintln(cmd.OutOrStdout(), " - If HTTPS fails, confirm ports 80/443 are reachable and Caddy is running.")
		fmt.Fprintln(cmd.OutOrStdout(), " - Use 'frameworks edge tune --write' to apply recommended sysctl/limits.")
		fmt.Fprintln(cmd.OutOrStdout(), " - Use 'frameworks edge diagnose media' for detailed stream analysis.")
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
			// GET current mode
			curlArgs := []string{"-s", "-f", helmsmanBase + "/node/mode"}
			var out, errOut string
			var err error
			if strings.TrimSpace(sshTarget) != "" {
				_, out, errOut, err = xexec.RunSSHWithKey(cmd.Context(), sshTarget, sshKey, "curl", curlArgs, "")
			} else {
				_, out, errOut, err = xexec.Run(cmd.Context(), "curl", curlArgs, "")
			}
			if err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "failed to get node mode: %v\n%s\n", err, errOut)
				return err
			}
			fmt.Fprint(cmd.OutOrStdout(), out+"\n")
			return nil
		}

		mode := strings.ToLower(args[0])
		switch mode {
		case "normal", "draining", "maintenance":
		default:
			return fmt.Errorf("invalid mode %q: must be normal, draining, or maintenance", mode)
		}

		body := fmt.Sprintf(`{"mode":%q,"reason":%q}`, mode, reason)
		curlArgs := []string{"-s", "-f", "-X", "POST", "-H", "Content-Type: application/json", "-d", body, helmsmanBase + "/node/mode"}
		var out, errOut string
		var err error
		if strings.TrimSpace(sshTarget) != "" {
			_, out, errOut, err = xexec.RunSSHWithKey(cmd.Context(), sshTarget, sshKey, "curl", curlArgs, "")
		} else {
			_, out, errOut, err = xexec.Run(cmd.Context(), "curl", curlArgs, "")
		}
		if err != nil {
			fmt.Fprintf(cmd.ErrOrStderr(), "failed to set node mode: %v\n%s\n", err, errOut)
			return err
		}
		fmt.Fprint(cmd.OutOrStdout(), out+"\n")
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

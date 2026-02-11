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
	"strings"
	"sync"
	"time"

	fwcfg "frameworks/cli/internal/config"
	"frameworks/cli/internal/preflight"
	"frameworks/cli/internal/templates"
	"frameworks/cli/internal/xexec"
	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/provisioner"
	fwssh "frameworks/cli/pkg/ssh"
	"frameworks/pkg/clients/foghorn"
	"frameworks/pkg/clients/navigator"
	"frameworks/pkg/clients/quartermaster"
	pb "frameworks/pkg/proto"
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
)

const (
	minDiskFreeBytes   = 20 * 1024 * 1024 * 1024
	minDiskFreePercent = 10.0
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
	return edge
}

func newEdgePreflightCmd() *cobra.Command {
	var domain string
	cmd := &cobra.Command{Use: "preflight", Short: "Check host readiness (DNS/ports/sysctl/limits)", RunE: func(cmd *cobra.Command, args []string) error {
		// Gather checks
		results := []preflight.Check{}
		if domain != "" {
			results = append(results, preflight.DNSResolution(domain))
		}
		results = append(results, preflight.HasDocker()...)
		results = append(results, preflight.LinuxSysctlChecks()...)
		results = append(results, preflight.ShmSize())
		results = append(results, preflight.UlimitNoFile())
		results = append(results, preflight.PortChecks()...)
		results = append(results, preflight.DiskSpace("/", minDiskFreeBytes, minDiskFreePercent))
		results = append(results, preflight.DiskSpace("/var/lib", minDiskFreeBytes, minDiskFreePercent))

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
			_, out, errOut, err = xexec.RunSSHWithKey(sshTarget, sshKey, "docker", []string{"compose", "-f", compose, "--env-file", envFile, "up", "-d"}, dir)
		} else {
			_, out, errOut, err = xexec.Run("docker", []string{"compose", "-f", compose, "--env-file", envFile, "up", "-d"}, dir)
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
func detectEdgeMode(dir, envFile, sshTarget, sshKey string) string {
	// For remote hosts, read .edge.env via SSH
	if strings.TrimSpace(sshTarget) != "" {
		remoteEnv := "/opt/frameworks/edge/" + envFile
		_, out, _, err := xexec.RunSSHWithKey(sshTarget, sshKey, "sh", []string{"-c", fmt.Sprintf("grep ^DEPLOY_MODE= %s 2>/dev/null", remoteEnv)}, "")
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

	cmd := &cobra.Command{
		Use:   "provision",
		Short: "Provision edge node(s) via SSH (preflight, tune, init, enroll)",
		Long: `Provision edge node(s) by SSHing and running the full edge setup:
  1. Run preflight checks (Docker, ports, sysctl, limits)
  2. Apply sysctl/limits tuning (optional)
  3. Register node in Quartermaster (triggers DNS sync via Navigator)
  4. Fetch TLS certificate from Navigator (optional, uses centralized DNS-01 challenge)
  5. Generate and upload edge templates (docker-compose, Caddyfile, .env)
  6. Start edge stack (docker compose up)
  7. Wait for HTTPS readiness

Single node example:
  frameworks edge provision --ssh ubuntu@edge-1.example.com \
    --pool-domain edge-egress.example.com \
    --node-domain edge-1.example.com \
    --node-name edge-us-east-1 \
    --email ops@example.com \
    --fetch-cert

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

			// Single node mode - require ssh target
			if sshTarget == "" {
				return fmt.Errorf("--ssh is required (user@host) or use --manifest for multi-node")
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
				preRegResp, preRegErr := preRegisterEdge(cmd.Context(), addr, enrollmentToken, sshTarget, sshKey)
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

			// Default node name from SSH target or domain
			if nodeName == "" {
				if nodeDomain != "" {
					nodeName = strings.Split(nodeDomain, ".")[0]
				} else {
					// Extract from ssh target (user@host -> host)
					parts := strings.Split(sshTarget, "@")
					if len(parts) > 1 {
						nodeName = parts[1]
					} else {
						nodeName = sshTarget
					}
				}
			}

			fmt.Fprintf(cmd.OutOrStdout(), "Provisioning edge node: %s (%s mode)\n", sshTarget, mode)
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
			host := sshTargetToHost(sshTarget, sshKey)
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
			}

			pool := fwssh.NewPool(30 * time.Second)
			ep := provisioner.NewEdgeProvisioner(pool)

			// Registration handled before EdgeProvisioner (needs QM client)
			if registerNode {
				fmt.Fprintln(cmd.OutOrStdout(), "\nRegistering node in Quartermaster...")
				externalIP, _ := getRemoteExternalIP(sshTarget, sshKey)
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
	cmd.Flags().StringVar(&poolDomain, "pool-domain", "", "Load balancer pool domain (e.g., edge-egress.example.com)")
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
	cmd.Flags().BoolVar(&registerNode, "register", false, "Register node in Quartermaster (triggers DNS sync)")
	cmd.Flags().BoolVar(&fetchCert, "fetch-cert", false, "Fetch TLS certificate from Navigator (DNS-01 challenge)")
	cmd.Flags().StringVar(&manifestPath, "manifest", "", "Path to edge manifest file (edges.yaml) for multi-node provisioning")
	cmd.Flags().IntVar(&parallel, "parallel", 1, "Number of nodes to provision in parallel (for manifest mode)")
	cmd.Flags().StringVar(&mode, "mode", "docker", "Deployment mode: docker (compose) or native (systemd)")
	cmd.Flags().StringVar(&version, "version", "", "Platform version for binary resolution (e.g., stable, v1.2.3)")

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
		externalIP, _ := getRemoteExternalIP(sshTarget, sshKey)
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
		Address: address,
		User:    user,
		SSHKey:  sshKey,
	}
}

// runRemotePreflight runs preflight checks on remote host via SSH
func runRemotePreflight(cmd *cobra.Command, sshTarget, sshKey string) error {
	// Check Docker
	_, out, errOut, err := xexec.RunSSHWithKey(sshTarget, sshKey, "docker", []string{"--version"}, "")
	if err != nil {
		fmt.Fprintf(cmd.OutOrStdout(), "  ✗ Docker: not found (%s)\n", strings.TrimSpace(errOut))
		return fmt.Errorf("docker not installed")
	}
	fmt.Fprintf(cmd.OutOrStdout(), "  ✓ Docker: %s\n", strings.TrimSpace(out))

	// Check Docker Compose
	_, out, errOut, err = xexec.RunSSHWithKey(sshTarget, sshKey, "docker", []string{"compose", "version"}, "")
	if err != nil {
		fmt.Fprintf(cmd.OutOrStdout(), "  ✗ Docker Compose: not found (%s)\n", strings.TrimSpace(errOut))
		return fmt.Errorf("docker compose not available")
	}
	fmt.Fprintf(cmd.OutOrStdout(), "  ✓ Docker Compose: %s\n", strings.TrimSpace(out))

	// Check ports 80/443 are available
	_, _, _, err = xexec.RunSSHWithKey(sshTarget, sshKey, "ss", []string{"-tlnp"}, "")
	if err == nil {
		// Parse output to check if 80/443 are in use
		fmt.Fprintf(cmd.OutOrStdout(), "  ✓ Port check: ss available\n")
	}

	// Check /dev/shm size
	_, out, _, err = xexec.RunSSHWithKey(sshTarget, sshKey, "df", []string{"-h", "/dev/shm"}, "")
	if err == nil {
		lines := strings.Split(strings.TrimSpace(out), "\n")
		if len(lines) >= 2 {
			fmt.Fprintf(cmd.OutOrStdout(), "  ✓ /dev/shm: available\n")
		}
	}

	checkDisk := func(path string) error {
		_, out, errOut, err := xexec.RunSSHWithKey(sshTarget, sshKey, "df", []string{"-Pk", path}, "")
		if err != nil {
			fmt.Fprintf(cmd.OutOrStdout(), "  ✗ %s: %s\n", path, strings.TrimSpace(errOut))
			return fmt.Errorf("disk check failed for %s", path)
		}
		result := preflight.DiskSpaceFromDF(out, path, minDiskFreeBytes, minDiskFreePercent)
		if result.OK {
			fmt.Fprintf(cmd.OutOrStdout(), "  ✓ %s: %s\n", path, result.Detail)
			return nil
		}
		fmt.Fprintf(cmd.OutOrStdout(), "  ✗ %s: %s\n", path, result.Detail)
		if result.Error != "" {
			fmt.Fprintf(cmd.OutOrStdout(), "    %s\n", result.Error)
		}
		return fmt.Errorf("insufficient disk space on %s", path)
	}

	if err := checkDisk("/"); err != nil {
		return err
	}
	if err := checkDisk("/var/lib"); err != nil {
		return err
	}

	return nil
}

// runRemoteTuning applies sysctl and limits tuning on remote host
func runRemoteTuning(cmd *cobra.Command, sshTarget, sshKey string) error {
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

	// Write sysctl config
	sysctlCmd := fmt.Sprintf("echo '%s' | sudo tee /etc/sysctl.d/frameworks-edge.conf", sysctl)
	_, _, errOut, err := xexec.RunSSHWithKey(sshTarget, sshKey, "sh", []string{"-c", sysctlCmd}, "")
	if err != nil {
		fmt.Fprintf(cmd.OutOrStdout(), "  ✗ sysctl config: %s\n", strings.TrimSpace(errOut))
		return fmt.Errorf("failed to write sysctl config: %w", err)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "  ✓ sysctl config written\n")

	// Write limits config
	limitsCmd := fmt.Sprintf("echo '%s' | sudo tee /etc/security/limits.d/frameworks-edge.conf", limits)
	_, _, errOut, err = xexec.RunSSHWithKey(sshTarget, sshKey, "sh", []string{"-c", limitsCmd}, "")
	if err != nil {
		fmt.Fprintf(cmd.OutOrStdout(), "  ✗ limits config: %s\n", strings.TrimSpace(errOut))
		return fmt.Errorf("failed to write limits config: %w", err)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "  ✓ limits config written\n")

	// Apply sysctl
	_, _, _, err = xexec.RunSSHWithKey(sshTarget, sshKey, "sudo", []string{"sysctl", "--system"}, "")
	if err == nil {
		fmt.Fprintf(cmd.OutOrStdout(), "  ✓ sysctl applied\n")
	}

	return nil
}

// uploadEdgeTemplates generates edge templates locally and uploads to remote host
func uploadEdgeTemplates(cmd *cobra.Command, sshTarget, sshKey string, vars templates.EdgeVars) error {
	// Create temp directory for templates
	tmpDir, err := os.MkdirTemp("", "frameworks-edge-*")
	if err != nil {
		return fmt.Errorf("failed to create temp directory: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	// Write templates to temp directory
	if errWrite := templates.WriteEdgeTemplates(tmpDir, vars, true); errWrite != nil {
		return fmt.Errorf("failed to write templates: %w", errWrite)
	}

	// Create remote directory
	remoteDir := "/opt/frameworks/edge"
	_, _, errOut, err := xexec.RunSSHWithKey(sshTarget, sshKey, "sudo", []string{"mkdir", "-p", remoteDir}, "")
	if err != nil {
		return fmt.Errorf("failed to create remote directory: %s", strings.TrimSpace(errOut))
	}

	// Set ownership to current user for upload
	_, _, _, _ = xexec.RunSSHWithKey(sshTarget, sshKey, "sudo", []string{"chown", "-R", "$USER:$USER", remoteDir}, "")

	// Upload each file using scp
	files := []string{"docker-compose.edge.yml", "Caddyfile", ".edge.env"}
	for _, f := range files {
		localPath := tmpDir + "/" + f
		remotePath := remoteDir + "/" + f

		// Use scp to upload (through shell)
		scpArgs := []string{"-o", "BatchMode=yes"}
		if strings.TrimSpace(sshKey) != "" {
			scpArgs = append(scpArgs, "-i", sshKey)
		}
		scpArgs = append(scpArgs, localPath, fmt.Sprintf("%s:%s", sshTarget, remotePath))
		_, _, errOut, err := xexec.Run("scp", scpArgs, "")
		if err != nil {
			return fmt.Errorf("failed to upload %s: %s", f, strings.TrimSpace(errOut))
		}
		fmt.Fprintf(cmd.OutOrStdout(), "  ✓ Uploaded %s\n", f)
	}

	return nil
}

// runRemoteDockerCompose starts the edge stack on remote host
func runRemoteDockerCompose(cmd *cobra.Command, sshTarget, sshKey string) error {
	remoteDir := "/opt/frameworks/edge"
	compose := "docker-compose.edge.yml"
	envFile := ".edge.env"

	_, out, errOut, err := xexec.RunSSHWithKey(sshTarget, sshKey, "docker", []string{"compose", "-f", compose, "--env-file", envFile, "up", "-d"}, remoteDir)
	if err != nil {
		fmt.Fprintf(cmd.ErrOrStderr(), "docker compose up failed: %v\n%s\n%s\n", err, out, errOut)
		return err
	}
	fmt.Fprintln(cmd.OutOrStdout(), "  ✓ Edge stack started (caddy, mistserver, helmsman)")
	return nil
}

// waitForHTTPS waits for the edge domain to be HTTPS-ready
func waitForHTTPS(cmd *cobra.Command, domain string, timeout time.Duration) error {
	if strings.TrimSpace(domain) == "" {
		fmt.Fprintln(cmd.OutOrStdout(), "  - No domain specified, skipping HTTPS check")
		return nil
	}

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
			fmt.Fprintf(cmd.OutOrStdout(), "  ✓ HTTPS ready at %s\n", url)
			return nil
		}
		if resp != nil && resp.Body != nil {
			_ = resp.Body.Close()
		}

		if time.Now().After(deadline) {
			if err != nil {
				return fmt.Errorf("HTTPS check failed: %w", err)
			}
			if resp != nil {
				return fmt.Errorf("HTTPS not ready: status %d", resp.StatusCode)
			}
			return fmt.Errorf("HTTPS not ready before timeout")
		}

		fmt.Fprintf(cmd.OutOrStdout(), "  - Waiting for HTTPS (retrying in 5s)...\n")
		time.Sleep(5 * time.Second)
	}
}

// getRemoteExternalIP detects the external IP of the remote host
func preRegisterEdgeLocal(ctx context.Context, foghornAddr, enrollmentToken string) (*pb.PreRegisterEdgeResponse, error) {
	return preRegisterEdge(ctx, foghornAddr, enrollmentToken, "", "")
}

func preRegisterEdge(ctx context.Context, foghornAddr, enrollmentToken, sshTarget, sshKey string) (*pb.PreRegisterEdgeResponse, error) {
	externalIP, _ := getRemoteExternalIP(sshTarget, sshKey)

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

func getRemoteExternalIP(sshTarget, sshKey string) (string, error) {
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
		_, out, _, err := xexec.RunSSHWithKey(sshTarget, sshKey, m.cmd, m.args, "")
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
		NodeType:  "edge", // This triggers DNS sync for "edge" service type
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
	fmt.Fprintf(cmd.OutOrStdout(), "  ✓ DNS sync triggered for 'edge' service type\n")

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

// uploadCertificates uploads certificate files to the edge node
func uploadCertificates(cmd *cobra.Command, sshTarget, sshKey, certPEM, keyPEM string) error {
	// Create certificate directory on remote host
	certDir := "/etc/frameworks/certs"
	_, _, errOut, err := xexec.RunSSHWithKey(sshTarget, sshKey, "sudo", []string{"mkdir", "-p", certDir}, "")
	if err != nil {
		return fmt.Errorf("failed to create cert directory: %s", strings.TrimSpace(errOut))
	}

	// Set ownership so we can write files
	_, _, _, _ = xexec.RunSSHWithKey(sshTarget, sshKey, "sudo", []string{"chown", "-R", "$USER:$USER", certDir}, "")

	// Create temp files locally
	tmpDir, err := os.MkdirTemp("", "frameworks-certs-*")
	if err != nil {
		return fmt.Errorf("failed to create temp directory: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	certPath := tmpDir + "/cert.pem"
	keyPath := tmpDir + "/key.pem"

	if err := os.WriteFile(certPath, []byte(certPEM), 0644); err != nil {
		return fmt.Errorf("failed to write cert file: %w", err)
	}
	if err := os.WriteFile(keyPath, []byte(keyPEM), 0600); err != nil {
		return fmt.Errorf("failed to write key file: %w", err)
	}

	// Upload files via scp
	files := []struct {
		local  string
		remote string
	}{
		{certPath, certDir + "/cert.pem"},
		{keyPath, certDir + "/key.pem"},
	}

	for _, f := range files {
		scpArgs := []string{"-o", "BatchMode=yes"}
		if strings.TrimSpace(sshKey) != "" {
			scpArgs = append(scpArgs, "-i", sshKey)
		}
		scpArgs = append(scpArgs, f.local, sshTarget+":"+f.remote)
		_, _, errOut, err := xexec.Run("scp", scpArgs, "")
		if err != nil {
			return fmt.Errorf("failed to upload %s: %s", f.local, strings.TrimSpace(errOut))
		}
	}

	// Set proper permissions on key file
	_, _, _, _ = xexec.RunSSHWithKey(sshTarget, sshKey, "sudo", []string{"chmod", "600", certDir + "/key.pem"}, "")

	fmt.Fprintf(cmd.OutOrStdout(), "  ✓ Certificate uploaded to %s/cert.pem\n", certDir)
	fmt.Fprintf(cmd.OutOrStdout(), "  ✓ Private key uploaded to %s/key.pem\n", certDir)
	return nil
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
		deployMode := detectEdgeMode(dir, envFile, sshTarget, sshKey)

		var out, errOut string
		var err error
		if deployMode == "native" {
			// Native: check systemd units
			statusCmd := "systemctl status frameworks-caddy frameworks-helmsman frameworks-mistserver --no-pager"
			if strings.TrimSpace(sshTarget) != "" {
				_, out, errOut, err = xexec.RunSSHWithKey(sshTarget, sshKey, "sh", []string{"-c", statusCmd}, "")
			} else {
				_, out, errOut, err = xexec.Run("sh", []string{"-c", statusCmd}, "")
			}
			if err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "systemctl status error: %v\n%s\n%s\n", err, out, errOut)
			} else {
				fmt.Fprint(cmd.OutOrStdout(), out)
			}
		} else {
			// Docker: compose ps
			compose := "docker-compose.edge.yml"
			if strings.TrimSpace(sshTarget) != "" {
				_, out, errOut, err = xexec.RunSSHWithKey(sshTarget, sshKey, "docker", []string{"compose", "-f", compose, "--env-file", envFile, "ps"}, dir)
			} else {
				_, out, errOut, err = xexec.Run("docker", []string{"compose", "-f", compose, "--env-file", envFile, "ps"}, dir)
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
		deployMode := detectEdgeMode(dir, envFile, sshTarget, sshKey)

		if deployMode == "native" {
			// Native: restart systemd units
			restartCmd := "systemctl restart frameworks-mistserver frameworks-helmsman frameworks-caddy"
			if strings.TrimSpace(sshTarget) != "" {
				if _, out, errOut, err := xexec.RunSSHWithKey(sshTarget, sshKey, "sh", []string{"-c", restartCmd}, ""); err != nil {
					fmt.Fprintf(cmd.ErrOrStderr(), "systemctl restart failed: %v\n%s\n%s\n", err, out, errOut)
					return err
				}
			} else if _, out, errOut, err := xexec.Run("sh", []string{"-c", restartCmd}, ""); err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "systemctl restart failed: %v\n%s\n%s\n", err, out, errOut)
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), "Edge services restarted (native)")
		} else {
			compose := "docker-compose.edge.yml"
			// pull
			if strings.TrimSpace(sshTarget) != "" {
				if _, out, errOut, err := xexec.RunSSHWithKey(sshTarget, sshKey, "docker", []string{"compose", "-f", compose, "--env-file", envFile, "pull"}, dir); err != nil {
					fmt.Fprintf(cmd.ErrOrStderr(), "compose pull failed: %v\n%s\n%s\n", err, out, errOut)
					return err
				}
			} else if _, out, errOut, err := xexec.Run("docker", []string{"compose", "-f", compose, "--env-file", envFile, "pull"}, dir); err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "compose pull failed: %v\n%s\n%s\n", err, out, errOut)
				return err
			}
			// up -d
			if strings.TrimSpace(sshTarget) != "" {
				if _, out, errOut, err := xexec.RunSSHWithKey(sshTarget, sshKey, "docker", []string{"compose", "-f", compose, "--env-file", envFile, "up", "-d"}, dir); err != nil {
					fmt.Fprintf(cmd.ErrOrStderr(), "compose up failed: %v\n%s\n%s\n", err, out, errOut)
					return err
				}
			} else if _, out, errOut, err := xexec.Run("docker", []string{"compose", "-f", compose, "--env-file", envFile, "up", "-d"}, dir); err != nil {
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
			deployMode := detectEdgeMode(dir, ".edge.env", sshTarget, sshKey)
			var out, errOut string
			var err error
			if deployMode == "native" {
				// Native: reload via systemctl
				if strings.TrimSpace(sshTarget) != "" {
					_, out, errOut, err = xexec.RunSSHWithKey(sshTarget, sshKey, "systemctl", []string{"reload", "frameworks-caddy"}, "")
				} else {
					_, out, errOut, err = xexec.Run("systemctl", []string{"reload", "frameworks-caddy"}, "")
				}
			} else {
				// Docker: exec into container
				if strings.TrimSpace(sshTarget) != "" {
					_, out, errOut, err = xexec.RunSSHWithKey(sshTarget, sshKey, "docker", []string{"exec", "edge-proxy", "caddy", "reload", "--config", "/etc/caddy/Caddyfile"}, dir)
				} else {
					_, out, errOut, err = xexec.Run("docker", []string{"exec", "edge-proxy", "caddy", "reload", "--config", "/etc/caddy/Caddyfile"}, dir)
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
		deployMode := detectEdgeMode(dir, envFile, sshTarget, sshKey)
		svc := ""
		if len(args) == 1 {
			svc = args[0]
		}

		var out, errOut string
		var err error

		if deployMode == "native" {
			// Native: use journalctl
			unit := ""
			if svc != "" {
				unit = "frameworks-" + svc
			} else {
				unit = "frameworks-caddy frameworks-helmsman frameworks-mistserver"
			}
			jArgs := []string{"-c", fmt.Sprintf("journalctl --no-pager -n %d %s -u %s", tail, func() string {
				if follow {
					return "-f"
				}
				return ""
			}(), unit)}
			if strings.TrimSpace(sshTarget) != "" {
				_, out, errOut, err = xexec.RunSSHWithKey(sshTarget, sshKey, "sh", jArgs, "")
			} else {
				_, out, errOut, err = xexec.Run("sh", jArgs, "")
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
				_, out, errOut, err = xexec.RunSSHWithKey(sshTarget, sshKey, "docker", arg, dir)
			} else {
				_, out, errOut, err = xexec.Run("docker", arg, dir)
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
		// Combine preflight + compose ps + https
		results := []preflight.Check{}
		if domain != "" {
			results = append(results, preflight.DNSResolution(domain))
		}
		results = append(results, preflight.HasDocker()...)
		results = append(results, preflight.LinuxSysctlChecks()...)
		results = append(results, preflight.ShmSize())
		results = append(results, preflight.UlimitNoFile())
		results = append(results, preflight.PortChecks()...)

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

		// Compose status
		if dir == "" {
			dir = "."
		}
		compose := "docker-compose.edge.yml"
		envFile := ".edge.env"
		_, out, errOut, err := xexec.Run("docker", []string{"compose", "-f", compose, "--env-file", envFile, "ps"}, dir)
		fmt.Fprintln(cmd.OutOrStdout(), "Compose Services:")
		if err != nil {
			fmt.Fprintf(cmd.OutOrStdout(), " compose ps error: %v\n%s\n", err, errOut)
		} else {
			fmt.Fprint(cmd.OutOrStdout(), out)
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
		// Hints minimal
		fmt.Fprintln(cmd.OutOrStdout(), "\nHints:")
		fmt.Fprintln(cmd.OutOrStdout(), " - Ensure DNS A/AAAA records point to this host before enrollment.")
		fmt.Fprintln(cmd.OutOrStdout(), " - If HTTPS fails, confirm ports 80/443 are reachable and Caddy is running.")
		fmt.Fprintln(cmd.OutOrStdout(), " - Use 'frameworks edge tune --write' to apply recommended sysctl/limits.")
		return nil
	}}
	cmd.Flags().StringVar(&domain, "domain", "", "edge domain to validate (DNS and HTTPS)")
	cmd.Flags().StringVar(&dir, "dir", ".", "directory with edge templates")
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

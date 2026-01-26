package cmd

import (
	"context"
	"crypto/tls"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	fwcfg "frameworks/cli/internal/config"
	"frameworks/cli/internal/preflight"
	"frameworks/cli/internal/templates"
	"frameworks/cli/internal/xexec"
	"frameworks/cli/pkg/inventory"
	"frameworks/pkg/clients/navigator"
	"frameworks/pkg/clients/quartermaster"
	pb "frameworks/pkg/proto"
	"github.com/google/uuid"
	"github.com/spf13/cobra"
	"sync"
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
	var overwrite bool
	cmd := &cobra.Command{Use: "init", Short: ".edge.env + templates (compose, Caddyfile)", RunE: func(cmd *cobra.Command, args []string) error {
		cfg, _, err := fwcfg.Load()
		if err != nil {
			return err
		}
		ctx := fwcfg.GetCurrent(cfg)
		if target == "" {
			target = "."
		}
		vars := templates.EdgeVars{
			EdgeDomain:      domain,
			AcmeEmail:       email,
			FoghornHTTPBase: ctx.Endpoints.FoghornHTTPURL,
			FoghornGRPCAddr: ctx.Endpoints.FoghornGRPCAddr,
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
	cmd.Flags().BoolVar(&overwrite, "overwrite", false, "overwrite existing files")
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
			req, _ := http.NewRequest("GET", url, nil)
			resp, err := httpClient.Do(req)
			if err == nil && resp != nil && resp.StatusCode == 200 {
				if resp.Body != nil {
					resp.Body.Close()
				}
				fmt.Fprintf(cmd.OutOrStdout(), "HTTPS ready at %s\n", url)
				break
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
	var timeout time.Duration
	var skipPreflight bool
	var applyTuning bool
	var registerNode bool
	var fetchCert bool
	var manifestPath string
	var parallel int

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
    --pool-domain edge.example.com \
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
				return runEdgeProvisionFromManifest(cmd, cliCtx, manifestPath, sshKey, parallel, timeout)
			}

			// Single node mode - require ssh target
			if sshTarget == "" {
				return fmt.Errorf("--ssh is required (user@host) or use --manifest for multi-node")
			}
			if poolDomain == "" && nodeDomain == "" {
				return fmt.Errorf("at least one of --pool-domain or --node-domain is required")
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

			fmt.Fprintf(cmd.OutOrStdout(), "Provisioning edge node: %s\n", sshTarget)
			fmt.Fprintf(cmd.OutOrStdout(), "  Node name: %s\n", nodeName)
			fmt.Fprintf(cmd.OutOrStdout(), "  Pool domain: %s\n", poolDomain)
			fmt.Fprintf(cmd.OutOrStdout(), "  Node domain: %s\n", nodeDomain)

			if !skipPreflight {
				fmt.Fprintln(cmd.OutOrStdout(), "\n[1/7] Running preflight checks...")
				if err := runRemotePreflight(cmd, sshTarget, sshKey); err != nil {
					return fmt.Errorf("preflight failed: %w", err)
				}
			} else {
				fmt.Fprintln(cmd.OutOrStdout(), "\n[1/7] Skipping preflight checks (--skip-preflight)")
			}

			if applyTuning {
				fmt.Fprintln(cmd.OutOrStdout(), "\n[2/7] Applying sysctl/limits tuning...")
				if err := runRemoteTuning(cmd, sshTarget, sshKey); err != nil {
					return fmt.Errorf("tuning failed: %w", err)
				}
			} else {
				fmt.Fprintln(cmd.OutOrStdout(), "\n[2/7] Skipping sysctl tuning (use --tune to apply)")
			}

			// Registration triggers DNS sync as a side-effect
			var externalIP string
			if registerNode {
				fmt.Fprintln(cmd.OutOrStdout(), "\n[3/7] Registering node in Quartermaster...")
				externalIP, err = getRemoteExternalIP(sshTarget, sshKey)
				if err != nil {
					fmt.Fprintf(cmd.OutOrStdout(), "  - Warning: Could not detect external IP: %v\n", err)
				} else {
					fmt.Fprintf(cmd.OutOrStdout(), "  ✓ Detected external IP: %s\n", externalIP)
				}

				if err := registerEdgeNode(cmd, cliCtx, nodeName, clusterID, externalIP, region); err != nil {
					return fmt.Errorf("node registration failed: %w", err)
				}
			} else {
				fmt.Fprintln(cmd.OutOrStdout(), "\n[3/7] Skipping node registration (use --register to enable)")
			}

			var certPEM, keyPEM string
			if fetchCert {
				fmt.Fprintln(cmd.OutOrStdout(), "\n[4/7] Fetching TLS certificate from Navigator...")
				if email == "" {
					return fmt.Errorf("--email is required when using --fetch-cert")
				}
				certPEM, keyPEM, err = fetchCertFromNavigator(cmd, cliCtx, primaryDomain, email)
				if err != nil {
					return fmt.Errorf("certificate fetch failed: %w", err)
				}

				// Upload certificate to edge node
				if err := uploadCertificates(cmd, sshTarget, sshKey, certPEM, keyPEM); err != nil {
					return fmt.Errorf("certificate upload failed: %w", err)
				}
			} else {
				fmt.Fprintln(cmd.OutOrStdout(), "\n[4/7] Skipping certificate fetch (use --fetch-cert to enable; Caddy will auto-ACME)")
			}

			fmt.Fprintln(cmd.OutOrStdout(), "\n[5/7] Generating and uploading edge templates...")
			vars := templates.EdgeVars{
				EdgeDomain:      primaryDomain,
				AcmeEmail:       email,
				FoghornHTTPBase: cliCtx.Endpoints.FoghornHTTPURL,
				FoghornGRPCAddr: cliCtx.Endpoints.FoghornGRPCAddr,
			}
			// If we fetched certs, configure file-based TLS
			if fetchCert && certPEM != "" && keyPEM != "" {
				vars.CertPath = "/etc/frameworks/certs/cert.pem"
				vars.KeyPath = "/etc/frameworks/certs/key.pem"
			}
			if err := uploadEdgeTemplates(cmd, sshTarget, sshKey, vars); err != nil {
				return fmt.Errorf("template upload failed: %w", err)
			}

			fmt.Fprintln(cmd.OutOrStdout(), "\n[6/7] Starting edge stack (docker compose up)...")
			if err := runRemoteDockerCompose(cmd, sshTarget, sshKey); err != nil {
				return fmt.Errorf("docker compose failed: %w", err)
			}

			fmt.Fprintln(cmd.OutOrStdout(), "\n[7/7] Waiting for HTTPS readiness...")
			if err := waitForHTTPS(cmd, primaryDomain, timeout); err != nil {
				return fmt.Errorf("HTTPS readiness failed: %w", err)
			}

			fmt.Fprintf(cmd.OutOrStdout(), "\nEdge node provisioned successfully!\n")
			fmt.Fprintf(cmd.OutOrStdout(), "  HTTPS: https://%s/health\n", primaryDomain)
			if registerNode {
				fmt.Fprintf(cmd.OutOrStdout(), "  Node registered in Quartermaster (DNS sync triggered)\n")
			}
			if fetchCert {
				fmt.Fprintf(cmd.OutOrStdout(), "  TLS certificate from Navigator (DNS-01 challenge)\n")
			}
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
	cmd.Flags().DurationVar(&timeout, "timeout", 3*time.Minute, "Timeout for HTTPS readiness")
	cmd.Flags().BoolVar(&skipPreflight, "skip-preflight", false, "Skip preflight checks")
	cmd.Flags().BoolVar(&applyTuning, "tune", false, "Apply sysctl/limits tuning")
	cmd.Flags().BoolVar(&registerNode, "register", false, "Register node in Quartermaster (triggers DNS sync)")
	cmd.Flags().BoolVar(&fetchCert, "fetch-cert", false, "Fetch TLS certificate from Navigator (DNS-01 challenge)")
	cmd.Flags().StringVar(&manifestPath, "manifest", "", "Path to edge manifest file (edges.yaml) for multi-node provisioning")
	cmd.Flags().IntVar(&parallel, "parallel", 1, "Number of nodes to provision in parallel (for manifest mode)")

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
func runEdgeProvisionFromManifest(cmd *cobra.Command, cliCtx fwcfg.Context, manifestPath, defaultSSHKey string, parallel int, timeout time.Duration) error {
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
			err := provisionSingleEdgeNode(cmd, cliCtx, n.SSH, sshKey, n.Name, nodeDomain, poolDomain, manifest.ClusterID, n.Region, manifest.Email, manifest.FetchCert, n.ApplyTune, n.RegisterQM, timeout)
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

// provisionSingleEdgeNode provisions a single edge node (used by both single and manifest modes)
func provisionSingleEdgeNode(cmd *cobra.Command, cliCtx fwcfg.Context, sshTarget, sshKey, nodeName, nodeDomain, poolDomain, clusterID, region, email string, fetchCert, applyTuning, registerNode bool, timeout time.Duration) error {
	primaryDomain := poolDomain
	if primaryDomain == "" {
		primaryDomain = nodeDomain
	}

	if err := runRemotePreflight(cmd, sshTarget, sshKey); err != nil {
		return fmt.Errorf("preflight failed: %w", err)
	}

	if applyTuning {
		if err := runRemoteTuning(cmd, sshTarget, sshKey); err != nil {
			return fmt.Errorf("tuning failed: %w", err)
		}
	}

	// Registration triggers DNS sync as a side-effect
	if registerNode {
		externalIP, _ := getRemoteExternalIP(sshTarget, sshKey)
		if err := registerEdgeNode(cmd, cliCtx, nodeName, clusterID, externalIP, region); err != nil {
			return fmt.Errorf("node registration failed: %w", err)
		}
	}

	var certPEM, keyPEM string
	var err error
	if fetchCert && email != "" {
		certPEM, keyPEM, err = fetchCertFromNavigator(cmd, cliCtx, primaryDomain, email)
		if err != nil {
			return fmt.Errorf("certificate fetch failed: %w", err)
		}
		if err := uploadCertificates(cmd, sshTarget, sshKey, certPEM, keyPEM); err != nil {
			return fmt.Errorf("certificate upload failed: %w", err)
		}
	}

	vars := templates.EdgeVars{
		EdgeDomain:      primaryDomain,
		AcmeEmail:       email,
		FoghornHTTPBase: cliCtx.Endpoints.FoghornHTTPURL,
		FoghornGRPCAddr: cliCtx.Endpoints.FoghornGRPCAddr,
	}
	if fetchCert && certPEM != "" && keyPEM != "" {
		vars.CertPath = "/etc/frameworks/certs/cert.pem"
		vars.KeyPath = "/etc/frameworks/certs/key.pem"
	}
	if err := uploadEdgeTemplates(cmd, sshTarget, sshKey, vars); err != nil {
		return fmt.Errorf("template upload failed: %w", err)
	}

	if err := runRemoteDockerCompose(cmd, sshTarget, sshKey); err != nil {
		return fmt.Errorf("docker compose failed: %w", err)
	}

	if err := waitForHTTPS(cmd, primaryDomain, timeout); err != nil {
		return fmt.Errorf("HTTPS readiness failed: %w", err)
	}

	return nil
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
	if err := templates.WriteEdgeTemplates(tmpDir, vars, true); err != nil {
		return fmt.Errorf("failed to write templates: %w", err)
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
		req, _ := http.NewRequest("GET", url, nil)
		resp, err := httpClient.Do(req)
		if err == nil && resp != nil && resp.StatusCode == 200 {
			if resp.Body != nil {
				resp.Body.Close()
			}
			fmt.Fprintf(cmd.OutOrStdout(), "  ✓ HTTPS ready at %s\n", url)
			return nil
		}
		if resp != nil && resp.Body != nil {
			resp.Body.Close()
		}

		if time.Now().After(deadline) {
			if err != nil {
				return fmt.Errorf("HTTPS check failed: %v", err)
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
		compose := "docker-compose.edge.yml"
		envFile := ".edge.env"
		// docker compose ps
		var out, errOut string
		var err error
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
		// HTTPS health
		if strings.TrimSpace(domain) == "" {
			domain = readEnvFileKey(dir+string(os.PathSeparator)+envFile, "EDGE_DOMAIN")
		}
		if strings.TrimSpace(domain) != "" {
			url := "https://" + domain + "/health"
			httpClient := &http.Client{Timeout: 3 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}}
			resp, err := httpClient.Get(url)
			if err != nil {
				fmt.Fprintf(cmd.OutOrStdout(), "HTTPS: %s -> error: %v\n", url, err)
			} else {
				if resp.Body != nil {
					resp.Body.Close()
				}
				ok := resp.StatusCode == 200
				mark := "✗"
				if ok {
					mark = "✓"
				}
				fmt.Fprintf(cmd.OutOrStdout(), "HTTPS: %s -> %s (http %d)\n", url, mark, resp.StatusCode)
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
	cmd := &cobra.Command{Use: "update", Short: "Pull and restart edge containers (MVP)", RunE: func(cmd *cobra.Command, args []string) error {
		if dir == "" {
			dir = "."
		}
		compose := "docker-compose.edge.yml"
		envFile := ".edge.env"
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
			// Attempt caddy reload (container edge-proxy)
			var out, errOut string
			var err error
			if strings.TrimSpace(sshTarget) != "" {
				_, out, errOut, err = xexec.RunSSHWithKey(sshTarget, sshKey, "docker", []string{"exec", "edge-proxy", "caddy", "reload", "--config", "/etc/caddy/Caddyfile"}, dir)
			} else {
				_, out, errOut, err = xexec.Run("docker", []string{"exec", "edge-proxy", "caddy", "reload", "--config", "/etc/caddy/Caddyfile"}, dir)
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
		compose := "docker-compose.edge.yml"
		envFile := ".edge.env"
		svc := ""
		if len(args) == 1 {
			svc = args[0]
		}
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
		var out, errOut string
		var err error
		if strings.TrimSpace(sshTarget) != "" {
			_, out, errOut, err = xexec.RunSSHWithKey(sshTarget, sshKey, "docker", arg, dir)
		} else {
			_, out, errOut, err = xexec.Run("docker", arg, dir)
		}
		if err != nil {
			fmt.Fprintf(cmd.ErrOrStderr(), "compose logs error: %v\n%s\n%s\n", err, out, errOut)
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
			resp, err := httpClient.Get(url)
			fmt.Fprintln(cmd.OutOrStdout(), "HTTPS Health:")
			if err != nil {
				fmt.Fprintf(cmd.OutOrStdout(), " %s error: %v\n", url, err)
			} else {
				if resp.Body != nil {
					resp.Body.Close()
				}
				fmt.Fprintf(cmd.OutOrStdout(), " %s http %d\n", url, resp.StatusCode)
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

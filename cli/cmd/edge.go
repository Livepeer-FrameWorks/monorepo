package cmd

import (
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
	"github.com/spf13/cobra"
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
			_, out, errOut, err = xexec.RunSSH(sshTarget, "docker", []string{"compose", "-f", compose, "--env-file", envFile, "up", "-d"}, dir)
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

func newEdgeStatusCmd() *cobra.Command {
	var dir string
	var domain string
	var sshTarget string
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
			_, out, errOut, err = xexec.RunSSH(sshTarget, "docker", []string{"compose", "-f", compose, "--env-file", envFile, "ps"}, dir)
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
	return cmd
}

func newEdgeUpdateCmd() *cobra.Command {
	var dir string
	var sshTarget string
	cmd := &cobra.Command{Use: "update", Short: "Pull and restart edge containers (MVP)", RunE: func(cmd *cobra.Command, args []string) error {
		if dir == "" {
			dir = "."
		}
		compose := "docker-compose.edge.yml"
		envFile := ".edge.env"
		// pull
		if strings.TrimSpace(sshTarget) != "" {
			if _, out, errOut, err := xexec.RunSSH(sshTarget, "docker", []string{"compose", "-f", compose, "--env-file", envFile, "pull"}, dir); err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "compose pull failed: %v\n%s\n%s\n", err, out, errOut)
				return err
			}
		} else if _, out, errOut, err := xexec.Run("docker", []string{"compose", "-f", compose, "--env-file", envFile, "pull"}, dir); err != nil {
			fmt.Fprintf(cmd.ErrOrStderr(), "compose pull failed: %v\n%s\n%s\n", err, out, errOut)
			return err
		}
		// up -d
		if strings.TrimSpace(sshTarget) != "" {
			if _, out, errOut, err := xexec.RunSSH(sshTarget, "docker", []string{"compose", "-f", compose, "--env-file", envFile, "up", "-d"}, dir); err != nil {
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
	return cmd
}

func newEdgeCertCmd() *cobra.Command {
	var dir string
	var domain string
	var sshTarget string
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
				_, out, errOut, err = xexec.RunSSH(sshTarget, "docker", []string{"exec", "edge-proxy", "caddy", "reload", "--config", "/etc/caddy/Caddyfile"}, dir)
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
	cmd.Flags().BoolVar(&reload, "reload", false, "reload Caddy inside edge-proxy container")
	return cmd
}

func newEdgeLogsCmd() *cobra.Command {
	var dir string
	var follow bool
	var tail int
	var sshTarget string
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
			_, out, errOut, err = xexec.RunSSH(sshTarget, "docker", arg, dir)
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

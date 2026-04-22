package cmd

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	fwcfg "frameworks/cli/internal/config"
	"frameworks/cli/internal/readiness"
	"frameworks/cli/internal/ux"
	"frameworks/cli/pkg/detect"
	fwgitops "frameworks/cli/pkg/gitops"
	"frameworks/cli/pkg/health"
	"frameworks/cli/pkg/inventory"
	fwssh "frameworks/cli/pkg/ssh"
	"frameworks/pkg/servicedefs"

	"github.com/mattn/go-isatty"
	"github.com/spf13/cobra"
)

func newClusterCmd() *cobra.Command {
	cluster := &cobra.Command{
		Use:   "cluster",
		Short: "Cluster infrastructure management (central/regional control planes)",
		Long: `Manage central and regional FrameWorks clusters including:
  - Infrastructure tier (Postgres, Kafka, Zookeeper, ClickHouse)
  - Application services (Quartermaster, Commodore, Bridge, Periscope, etc.)
  - Interface services (Nginx/Caddy, Chartroom, Foredeck, Logbook)

Manifest-source selection is shared across all subcommands via these
persistent flags. Set a default via 'frameworks setup' or pass them per
invocation. Explicit flags always win over saved context defaults.`,
	}

	cluster.PersistentFlags().String("manifest", "", "path to a single cluster.yaml (overrides gitops sources)")
	cluster.PersistentFlags().String("gitops-dir", "", "path to a local gitops repo (uses <dir>/clusters/<cluster>/cluster.yaml)")
	cluster.PersistentFlags().String("github-repo", "", "GitHub repo (owner/repo) to fetch the manifest from")
	cluster.PersistentFlags().String("github-ref", "", "branch/tag for --github-repo (default 'main')")
	cluster.PersistentFlags().String("cluster", "", "cluster name within the gitops repo (e.g. 'production')")
	cluster.PersistentFlags().String("age-key", "", "path to an age private key for SOPS-encrypted files (default: $SOPS_AGE_KEY_FILE)")
	cluster.PersistentFlags().String("ssh-key", "", "SSH private key path (overrides ssh-agent/ssh_config defaults)")
	cluster.PersistentFlags().Int64("github-app-id", 0, "GitHub App ID (for --github-repo)")
	cluster.PersistentFlags().Int64("github-installation-id", 0, "GitHub Installation ID (for --github-repo)")
	cluster.PersistentFlags().String("github-private-key", "", "path to GitHub App private key PEM (for --github-repo)")

	cluster.AddCommand(newClusterDetectCmd())
	cluster.AddCommand(newClusterDoctorCmd())
	cluster.AddCommand(newClusterStatusCmd())
	cluster.AddCommand(newClusterProvisionCmd())
	cluster.AddCommand(newClusterInitCmd())
	cluster.AddCommand(newClusterLogsCmd())
	cluster.AddCommand(newClusterRestartCmd())
	cluster.AddCommand(newClusterUpgradeCmd())
	cluster.AddCommand(newClusterBackupCmd())
	cluster.AddCommand(newClusterRestoreCmd())
	cluster.AddCommand(newClusterDiagnoseCmd())
	cluster.AddCommand(newClusterSyncGeoIPCmd())
	cluster.AddCommand(newClusterSetChannelCmd())
	cluster.AddCommand(newClusterPreflightCmd())
	cluster.AddCommand(newClusterMigrateCmd())
	cluster.AddCommand(newClusterSeedCmd())

	return cluster
}

type resolvedCluster struct {
	Manifest     *inventory.Manifest
	ManifestPath string
	AgeKey       string
	Source       inventory.ManifestSource
	Cleanup      func()

	sharedEnvOnce sync.Once
	sharedEnv     map[string]string
	sharedEnvErr  error
}

// SharedEnv decrypts and merges the manifest's top-level env_files on first
// call and caches the result. Only call from commands that consume platform
// secrets — read-only commands (status/logs/detect/diagnose/doctor/backup/
// restore/channel) must not trigger SOPS decryption here.
func (rc *resolvedCluster) SharedEnv() (map[string]string, error) {
	rc.sharedEnvOnce.Do(func() {
		rc.sharedEnv, rc.sharedEnvErr = inventory.LoadSharedEnv(
			rc.Manifest, filepath.Dir(rc.ManifestPath), rc.AgeKey,
		)
	})
	return rc.sharedEnv, rc.sharedEnvErr
}

func resolveClusterManifest(cmd *cobra.Command) (*resolvedCluster, error) {
	cfg, err := fwcfg.Load()
	if err != nil {
		return nil, err
	}
	rt := fwcfg.GetRuntimeOverrides()
	ctxCfg, err := fwcfg.MaybeActiveContext(rt, fwcfg.OSEnv{}, cfg)
	if err != nil {
		return nil, err
	}

	cwd, cwdErr := os.Getwd()
	if cwdErr != nil {
		cwd = ""
	}

	manifestFlag := stringFlag(cmd, "manifest")
	gitopsDirFlag := stringFlag(cmd, "gitops-dir")
	githubRepoFlag := stringFlag(cmd, "github-repo")
	if !manifestFlag.Changed && !gitopsDirFlag.Changed && !githubRepoFlag.Changed &&
		!manifestSourceInEnv() && ctxCfg.Gitops == nil && ctxCfg.LastManifestPath != "" {
		if _, statErr := os.Stat(ctxCfg.LastManifestPath); statErr == nil {
			ux.ContextNotice(cmd.OutOrStdout(), "manifest", ctxCfg.LastManifestPath)
			manifestFlag = inventory.StringFlag{Value: ctxCfg.LastManifestPath, Changed: true}
		}
	}

	in := inventory.ResolveInput{
		Manifest:    manifestFlag,
		GitopsDir:   gitopsDirFlag,
		GithubRepo:  githubRepoFlag,
		GithubRef:   stringFlag(cmd, "github-ref"),
		Cluster:     stringFlag(cmd, "cluster"),
		AgeKey:      stringFlag(cmd, "age-key"),
		GithubAppID: int64Flag(cmd, "github-app-id"),
		GithubInst:  int64Flag(cmd, "github-installation-id"),
		GithubKey:   stringFlag(cmd, "github-private-key"),
		Env:         fwcfg.OSEnv{},
		Context:     ctxCfg,
		GitHubCfg:   cfg.GitHub,
		Cwd:         cwd,
		GithubFetch: fwgitops.NewGithubAppFetcher(),
		Stdout:      cmd.OutOrStdout(),
		Ctx:         cmd.Context(),
	}

	rm, err := inventory.ResolveManifestSource(in)
	if err != nil {
		return nil, err
	}

	manifest, err := inventory.LoadWithHosts(rm.Path, rm.AgeKey)
	if err != nil {
		if rm.Cleanup != nil {
			rm.Cleanup()
		}
		return nil, fmt.Errorf("load manifest %s: %w", rm.Path, err)
	}

	maybePrintSetupHint(cmd, rm, ctxCfg, rt)

	return &resolvedCluster{
		Manifest:     manifest,
		ManifestPath: rm.Path,
		AgeKey:       rm.AgeKey,
		Source:       rm.Source,
		Cleanup:      rm.Cleanup,
	}, nil
}

func manifestSourceInEnv() bool {
	for _, k := range []string{"FRAMEWORKS_MANIFEST", "FRAMEWORKS_GITOPS_DIR", "FRAMEWORKS_GITHUB_REPO"} {
		if os.Getenv(k) != "" {
			return true
		}
	}
	return false
}

func stringFlag(cmd *cobra.Command, name string) inventory.StringFlag {
	f := cmd.Flags().Lookup(name)
	if f == nil {
		return inventory.StringFlag{}
	}
	return inventory.StringFlag{Value: f.Value.String(), Changed: f.Changed}
}

func int64Flag(cmd *cobra.Command, name string) inventory.Int64Flag {
	f := cmd.Flags().Lookup(name)
	if f == nil {
		return inventory.Int64Flag{}
	}
	var v int64
	if _, err := fmt.Sscanf(f.Value.String(), "%d", &v); err != nil {
		v = 0
	}
	return inventory.Int64Flag{Value: v, Changed: f.Changed}
}

func maybePrintSetupHint(cmd *cobra.Command, rm inventory.Resolved, ctx fwcfg.Context, rt fwcfg.RuntimeOverrides) {
	if rm.Source != inventory.SourceCwdHeuristic {
		return
	}
	if ctx.Gitops != nil {
		return
	}
	if rt.OutputJSON || rt.NoHints {
		return
	}
	if !isatty.IsTerminal(os.Stdin.Fd()) {
		return
	}
	fmt.Fprintln(cmd.ErrOrStderr(), "Tip: run 'frameworks setup' to configure manifest defaults.")
}

const perHostTimeout = 15 * time.Second

func newClusterDetectCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "detect",
		Short: "Detect current state of all services in the cluster",
		Long: `Scan the cluster and detect the current state of all services:
  - Which services are running (docker, native, or unknown)
  - Service versions
  - Health status
  - Configuration state`,
		Example: `  # Detect all services in the current context's cluster
  frameworks cluster detect

  # Detect using an explicit manifest
  frameworks cluster detect --manifest ./cluster.yaml`,
		RunE: func(cmd *cobra.Command, args []string) error {
			rc, err := resolveClusterManifest(cmd)
			if err != nil {
				return err
			}
			defer rc.Cleanup()
			if err := runDetect(cmd, rc.Manifest, rc.ManifestPath); err != nil {
				return err
			}
			if rc.Source == inventory.SourceManifestFlag {
				rememberLastManifest(cmd, rc.ManifestPath)
			}
			return nil
		},
	}
	return cmd
}

func newClusterDoctorCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Comprehensive health check of all cluster services",
		Long: `Run comprehensive health checks across the entire cluster:

Infrastructure Health:
  - Postgres: connection, databases, query performance, replication
  - Kafka: cluster health, topics, consumer lag, broker disk
  - Zookeeper: quorum, leader election
  - ClickHouse: connection, tables, query performance

Application Services:
  - Health endpoints (/health)
  - Database connectivity
  - Kafka consumer status
  - Active connections/subscriptions

Networking & Connectivity:
  - WireGuard mesh (if configured)
  - /etc/hosts overrides
  - Service discovery (can services reach dependencies)

Data Initialization:
  - Postgres schemas and tables
  - Kafka topics and partitions
  - ClickHouse tables

Reports critical issues and provides actionable recommendations.`,
		Example: `  frameworks cluster doctor
  frameworks cluster doctor --manifest ./cluster.yaml`,
		RunE: func(cmd *cobra.Command, args []string) error {
			rc, err := resolveClusterManifest(cmd)
			if err != nil {
				return err
			}
			defer rc.Cleanup()
			return runDoctor(cmd, rc.Manifest, rc.ManifestPath)
		},
	}
	return cmd
}

func runDetect(cmd *cobra.Command, manifest *inventory.Manifest, manifestPath string) error {
	ux.Heading(cmd.OutOrStdout(), fmt.Sprintf("Detecting cluster state from manifest: %s", manifestPath))
	fmt.Fprintf(cmd.OutOrStdout(), "Cluster type: %s, Profile: %s\n", manifest.Type, manifest.Profile)
	fmt.Fprintf(cmd.OutOrStdout(), "Hosts: %d\n\n", len(manifest.Hosts))

	sshKey := stringFlag(cmd, "ssh-key").Value
	sshPool := fwssh.NewPool(30*time.Second, sshKey)
	defer sshPool.Close()

	if manifest.Infrastructure.Postgres != nil && manifest.Infrastructure.Postgres.Enabled {
		detectServiceWithTimeout(cmd, sshPool, manifest, "postgres", "postgres", manifest.Infrastructure.Postgres.Host)
	}

	if manifest.Infrastructure.ClickHouse != nil && manifest.Infrastructure.ClickHouse.Enabled {
		detectServiceWithTimeout(cmd, sshPool, manifest, "clickhouse", "clickhouse", manifest.Infrastructure.ClickHouse.Host)
	}

	if manifest.Infrastructure.Kafka != nil && manifest.Infrastructure.Kafka.Enabled {
		for _, ctrl := range manifest.Infrastructure.Kafka.Controllers {
			serviceName := fmt.Sprintf("kafka-controller-%d", ctrl.ID)
			detectServiceWithTimeout(cmd, sshPool, manifest, serviceName, "kafka-controller", ctrl.Host)
		}
		for _, broker := range manifest.Infrastructure.Kafka.Brokers {
			serviceName := fmt.Sprintf("kafka-broker-%d", broker.ID)
			detectServiceWithTimeout(cmd, sshPool, manifest, serviceName, "kafka", broker.Host)
		}
	}

	if manifest.Infrastructure.Zookeeper != nil && manifest.Infrastructure.Zookeeper.Enabled {
		for _, node := range manifest.Infrastructure.Zookeeper.Ensemble {
			serviceName := fmt.Sprintf("zookeeper-%d", node.ID)
			detectServiceWithTimeout(cmd, sshPool, manifest, serviceName, "zookeeper", node.Host)
		}
	}

	for name, svc := range manifest.Services {
		if !svc.Enabled {
			continue
		}
		if svc.Host != "" {
			deploy, err := resolveDeployName(name, svc)
			if err != nil {
				ux.Fail(cmd.OutOrStdout(), fmt.Sprintf("%s: %v", name, err))
				continue
			}
			detectServiceWithTimeout(cmd, sshPool, manifest, name, deploy, svc.Host)
		} else if len(svc.Hosts) > 0 {
			for i, hostName := range svc.Hosts {
				deploy, err := resolveDeployName(name, svc)
				if err != nil {
					ux.Fail(cmd.OutOrStdout(), fmt.Sprintf("%s: %v", name, err))
					continue
				}
				serviceName := fmt.Sprintf("%s-%d", name, i+1)
				detectServiceWithTimeout(cmd, sshPool, manifest, serviceName, deploy, hostName)
			}
		}
	}

	for name, iface := range manifest.Interfaces {
		if !iface.Enabled {
			continue
		}
		if iface.Host != "" {
			deploy, err := resolveDeployName(name, iface)
			if err != nil {
				ux.Fail(cmd.OutOrStdout(), fmt.Sprintf("%s: %v", name, err))
				continue
			}
			detectServiceWithTimeout(cmd, sshPool, manifest, name, deploy, iface.Host)
		}
	}

	for name, obs := range manifest.Observability {
		if !obs.Enabled {
			continue
		}
		if obs.Host != "" {
			deploy, err := resolveDeployName(name, obs)
			if err != nil {
				ux.Fail(cmd.OutOrStdout(), fmt.Sprintf("%s: %v", name, err))
				continue
			}
			detectServiceWithTimeout(cmd, sshPool, manifest, name, deploy, obs.Host)
		}
	}

	return nil
}

func detectServiceWithTimeout(cmd *cobra.Command, sshPool *fwssh.Pool, manifest *inventory.Manifest, serviceName, deployName, hostName string) {
	ctx, cancel := context.WithTimeout(context.Background(), perHostTimeout)
	defer cancel()

	done := make(chan struct{})
	go func() {
		detectService(ctx, cmd, sshPool, manifest, serviceName, deployName, hostName)
		close(done)
	}()

	select {
	case <-done:
	case <-ctx.Done():
		ux.Fail(cmd.OutOrStdout(), fmt.Sprintf("%s (%s): timed out after %s", serviceName, hostName, perHostTimeout))
	}
}

func detectService(ctx context.Context, cmd *cobra.Command, sshPool *fwssh.Pool, manifest *inventory.Manifest, serviceName, deployName, hostName string) {
	host, ok := manifest.GetHost(hostName)
	if !ok {
		ux.Fail(cmd.OutOrStdout(), fmt.Sprintf("%s: host %q not found", serviceName, hostName))
		return
	}

	detector := detect.NewDetector(sshPool, host)
	state, err := detector.Detect(ctx, deployName)

	if err != nil {
		ux.Fail(cmd.OutOrStdout(), fmt.Sprintf("%s (%s): detection error: %v", serviceName, hostName, err))
		return
	}

	if !state.Exists {
		ux.Warn(cmd.OutOrStdout(), fmt.Sprintf("%s (%s): not installed", serviceName, hostName))
		return
	}

	modeStr := state.Mode
	if state.Version != "" {
		modeStr = fmt.Sprintf("%s, v%s", state.Mode, state.Version)
	}

	line := fmt.Sprintf("%s (%s): running [%s, detected by: %s]", serviceName, hostName, modeStr, state.DetectedBy)
	if state.Running {
		ux.Success(cmd.OutOrStdout(), line)
	} else {
		ux.Warn(cmd.OutOrStdout(), fmt.Sprintf("%s (%s): stopped [%s, detected by: %s]", serviceName, hostName, modeStr, state.DetectedBy))
	}

	if verbose && len(state.Metadata) > 0 {
		for k, v := range state.Metadata {
			fmt.Fprintf(cmd.OutOrStdout(), "    %s: %s\n", k, v)
		}
	}
}

func runDoctor(cmd *cobra.Command, manifest *inventory.Manifest, manifestPath string) error {
	ux.Heading(cmd.OutOrStdout(), "Running cluster health checks")
	fmt.Fprintf(cmd.OutOrStdout(), "Manifest: %s (type: %s, profile: %s)\n\n", manifestPath, manifest.Type, manifest.Profile)

	totalChecks := 0
	passedChecks := 0

	fmt.Fprintln(cmd.OutOrStdout(), "Infrastructure Health:")
	fmt.Fprintln(cmd.OutOrStdout(), "")

	if manifest.Infrastructure.Postgres != nil && manifest.Infrastructure.Postgres.Enabled {
		host, ok := manifest.GetHost(manifest.Infrastructure.Postgres.Host)
		if !ok {
			fmt.Fprintf(cmd.OutOrStdout(), "✗ Postgres: host '%s' not found\n", manifest.Infrastructure.Postgres.Host)
		} else {
			totalChecks++
			checker := &health.PostgresChecker{
				User:     "postgres",
				Password: "",
				Database: "postgres",
			}
			result := checker.Check(host.ExternalIP, manifest.Infrastructure.Postgres.Port)
			printHealthResult(cmd, "Postgres/Yugabyte", result)
			if result.OK {
				passedChecks++
			}
		}
	}

	if manifest.Infrastructure.ClickHouse != nil && manifest.Infrastructure.ClickHouse.Enabled {
		host, ok := manifest.GetHost(manifest.Infrastructure.ClickHouse.Host)
		if !ok {
			fmt.Fprintf(cmd.OutOrStdout(), "✗ ClickHouse: host '%s' not found\n", manifest.Infrastructure.ClickHouse.Host)
		} else {
			totalChecks++
			checker := &health.ClickHouseChecker{
				User:     "default",
				Password: "",
				Database: "default",
			}
			result := checker.Check(host.ExternalIP, manifest.Infrastructure.ClickHouse.Port)
			printHealthResult(cmd, "ClickHouse", result)
			if result.OK {
				passedChecks++
			}
		}
	}

	if manifest.Infrastructure.Kafka != nil && manifest.Infrastructure.Kafka.Enabled {
		for _, broker := range manifest.Infrastructure.Kafka.Brokers {
			host, ok := manifest.GetHost(broker.Host)
			if !ok {
				fmt.Fprintf(cmd.OutOrStdout(), "✗ Kafka broker %d: host '%s' not found\n", broker.ID, broker.Host)
				continue
			}
			totalChecks++
			checker := &health.KafkaChecker{}
			result := checker.Check(host.ExternalIP, broker.Port)
			printHealthResult(cmd, fmt.Sprintf("Kafka Broker %d", broker.ID), result)
			if result.OK {
				passedChecks++
			}
		}
	}

	fmt.Fprintln(cmd.OutOrStdout(), "")
	fmt.Fprintln(cmd.OutOrStdout(), "Application Services:")
	fmt.Fprintln(cmd.OutOrStdout(), "")

	for name, svc := range manifest.Services {
		if !svc.Enabled {
			continue
		}

		hostName := svc.Host
		if hostName == "" && len(svc.Hosts) > 0 {
			hostName = svc.Hosts[0]
		}

		if hostName == "" {
			continue
		}

		host, ok := manifest.GetHost(hostName)
		if !ok {
			fmt.Fprintf(cmd.OutOrStdout(), "✗ %s: host '%s' not found\n", name, hostName)
			continue
		}

		port, err := resolvePort(name, svc)
		if err != nil {
			fmt.Fprintf(cmd.OutOrStdout(), "✗ %s: %v\n", name, err)
			continue
		}
		totalChecks++
		path := "/health"
		if def, ok := servicedefs.Lookup(name); ok && def.HealthPath != "" {
			path = def.HealthPath
		}
		checker := &health.HTTPChecker{
			Path:    path,
			Timeout: 5 * time.Second,
		}
		result := checker.Check(host.ExternalIP, port)
		printHealthResult(cmd, name, result)
		if result.OK {
			passedChecks++
		}
	}

	fmt.Fprintln(cmd.OutOrStdout(), "")
	fmt.Fprintln(cmd.OutOrStdout(), "Interface Services:")
	fmt.Fprintln(cmd.OutOrStdout(), "")

	for name, svc := range manifest.Interfaces {
		if !svc.Enabled {
			continue
		}
		if svc.Host == "" {
			continue
		}

		host, ok := manifest.GetHost(svc.Host)
		if !ok {
			fmt.Fprintf(cmd.OutOrStdout(), "✗ %s: host '%s' not found\n", name, svc.Host)
			continue
		}

		port, err := resolvePort(name, svc)
		if err != nil {
			fmt.Fprintf(cmd.OutOrStdout(), "✗ %s: %v\n", name, err)
			continue
		}

		totalChecks++
		path := "/health"
		if def, ok := servicedefs.Lookup(name); ok && def.HealthPath != "" {
			path = def.HealthPath
		}
		checker := &health.HTTPChecker{
			Path:    path,
			Timeout: 5 * time.Second,
		}
		result := checker.Check(host.ExternalIP, port)
		printHealthResult(cmd, name, result)
		if result.OK {
			passedChecks++
		}
	}

	fmt.Fprintln(cmd.OutOrStdout(), "")

	if len(manifest.Observability) > 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "Observability Services:")
		fmt.Fprintln(cmd.OutOrStdout(), "")
		for name, svc := range manifest.Observability {
			if !svc.Enabled {
				continue
			}
			if svc.Host == "" {
				continue
			}
			host, ok := manifest.GetHost(svc.Host)
			if !ok {
				fmt.Fprintf(cmd.OutOrStdout(), "✗ %s: host '%s' not found\n", name, svc.Host)
				continue
			}
			port, err := resolvePort(name, svc)
			if err != nil {
				ux.Fail(cmd.OutOrStdout(), fmt.Sprintf("%s: %v", name, err))
				continue
			}
			totalChecks++
			path := "/health"
			if def, ok := servicedefs.Lookup(name); ok && def.HealthPath != "" {
				path = def.HealthPath
			}
			checker := &health.HTTPChecker{
				Path:    path,
				Timeout: 5 * time.Second,
			}
			result := checker.Check(host.ExternalIP, port)
			printHealthResult(cmd, name, result)
			if result.OK {
				passedChecks++
			}
		}
		fmt.Fprintln(cmd.OutOrStdout(), "")
	}

	fmt.Fprintf(cmd.OutOrStdout(), "Summary: %d/%d checks passed\n", passedChecks, totalChecks)

	printDoctorControlPlaneSection(cmd, manifest)

	if passedChecks < totalChecks {
		ux.PrintNextSteps(cmd.OutOrStdout(), []ux.NextStep{
			{Cmd: "frameworks cluster detect", Why: "See which services are running vs. missing."},
			{Cmd: "frameworks cluster logs <service>", Why: "Review logs for the failing service."},
		})
	}

	return nil
}

// printDoctorControlPlaneSection runs the shared ControlPlaneReadiness check
// against what doctor can see without SOPS access: the tenant ID the
// context remembered at provision time, plus the Quartermaster address
// from the manifest. With no service token, readiness returns Checked=false
// and we render "not re-verified" honestly rather than fabricating a
// healthy-looking summary.
//
// Doctor stays read-only by design (no SOPS decryption). Operators who
// need the deep check re-run `cluster provision --ready` or use admin
// commands that already carry a service token.
func printDoctorControlPlaneSection(cmd *cobra.Command, manifest *inventory.Manifest) {
	out := cmd.OutOrStdout()
	cfg, err := fwcfg.Load()
	if err != nil {
		return
	}
	active, mErr := fwcfg.MaybeActiveContext(fwcfg.GetRuntimeOverrides(), fwcfg.OSEnv{}, cfg)
	if mErr != nil || active.SystemTenantID == "" {
		return
	}

	qmAddr, _ := resolveServiceGRPCAddr(manifest, "quartermaster", 19002)    //nolint:errcheck // empty on miss is the intent
	commodoreAddr, _ := resolveServiceGRPCAddr(manifest, "commodore", 19001) //nolint:errcheck // empty on miss is the intent
	purserAddr, _ := resolveServiceGRPCAddr(manifest, "purser", 19003)       //nolint:errcheck // empty on miss is the intent

	var pricings []readiness.ClusterPricing
	for clusterID, cc := range manifest.Clusters {
		if cc.Pricing != nil {
			pricings = append(pricings, readiness.ClusterPricing{ClusterID: clusterID})
		}
	}

	report := readiness.ControlPlaneReadiness(cmd.Context(), readiness.ControlPlaneInputs{
		SystemTenantID:    active.SystemTenantID,
		QuartermasterAddr: qmAddr,
		CommodoreAddr:     commodoreAddr,
		PurserAddr:        purserAddr,
		DeclaredPricings:  pricings,
		// ServiceToken intentionally empty — doctor doesn't SOPS-decrypt.
	})

	fmt.Fprintln(out, "")
	ux.Subheading(out, "Control Plane:")
	fmt.Fprintf(out, "  system tenant:  %s\n", active.SystemTenantID)
	if qmAddr != "" {
		fmt.Fprintf(out, "  quartermaster:  %s\n", qmAddr)
	}
	renderReadinessBlock(cmd, report, []ux.NextStep{
		{Cmd: "frameworks cluster provision --ready", Why: "Re-run with SOPS access so the control-plane check gets a real service token."},
		{Cmd: "frameworks admin clusters list", Why: "Any admin command that touches Quartermaster reads the service token and will succeed/fail explicitly."},
	})
}

// renderReadinessBlock prints a readiness.Report inline: per-warning
// status lines + remediation next-steps. When the report couldn't run
// (Checked=false), it says so instead of implying "healthy" and renders
// the caller-supplied fallback next-steps so the operator knows how to
// actually re-verify.
func renderReadinessBlock(cmd *cobra.Command, report readiness.Report, notCheckedFallback []ux.NextStep) {
	out := cmd.OutOrStdout()
	if !report.Checked {
		ux.Warn(out, "control-plane not verified (no service token available in this command)")
		ux.PrintNextSteps(out, notCheckedFallback)
		return
	}
	if len(report.Warnings) == 0 {
		ux.Success(out, "control-plane verified")
		return
	}
	for _, w := range report.Warnings {
		ux.Warn(out, w.Detail)
	}
	steps := make([]ux.NextStep, 0, len(report.Warnings))
	for _, w := range report.Warnings {
		if w.Remediation.Cmd == "" && w.Remediation.Why == "" {
			continue
		}
		steps = append(steps, ux.NextStep{Cmd: w.Remediation.Cmd, Why: w.Remediation.Why})
	}
	ux.PrintNextSteps(out, steps)
}

func printHealthResult(cmd *cobra.Command, serviceName string, result *health.CheckResult) {
	statusStr := result.Status
	if result.Message != "" {
		statusStr = result.Message
	} else if result.Error != "" {
		statusStr = result.Error
	}

	line := fmt.Sprintf("%-20s: %s", serviceName, statusStr)
	switch {
	case result.OK:
		ux.Success(cmd.OutOrStdout(), line)
	case result.Status == "degraded":
		ux.Warn(cmd.OutOrStdout(), line)
	default:
		ux.Fail(cmd.OutOrStdout(), line)
	}

	if verbose && len(result.Metadata) > 0 {
		for k, v := range result.Metadata {
			fmt.Fprintf(cmd.OutOrStdout(), "    %s: %s\n", k, v)
		}
	}
}

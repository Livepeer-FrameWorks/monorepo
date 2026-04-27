package cmd

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
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
	cluster.AddCommand(newClusterDriftCmd())
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
	Cluster      string
	ReleaseRepos []string
	Cleanup      func()

	sharedEnvOnce sync.Once
	sharedEnv     map[string]string
	sharedEnvErr  error
}

// SharedEnv decrypts and merges the manifest's top-level env_files on
// first call and caches the result. Only call from commands that consume
// platform secrets — read-only commands must not trigger SOPS here.
func (rc *resolvedCluster) SharedEnv() (map[string]string, error) {
	rc.sharedEnvOnce.Do(func() {
		rc.sharedEnv, rc.sharedEnvErr = inventory.LoadSharedEnv(
			rc.Manifest, filepath.Dir(rc.ManifestPath), rc.AgeKey,
		)
	})
	return rc.sharedEnv, rc.sharedEnvErr
}

func (rc *resolvedCluster) applyReleaseMetadata(metadata map[string]any) {
	if metadata == nil || len(rc.ReleaseRepos) == 0 {
		return
	}
	repos := make([]string, len(rc.ReleaseRepos))
	copy(repos, rc.ReleaseRepos)
	metadata["gitops_repositories"] = repos
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
		Cluster:      rm.Cluster,
		ReleaseRepos: resolveReleaseRepositories(cmd, cfg, ctxCfg, rm, cwd),
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

func resolveReleaseRepositories(cmd *cobra.Command, cfg fwcfg.Config, ctx fwcfg.Context, rm inventory.Resolved, cwd string) []string {
	var repos []string
	add := func(repo string) {
		repo = strings.TrimSpace(repo)
		if repo == "" {
			return
		}
		if slices.Contains(repos, repo) {
			return
		}
		repos = append(repos, repo)
	}

	switch rm.Source {
	case inventory.SourceGitopsDirFlag:
		add(stringFlag(cmd, "gitops-dir").Value)
	case inventory.SourceGitopsDirEnv:
		add(os.Getenv("FRAMEWORKS_GITOPS_DIR"))
	case inventory.SourceCwdHeuristic:
		add(cwd)
	case inventory.SourceGithubRepoFlag:
		add(rawGitHubRepoURL(stringFlag(cmd, "github-repo").Value, explicitGitHubRef(cmd, cfg, false)))
	case inventory.SourceGithubRepoEnv:
		add(rawGitHubRepoURL(os.Getenv("FRAMEWORKS_GITHUB_REPO"), explicitGitHubRef(cmd, cfg, true)))
	case inventory.SourceContext:
		if ctx.Gitops != nil {
			switch ctx.Gitops.Source {
			case fwcfg.GitopsLocal:
				add(ctx.Gitops.LocalPath)
			case fwcfg.GitopsGitHub:
				add(rawGitHubRepoURL(ctx.Gitops.Repo, contextGitHubRef(cfg, ctx)))
			case fwcfg.GitopsManifest:
				add(findGitopsRootFromManifest(rm.Path))
			}
		}
	case inventory.SourceManifestFlag, inventory.SourceManifestEnv:
		add(findGitopsRootFromManifest(rm.Path))
	}

	add(fwgitops.DefaultRepository)
	return repos
}

func explicitGitHubRef(cmd *cobra.Command, cfg fwcfg.Config, preferEnv bool) string {
	if preferEnv {
		if ref := strings.TrimSpace(os.Getenv("FRAMEWORKS_GITHUB_REF")); ref != "" {
			return ref
		}
	} else if ref := strings.TrimSpace(stringFlag(cmd, "github-ref").Value); ref != "" {
		return ref
	}
	if cfg.GitHub != nil && strings.TrimSpace(cfg.GitHub.Ref) != "" {
		return strings.TrimSpace(cfg.GitHub.Ref)
	}
	return "main"
}

func contextGitHubRef(cfg fwcfg.Config, ctx fwcfg.Context) string {
	if ctx.Gitops != nil && strings.TrimSpace(ctx.Gitops.Ref) != "" {
		return strings.TrimSpace(ctx.Gitops.Ref)
	}
	if cfg.GitHub != nil && strings.TrimSpace(cfg.GitHub.Ref) != "" {
		return strings.TrimSpace(cfg.GitHub.Ref)
	}
	return "main"
}

func rawGitHubRepoURL(repo, ref string) string {
	repo = strings.TrimSpace(repo)
	ref = strings.TrimSpace(ref)
	if repo == "" {
		return ""
	}
	if ref == "" {
		ref = "main"
	}
	return fmt.Sprintf("https://raw.githubusercontent.com/%s/%s", repo, ref)
}

func findGitopsRootFromManifest(manifestPath string) string {
	if strings.TrimSpace(manifestPath) == "" {
		return ""
	}
	dir := filepath.Dir(manifestPath)
	for {
		if looksLikeReleaseRepoRoot(dir) {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

func looksLikeReleaseRepoRoot(dir string) bool {
	if dir == "" {
		return false
	}
	for _, name := range []string{"clusters", "channels", "releases"} {
		st, err := os.Stat(filepath.Join(dir, name))
		if err != nil || !st.IsDir() {
			return false
		}
	}
	return true
}

func stringFlag(cmd *cobra.Command, name string) inventory.StringFlag {
	f := cmd.Flag(name)
	if f == nil {
		return inventory.StringFlag{}
	}
	return inventory.StringFlag{Value: f.Value.String(), Changed: f.Changed}
}

func int64Flag(cmd *cobra.Command, name string) inventory.Int64Flag {
	f := cmd.Flag(name)
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
	var deep bool
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Health check for cluster infrastructure and services",
		Long: `Health check for the cluster's infrastructure and application services.

Default mode (read-only, no SOPS decryption):
  - Infrastructure reachability: Postgres/Yugabyte, Kafka, Zookeeper, ClickHouse,
    Redis — port/connection probes only, not query performance or replication state.
  - Application services: HTTP /health endpoints.
  - Control plane: read-only view of SystemTenantID + Quartermaster address from
    the active context. Authenticated checks are skipped (reported as
    "not verified") — pass --deep for the full check.

--deep mode (opts into SOPS decryption to obtain SERVICE_TOKEN):
  - All of the above, plus authenticated Quartermaster/Commodore/Purser checks:
    default cluster + platform-official cluster flags, operator-account presence
    in the system tenant, pricing config for clusters that declared it.
  - Requires a readable age key (SOPS_AGE_KEY_FILE, --age-key, or the active
    context's set-age-key value). Fails explicitly if the key is missing or
    decryption errors — never silently falls back.

Failing checks produce actionable remediation commands where one exists (e.g.
'cluster logs <service>', 'cluster diagnose kafka').`,
		Example: `  frameworks cluster doctor
  frameworks cluster doctor --deep
  frameworks cluster doctor --manifest ./cluster.yaml --deep`,
		RunE: func(cmd *cobra.Command, args []string) error {
			rc, err := resolveClusterManifest(cmd)
			if err != nil {
				return err
			}
			defer rc.Cleanup()
			return runDoctor(cmd, rc, deep)
		},
	}
	cmd.Flags().BoolVar(&deep, "deep", false, "Decrypt SOPS env_files to obtain SERVICE_TOKEN and run authenticated control-plane checks (Quartermaster/Commodore/Purser). Requires an age key.")
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

// runDoctor is an observed-state survey: direct port / HTTP / SQL probes
// from the CLI host. For a role-level diff of what would change on apply,
// use `cluster provision --dry-run` (ansible-playbook --check --diff).
func runDoctor(cmd *cobra.Command, rc *resolvedCluster, deep bool) error {
	manifest := rc.Manifest
	out := cmd.OutOrStdout()
	ux.Heading(out, "Running cluster health checks")
	fmt.Fprintf(out, "Manifest: %s (type: %s, profile: %s)\n", rc.ManifestPath, manifest.Type, manifest.Profile)
	if deep {
		fmt.Fprintln(out, "Mode: --deep (authenticated control-plane check enabled)")
	}
	fmt.Fprintln(out, "")

	var serviceToken string
	if deep {
		sharedEnv, err := rc.SharedEnv()
		if err != nil {
			ux.FormatError(cmd.ErrOrStderr(), fmt.Errorf("--deep requires SOPS decryption, which failed: %w", err), "set an age key via 'frameworks context set-age-key <path>' or SOPS_AGE_KEY_FILE, then re-run")
			return fmt.Errorf("--deep: SOPS decrypt failed: %w", err)
		}
		serviceToken = strings.TrimSpace(sharedEnv["SERVICE_TOKEN"])
		if serviceToken == "" {
			ux.FormatError(cmd.ErrOrStderr(), fmt.Errorf("--deep: SERVICE_TOKEN missing from manifest env_files"), "confirm SERVICE_TOKEN is set in the decrypted shared env")
			return fmt.Errorf("--deep: SERVICE_TOKEN missing")
		}
	}

	var remediationSteps []ux.NextStep
	totalChecks := 0
	passedChecks := 0

	fmt.Fprintln(cmd.OutOrStdout(), "Infrastructure Health:")
	fmt.Fprintln(cmd.OutOrStdout(), "")

	runInfraCheck := func(name string, result *health.CheckResult) {
		totalChecks++
		printHealthResult(cmd, name, result)
		if result.OK {
			passedChecks++
			return
		}
		if step := doctorServiceRemediation(name); step.Cmd != "" || step.Why != "" {
			remediationSteps = append(remediationSteps, step)
		}
	}

	recordMiss := func(name, reason string) {
		totalChecks++
		ux.Fail(out, fmt.Sprintf("%s: %s", name, reason))
		if step := doctorServiceRemediation(name); step.Cmd != "" || step.Why != "" {
			remediationSteps = append(remediationSteps, step)
		}
	}

	if manifest.Infrastructure.Postgres != nil && manifest.Infrastructure.Postgres.Enabled {
		host, ok := manifest.GetHost(manifest.Infrastructure.Postgres.Host)
		if !ok {
			recordMiss("Postgres/Yugabyte", fmt.Sprintf("host %q not found in manifest", manifest.Infrastructure.Postgres.Host))
		} else {
			checker := &health.PostgresChecker{User: "postgres", Password: "", Database: "postgres"}
			runInfraCheck("Postgres/Yugabyte", checker.Check(host.ExternalIP, manifest.Infrastructure.Postgres.Port))
		}
	}

	if manifest.Infrastructure.ClickHouse != nil && manifest.Infrastructure.ClickHouse.Enabled {
		host, ok := manifest.GetHost(manifest.Infrastructure.ClickHouse.Host)
		if !ok {
			recordMiss("ClickHouse", fmt.Sprintf("host %q not found in manifest", manifest.Infrastructure.ClickHouse.Host))
		} else {
			checker := &health.ClickHouseChecker{User: "default", Password: "", Database: "default"}
			runInfraCheck("ClickHouse", checker.Check(host.ExternalIP, manifest.Infrastructure.ClickHouse.Port))
		}
	}

	if manifest.Infrastructure.Kafka != nil && manifest.Infrastructure.Kafka.Enabled {
		for _, broker := range manifest.Infrastructure.Kafka.Brokers {
			brokerName := fmt.Sprintf("Kafka Broker %d", broker.ID)
			host, ok := manifest.GetHost(broker.Host)
			if !ok {
				recordMiss(brokerName, fmt.Sprintf("host %q not found in manifest", broker.Host))
				continue
			}
			checker := &health.KafkaChecker{}
			runInfraCheck(brokerName, checker.Check(host.ExternalIP, broker.Port))
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
			recordMiss(name, fmt.Sprintf("host %q not found in manifest", hostName))
			continue
		}

		port, err := resolvePort(name, svc)
		if err != nil {
			recordMiss(name, fmt.Sprintf("resolve port: %v", err))
			continue
		}
		path := "/health"
		if def, ok := servicedefs.Lookup(name); ok && def.HealthPath != "" {
			path = def.HealthPath
		}
		checker := &health.HTTPChecker{Path: path, Timeout: 5 * time.Second}
		runInfraCheck(name, checker.Check(host.ExternalIP, port))
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
			recordMiss(name, fmt.Sprintf("host %q not found in manifest", svc.Host))
			continue
		}

		port, err := resolvePort(name, svc)
		if err != nil {
			recordMiss(name, fmt.Sprintf("resolve port: %v", err))
			continue
		}

		path := "/health"
		if def, ok := servicedefs.Lookup(name); ok && def.HealthPath != "" {
			path = def.HealthPath
		}
		checker := &health.HTTPChecker{Path: path, Timeout: 5 * time.Second}
		runInfraCheck(name, checker.Check(host.ExternalIP, port))
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
				recordMiss(name, fmt.Sprintf("host %q not found in manifest", svc.Host))
				continue
			}
			port, err := resolvePort(name, svc)
			if err != nil {
				recordMiss(name, fmt.Sprintf("resolve port: %v", err))
				continue
			}
			path := "/health"
			if def, ok := servicedefs.Lookup(name); ok && def.HealthPath != "" {
				path = def.HealthPath
			}
			checker := &health.HTTPChecker{Path: path, Timeout: 5 * time.Second}
			runInfraCheck(name, checker.Check(host.ExternalIP, port))
		}
		fmt.Fprintln(out, "")
	}

	cpReport, cpSteps := doctorControlPlane(cmd, manifest, serviceToken)

	ux.Result(out, []ux.ResultField{
		{Key: "infrastructure + services", OK: passedChecks == totalChecks, Detail: fmt.Sprintf("%d/%d healthy", passedChecks, totalChecks)},
		{Key: "control-plane", OK: cpReport.OK(), Detail: doctorControlPlaneDetail(cpReport, deep)},
	})

	steps := append([]ux.NextStep{}, remediationSteps...)
	steps = append(steps, cpSteps...)
	if !deep && !cpReport.Checked {
		steps = append(steps, ux.NextStep{
			Cmd: "frameworks cluster doctor --deep",
			Why: "Run authenticated control-plane checks (default/official cluster, operator account, pricing).",
		})
	}
	ux.PrintNextSteps(out, steps)

	return nil
}

// doctorControlPlane runs ControlPlaneReadiness, prints its section, and
// returns the report plus next-steps derived from any warnings.
func doctorControlPlane(cmd *cobra.Command, manifest *inventory.Manifest, serviceToken string) (readiness.Report, []ux.NextStep) {
	out := cmd.OutOrStdout()
	cfg, err := fwcfg.Load()
	if err != nil {
		return readiness.Report{}, nil
	}
	active, mErr := fwcfg.MaybeActiveContext(fwcfg.GetRuntimeOverrides(), fwcfg.OSEnv{}, cfg)
	if mErr != nil || active.SystemTenantID == "" {
		return readiness.Report{}, nil
	}

	qmAddr, _ := resolveServiceGRPCAddr(manifest, "quartermaster", 19002) //nolint:errcheck // empty on miss is the intent

	// Route through buildControlPlaneReport so endpoint-resolution failures
	// surface as warnings instead of degrading silently to Checked=false.
	report := buildControlPlaneReport(cmd.Context(), manifest, map[string]any{
		"system_tenant_id": active.SystemTenantID,
		"service_token":    serviceToken,
	}, nil)

	fmt.Fprintln(out, "")
	ux.Subheading(out, "Control Plane:")
	fmt.Fprintf(out, "  system tenant:  %s\n", active.SystemTenantID)
	if qmAddr != "" {
		fmt.Fprintf(out, "  quartermaster:  %s\n", qmAddr)
	}
	switch {
	case !report.Checked:
		ux.Warn(out, "control-plane not verified (pass --deep for the authenticated check)")
	case len(report.Warnings) == 0:
		ux.Success(out, "control-plane verified")
	default:
		for _, w := range report.Warnings {
			ux.Warn(out, w.Detail)
		}
	}

	var steps []ux.NextStep
	for _, w := range report.Warnings {
		if w.Remediation.Cmd == "" && w.Remediation.Why == "" {
			continue
		}
		steps = append(steps, ux.NextStep{Cmd: w.Remediation.Cmd, Why: w.Remediation.Why})
	}
	return report, steps
}

func doctorControlPlaneDetail(r readiness.Report, deep bool) string {
	if !r.Checked {
		if deep {
			return "not verified (insufficient context; run 'cluster provision --ready' first)"
		}
		return "not verified (pass --deep for authenticated check)"
	}
	if len(r.Warnings) == 0 {
		return "healthy"
	}
	if len(r.Warnings) == 1 {
		return "1 warning"
	}
	return fmt.Sprintf("%d warnings", len(r.Warnings))
}

// doctorServiceRemediation maps a failing service name to a next-step.
// Known infrastructure names get curated commands; everything else falls
// through to `cluster logs <name>`.
func doctorServiceRemediation(serviceName string) ux.NextStep {
	n := strings.ToLower(serviceName)
	switch {
	case strings.HasPrefix(n, "postgres"), strings.HasPrefix(n, "yugabyte"):
		return ux.NextStep{
			Cmd: "frameworks cluster logs postgres",
			Why: "Inspect Postgres for connection/startup errors.",
		}
	case strings.HasPrefix(n, "kafka"):
		return ux.NextStep{
			Cmd: "frameworks cluster diagnose kafka",
			Why: "Kafka diagnosis checks broker health and topic state.",
		}
	case strings.HasPrefix(n, "clickhouse"):
		return ux.NextStep{
			Cmd: "frameworks cluster logs clickhouse",
			Why: "Inspect ClickHouse for startup/credential errors.",
		}
	case strings.HasPrefix(n, "zookeeper"):
		return ux.NextStep{
			Cmd: "frameworks cluster logs zookeeper-1",
			Why: "Zookeeper quorum issues usually show in its logs.",
		}
	case strings.HasPrefix(n, "redis"):
		return ux.NextStep{
			Cmd: "frameworks cluster logs redis",
			Why: "Inspect Redis for connection/persistence errors.",
		}
	}
	return ux.NextStep{
		Cmd: fmt.Sprintf("frameworks cluster logs %s", serviceName),
		Why: "Review service logs for startup or dependency errors.",
	}
}

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

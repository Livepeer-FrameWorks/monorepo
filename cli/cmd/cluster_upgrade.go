package cmd

import (
	"bufio"
	"context"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"frameworks/cli/internal/ux"
	"frameworks/cli/pkg/detect"
	"frameworks/cli/pkg/gitops"
	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/orchestrator"
	"frameworks/cli/pkg/provisioner"
	"frameworks/cli/pkg/ssh"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/servicedefs"
	"github.com/spf13/cobra"
)

// copyMetadata shallow-copies the metadata map so rollback can mutate its
// own version/mode without leaking into the forward-upgrade config.
func copyMetadata(in map[string]any) map[string]any {
	out := make(map[string]any, len(in))
	maps.Copy(out, in)
	return out
}

// newClusterUpgradeCmd creates the upgrade command
func newClusterUpgradeCmd() *cobra.Command {
	var version string
	var dryRun bool
	var skipValidation bool
	var yes bool
	var noRollback bool
	var all bool
	var skipMigrationCheck bool
	var skipDataMigrationCheck bool

	cmd := &cobra.Command{
		Use:   "upgrade [service]",
		Short: "Upgrade a service (or all services) to a new version",
		Long: `Upgrade a service to a new version using GitOps release manifests.

The upgrade process:
  1. Detect current version and mode (Docker or native)
  2. Fetch new version from GitOps manifest
  3. Stop the service
  4. Pull new image (Docker) or download new binary (native)
  5. Start the service with new version
  6. Validate health (unless --skip-validation)
  7. On health failure, rollback to previous version (unless --no-rollback)

Version defaults to the cluster's configured channel (set with
'frameworks cluster set-channel'). If no channel is set, defaults to stable.

Version can be:
  - Specific version: v0.0.0-rc1, v1.2.3
  - Channel: stable, rc (uses latest from channel)
  - Default: cluster channel (or stable)

Use --all to upgrade all enabled services in dependency order.

Before upgrading services that depend on PostgreSQL/YugabyteDB schema changes,
run expand-compatible migrations with 'frameworks cluster migrate'. Service
rollback does not undo schema or data migrations, so destructive contract steps
and required data migrations must follow the target release notes.`,
		Example: `  frameworks cluster upgrade quartermaster
  frameworks cluster upgrade commodore --version v0.0.0-rc2
  frameworks cluster upgrade bridge --version rc --dry-run
  frameworks cluster upgrade --all --yes`,
		Args: func(cmd *cobra.Command, args []string) error {
			if all && len(args) > 0 {
				return fmt.Errorf("--all and a service name are mutually exclusive")
			}
			if !all && len(args) != 1 {
				return fmt.Errorf("requires exactly 1 service name (or use --all)")
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			rc, err := resolveClusterManifest(cmd)
			if err != nil {
				return err
			}
			defer rc.Cleanup()
			if all {
				return runUpgradeAll(cmd, rc, version, dryRun, skipValidation, yes, noRollback, skipMigrationCheck, skipDataMigrationCheck)
			}
			return runUpgrade(cmd, rc, args[0], version, dryRun, skipValidation, yes, noRollback, skipMigrationCheck, skipDataMigrationCheck, false)
		},
	}

	cmd.Flags().StringVar(&version, "version", "", "Version to upgrade to (stable, rc, v1.2.3); defaults to cluster channel")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Show what would be upgraded without executing")
	cmd.Flags().BoolVar(&skipValidation, "skip-validation", false, "Skip health validation after upgrade")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "Skip confirmation prompt")
	cmd.Flags().BoolVar(&noRollback, "no-rollback", false, "Skip automatic rollback on health check failure")
	cmd.Flags().BoolVar(&all, "all", false, "Upgrade all enabled services in dependency order")
	cmd.Flags().BoolVar(&skipMigrationCheck, "skip-migration-check", false, "DANGEROUS: skip pre-deploy schema migration gate (expand + prior postdeploy)")
	cmd.Flags().BoolVar(&skipDataMigrationCheck, "skip-data-migration-check", false, "DANGEROUS: skip pre-deploy data migration gate (prior required data migrations)")
	cmd.Flags().Bool(unsafeCLIFloorFlag, false, "UNSAFE: let a non-concrete (dev/unversioned) CLI bypass the release's min_cli_version floor")

	cmd.AddCommand(newClusterUpgradePlanCmd())

	return cmd
}

func newClusterUpgradePlanCmd() *cobra.Command {
	var version string

	cmd := &cobra.Command{
		Use:   "plan",
		Short: "Show an upgrade plan without changing the cluster",
		Long: `Show a non-mutating upgrade plan for the selected release.

The plan includes service rollout order and embedded PostgreSQL/YugabyteDB
migrations grouped by phase. Upgrade gates use the embedded release catalog,
live applied migration state, and service-owned data-migration state before
rollout.`,
		Example: `  frameworks cluster upgrade plan
  frameworks cluster upgrade plan --version v0.3.0`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			rc, err := resolveClusterManifest(cmd)
			if err != nil {
				return err
			}
			defer rc.Cleanup()
			return runUpgradePlan(cmd, rc, version)
		},
	}

	cmd.Flags().StringVar(&version, "version", "", "Version to plan for (stable, rc, v1.2.3); defaults to cluster channel")
	return cmd
}

// resolveUpgradeVersion defaults to the cluster's channel when no explicit --version is given.
// Warns if the requested version implies a different channel than the manifest's.
func resolveUpgradeVersion(cmd *cobra.Command, manifest *inventory.Manifest, version string) string {
	if version == "" {
		version = manifest.ResolvedChannel()
	}

	clusterChannel := manifest.ResolvedChannel()
	requestedChannel, _ := gitops.ResolveVersion(version)
	if requestedChannel != clusterChannel {
		fmt.Fprintf(cmd.OutOrStderr(), "Warning: cluster channel is %q but upgrading from %q channel\n", clusterChannel, requestedChannel)
	}
	return version
}

func resolveUpgradePlanTarget(rc *resolvedCluster, version string) (string, error) {
	channel, resolvedVersion := gitops.ResolveVersion(version)
	gitopsManifest, err := gitops.FetchFromRepositories(gitops.FetchOptions{}, rc.ReleaseRepos, channel, resolvedVersion)
	if err != nil {
		return "", fmt.Errorf("resolve upgrade plan target from %s/%s: %w", channel, resolvedVersion, err)
	}
	target := strings.TrimSpace(gitopsManifest.PlatformVersion)
	if !isConcreteVersion(target) {
		return "", fmt.Errorf("selected release manifest has non-concrete platform_version %q; expected vX.Y.Z", target)
	}
	return target, nil
}

func runUpgradePlan(cmd *cobra.Command, rc *resolvedCluster, version string) error {
	manifest := rc.Manifest
	version = resolveUpgradeVersion(cmd, manifest, version)

	target, err := resolveUpgradePlanTarget(rc, version)
	if err != nil {
		return err
	}

	planner := orchestrator.NewPlanner(manifest)
	plan, err := planner.Plan(context.Background(), orchestrator.ProvisionOptions{
		Phase: orchestrator.PhaseAll,
	})
	if err != nil {
		return fmt.Errorf("build service rollout plan: %w", err)
	}
	services := collectUpgradeableServices(plan)

	ux.Heading(cmd.OutOrStdout(), fmt.Sprintf("Upgrade plan (channel: %s, version: %s)", manifest.ResolvedChannel(), version))
	if len(services) == 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "Services: none found in manifest")
	} else {
		fmt.Fprintf(cmd.OutOrStdout(), "Services: %s\n", strings.Join(services, " -> "))
	}

	if err := printUpgradePlanTargetIdentities(cmd, rc, version, services); err != nil {
		// Identity rendering is informational — a fetch failure here shouldn't
		// abort the plan output; surface a warning and continue.
		fmt.Fprintf(cmd.OutOrStderr(), "\nWarning: could not render target identities: %v\n", err)
	}

	dbNames := postgresDatabaseNames(manifest)
	switch {
	case len(dbNames) == 0:
		fmt.Fprintln(cmd.OutOrStdout(), "\nPostgreSQL/YugabyteDB migrations: postgres not enabled")
	case target == "":
		fmt.Fprintln(cmd.OutOrStdout(), "\nPostgreSQL/YugabyteDB migrations: skipped (target version unresolved)")
	default:
		fmt.Fprintf(cmd.OutOrStdout(), "\nPostgreSQL/YugabyteDB migrations (up to %s):\n", target)
		for _, phase := range []string{"expand", "postdeploy", "contract"} {
			items, itemErr := provisioner.BuildMigrationItems(dbNames, phase, target)
			if itemErr != nil {
				return fmt.Errorf("collect %s migrations: %w", phase, itemErr)
			}
			printMigrationPlanPhase(cmd, phase, items)
		}
	}

	fmt.Fprintln(cmd.OutOrStdout(), "\nPlanner state:")
	fmt.Fprintln(cmd.OutOrStdout(), "  target version: from selected GitOps/release manifest")
	fmt.Fprintln(cmd.OutOrStdout(), "  current service versions: not implemented yet")
	fmt.Fprintln(cmd.OutOrStdout(), "  embedded SQL migrations: available")
	fmt.Fprintln(cmd.OutOrStdout(), "  embedded release catalog: available")
	fmt.Fprintln(cmd.OutOrStdout(), "  applied SQL migration query: checked by upgrade gates")
	fmt.Fprintln(cmd.OutOrStdout(), "  service data migration state: checked by data-migration gates")
	fmt.Fprintln(cmd.OutOrStdout(), "\nSuggested flow:")
	fmt.Fprintln(cmd.OutOrStdout(), "  frameworks cluster migrate --phase expand --dry-run")
	fmt.Fprintln(cmd.OutOrStdout(), "  frameworks cluster migrate --phase expand")
	fmt.Fprintf(cmd.OutOrStdout(), "  frameworks cluster upgrade --all --version %s --dry-run\n", version)
	fmt.Fprintf(cmd.OutOrStdout(), "  frameworks cluster upgrade --all --version %s --yes\n", version)
	fmt.Fprintln(cmd.OutOrStdout(), "  frameworks cluster status")
	return nil
}

// printUpgradePlanTargetIdentities renders the target Docker and native
// identities each service would resolve to in the selected release. The
// upgrade-plan command is cluster-wide and doesn't know each node's
// deploy_mode, so we show both modes per service.
func printUpgradePlanTargetIdentities(cmd *cobra.Command, rc *resolvedCluster, version string, services []string) error {
	if len(services) == 0 {
		return nil
	}
	channel, resolvedVersion := gitops.ResolveVersion(version)
	gitopsManifest, err := gitops.FetchFromRepositories(gitops.FetchOptions{}, rc.ReleaseRepos, channel, resolvedVersion)
	if err != nil {
		return fmt.Errorf("fetch gitops manifest: %w", err)
	}

	fmt.Fprintln(cmd.OutOrStdout(), "\nTarget identities (per service, both modes):")
	for _, svcName := range services {
		// Plan service IDs may not match the manifest's deploy names
		// (e.g. periscope vs periscope-ingest). When lookup fails we
		// surface a "not in release manifest" note rather than aborting
		// — operators still get the plan's main output even if a
		// renamed service slips this view.
		info, lookupErr := gitopsManifest.GetServiceInfo(svcName)
		if lookupErr != nil || info == nil {
			fmt.Fprintf(cmd.OutOrStdout(), "  %-20s not in release manifest\n", svcName)
			continue
		}
		dockerIdentity := info.Image
		if info.Digest != "" {
			dockerIdentity = info.FullImage
		}
		nativeIdentity := "no native binary"
		if bin, ok := info.Binaries["linux-amd64"]; ok && bin.URL != "" {
			nativeIdentity = fmt.Sprintf("%s (%s)", bin.URL, bin.Checksum)
		}
		fmt.Fprintf(cmd.OutOrStdout(), "  %-20s docker: %s\n", svcName, dockerIdentity)
		fmt.Fprintf(cmd.OutOrStdout(), "  %-20s native: %s\n", "", nativeIdentity)
	}
	return nil
}

func postgresDatabaseNames(manifest *inventory.Manifest) []string {
	pg := manifest.Infrastructure.Postgres
	if pg == nil || !pg.Enabled {
		return nil
	}
	dbNames := make([]string, 0, len(pg.Databases))
	for _, db := range pg.Databases {
		dbNames = append(dbNames, db.Name)
	}
	return dbNames
}

func printMigrationPlanPhase(cmd *cobra.Command, phase string, items []map[string]any) {
	if len(items) == 0 {
		fmt.Fprintf(cmd.OutOrStdout(), "  %s: none\n", phase)
		return
	}
	fmt.Fprintf(cmd.OutOrStdout(), "  %s:\n", phase)
	for _, item := range items {
		tx := "tx"
		if transactional, ok := item["transactional"].(bool); ok && !transactional {
			tx = "notx"
		}
		fmt.Fprintf(cmd.OutOrStdout(), "    - %s/%s/%s (%s)\n",
			item["db"], item["version"], item["filename"], tx)
	}
}

// preflightReleaseTransitionBlockers refuses a direct `cluster upgrade --all` up front — before any confirmation or
// deployment — if ANY service in the list sits on the downstream side (BeforeServices) of a required release
// transition. Direct upgrade neither runs nor verifies transitions, so a transition-gated service must go through
// `release apply`; checking the whole list here (not per-service inside the deploy loop) is what guarantees zero
// mutation when the command is refused. Pure over the provided inputs (no SSH/deploy), so it is unit-testable.
func preflightReleaseTransitionBlockers(manifest *inventory.Manifest, platformVersion string, services []string) error {
	transitions, err := selectReleaseTransitions(platformVersion)
	if err != nil {
		return err
	}
	for _, svc := range services {
		deploy := releaseDeployName(manifest, svc)
		for _, t := range transitions {
			if stringInSlice(deploy, t.BeforeServices()) {
				return fmt.Errorf("cluster upgrade --all cannot proceed: %s (deploy %q) is gated by required release transition %q (%s), which `cluster upgrade` does not run or verify — use `frameworks cluster release apply` so the transition runs first", svc, deploy, t.ID(), t.Title())
			}
		}
	}
	return nil
}

// runUpgrade executes the upgrade command against an already-resolved manifest.
func runUpgrade(cmd *cobra.Command, rc *resolvedCluster, serviceName, version string, dryRun, skipValidation, yes, noRollback, skipMigrationCheck, skipDataMigrationCheck, withinRelease bool) error {
	manifest := rc.Manifest
	manifestPath := rc.ManifestPath
	var err error
	version = resolveUpgradeVersion(cmd, manifest, version)

	// Resolve deploy name (services/interfaces) or use serviceName for infrastructure
	deployName := serviceName
	if svcCfg, ok := manifest.Services[serviceName]; ok {
		deployName, err = resolveDeployName(serviceName, svcCfg)
		if err != nil {
			return err
		}
	} else if ifaceCfg, ok := manifest.Interfaces[serviceName]; ok {
		deployName, err = resolveDeployName(serviceName, ifaceCfg)
		if err != nil {
			return err
		}
	} else if obsCfg, ok := manifest.Observability[serviceName]; ok {
		deployName, err = resolveDeployName(serviceName, obsCfg)
		if err != nil {
			return err
		}
	}

	// Resolve EVERY host the upgrade must touch. Infrastructure services resolve
	// to their single documented primary host; application services, interfaces,
	// and observability components resolve to all hosts they run on so HA
	// replicas move together. Persisting the manifest version is deferred until
	// every replica succeeds (below).
	hosts, found := resolveUpgradeHosts(manifest, serviceName)
	if !found || len(hosts) == 0 {
		return fmt.Errorf("service %s not found or not enabled in manifest", serviceName)
	}

	ux.Heading(cmd.OutOrStdout(), fmt.Sprintf("Upgrading %s (%d host(s)) to version: %s", serviceName, len(hosts), version))

	// Scale the deadline with replica count — each host runs the full
	// provision+health cycle sequentially.
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(len(hosts))*10*time.Minute)
	defer cancel()

	// Create SSH pool
	sshKey := stringFlag(cmd, "ssh-key").Value
	sshPool := ssh.NewPool(30*time.Second, sshKey)
	defer sshPool.Close()

	// At SERVING time Chandler has no runtime dependency on Foghorn's version: it serves thumbnails from a
	// DETERMINISTIC static key with no per-request Foghorn resolver call. The planner does impose an INSTALL-time
	// order (Chandler after the in-cell Foghorn, which establishes Chandler's /ready sentinel) — a deploy-order edge,
	// not request-time coupling (see docs/architecture/thumbnails.md).

	// Fetch GitOps manifest once — the target version is cluster-wide.
	fmt.Fprintf(cmd.OutOrStdout(), "\n[1/4] Fetching GitOps manifest...\n")
	channel, resolvedVersion := gitops.ResolveVersion(version)
	fmt.Fprintf(cmd.OutOrStdout(), "  Channel: %s, Version: %s\n", channel, resolvedVersion)

	gitopsManifest, err := gitops.FetchFromRepositories(gitops.FetchOptions{}, rc.ReleaseRepos, channel, resolvedVersion)
	if err != nil {
		return fmt.Errorf("failed to fetch gitops manifest: %w", err)
	}

	// Fail closed BEFORE the migration gate / deploy if the FETCHED metadata says this CLI is too old or is missing a
	// required reconciliation transition — an outdated CLI must not proceed on stale embedded knowledge.
	if compatErr := validateFetchedReleaseCompatibility(cmd.ErrOrStderr(), gitopsManifest, unsafeCLIFloor(cmd)); compatErr != nil {
		return compatErr
	}

	// Required release transitions are executed and VERIFIED only by `release apply`, which interleaves them with the
	// dependency-ordered upgrades. A DIRECT `cluster upgrade` (including `--all`) neither runs nor proves them, so
	// deploying a service that sits on a transition's downstream side (BeforeServices) here could start it before, e.g.,
	// storage-descriptor-adoption and then fail its live-state boot check. Refuse and route to `release apply`, which
	// runs the transition first. Skipped when this upgrade is already being driven BY `release apply` (withinRelease),
	// which owns the ordering itself.
	if !withinRelease {
		transitions, tErr := selectReleaseTransitions(gitopsManifest.PlatformVersion)
		if tErr != nil {
			return tErr
		}
		for _, t := range transitions {
			if stringInSlice(deployName, t.BeforeServices()) {
				return fmt.Errorf("%s (deploy %q) is gated by required release transition %q (%s), which `cluster upgrade` does not run or verify — use `frameworks cluster release apply` so the transition runs before this service deploys", serviceName, deployName, t.ID(), t.Title())
			}
		}
	}

	svcInfo, err := gitopsManifest.GetServiceInfo(deployName)
	if err != nil {
		return fmt.Errorf("service %s not found in GitOps manifest: %w", deployName, err)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "  New version: %s\n", svcInfo.Version)

	// The fetched release manifest may list this service under rollback_disabled: its readiness contract changed such
	// that a restored previous binary cannot pass the current gate, so automatic rollback is unsafe for this upgrade
	// and is turned off from release data (not a permanent code exception — a release that omits the entry keeps
	// ordinary rollback). This composes with the operator's --no-rollback.
	if !noRollback && gitopsManifest.IsRollbackDisabled(deployName) {
		fmt.Fprintf(cmd.OutOrStdout(), "  NOTE: this release disables automatic rollback for %s (readiness-contract change); on a health failure recover by redeploy/forward-fix.\n", deployName)
		noRollback = true
	}

	// Pre-deploy gate once: schema + prior data migrations are cluster-level, not per-replica. Runs in dry-run too —
	// fail-closed semantics matter most when about to commit.
	fmt.Fprintf(cmd.OutOrStdout(), "\n[2/4] Pre-deploy gate (schema + data migrations)...\n")
	// Migration completeness is proven SOLELY from the migration ledgers plus target-relative catalog metadata: the
	// prior-postdeploy scan reads EVERY catalog release below the target from the ledger, so a skewed/unreadable replica
	// version after an interrupted HA rollout can neither block nor narrow the gate — the gate reads no running-service
	// version at all. There is no version compatibility floor either — a DB-less service (e.g. Chandler) has none; its
	// cross-version contract is the runtime /ready sentinel, enforced at deploy by install ordering + the sentinel gate
	// + rollback_disabled.
	if gateErr := runUpgradePreDeployGate(ctx, cmd, rc, sshPool, manifest, gitopsManifest.PlatformVersion, serviceName, deployName, skipMigrationCheck, skipDataMigrationCheck); gateErr != nil {
		return gateErr
	}

	// Confirmation once, before touching any replica.
	if !dryRun && !yes {
		fmt.Fprintf(os.Stderr, "\nUpgrade %s (%d host(s)) to %s? [y/N]: ", serviceName, len(hosts), svcInfo.Version)
		reader := bufio.NewReader(os.Stdin)
		response, errRead := reader.ReadString('\n')
		if errRead != nil {
			return fmt.Errorf("failed to read confirmation: %w", errRead)
		}
		response = strings.TrimSpace(strings.ToLower(response))
		if response != "y" && response != "yes" {
			fmt.Fprintln(cmd.OutOrStdout(), "Cancelled")
			return nil
		}
	}

	// Upgrade every replica in turn. A per-host failure aborts the sequence and
	// returns immediately — the manifest version is NOT advanced, so a partial
	// rollout never advertises a version the pool is not fully running.
	fmt.Fprintf(cmd.OutOrStdout(), "\n[3/4] Deploying to %d host(s)...\n", len(hosts))
	anyUpgraded := false
	for i, host := range hosts {
		if len(hosts) > 1 {
			fmt.Fprintf(cmd.OutOrStdout(), "\n--- Replica %d/%d: %s ---\n", i+1, len(hosts), host.ExternalIP)
		}
		upgraded, hostErr := upgradeServiceOnHost(ctx, cmd, rc, sshPool, manifest, host, serviceName, deployName, svcInfo, dryRun, skipValidation, noRollback)
		if hostErr != nil {
			return hostErr
		}
		if upgraded {
			anyUpgraded = true
		}
	}

	if dryRun {
		fmt.Fprintln(cmd.OutOrStdout(), "\nDry-run complete. Use without --dry-run to execute.")
		return nil
	}

	// Record the deployed version on the in-memory manifest for this run's remaining steps only. It is deliberately NOT
	// written to the local manifest, and this CLI does not persist a completed-release record anywhere: the OBSERVED
	// version lives in actual-state stores — edge nodes self-report per-component versions into foghorn.node_components
	// (scraped by Foghorn's reconciler) and control-plane hosts expose it via disk-local state (detect.NewDetector) —
	// while GitOps carries DESIRED release data. Serializing the resolved manifest would also leak decrypted host
	// inventory (see saveUpgradedVersion). So this advance is an in-session note, not a disk write or an authority.
	fmt.Fprintf(cmd.OutOrStdout(), "\n[4/4] Noting deployed version for this session (observed state lives in node self-report/host detection; local manifest not modified)...\n")
	if anyUpgraded {
		saveUpgradedVersion(manifest, serviceName, svcInfo.Version, manifestPath, cmd)
	}
	ux.Success(cmd.OutOrStdout(), fmt.Sprintf("%s upgraded to %s across %d host(s)", serviceName, svcInfo.Version, len(hosts)))

	return nil
}

// resolveUpgradeHosts returns every host an upgrade must touch for serviceName.
//
// Infrastructure services resolve to their single documented primary host —
// multi-node infra (ensemble Yugabyte, Kafka KRaft, ClickHouse) upgrades one
// node at a time via Provision's idempotent role run, and the CLI targets the
// coordinator/first node in each case. Application services, interfaces, and
// observability components resolve to ALL hosts they run on so HA replicas are
// upgraded together rather than leaving stale replicas behind.
func resolveUpgradeHosts(manifest *inventory.Manifest, serviceName string) ([]inventory.Host, bool) {
	switch serviceName {
	case "postgres":
		if pg := manifest.Infrastructure.Postgres; pg != nil && pg.Enabled {
			if host, ok := manifest.GetHost(pg.Host); ok {
				return []inventory.Host{host}, true
			}
		}
		return nil, false
	case "yugabyte":
		if pg := manifest.Infrastructure.Postgres; pg != nil && pg.Enabled && pg.IsYugabyte() && len(pg.Nodes) > 0 {
			if host, ok := manifest.GetHost(pg.Nodes[0].Host); ok {
				return []inventory.Host{host}, true
			}
		}
		return nil, false
	case "kafka":
		if k := manifest.Infrastructure.Kafka; k != nil && k.Enabled && len(k.Brokers) > 0 {
			if host, ok := manifest.GetHost(k.Brokers[0].Host); ok {
				return []inventory.Host{host}, true
			}
		}
		return nil, false
	case "kafka-controller":
		if k := manifest.Infrastructure.Kafka; k != nil && k.Enabled && len(k.Controllers) > 0 {
			if host, ok := manifest.GetHost(k.Controllers[0].Host); ok {
				return []inventory.Host{host}, true
			}
		}
		return nil, false
	case "clickhouse":
		if ch := manifest.Infrastructure.ClickHouse; ch != nil && ch.Enabled {
			if host, ok := manifest.GetHost(ch.CoordinatorHost()); ok {
				return []inventory.Host{host}, true
			}
		}
		return nil, false
	case "redis":
		if r := manifest.Infrastructure.Redis; r != nil && r.Enabled && len(r.Instances) > 0 {
			if host, ok := manifest.GetHost(r.Instances[0].Host); ok {
				return []inventory.Host{host}, true
			}
		}
		return nil, false
	}

	// Application services / interfaces / observability — every enabled host.
	var svc inventory.ServiceConfig
	var ok bool
	if s, found := manifest.Services[serviceName]; found {
		svc, ok = s, true
	} else if s, found := manifest.Interfaces[serviceName]; found {
		svc, ok = s, true
	} else if s, found := manifest.Observability[serviceName]; found {
		svc, ok = s, true
	}
	if !ok || !svc.Enabled {
		return nil, false
	}
	var hosts []inventory.Host
	for _, name := range serviceHosts(svc) {
		if host, hostOK := manifest.GetHost(name); hostOK {
			hosts = append(hosts, host)
		}
	}
	if len(hosts) == 0 {
		return nil, false
	}
	return hosts, true
}

// upgradeServiceOnHost runs the detect → build → deploy → health → rollback
// cycle for one replica. It returns whether the host was actually upgraded
// (false when already at target or in dry-run) so the caller can decide whether
// to advance the manifest version. GitOps fetch, the pre-deploy gate, and the
// operator confirmation are performed once by the caller, not per host.
func upgradeServiceOnHost(ctx context.Context, cmd *cobra.Command, rc *resolvedCluster, sshPool *ssh.Pool, manifest *inventory.Manifest, host inventory.Host, serviceName, deployName string, svcInfo *gitops.ServiceInfo, dryRun, skipValidation, noRollback bool) (bool, error) {
	// Detect current state on this replica.
	fmt.Fprintf(cmd.OutOrStdout(), "  [detect] %s...\n", host.ExternalIP)
	detector := detect.NewDetector(sshPool, host)
	state, err := detector.Detect(ctx, deployName)
	if err != nil {
		return false, fmt.Errorf("failed to detect service on %s: %w", host.ExternalIP, err)
	}
	if !state.Exists {
		return false, fmt.Errorf("service %s does not exist on %s (cannot upgrade non-existent service)", serviceName, host.ExternalIP)
	}

	previousVersion := state.Version
	previousMode := state.Mode
	canRollback := upgradeRollbackSupported(previousVersion, previousMode)
	rollbackDisabledReason := ""
	if !canRollback {
		rollbackDisabledReason = fmt.Sprintf("current version/mode is incomplete (version=%q mode=%q)", previousVersion, previousMode)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "    Current: %s (mode: %s, running: %v)\n", state.Version, state.Mode, state.Running)
	if !canRollback {
		fmt.Fprintf(cmd.OutOrStderr(), "    WARNING: automatic rollback disabled: %s\n", rollbackDisabledReason)
	}
	if state.Mode == "docker" {
		fmt.Fprintf(cmd.OutOrStdout(), "    New image: %s\n", svcInfo.FullImage)
	}

	// Skip replicas already at the target version (not in dry-run, which always
	// runs the check-diff preview).
	if state.Version == svcInfo.Version && !dryRun {
		ux.Success(cmd.OutOrStdout(), fmt.Sprintf("  %s already at version %s, nothing to do", host.ExternalIP, svcInfo.Version))
		return false, nil
	}

	// Build the same ServiceConfig the real provision flow uses — role vars
	// builders depend on env + manifest-derived metadata, not just
	// Mode/Version/Port. A synthetic orchestrator.Task feeds buildTaskConfig.
	//
	// The ClusterID MUST be set: buildServiceEnvVars only layers a cluster's env_files (which carry regional
	// STORAGE_S3_* credentials + descriptor) when task.ClusterID is set. Omitting it renders shared/empty S3 config,
	// which the storage-backend agreement gate would then skip on an empty bucket. Resolve the service's effective
	// cluster — its explicit assignment, else the target host's cluster.
	clusterID := ""
	if svcCfg, ok := manifest.Services[serviceName]; ok {
		if len(svcCfg.Clusters) > 0 {
			clusterID = svcCfg.Clusters[0]
		} else if svcCfg.Cluster != "" {
			clusterID = svcCfg.Cluster
		}
	}
	if clusterID == "" {
		clusterID = host.Cluster
	}
	task := &orchestrator.Task{
		Name:       serviceName,
		Type:       deployName,
		ServiceID:  serviceName,
		InstanceID: "",
		Host:       host.Name,
		ClusterID:  clusterID,
		Phase:      orchestrator.PhaseApplications,
		Idempotent: true,
	}
	manifestDir := filepath.Dir(rc.ManifestPath)
	sharedEnv, envErr := rc.SharedEnv()
	if envErr != nil {
		return false, fmt.Errorf("load manifest env_files: %w", envErr)
	}
	clusterEnvs, clusterEnvsErr := rc.ClusterEnvs()
	if clusterEnvsErr != nil {
		return false, fmt.Errorf("load cluster env_files: %w", clusterEnvsErr)
	}
	config, err := buildTaskConfig(task, manifest, map[string]any{}, true, manifestDir, sharedEnv, clusterEnvs, rc.ReleaseRepos)
	if err != nil {
		return false, fmt.Errorf("build upgrade config: %w", err)
	}
	if validateErr := validateProductionServiceEnv(manifest, serviceName, config.EnvVars); validateErr != nil {
		return false, fmt.Errorf("upgrade target %s: %w", deployName, validateErr)
	}
	// Foghorn's S3 descriptor env must agree with the cluster row Quartermaster persists and Chandler serves from —
	// catch a divergent/repointed backend before deploying, not at Foghorn's crash-on-boot immutability guard.
	if agreeErr := validateStorageBackendAgreement(manifest, serviceName, deployName, clusterID, config.EnvVars); agreeErr != nil {
		return false, fmt.Errorf("upgrade target %s: %w", deployName, agreeErr)
	}
	// Use the concrete artifact version from the selected GitOps release; selectors such as "stable" are not
	// installable service versions. The image is resolved from this version by the provisioner
	// (resolveGenericImage → selectedReleaseImage), which is REGISTRY-AWARE (honors image_registry /
	// FRAMEWORKS_IMAGE_REGISTRY) and digest-pinned — so we do NOT pin config.Image here. Pinning it to the target
	// digest would (a) ignore the selected registry and (b) leak into the rollback config below, "restoring" the
	// failed target image instead of the previous one; leaving Version to drive resolution restores the correct
	// image for whichever version is being deployed (forward or rollback). Interfaces still pin by digest because
	// their config.Version is now a real version (GetServiceInfo stamps service_version / platform version).
	config.Version = svcInfo.Version
	config.Mode = state.Mode
	rc.applyReleaseMetadata(config.Metadata)

	if dryRun {
		fmt.Fprintf(cmd.OutOrStdout(), "    [DRY-RUN] Would upgrade %s from %s to %s (mode: %s)\n", host.ExternalIP, state.Version, svcInfo.Version, state.Mode)
		prov, provErr := provisioner.GetProvisioner(deployName, sshPool)
		if provErr != nil {
			return false, fmt.Errorf("failed to get provisioner: %w", provErr)
		}
		checker, ok := prov.(provisioner.CheckDiffer)
		if !ok {
			fmt.Fprintln(cmd.OutOrStdout(), "    (provisioner does not support --check --diff; preview above is the summary)")
			return false, nil
		}
		fmt.Fprintln(cmd.OutOrStdout(), "    Running ansible-playbook --check --diff against the target...")
		if checkErr := checker.CheckDiff(ctx, host, config); checkErr != nil {
			return false, fmt.Errorf("dry-run: %w", checkErr)
		}
		return false, nil
	}

	// Role-backed services handle stop/restart via handlers notified on
	// binary/config change — explicit stop between install phases would
	// only duplicate work the role already does.
	prov, err := provisioner.GetProvisioner(deployName, sshPool)
	if err != nil {
		return false, fmt.Errorf("failed to get provisioner: %w", err)
	}

	// DEPLOY WITHOUT validating (pull image / download binary + start). Validation is a SEPARATE step below so a
	// readiness failure lands in the health-check rollback block rather than returning here — Provision bundles the
	// validate tag, which would abort before rollback. Deploy is part of the Provisioner contract.
	if err := prov.Deploy(ctx, host, config); err != nil {
		return false, fmt.Errorf("failed to provision new version on %s: %w", host.ExternalIP, err)
	}
	if err := prov.Initialize(ctx, host, config); err != nil {
		return false, fmt.Errorf("failed to initialize %s on %s: %w", serviceName, host.ExternalIP, err)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "    ✓ Deployed %s\n", svcInfo.Version)

	// Validate health
	if !skipValidation {
		if err := waitForHealth(ctx, func() error {
			return prov.Validate(ctx, host, config)
		}, 5*time.Second, 90*time.Second); err != nil {
			fmt.Fprintf(cmd.OutOrStderr(), "    ✗ Health check failed on %s: %v\n", host.ExternalIP, err)

			// Attempt rollback unless --no-rollback is set
			if !noRollback && canRollback {
				fmt.Fprintf(cmd.OutOrStdout(), "    [ROLLBACK] Reverting %s to previous version %s...\n", host.ExternalIP, previousVersion)

				// Rollback uses the same config surface but pinned to the
				// previous version/mode the host was running before upgrade.
				rollbackConfig := config
				rollbackConfig.Version = previousVersion
				rollbackConfig.Mode = previousMode
				rollbackConfig.Force = true
				rollbackConfig.Metadata = copyMetadata(config.Metadata)
				rc.applyReleaseMetadata(rollbackConfig.Metadata)

				if cleanupErr := prov.Cleanup(ctx, host, rollbackConfig); cleanupErr != nil {
					fmt.Fprintf(cmd.OutOrStderr(), "    ⚠ Rollback cleanup warning: %v\n", cleanupErr)
				}

				if rollbackErr := prov.Deploy(ctx, host, rollbackConfig); rollbackErr != nil {
					fmt.Fprintf(cmd.OutOrStderr(), "    ✗ Rollback failed: %v\n", rollbackErr)
					fmt.Fprintln(cmd.OutOrStderr(), "\nCRITICAL: Service may be in broken state!")
					fmt.Fprintln(cmd.OutOrStderr(), "Manual intervention required. Check logs with: frameworks cluster logs "+serviceName)
					return false, fmt.Errorf("upgrade failed and rollback failed on %s: %w", host.ExternalIP, rollbackErr)
				}

				if err := prov.Initialize(ctx, host, rollbackConfig); err != nil {
					fmt.Fprintf(cmd.OutOrStderr(), "    ✗ Rollback initialization failed: %v\n", err)
					return false, fmt.Errorf("upgrade failed, rollback initialization failed on %s: %w", host.ExternalIP, err)
				}

				if err := waitForHealth(ctx, func() error {
					return prov.Validate(ctx, host, rollbackConfig)
				}, 5*time.Second, 90*time.Second); err != nil {
					fmt.Fprintf(cmd.OutOrStderr(), "    ✗ Rollback health check failed: %v\n", err)
					return false, fmt.Errorf("upgrade failed, rollback health check failed on %s: %w", host.ExternalIP, err)
				}

				fmt.Fprintf(cmd.OutOrStdout(), "    ✓ Rolled back to %s\n", previousVersion)
				return false, fmt.Errorf("upgrade failed on %s, rolled back to %s", host.ExternalIP, previousVersion)
			}
			if !noRollback && !canRollback {
				fmt.Fprintln(cmd.OutOrStderr(), "\nWARNING: Service upgraded but health check failed and automatic rollback is unavailable.")
				fmt.Fprintf(cmd.OutOrStderr(), "Reason: %s\n", rollbackDisabledReason)
				fmt.Fprintln(cmd.OutOrStderr(), "Recover manually: redeploy the previous version or clean-redeploy this service, then check logs with: frameworks cluster logs "+serviceName)
				return false, fmt.Errorf("health validation failed on %s; automatic rollback unavailable (%s)", host.ExternalIP, rollbackDisabledReason)
			}

			fmt.Fprintln(cmd.OutOrStderr(), "\nWARNING: Service upgraded but health check failed!")
			fmt.Fprintln(cmd.OutOrStderr(), "Check service logs with: frameworks cluster logs "+serviceName)
			return false, fmt.Errorf("health validation failed on %s", host.ExternalIP)
		}
		fmt.Fprintf(cmd.OutOrStdout(), "    ✓ Healthy\n")
	}

	ux.Success(cmd.OutOrStdout(), fmt.Sprintf("  %s upgraded from %s to %s", host.ExternalIP, previousVersion, svcInfo.Version))
	return true, nil
}

// saveUpgradedVersion records the deployed version on the IN-MEMORY manifest only, for the rest of this run's steps. It
// does NOT write the manifest to disk: the RESOLVED manifest carries host inventory decrypted+merged from the encrypted
// hosts file, so serializing it would materialize external IPs / SSH users / key-file metadata into plaintext
// cluster.yaml (and clobber comments/order). No completed-release record is persisted anywhere: GitOps holds DESIRED
// release data, and the OBSERVED version lives in actual-state stores (edge node self-report in foghorn.node_components,
// control-plane disk-local detection) — not written by this function.
func saveUpgradedVersion(manifest *inventory.Manifest, serviceName, newVersion, _ string, _ *cobra.Command) {
	if svc, ok := manifest.Services[serviceName]; ok {
		svc.Version = newVersion
		manifest.Services[serviceName] = svc
	} else if iface, ok := manifest.Interfaces[serviceName]; ok {
		iface.Version = newVersion
		manifest.Interfaces[serviceName] = iface
	} else if obs, ok := manifest.Observability[serviceName]; ok {
		obs.Version = newVersion
		manifest.Observability[serviceName] = obs
	}
}

// runUpgradeAll upgrades all enabled services in dependency order.
func runUpgradeAll(cmd *cobra.Command, rc *resolvedCluster, version string, dryRun, skipValidation, yes, noRollback, skipMigrationCheck, skipDataMigrationCheck bool) error {
	manifest := rc.Manifest
	var err error
	version = resolveUpgradeVersion(cmd, manifest, version)

	// Pin the channel to ONE concrete platform version before the loop. `--all` upgrades many services in sequence; if
	// `version` is a moving channel (e.g. "stable"), each per-service runUpgrade re-resolves it independently, so a
	// release published mid-run would tear the cluster across two platform versions. Fetch the release manifest once
	// here, pin that exact tag for every service, and reuse the manifest for the preflight below.
	pinChannel, pinResolved := gitops.ResolveVersion(version)
	gm, err := gitops.FetchFromRepositories(gitops.FetchOptions{}, rc.ReleaseRepos, pinChannel, pinResolved)
	if err != nil {
		return fmt.Errorf("fetch release manifest for --all: %w", err)
	}
	pinnedVersion := strings.TrimSpace(gm.PlatformVersion)
	if !isConcreteVersion(pinnedVersion) {
		return fmt.Errorf("selected release manifest has non-concrete platform_version %q; expected vX.Y.Z", pinnedVersion)
	}
	version = pinnedVersion

	// Build dependency-ordered execution plan
	planner := orchestrator.NewPlanner(manifest)
	plan, err := planner.Plan(context.Background(), orchestrator.ProvisionOptions{
		Phase: orchestrator.PhaseAll,
	})
	if err != nil {
		return fmt.Errorf("failed to build execution plan: %w", err)
	}

	// Same fail-closed classification `release apply` uses: skip pinned provision-only roles (nginx/observability) so
	// `--all` cannot abort after partially upgrading applications, while a missing FrameWorks artifact still fails
	// closed in runUpgrade.
	services, err := classifyUpgradeableServices(cmd, manifest, collectUpgradeableServices(plan))
	if err != nil {
		return err
	}

	// Preflight: every planned artifact must resolve in the fetched manifest BEFORE upgrading any service (image digest
	// for docker; per-host arch binary for native, detected read-only over SSH), so a missing artifact aborts while the
	// cluster is still untouched rather than mid-sequence after earlier services moved.
	preflightPool := ssh.NewPool(30*time.Second, stringFlag(cmd, "ssh-key").Value)
	defer preflightPool.Close()
	archResolver := provisioner.NewBaseProvisioner("preflight", preflightPool).DetectRemoteArch
	if resolveErr := ensurePlannedArtifactsResolvable(cmd.Context(), gm, manifest, services, archResolver); resolveErr != nil {
		return resolveErr
	}

	if len(services) == 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "No upgradeable services found in manifest.")
		return nil
	}

	// Preflight transition blockers across the WHOLE list BEFORE any confirmation or mutation. The per-service refusal
	// inside runUpgrade would otherwise let predecessors (e.g. Quartermaster) deploy before a later gated service
	// (Foghorn/Chandler) aborts the sequence — a partial deploy. Refuse the entire command up front and route to
	// `release apply`, which runs the transition. (Same reason the artifact preflight above runs before any mutation.)
	if blockErr := preflightReleaseTransitionBlockers(manifest, gm.PlatformVersion, services); blockErr != nil {
		return blockErr
	}

	ux.Heading(cmd.OutOrStdout(), fmt.Sprintf("Upgrading %d services (channel: %s, version: %s)", len(services), manifest.ResolvedChannel(), version))
	fmt.Fprintf(cmd.OutOrStdout(), "Order: %s\n\n", strings.Join(services, " -> "))

	if dryRun {
		fmt.Fprintln(cmd.OutOrStdout(), "Dry-run mode: checking each service gate without applying changes.")
	}

	if !dryRun && !yes {
		fmt.Fprintf(os.Stderr, "Upgrade %d services to %s? [y/N]: ", len(services), version)
		reader := bufio.NewReader(os.Stdin)
		response, readErr := reader.ReadString('\n')
		if readErr != nil {
			return fmt.Errorf("failed to read confirmation: %w", readErr)
		}
		response = strings.TrimSpace(strings.ToLower(response))
		if response != "y" && response != "yes" {
			fmt.Fprintln(cmd.OutOrStdout(), "Cancelled")
			return nil
		}
	}

	var succeeded, failed []string
	for i, svc := range services {
		fmt.Fprintf(cmd.OutOrStdout(), "\n[%d/%d] Upgrading %s...\n", i+1, len(services), svc)
		if err := runUpgrade(cmd, rc, svc, version, dryRun, skipValidation, true, noRollback, skipMigrationCheck, skipDataMigrationCheck, false); err != nil {
			fmt.Fprintf(cmd.OutOrStderr(), "  ✗ %s failed: %v\n", svc, err)
			failed = append(failed, svc)
			fmt.Fprintf(cmd.OutOrStderr(), "\nStopping upgrade sequence. Succeeded: %v, Failed: %v, Remaining: %v\n",
				succeeded, failed, services[i+1:])
			return fmt.Errorf("upgrade --all stopped: %s failed", svc)
		}
		succeeded = append(succeeded, svc)
	}

	if dryRun {
		ux.Success(cmd.OutOrStdout(), fmt.Sprintf("Dry-run complete: %d service(s) passed upgrade gates", len(succeeded)))
		return nil
	}

	ux.Heading(cmd.OutOrStdout(), "Syncing edge release target")
	if err := syncClusterEdgeReleaseTargetFromGitOps(cmd, rc, version, nil); err != nil {
		return fmt.Errorf("edge release target sync after upgrade --all: %w", err)
	}

	ux.Success(cmd.OutOrStdout(), fmt.Sprintf("All %d services upgraded", len(succeeded)))
	ux.PrintNextSteps(cmd.OutOrStdout(), []ux.NextStep{
		{Cmd: "frameworks cluster status", Why: "Verify deployed versions match the target channel."},
	})
	return nil
}

func waitForHealth(ctx context.Context, check func() error, interval, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	var lastErr error
	for {
		if err := check(); err == nil {
			return nil
		} else {
			lastErr = err
		}

		select {
		case <-ctx.Done():
			return lastErr
		case <-ticker.C:
		}
	}
}

func upgradeRollbackSupported(version, mode string) bool {
	version = strings.TrimSpace(version)
	mode = strings.TrimSpace(mode)
	return version != "" && (mode == "docker" || mode == "native")
}

// collectUpgradeableServices extracts deduplicated service IDs from app and
// interface plan tasks. Mesh and infrastructure phases have role/fanout
// semantics and are not valid inputs to runUpgrade's single-service loop.
func collectUpgradeableServices(plan *orchestrator.ExecutionPlan) []string {
	seen := make(map[string]bool)
	var services []string
	for _, batch := range plan.Batches {
		for _, task := range batch {
			if task.Phase == orchestrator.PhaseMesh || task.Phase == orchestrator.PhaseInfrastructure {
				continue
			}
			svcID := task.ServiceID
			if svcID == "privateer" {
				continue
			}
			if !seen[svcID] {
				seen[svcID] = true
				services = append(services, svcID)
			}
		}
	}
	return services
}

// classifyUpgradeableServices is the SHARED, FAIL-CLOSED classification both `release apply` and `upgrade --all` run
// before any mutation. It EXCLUDES only EXPLICITLY provision-only roles — pinned external/OS-managed components
// (nginx, VictoriaMetrics, …) provisioned via `cluster provision`, which the interface/application phases also collect
// but which carry no FrameWorks release artifact. Everything else is treated as an EXPECTED FrameWorks artifact and
// kept; whether each kept service's image actually resolves in the fetched release manifest is verified separately by
// ensurePlannedArtifactsResolvable (also before mutation), so a missing artifact aborts the release up front rather
// than mid-rollout. A deploy-name that cannot be resolved is a hard error here, not an ignored skip. Alias-aware.
func classifyUpgradeableServices(cmd *cobra.Command, manifest *inventory.Manifest, services []string) ([]string, error) {
	var kept, skipped []string
	for _, svcID := range services {
		deployName, err := classificationDeployName(manifest, svcID)
		if err != nil {
			return nil, fmt.Errorf("cannot classify %q for release: %w", svcID, err)
		}
		if servicedefs.IsProvisionOnly(deployName) {
			skipped = append(skipped, svcID)
			continue
		}
		kept = append(kept, svcID)
	}
	if len(skipped) > 0 {
		fmt.Fprintf(cmd.OutOrStdout(), "  provisioned via `cluster provision`, not upgraded here: %s\n", strings.Join(skipped, ", "))
	}
	return kept, nil
}

// ensurePlannedArtifactsResolvable verifies that EVERY classified service actually RESOLVES to a deployable artifact in
// the already-fetched release manifest BEFORE any mutation (migration expand, service upgrade, reconciliation), using
// the service's EFFECTIVE MODE — the manifest's declared mode, or "docker" when omitted, exactly as buildTaskConfig
// resolves it at deploy — not a bare entry-presence check and not a guess from the artifacts present:
//   - native mode: the manifest must carry native binaries and every declared arch must have BOTH a download URL and a
//     checksum (the full identity the deploy-time native provisioner requires). Production control services run mode:
//     native, so checking the image here would validate an unused artifact and miss the binary the deploy installs.
//   - docker mode: provisioner.SelectedReleaseImage must resolve — it applies the registry selector (image_registry /
//     FRAMEWORKS_IMAGE_REGISTRY) and requires a digest, catching a missing selected-registry entry or absent digest
//     that GetServiceInfo alone would pass.
//
// For native services the PER-HOST-ARCHITECTURE binary is also resolved: archResolver detects each planned host's
// OS/arch — the SAME DetectRemoteArch the provisioner uses at deploy — read-only, before any mutation, and the release
// must carry a binary for it (svc.GetBinary), so an arm64-only release can no longer pass preflight for an amd64 host
// and tear the rollout later. Detection is cached per host. A nil archResolver skips only the per-host check (the
// well-formed binary check still runs). All failures are AGGREGATED so one run reports every problem and the whole
// release fails closed while the cluster is untouched. Registry metadata mirrors deploy: image_registry is env-driven,
// so an empty map selects identically.
func ensurePlannedArtifactsResolvable(ctx context.Context, gm *gitops.Manifest, manifest *inventory.Manifest, services []string, archResolver hostArchResolver) error {
	var problems []string
	archCache := map[string]detectedArch{}
	for _, svcID := range services {
		deployName, err := classificationDeployName(manifest, svcID)
		if err != nil {
			return fmt.Errorf("cannot resolve deploy name for planned service %q: %w", svcID, err)
		}
		svc, err := gm.GetServiceInfo(deployName)
		if err != nil {
			problems = append(problems, fmt.Sprintf("%s (%s): %v", svcID, deployName, err))
			continue
		}
		// Resolve the SAME effective mode buildTaskConfig deploys with: the manifest's declared mode, or "docker" when
		// omitted (buildTaskConfig's default). Guessing from the available artifacts would let a binaries-only entry with
		// no declared mode pass here as native and then fail at deploy, which defaults it to docker and finds no image.
		mode := plannedServiceMode(manifest, svcID)
		if mode == "" {
			mode = "docker"
		}
		switch mode {
		case "native":
			if err := validateNativeBinariesResolvable(svc); err != nil {
				problems = append(problems, fmt.Sprintf("%s (%s): %v", svcID, deployName, err))
				continue
			}
			hosts, _ := resolveUpgradeHosts(manifest, svcID)
			problems = append(problems, nativeHostArchProblems(ctx, svc, svcID, hosts, archResolver, archCache)...)
		case "docker":
			if _, err := provisioner.SelectedReleaseImage(svc, nil); err != nil {
				problems = append(problems, fmt.Sprintf("%s (%s): %v", svcID, deployName, err))
			}
		default:
			// An explicitly-declared mode the deploy provisioner does not support (only docker/native exist) — it would
			// reject this at deploy, so fail closed here too.
			problems = append(problems, fmt.Sprintf("%s (%s): unsupported deploy mode %q", svcID, deployName, mode))
		}
	}
	if len(problems) > 0 {
		return fmt.Errorf("release artifacts do not resolve for planned service(s), refusing before mutating the cluster (upgrade the CLI/release or fix the manifest):\n  - %s", strings.Join(problems, "\n  - "))
	}
	return nil
}

// hostArchResolver detects a host's OS and Go architecture (e.g. "linux", "amd64"). It matches
// provisioner.BaseProvisioner.DetectRemoteArch, so the preflight resolves architecture exactly as deploy does.
type hostArchResolver func(ctx context.Context, host inventory.Host) (osName, arch string, err error)

// detectedArch caches one host's resolution (or its failure) so hosts shared by several services are probed once.
type detectedArch struct {
	os, arch string
	err      error
}

// nativeHostArchProblems confirms the release carries a binary for the detected OS/arch of every host the native
// service will land on — the same per-host selection deploy performs (resolveGenericBinary → svc.GetBinary), done
// read-only before mutation. A detection failure is itself a problem (fail closed: an unreachable/unknowable host
// cannot be proven deployable). With no resolver or no hosts the per-host check is skipped.
func nativeHostArchProblems(ctx context.Context, svc *gitops.ServiceInfo, svcID string, hosts []inventory.Host, archResolver hostArchResolver, cache map[string]detectedArch) []string {
	if archResolver == nil || len(hosts) == 0 {
		return nil
	}
	var problems []string
	for _, h := range hosts {
		da, seen := cache[h.ExternalIP]
		if !seen {
			os, arch, err := archResolver(ctx, h)
			da = detectedArch{os: os, arch: arch, err: err}
			cache[h.ExternalIP] = da
		}
		if da.err != nil {
			problems = append(problems, fmt.Sprintf("%s: cannot detect architecture of host %s: %v", svcID, h.ExternalIP, da.err))
			continue
		}
		if _, err := svc.GetBinary(da.os, da.arch); err != nil {
			problems = append(problems, fmt.Sprintf("%s: release has no %s-%s binary for host %s", svcID, da.os, da.arch, h.ExternalIP))
		}
	}
	return problems
}

// plannedServiceMode returns the deploy mode declared for svcID in the manifest. It returns "" both when svcID is not a
// manifest service/interface/observability entry AND when the entry declares no mode; callers treat "" as the deploy
// default (docker), which is what buildTaskConfig also does for an omitted mode.
func plannedServiceMode(manifest *inventory.Manifest, svcID string) string {
	if cfg, ok := manifest.Services[svcID]; ok {
		return strings.ToLower(strings.TrimSpace(cfg.Mode))
	}
	if cfg, ok := manifest.Interfaces[svcID]; ok {
		return strings.ToLower(strings.TrimSpace(cfg.Mode))
	}
	if cfg, ok := manifest.Observability[svcID]; ok {
		return strings.ToLower(strings.TrimSpace(cfg.Mode))
	}
	return ""
}

// validateNativeBinariesResolvable proves a native service's release entry carries a well-formed binary set: at least
// one binary, and every declared arch has BOTH a download URL and a checksum — the full identity the deploy-time native
// provisioner requires (serviceRoleFingerprint rejects a binary whose url or checksum is empty). Per-host arch
// selection happens at deploy, where the target architecture is detected.
func validateNativeBinariesResolvable(svc *gitops.ServiceInfo) error {
	if len(svc.Binaries) == 0 {
		return fmt.Errorf("native service %s has no binary artifacts in the release manifest", svc.Name)
	}
	var missing []string
	for arch, bin := range svc.Binaries {
		if strings.TrimSpace(bin.URL) == "" {
			missing = append(missing, arch+" (url)")
		}
		if strings.TrimSpace(bin.Checksum) == "" {
			missing = append(missing, arch+" (checksum)")
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return fmt.Errorf("native service %s has incomplete binary identity for: %s", svc.Name, strings.Join(missing, ", "))
	}
	return nil
}

// classificationDeployName resolves a service id to its canonical deploy name (alias-aware) for classification. A
// resolution failure is returned as an error so a malformed alias fails closed rather than being silently kept.
func classificationDeployName(manifest *inventory.Manifest, svcID string) (string, error) {
	if cfg, ok := manifest.Services[svcID]; ok {
		return resolveDeployName(svcID, cfg)
	}
	if cfg, ok := manifest.Interfaces[svcID]; ok {
		return resolveDeployName(svcID, cfg)
	}
	if cfg, ok := manifest.Observability[svcID]; ok {
		return resolveDeployName(svcID, cfg)
	}
	return svcID, nil // not a manifest service/interface/observability key: use the id verbatim
}

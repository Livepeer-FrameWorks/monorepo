package cmd

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"frameworks/cli/internal/releases"
	"frameworks/cli/internal/ux"
	"frameworks/cli/pkg/gitops"
	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/orchestrator"
	"frameworks/cli/pkg/provisioner"
	"frameworks/cli/pkg/ssh"
	fwv "github.com/Livepeer-FrameWorks/monorepo/pkg/version"

	"github.com/spf13/cobra"
)

func newClusterReleaseCmd() *cobra.Command {
	release := &cobra.Command{
		Use:   "release",
		Short: "Apply a cluster release as one ordered, resumable plan",
		Long: `Run a full cluster release as a single ordered plan:

  pre-upgrade host convergence (Privateer seeds, declared Kafka topics,
    MirrorMaker2 workers; unchanged hosts skipped)
  → service databases and expand migrations
  → service upgrades in dependency order
  → reconciliation transitions interleaved at their declared points
  → stale placement cleanup (service replicas, MirrorMaker2 workers)
  → postdeploy migrations

Contract migrations are intentionally excluded from automatic apply. Run the
printed contract command only after the rollback window closes.

Reconciliation transitions are constrained Check → Apply → Verify nodes registered by
compiled handler id (never shell commands). Each converges a declared invariant against
NATURAL authoritative state, so the plan is RESUMABLE: a rerun after a mid-release failure
re-checks reality and skips whatever is already Complete. Use 'cluster migrate',
'cluster upgrade', and domain-specific repair commands as low-level recovery tools;
'release apply' is the normal path.`,
	}
	release.AddCommand(newClusterReleasePlanCmd())
	release.AddCommand(newClusterReleaseApplyCmd())
	return release
}

type releasePlanOptions struct {
	version string
}

func newClusterReleasePlanCmd() *cobra.Command {
	opts := releasePlanOptions{}
	cmd := &cobra.Command{
		Use:   "plan",
		Short: "Print the complete static release plan without cluster credentials",
		Args:  cobra.NoArgs,
		Long: `Resolve the target release and manifest topology, then print every schema,
platform-artifact, dependency, infrastructure, and reconciliation lifecycle involved.
This command does not decrypt cluster secrets, open SSH tunnels, dial services, or run
live-state gates. Use 'release apply --dry-run' when you also want those live checks.`,
		Example: `  frameworks cluster release plan --manifest ./cluster.yaml --version v0.3.0
  frameworks cluster release plan --version stable`,
		RunE: func(cmd *cobra.Command, args []string) error {
			rc, err := resolveClusterManifest(cmd)
			if err != nil {
				return err
			}
			defer rc.Cleanup()
			if err := requirePlatformIfImplicitManifest(rc, cmd.OutOrStdout()); err != nil {
				return err
			}
			return runReleasePlan(cmd, rc, opts)
		},
	}
	cmd.Flags().StringVar(&opts.version, "version", "", "Target version (stable, candidate, rc, v1.2.3); defaults to cluster channel")
	cmd.Flags().Bool(unsafeCLIFloorFlag, false, "UNSAFE: let a non-concrete (dev/unversioned) CLI bypass the release's min_cli_version floor")
	return cmd
}

func runReleasePlan(cmd *cobra.Command, rc *resolvedCluster, opts releasePlanOptions) error {
	manifest := rc.Manifest
	if err := validateRequiredServiceDependencies(manifest); err != nil {
		return fmt.Errorf("invalid manifest: %w", err)
	}
	selector := resolveUpgradeVersion(cmd, manifest, opts.version)
	platformVersion, err := resolveReleasePlatformVersion(rc, selector)
	if err != nil {
		return fmt.Errorf("resolve target platform version: %w", err)
	}
	channel, _ := gitops.ResolveVersion(selector)
	gm, err := gitops.FetchFromRepositories(gitops.FetchOptions{}, rc.ReleaseRepos, channel, platformVersion)
	if err != nil {
		return fmt.Errorf("fetch release metadata for compatibility check: %w", err)
	}
	if compatibilityErr := validateFetchedReleaseCompatibility(cmd.ErrOrStderr(), gm, unsafeCLIFloor(cmd)); compatibilityErr != nil {
		return compatibilityErr
	}

	execution, err := orchestrator.NewPlanner(manifest).Plan(cmd.Context(), orchestrator.ProvisionOptions{Phase: orchestrator.PhaseAll})
	if err != nil {
		return fmt.Errorf("failed to build execution plan: %w", err)
	}
	classes, err := classifyReleaseServices(manifest, collectUpgradeableServices(execution))
	if err != nil {
		return err
	}
	transitions, err := selectReleaseTransitions(platformVersion)
	if err != nil {
		return err
	}

	out := cmd.OutOrStdout()
	ux.Heading(out, fmt.Sprintf("Static release plan for %s (platform %s)", selector, platformVersion))
	writeReleaseHostConvergencePlan(out, "1. pre-upgrade host convergence", planReleaseHostConvergence(execution, manifest))
	writeReleasePlanGroup(out, "2. service databases (created with roles and current baseline when missing on the cluster)", releasePlanServiceDatabases(manifest))
	fmt.Fprintln(out, "  3. schema expand migrations")
	writeReleasePlanGroup(out, "4. platform artifact upgrades", classes.Platform)
	if len(transitions) == 0 {
		fmt.Fprintln(out, "  5. release reconciliations: none")
	} else {
		fmt.Fprintln(out, "  5. release reconciliations:")
		for _, transition := range transitions {
			fmt.Fprintf(out, "     · %s after [%s] before [%s]\n", transition.ID(), strings.Join(transition.AfterServices(), ","), strings.Join(transition.BeforeServices(), ","))
		}
	}
	fmt.Fprintln(out, "  6. stale placement cleanup: platform service replicas and kafka-mirrormaker workers the manifest no longer places")
	fmt.Fprintln(out, "  7. schema postdeploy migrations")
	fmt.Fprintf(out, "  8. schema contract migrations: deferred until the rollback window closes; take a fresh backup first (`cluster migrate --phase contract --to-version %s --backup <completed-backup-path> --yes`)\n", platformVersion)
	writeReleasePlanGroup(out, "managed dependencies (independent manifest pins)", classes.Dependencies)
	writeReleasePlanGroup(out, "host infrastructure (independent OS/data lifecycle)", classes.Infrastructure)
	fmt.Fprintln(out, "  control-plane desired state: inspect with `cluster control-plane plan`; reconcile explicitly when needed")
	fmt.Fprintln(out, "\nStatic plan complete. No secrets were decrypted and no cluster connections were opened.")
	return nil
}

func writeReleasePlanGroup(out io.Writer, label string, values []string) {
	if len(values) == 0 {
		fmt.Fprintf(out, "  %s: none\n", label)
		return
	}
	fmt.Fprintf(out, "  %s: %s\n", label, strings.Join(values, " -> "))
}

// fetchReleaseManifestFn reads the published release manifest `release apply` checks compatibility against. Tests
// substitute a fixture so the apply sequence runs without the release repositories.
var fetchReleaseManifestFn = gitops.FetchFromRepositories

// releaseRunMigrateFn and releaseRunUpgradesFn run the migration phases and the service upgrades of `release apply`.
// Tests substitute them to observe step order without a cluster.
var (
	releaseRunMigrateFn  = runMigrate
	releaseRunUpgradesFn = runReleaseUpgradesInterleaved
)

type releaseApplyOptions struct {
	version                      string
	dryRun                       bool
	yes                          bool
	skipValidation               bool
	noRollback                   bool
	completeInterruptedBaselines bool
}

func newClusterReleaseApplyCmd() *cobra.Command {
	opts := releaseApplyOptions{}
	cmd := &cobra.Command{
		Use:   "apply",
		Short: "Run the ordered release plan (migrations, reconciliations, upgrades)",
		Example: `  frameworks cluster release apply --dry-run
  frameworks cluster release apply --version stable --yes`,
		RunE: func(cmd *cobra.Command, args []string) error {
			rc, err := resolveClusterManifest(cmd)
			if err != nil {
				return err
			}
			defer rc.Cleanup()
			if err := requirePlatformIfImplicitManifest(rc, cmd.OutOrStdout()); err != nil {
				return err
			}
			return runReleaseApply(cmd, rc, opts)
		},
	}
	cmd.Flags().StringVar(&opts.version, "version", "", "Target version (stable, candidate, rc, v1.2.3); defaults to cluster channel")
	cmd.Flags().BoolVar(&opts.dryRun, "dry-run", false, "Show the plan and run every gate/Check without mutating anything")
	cmd.Flags().BoolVarP(&opts.yes, "yes", "y", false, "Skip the confirmation prompt")
	cmd.Flags().BoolVar(&opts.skipValidation, "skip-validation", false, "Skip health validation after each service upgrade")
	cmd.Flags().BoolVar(&opts.noRollback, "no-rollback", false, "Skip automatic rollback on a service health-check failure")
	cmd.Flags().BoolVar(&opts.completeInterruptedBaselines, "complete-interrupted-baselines", false, "Reapply and verify baselines for populated databases without migration provenance")
	cmd.Flags().Bool(unsafeCLIFloorFlag, false, "UNSAFE: let a non-concrete (dev/unversioned) CLI bypass the release's min_cli_version floor")
	return cmd
}

func runReleaseApply(cmd *cobra.Command, rc *resolvedCluster, opts releaseApplyOptions) error {
	out := cmd.OutOrStdout()
	manifest := rc.Manifest
	if err := validateRequiredServiceDependencies(manifest); err != nil {
		return fmt.Errorf("invalid manifest: %w", err)
	}
	// Resolve once so every release step uses the same concrete version.
	version := resolveUpgradeVersion(cmd, manifest, opts.version)
	platformVersion, err := resolveReleasePlatformVersion(rc, version)
	if err != nil {
		return fmt.Errorf("resolve target platform version: %w", err)
	}
	// Preserve the resolved channel for the pinned edge target.
	releaseChannel, _ := gitops.ResolveVersion(version)

	// Compatibility must be established before migrations mutate the cluster.
	gm, gmErr := fetchReleaseManifestFn(gitops.FetchOptions{}, rc.ReleaseRepos, releaseChannel, platformVersion)
	if gmErr != nil {
		return fmt.Errorf("fetch release metadata for compatibility check: %w", gmErr)
	}
	if compatErr := validateFetchedReleaseCompatibility(cmd.ErrOrStderr(), gm, unsafeCLIFloor(cmd)); compatErr != nil {
		return compatErr
	}

	// Foghorn precedes Chandler because it establishes Chandler's readiness sentinel.
	plan, err := orchestrator.NewPlanner(manifest).Plan(context.Background(), orchestrator.ProvisionOptions{Phase: orchestrator.PhaseAll})
	if err != nil {
		return fmt.Errorf("failed to build execution plan: %w", err)
	}
	// External roles are provisioned independently and are not release artifacts.
	services, err := classifyUpgradeableServices(cmd, manifest, collectUpgradeableServices(plan))
	if err != nil {
		return err
	}

	sshKey := stringFlag(cmd, "ssh-key").Value
	sshPool := ssh.NewPool(30*time.Second, sshKey)
	defer sshPool.Close()

	// Resolve every artifact before the first cluster mutation.
	archResolver := provisioner.NewBaseProvisioner("preflight", sshPool).DetectRemoteArch
	if resolveErr := ensurePlannedArtifactsResolvable(cmd.Context(), gm, manifest, services, archResolver); resolveErr != nil {
		return resolveErr
	}

	// Every running replica must be within the release floor of this move before the first mutation.
	if floorErr := enforceSourceFloor(cmd.Context(), out, rc, sshPool, platformVersion); floorErr != nil {
		return floorErr
	}
	// Every earlier release must be finished before this one mutates anything; the per-service gate would otherwise
	// refuse only after host convergence and this release's expand migrations.
	if priorErr := enforcePriorReleasesComplete(cmd.Context(), out, rc, sshPool, platformVersion); priorErr != nil {
		return priorErr
	}

	// Every transition required by the release must exist in this CLI.
	required, err := selectReleaseTransitions(platformVersion)
	if err != nil {
		return err
	}

	// Ignore transitions with no manifest scopes to converge.
	probeEnv := &reconcileEnv{cmd: cmd, rc: rc, sshPool: sshPool}
	var transitions []ReleaseTransition
	for _, t := range required {
		sc, sErr := t.Scopes(probeEnv)
		if sErr != nil {
			return fmt.Errorf("reconciliation %q scopes: %w", t.ID(), sErr)
		}
		if len(sc) > 0 {
			transitions = append(transitions, t)
		}
	}

	hostSteps := planReleaseHostConvergence(plan, manifest)
	ux.Heading(out, fmt.Sprintf("Release plan for %s (platform %s)", version, platformVersion))
	writeReleaseHostConvergencePlan(out, "1. pre-upgrade host convergence", hostSteps)
	fmt.Fprintln(out, "  2. service databases missing on the cluster (created with roles and current baseline), then expand migrations")
	fmt.Fprintln(out, "     · read-only Quartermaster report: nodes without an identity key, DNS grants the entitlement migration clears")
	if len(services) == 0 {
		fmt.Fprintln(out, "  3. service upgrades: none pending")
	} else {
		fmt.Fprintf(out, "  3. service upgrades: %s\n", strings.Join(services, " -> "))
	}
	for _, t := range transitions {
		fmt.Fprintf(out, "     · reconcile %q after [%s] before [%s]\n", t.ID(), strings.Join(t.AfterServices(), ","), strings.Join(t.BeforeServices(), ","))
	}
	fmt.Fprintln(out, "  4. stale placement cleanup (platform service replicas, kafka-mirrormaker workers)")
	fmt.Fprintln(out, "  5. postdeploy migrations")
	fmt.Fprintf(out, "  6. contract migrations: deferred until the rollback window closes; take a fresh backup first (`cluster migrate --phase contract --to-version %s --backup <completed-backup-path> --yes`)\n", platformVersion)

	// Every service env the release deploys must pass the schema contract, and
	// required operator inputs must exist, before the first mutation.
	var hostConvergence *releaseHostConvergence
	if len(hostSteps) > 0 {
		hostConvergence, err = newReleaseHostConvergence(cmd, rc, platformVersion, sshPool)
		if err != nil {
			return fmt.Errorf("pre-upgrade host convergence: %w", err)
		}
		if contractErr := hostConvergence.preflightEnvContract(hostSteps); contractErr != nil {
			return contractErr
		}
	}
	if envErr := preflightReleaseRequiredEnv(cmd.Context(), rc, services); envErr != nil {
		return envErr
	}

	var env *reconcileEnv
	if len(transitions) > 0 {
		qc, jwt, cleanup, qErr := buildReconcileQM(cmd.Context(), rc)
		if qErr != nil {
			return fmt.Errorf("connect to Quartermaster for reconciliation: %w", qErr)
		}
		defer cleanup()
		env = &reconcileEnv{cmd: cmd, rc: rc, sshPool: sshPool, qm: qc, operatorJWT: jwt, dryRun: opts.dryRun}
	}

	if !opts.dryRun && !opts.yes {
		fmt.Fprintf(os.Stderr, "\nApply this release to the cluster? [y/N]: ")
		reader := bufio.NewReader(os.Stdin)
		response, readErr := reader.ReadString('\n')
		if readErr != nil {
			return fmt.Errorf("failed to read confirmation: %w", readErr)
		}
		if r := strings.TrimSpace(strings.ToLower(response)); r != "y" && r != "yes" {
			fmt.Fprintln(out, "Cancelled")
			return nil
		}
	}

	ux.Subheading(out, "[1/4] Pre-upgrade host convergence")
	if len(hostSteps) == 0 {
		fmt.Fprintln(out, "  no mesh, Kafka topic, or MirrorMaker2 hosts in this manifest")
	} else {
		if convergeErr := hostConvergence.run(cmd.Context(), hostSteps, opts.dryRun); convergeErr != nil {
			return fmt.Errorf("pre-upgrade host convergence: %w", convergeErr)
		}
	}

	ux.Subheading(out, "[2/4] Expand migrations")
	if migrateErr := releaseRunMigrateFn(cmd, rc, opts.dryRun, "expand", true, platformVersion, false, opts.completeInterruptedBaselines); migrateErr != nil {
		return fmt.Errorf("expand migrations: %w", migrateErr)
	}
	// Runs once the expand columns exist and before the upgrades install the
	// binaries that refuse keyless nodes, so operators see who must re-enroll
	// while those nodes are still served.
	runReleasePostExpandReport(cmd.Context(), cmd, rc, sshPool)

	ux.Subheading(out, "[3/4] Service upgrades + reconciliations")
	installed, err := releaseRunUpgradesFn(cmd, rc, env, transitions, services, platformVersion, opts)
	if err != nil {
		return err
	}
	if err := reconcileReleasePlacements(cmd.Context(), cmd, rc, installed, opts.dryRun); err != nil {
		return fmt.Errorf("service placement reconciliation: %w", err)
	}
	// Remove mirror workers only after all consumers have rolled.
	if mmErr := reconcileStaleKafkaMirrorMakerWorkersOverSSH(cmd.Context(), out, rc.Manifest, sshPool, opts.dryRun); mmErr != nil {
		return fmt.Errorf("kafka-mirrormaker worker cleanup: %w", mmErr)
	}

	// Postdeploy migrations gate on, but do not execute, required data migrations.
	ux.Subheading(out, "[4/4] Postdeploy migrations")
	if err := releaseRunMigrateFn(cmd, rc, opts.dryRun, "postdeploy", true, platformVersion, false, opts.completeInterruptedBaselines); err != nil {
		return fmt.Errorf("postdeploy migrations: %w", err)
	}
	fmt.Fprintf(out, "  Contract migrations remain deferred. After the rollback window closes, take a fresh backup with `frameworks cluster backup create --to <backup-dir> --all`, then run `frameworks cluster migrate --phase contract --to-version %s --backup <completed-backup-path> --dry-run` and repeat with --yes in place of --dry-run.\n", platformVersion)

	// Keep the edge target pinned to this release's resolved version.
	if !opts.dryRun {
		ux.Subheading(out, "Syncing edge release target")
		if err := syncClusterEdgeReleaseTargetPinned(cmd, rc, releaseChannel, platformVersion, nil); err != nil {
			return fmt.Errorf("edge release target sync: %w", err)
		}
	}

	if opts.dryRun {
		ux.Success(out, "Dry-run complete: all gates and reconciliation Checks passed; nothing was mutated")
		return nil
	}
	ux.Success(out, fmt.Sprintf("Release %s applied", version))
	return nil
}

// resolveReleasePlatformVersion resolves a version SELECTOR (channel or concrete) to a concrete vX.Y.Z platform
// version via the GitOps release manifest — the value migrations and transition selection require.
func resolveReleasePlatformVersion(rc *resolvedCluster, selector string) (string, error) {
	return resolveConcretePlatformVersion(rc.ReleaseRepos, selector, gitops.FetchOptions{})
}

// resolveConcretePlatformVersion is the single selector resolver shared by `release apply --version` and
// `provision --version`: a concrete vX.Y.Z is used as given, and a channel resolves to the platform_version of the
// release manifest it points at.
func resolveConcretePlatformVersion(releaseRepos []string, selector string, opts gitops.FetchOptions) (string, error) {
	selector = strings.TrimSpace(selector)
	if isConcreteVersion(selector) {
		return selector, nil
	}
	channel, v := gitops.ResolveVersion(selector)
	if !isConcreteVersion(v) {
		gm, err := gitops.FetchFromRepositories(opts, releaseRepos, channel, v)
		if err != nil {
			return "", fmt.Errorf("cannot resolve %q to a concrete version: %w", selector, err)
		}
		v = strings.TrimSpace(gm.PlatformVersion)
	}
	if !isConcreteVersion(v) {
		return "", fmt.Errorf("cannot resolve %q to a concrete vX.Y.Z (got %q)", selector, v)
	}
	return v, nil
}

// validateFetchedReleaseCompatibility fails closed BEFORE any migration or upgrade when the FETCHED release metadata
// says this CLI is too old, or requires a reconciliation transition this CLI's compiled registry does not implement.
// Carrying min_cli_version + required_transitions in the FETCHED manifest (not only the CLI's embedded catalog) lets a
// release raise the floor for any CLI that ALREADY runs this check — it reads the requirement from the release rather
// than only its own compiled knowledge.
//
// This is NOT a guarantee against a genuinely OLD CLI: a binary predating this validation ignores the additive metadata
// fields entirely and cannot be "told" to stop. There is no format-breaking parser or server-side gate that would force
// an arbitrarily old client to fail closed. The real operational rule is therefore: **upgrade the CLI before applying a
// release** (the release docs state the min CLI version). This check backstops the common case (a CLI new enough to have
// it but below the release's floor); it does not, and cannot, backstop a pre-check CLI.
func validateFetchedReleaseCompatibility(out io.Writer, gm *gitops.Manifest, allowUnsafe bool) error {
	if gm == nil {
		return nil
	}
	// Shared fail-closed floor: a non-concrete local build no longer slips past this fetched-manifest gate (it is
	// refused unless --unsafe-ignore-cli-version-floor), and it is the SAME checker the embedded-catalog gate uses, so
	// `release apply` cannot pass this early check and then fail later at a stricter per-service gate.
	if err := checkCLIVersionFloor(out, gm.MinCLIVersion, allowUnsafe); err != nil {
		return err
	}
	for _, id := range gm.RequiredTransitions {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, ok := releaseTransitionRegistry[id]; !ok {
			return fmt.Errorf("release %s requires the reconciliation transition %q which this CLI does not implement "+
				"(the fetched release metadata declares it required) — upgrade the frameworks CLI before deploying", gm.PlatformVersion, id)
		}
	}

	// The fetched manifest is the AUTHORITY for which reconciliation transitions this release requires, but execution
	// derives them from the embedded catalog (selectReleaseTransitions). BIND the two so authority and execution cannot
	// diverge: the fetched required-transition set must EQUAL the embedded catalog's set for this platform version. A
	// disagreement means this CLI's compiled catalog and the release's declaration differ — fail closed rather than run
	// a different set of transitions than the release declares.
	embeddedIDs, eErr := releases.RequiredTransitionIDs(gm.PlatformVersion)
	if eErr != nil {
		return fmt.Errorf("read embedded release-transition catalog for %s: %w", gm.PlatformVersion, eErr)
	}
	if diff := stringSetDisagreement(gm.RequiredTransitions, embeddedIDs); diff != "" {
		return fmt.Errorf("release %s required-transition set disagrees between the fetched manifest (authoritative) and this CLI's catalog: %s — align/upgrade the CLI before deploying", gm.PlatformVersion, diff)
	}
	return checkFetchedSourceFloor(gm, sourceFloorForFn)
}

// checkFetchedSourceFloor binds the fetched release's min_source_version to the floor this CLI's catalog derives for
// the same release; the source-floor gate enforces the embedded value, so the two must agree. A release this catalog
// does not declare is refused by the pre-deploy catalog gate; here it only fails when it declares a floor this CLI
// cannot enforce.
func checkFetchedSourceFloor(gm *gitops.Manifest, floorFor func(string) (string, bool)) error {
	embedded, known := floorFor(gm.PlatformVersion)
	if !known {
		if gm.MinSourceVersion != "" {
			return fmt.Errorf("release %s declares min_source_version %s but is not in this CLI's release catalog; upgrade the frameworks CLI before deploying", gm.PlatformVersion, gm.MinSourceVersion)
		}
		return nil
	}
	if gm.MinSourceVersion != embedded {
		return fmt.Errorf("release %s min_source_version disagrees between the fetched manifest (%q) and this CLI's catalog (%q); align or upgrade the CLI before deploying", gm.PlatformVersion, gm.MinSourceVersion, embedded)
	}
	return nil
}

// stringSetDisagreement returns "" when a and b contain the same set of non-empty (trimmed) strings, ignoring order and
// duplicates; otherwise it describes which entries appear in only one side.
func stringSetDisagreement(a, b []string) string {
	set := func(ss []string) map[string]struct{} {
		m := map[string]struct{}{}
		for _, s := range ss {
			if s = strings.TrimSpace(s); s != "" {
				m[s] = struct{}{}
			}
		}
		return m
	}
	as, bs := set(a), set(b)
	var onlyA, onlyB []string
	for s := range as {
		if _, ok := bs[s]; !ok {
			onlyA = append(onlyA, s)
		}
	}
	for s := range bs {
		if _, ok := as[s]; !ok {
			onlyB = append(onlyB, s)
		}
	}
	if len(onlyA) == 0 && len(onlyB) == 0 {
		return ""
	}
	sort.Strings(onlyA)
	sort.Strings(onlyB)
	var parts []string
	if len(onlyA) > 0 {
		parts = append(parts, fmt.Sprintf("declared by the fetched manifest but not this CLI's catalog: %v", onlyA))
	}
	if len(onlyB) > 0 {
		parts = append(parts, fmt.Sprintf("in this CLI's catalog but not the fetched manifest: %v", onlyB))
	}
	return strings.Join(parts, "; ")
}

// isConcreteCLIVersion reports whether v is a real SemVer the min-CLI floor can compare against (vX.Y.Z[...]), as
// opposed to a dev/unversioned build like "dev"/"unknown"/"".
func isConcreteCLIVersion(v string) bool {
	v = strings.TrimSpace(v)
	return isConcreteVersion(v) || isConcreteVersion("v"+v)
}

// unsafeCLIFloorFlag opts a non-concrete (dev/unversioned) CLI past a release's min_cli_version floor. It is registered
// on `cluster release`/`upgrade`/`provision` and read per-invocation from the command (not bound to a shared package
// var — that would data-race when the command tree is built concurrently, and leak between tests).
const unsafeCLIFloorFlag = "unsafe-ignore-cli-version-floor"

// unsafeCLIFloor reports whether the operator passed --unsafe-ignore-cli-version-floor on cmd. Absent flag ⇒ false.
func unsafeCLIFloor(cmd *cobra.Command) bool {
	v, _ := cmd.Flags().GetBool(unsafeCLIFloorFlag) //nolint:errcheck // a command without the flag defaults to false
	return v
}

// checkCLIVersionFloor enforces a release's min_cli_version floor against this CLI's embedded version, FAIL CLOSED. It
// is the SINGLE checker shared by the fetched-manifest gate (validateFetchedReleaseCompatibility) and the embedded-
// catalog gate (enforceCatalogPath), so a local build cannot satisfy one path while implicitly bypassing the other. A
// non-concrete version (dev/unknown/unversioned/empty) cannot be compared against the floor, so it is REFUSED unless
// allowUnsafe (the --unsafe-ignore-cli-version-floor escape hatch) downgrades the refusal to a loud warning on out. A
// concrete SemVer below the floor is always refused. required == "" is a no-op.
func checkCLIVersionFloor(out io.Writer, required string, allowUnsafe bool) error {
	required = strings.TrimSpace(required)
	if required == "" {
		return nil
	}
	shown := strings.TrimSpace(fwv.Version)
	if shown == "" {
		shown = "<none>"
	}
	// A `git describe` build reports vX.Y.Z-<N>-g<sha>: N commits PAST the tag, i.e. newer than it, but SemVer reads
	// that tail as a prerelease sorting below the tag and below its rc's. Compare such a build as the tag it descends
	// from; a released or rc version is unaffected.
	current := releases.StripGitDescribeSuffix(fwv.Version)
	if !isConcreteCLIVersion(current) {
		if allowUnsafe {
			fmt.Fprintf(out, "WARNING: this frameworks CLI reports a non-concrete version %q that cannot be verified against the release's min_cli_version %s; --%s is set, so the floor is BYPASSED. This is unsupported for a real deploy.\n", shown, required, unsafeCLIFloorFlag)
			return nil
		}
		return fmt.Errorf("this frameworks CLI reports a non-concrete version %q that cannot be verified against the release's required minimum (min_cli_version %s); build/install a released CLI, or pass --%s to bypass (unsupported)", shown, required, unsafeCLIFloorFlag)
	}
	if releases.CompareSemver(current, required) < 0 {
		return fmt.Errorf("this frameworks CLI (%s) is older than the release's required minimum (min_cli_version %s) — upgrade the CLI before deploying", current, required)
	}
	return nil
}

// assertProvisionSatisfiesTransitions fails closed unless EVERY required transition can be established by a clean
// `cluster provision`, classified by ProvisionDisposition:
//
//   - ProvisionMustExecute (the safe default/zero value): the transition converges EXISTING state and can only be
//     established by the reconciler. A clean provision does not run the Check→Apply→Verify DAG, so it refuses and routes
//     to `release apply` rather than silently omitting a required convergence step.
//   - ProvisionEstablishedByBootstrap: the desired-state bootstrap establishes the invariant during install, via the
//     services the transition names in AfterServices. Provision does NOT take this on faith — it holds the claim to its
//     stated mechanism by requiring every establishing AfterService to be an ENABLED service in this provision manifest.
//     A transition that claims bootstrap-establishment whose establishing service is not part of this provision fails
//     closed, so a mis-declared future transition cannot silently pass an install.
func assertProvisionSatisfiesTransitions(platformVersion string, plannedDeploys map[string]bool, transitions []ReleaseTransition) error {
	for _, t := range transitions {
		// Applicability is phase-aware, but an EMPTY boundary is NOT a skip. A transition only ceases to apply when it
		// GATES specific services (non-empty BeforeServices) and NONE of them are scheduled in this partial provision —
		// e.g. `--only infrastructure`, which schedules no Foghorn/Chandler. A transition with an empty BeforeServices
		// gates nothing in particular and is treated as globally applicable, so its disposition is still evaluated: the
		// ProvisionMustExecute zero value must fail closed, never be silently skipped because the boundary was left empty.
		before := t.BeforeServices()
		if len(before) > 0 && !anyPlanned(before, plannedDeploys) {
			continue
		}
		switch t.ProvisionDisposition() {
		case ProvisionEstablishedByBootstrap:
			after := t.AfterServices()
			// A bootstrap-established claim MUST name the establishing service(s). An empty list proves nothing — it
			// would let a future transition assert install-time satisfaction with no mechanism at all — so refuse it.
			if len(after) == 0 {
				return fmt.Errorf("release %s transition %q (%s) claims a clean provision establishes it but names no establishing service (AfterServices is empty) — it proves nothing; make it name its bootstrap provider or run `frameworks cluster release apply`", platformVersion, t.ID(), t.Title())
			}
			// Each establishing service must be SCHEDULED in this provision's plan for this phase (not merely enabled in
			// the manifest): if the bootstrap provider is not being deployed this run, its invariant is not established.
			for _, svc := range after {
				if !plannedDeploys[svc] {
					return fmt.Errorf("release %s transition %q (%s) claims a clean provision establishes it via %q, but %q is not scheduled in this provision plan — its bootstrap mechanism will not run; run `frameworks cluster release apply` instead", platformVersion, t.ID(), t.Title(), svc, svc)
				}
			}
		default:
			return fmt.Errorf("release %s requires reconciliation transition %q (%s) which a clean `cluster provision` does not execute — run `frameworks cluster release apply` for this release instead", platformVersion, t.ID(), t.Title())
		}
	}
	return nil
}

// anyPlanned reports whether any of services is scheduled in this provision plan (plannedDeploys).
func anyPlanned(services []string, plannedDeploys map[string]bool) bool {
	for _, s := range services {
		if plannedDeploys[s] {
			return true
		}
	}
	return false
}

// selectReleaseTransitions returns the compiled transitions required for the target platform version, per the
// versioned catalog. A required id absent from the compiled registry fails closed (outdated CLI).
func selectReleaseTransitions(target string) ([]ReleaseTransition, error) {
	required, err := releases.ReleaseTransitionsUpTo(target)
	if err != nil {
		return nil, fmt.Errorf("read release-transition catalog: %w", err)
	}
	var selected []ReleaseTransition
	for _, req := range required {
		t, ok := releaseTransitionRegistry[req.ID]
		if !ok {
			return nil, fmt.Errorf("release to %s requires the reconciliation transition %q (introduced in %s) which this CLI does not implement — upgrade the CLI before running the release", target, req.ID, req.IntroducedIn)
		}
		selected = append(selected, t)
	}
	return selected, nil
}

// releaseStep is one node in the interleaved plan: either a service upgrade or a reconciliation transition.
type releaseStep struct {
	upgradeService string // service id to upgrade, or ""
	transition     ReleaseTransition
}

// planReleaseSequence interleaves reconciliation transitions with the dependency-ordered service list. A transition
// runs at its declared point: after all its AfterServices are current (already at target — i.e. not in this release —
// or upgraded earlier in the sequence) and before any of its BeforeServices upgrade. Transitions whose BeforeServices
// are not in this release run at the end. It is PURE (no I/O) so the ordering contract is unit-testable. It errors on
// a release-ordering violation (a BeforeService about to upgrade while an AfterService prerequisite is still pending).
func planReleaseSequence(services []string, deployOf func(string) string, transitions []ReleaseTransition) ([]releaseStep, error) {
	willUpgrade := map[string]bool{}
	for _, svcID := range services {
		willUpgrade[deployOf(svcID)] = true
	}
	upgraded := map[string]bool{}
	pending := append([]ReleaseTransition{}, transitions...)
	var steps []releaseStep

	afterSatisfied := func(t ReleaseTransition) (bool, []string) {
		var missing []string
		for _, a := range t.AfterServices() {
			if !upgraded[a] && willUpgrade[a] {
				missing = append(missing, a)
			}
		}
		return len(missing) == 0, missing
	}
	runDue := func(beforeDeploy string) error {
		var still []ReleaseTransition
		for _, t := range pending {
			due := false
			if beforeDeploy == "" {
				ok, _ := afterSatisfied(t)
				due = ok
			} else if stringInSlice(beforeDeploy, t.BeforeServices()) {
				ok, missing := afterSatisfied(t)
				if !ok {
					return fmt.Errorf("reconciliation %q must run before %s but its prerequisite service(s) %v are not upgraded — release-ordering bug", t.ID(), beforeDeploy, missing)
				}
				due = true
			}
			if due {
				steps = append(steps, releaseStep{transition: t})
			} else {
				still = append(still, t)
			}
		}
		pending = still
		return nil
	}

	for _, svcID := range services {
		if err := runDue(deployOf(svcID)); err != nil {
			return nil, err
		}
		steps = append(steps, releaseStep{upgradeService: svcID})
		upgraded[deployOf(svcID)] = true
	}
	if err := runDue(""); err != nil {
		return nil, err
	}
	return steps, nil
}

// runReleaseUpgradesInterleaved plans the interleaved sequence and executes it.
func runReleaseUpgradesInterleaved(cmd *cobra.Command, rc *resolvedCluster, env *reconcileEnv, transitions []ReleaseTransition, services []string, version string, opts releaseApplyOptions) (map[string]struct{}, error) {
	installed := map[string]struct{}{}
	steps, err := planReleaseSequence(services, func(svcID string) string { return releaseDeployName(rc.Manifest, svcID) }, transitions)
	if err != nil {
		return nil, err
	}
	upgradeIdx, total := 0, len(services)
	for _, step := range steps {
		if step.transition != nil {
			if err := runReleaseTransition(cmd, env, step.transition, opts.dryRun); err != nil {
				return nil, err
			}
			continue
		}
		upgradeIdx++
		fmt.Fprintf(cmd.OutOrStdout(), "\n[upgrade %d/%d] %s\n", upgradeIdx, total, step.upgradeService)
		result, err := runUpgrade(cmd, rc, step.upgradeService, version, opts.dryRun, opts.skipValidation, true, opts.noRollback, false, false, true)
		if err != nil {
			return nil, fmt.Errorf("upgrade %s: %w", step.upgradeService, err)
		}
		if result.installed {
			installed[releaseDeployName(rc.Manifest, step.upgradeService)] = struct{}{}
		}
	}
	return installed, nil
}

// runReleaseTransition runs one transition's Check across its scopes and, for Pending scopes, Apply + Verify (unless
// dry-run). A Blocked scope halts the release.
func runReleaseTransition(cmd *cobra.Command, env *reconcileEnv, t ReleaseTransition, dryRun bool) error {
	if env == nil {
		return fmt.Errorf("reconciliation %q required but no Quartermaster client was built", t.ID())
	}
	out := cmd.OutOrStdout()
	baseCtx := cmd.Context()
	if baseCtx == nil {
		baseCtx = context.Background()
	}
	ctx, cancel := context.WithTimeout(baseCtx, 5*time.Minute)
	defer cancel()

	scopes, err := t.Scopes(env)
	if err != nil {
		return fmt.Errorf("reconciliation %q scopes: %w", t.ID(), err)
	}
	if len(scopes) == 0 {
		return nil
	}
	converger, hasConverger := t.(releaseSideStateConverger)
	fmt.Fprintf(out, "\n[reconcile] %s (%s)\n", t.Title(), t.ID())
	for _, scope := range scopes {
		chk := t.Check(ctx, env, scope)
		fmt.Fprintf(out, "  %s: %s — %s\n", scope.Label, chk.Status, chk.Detail)
		switch chk.Status {
		case ReconcileNotApplicable:
			continue
		case ReconcileComplete:
			// Even when the invariant already holds, converge any derived side-state so a resumed release presents the
			// same in-process state to downstream checks as the original run.
			if hasConverger {
				if err := converger.ConvergeSideState(ctx, env, scope); err != nil {
					return fmt.Errorf("reconciliation %q converge side-state for %s: %w", t.ID(), scope.Label, err)
				}
			}
			continue
		case ReconcileBlocked:
			return fmt.Errorf("reconciliation %q is BLOCKED for %s: %s", t.ID(), scope.Label, chk.Detail)
		case ReconcilePending:
			// Validate Apply's authorization precondition now so --dry-run fails the same way the real run would.
			if strings.TrimSpace(env.operatorJWT) == "" {
				return fmt.Errorf("reconciliation %q would apply for %s but requires a platform-operator login (no operator JWT available)", t.ID(), scope.Label)
			}
			if dryRun {
				fmt.Fprintf(out, "    [DRY-RUN] would apply%s\n", irreversibleNote(t))
				// Overlay the would-be state (in-memory only) so downstream gates in this dry-run see what the real
				// run would produce, not the stale pre-transition state.
				if hasConverger {
					if err := converger.PreviewSideState(ctx, env, scope); err != nil {
						return fmt.Errorf("reconciliation %q preview for %s: %w", t.ID(), scope.Label, err)
					}
				}
				continue
			}
			fmt.Fprintf(out, "    applying%s...\n", irreversibleNote(t))
			if err := t.Apply(ctx, env, scope); err != nil {
				return fmt.Errorf("reconciliation %q apply for %s: %w", t.ID(), scope.Label, err)
			}
			if err := t.Verify(ctx, env, scope); err != nil {
				return fmt.Errorf("reconciliation %q verify for %s: %w", t.ID(), scope.Label, err)
			}
			fmt.Fprintf(out, "    ✓ applied and verified\n")
		default:
			return fmt.Errorf("reconciliation %q returned unknown status %q for %s", t.ID(), chk.Status, scope.Label)
		}
	}
	return nil
}

func irreversibleNote(t ReleaseTransition) string {
	if t.Irreversible() {
		return " (irreversible — stays committed if a later step rolls back)"
	}
	return ""
}

// releaseDeployName resolves a service id to its deploy name (alias-aware); infrastructure ids map to themselves.
func releaseDeployName(manifest *inventory.Manifest, serviceID string) string {
	if svc, ok := manifest.Services[serviceID]; ok {
		if dn, err := resolveDeployName(serviceID, svc); err == nil {
			return dn
		}
	}
	if svc, ok := manifest.Interfaces[serviceID]; ok {
		if dn, err := resolveDeployName(serviceID, svc); err == nil {
			return dn
		}
	}
	if svc, ok := manifest.Observability[serviceID]; ok {
		if dn, err := resolveDeployName(serviceID, svc); err == nil {
			return dn
		}
	}
	return serviceID
}

func stringInSlice(s string, list []string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

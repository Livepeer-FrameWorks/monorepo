package cmd

import (
	"context"
	"errors"
	"fmt"
	"frameworks/cli/pkg/inventory"
	"io"
	"maps"
	"strings"
	"time"

	"frameworks/cli/internal/ux"
	"frameworks/cli/pkg/provisioner"
	"frameworks/cli/pkg/ssh"
	"frameworks/cli/pkg/watchdog"

	"github.com/spf13/cobra"
)

func newClusterMigrateCmd() *cobra.Command {
	var dryRun bool
	var phase string
	var yes bool
	var toVersion string
	var skipDataMigrationCheck bool
	var completeInterruptedBaselines bool

	cmd := &cobra.Command{
		Use:   "migrate",
		Short: "Apply pending database migrations",
		Long: `Apply versioned SQL migrations from pkg/database/sql/migrations/.

Migrations are tracked in a _migrations table per database. Files ending in
.notx.sql run with autocommit for operations such as CREATE INDEX
CONCURRENTLY. Safe to run multiple times — already-applied migrations are
skipped.

Migrations are filtered to versions <= --to-version. When --to-version is
omitted, the cluster's release channel is resolved to a concrete vX.Y.Z via
the GitOps release manifest; if that cannot be resolved, --to-version must
be passed explicitly.

Routine rolling upgrades should only apply expand-compatible migrations here:
additive tables/columns/indexes, nullable/defaulted fields, or broader
constraints that the currently running binaries can still tolerate. Destructive
contract steps run only when explicitly requested with '--phase contract' (never
during a routine expand upgrade); background data migrations and read/write flips
are release-specific operations run by other commands, not this one.

Before expand migrations, every manifest database with an embedded baseline
that is missing on the cluster (or present with an empty service schema) is
created with its owner and runtime roles and its current baseline. A populated
database without a baseline marker or migration history fails closed unless
--complete-interrupted-baselines explicitly authorizes baseline completion and
schema verification.

The underlying Ansible role filters applied vs pending on the target; in
--dry-run mode the role runs under ansible-playbook --check --diff so
nothing is actually written. Dry-run lists databases it would create without
sending any database, role, or baseline SQL.`,
		Example: `  frameworks cluster migrate --phase expand --to-version v0.5.0 --dry-run
  frameworks cluster migrate --phase expand --to-version v0.5.0`,
		RunE: func(cmd *cobra.Command, args []string) error {
			rc, err := resolveClusterManifest(cmd)
			if err != nil {
				return err
			}
			defer rc.Cleanup()
			if err := requirePlatformIfImplicitManifest(rc, cmd.OutOrStdout()); err != nil {
				return err
			}
			return runMigrate(cmd, rc, dryRun, phase, yes, toVersion, skipDataMigrationCheck, completeInterruptedBaselines)
		},
	}

	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Report pending migrations via ansible --check --diff without applying")
	cmd.Flags().StringVar(&phase, "phase", "expand", "Migration phase to apply (expand, postdeploy, or contract)")
	cmd.Flags().BoolVar(&yes, "yes", false, "Confirm contract migrations")
	cmd.Flags().StringVar(&toVersion, "to-version", "", "Concrete vX.Y.Z to migrate up to (defaults to cluster's resolved platform version)")
	cmd.Flags().BoolVar(&skipDataMigrationCheck, "skip-data-migration-check", false, "DANGEROUS: skip pre-postdeploy data migration gate")
	cmd.Flags().BoolVar(&completeInterruptedBaselines, "complete-interrupted-baselines", false, "Reapply and verify baselines for populated databases without migration provenance")
	cmd.Flags().String(backupFlag, "", "Backup (directory or s3:// URL) taken within the last hour; required by --phase contract when contract migrations are pending")

	cmd.AddCommand(newClusterMigrateValidateCmd())

	return cmd
}

func newClusterMigrateValidateCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "validate",
		Short: "Validate embedded database migrations",
		Long: `Validate embedded PostgreSQL/YugabyteDB and ClickHouse migration files.

This checks the database/version/phase directory layout, the release-catalog
version ceiling, and conservative expand-phase safety rules before migrations
reach an operator cluster.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			pgErr := provisioner.ValidateEmbeddedPostgresMigrations()
			chErr := provisioner.ValidateEmbeddedClickHouseMigrations()
			switch {
			case pgErr != nil && chErr != nil:
				return fmt.Errorf("migration validation failed: %w", errors.Join(pgErr, chErr))
			case pgErr != nil:
				return pgErr
			case chErr != nil:
				return chErr
			}
			ux.Success(cmd.OutOrStdout(), "Migration files validated")
			return nil
		},
	}
}

// migrateStallWindow is how long one migrate step may go without progress before it is cancelled. Progress is every
// ansible-playbook output line of a migrate role (each task and loop item) and each preflight check that finished, so
// a step runs as long as it keeps moving. The window must outlast the longest single silent task: a .notx.sql
// concurrent index build runs without a statement timeout and prints nothing until it finishes.
var migrateStallWindow = 30 * time.Minute

func runMigrate(cmd *cobra.Command, rc *resolvedCluster, dryRun bool, phase string, yes bool, toVersion string, skipDataMigrationCheck, completeInterruptedBaselines bool) error {
	switch phase {
	case "expand", "postdeploy":
	case "contract":
		if !dryRun && !yes {
			return fmt.Errorf("contract migrations require --yes")
		}
	default:
		return fmt.Errorf("unsupported migration phase %q: expected expand, postdeploy, or contract", phase)
	}

	target, err := resolveMigrationTarget(rc, toVersion)
	if err != nil {
		return err
	}
	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "Target version: %s (phase: %s)\n", target, phase)

	sshKey := stringFlag(cmd, "ssh-key").Value
	sshPool := ssh.NewPool(30*time.Second, sshKey)
	defer sshPool.Close()

	// Service databases a live cluster lacks (a database introduced by this
	// release) are created with their roles and current baseline before the
	// floor guard and any migration read their ledgers. In dry-run nothing is
	// created, so the planned databases are excluded from those ledger reads,
	// and recorded on rc so the dry-run gates and postdeploy step of the same
	// release skip them too.
	// Baseline work runs before the migrate watchdogs start and is bounded per
	// database inside the step: completing a large YugabyteDB database alone
	// runs long, and cancelling it midway only leaves it unverified again.
	var plannedDatabases map[string]struct{}
	if dryRun {
		plannedDatabases = rc.dryRunCreatedDatabases
	}
	if dryRun && phase == "expand" {
		// Allocated before the branches so the per-deployment views, which share this map, record into it.
		rc.dryRunExpandMigrations = map[string]struct{}{}
	}
	if phase == "expand" {
		created, ensureErr := ensureServiceDatabasesFn(context.Background(), cmd, rc, sshPool, dryRun, completeInterruptedBaselines)
		if ensureErr != nil {
			return fmt.Errorf("service databases: %w", ensureErr)
		}
		if dryRun {
			plannedDatabases = schemaDatabaseNameSet(created)
			rc.dryRunCreatedDatabases = plannedDatabases
		}
	}

	// The preflight checks and each engine branch run under their own watchdog, so a slow step is never cut short by
	// a budget an earlier step used up, and one branch cannot starve the next.
	preflightCtx, preflight := watchdog.Start(context.Background(), "migration preflight checks", migrateStallWindow)
	defer preflight.Stop()

	if phase == "postdeploy" || phase == "contract" {
		if err := runPhaseDataMigrationGate(preflightCtx, cmd, rc, sshPool, phase, target, skipDataMigrationCheck); err != nil {
			return preflight.Err(err)
		}
		preflight.Report("data-migration gate passed")
	}
	// Contract migrations drop what a rollback would need, so they require a fresh backup that matches the live
	// ledgers of the databases they change.
	if phase == "contract" {
		if err := requireContractBackup(preflightCtx, cmd, rc, sshPool, target); err != nil {
			return preflight.Err(err)
		}
		preflight.Report("contract backup gate passed")
	}

	// Minimum-upgrade-version guard: refuse if the cluster is missing any migration
	// folded below the baseline floor (it would be silently skipped). Runs before
	// any migration is applied. See docs/standards/schema-migrations.md.
	if err := migrateBelowFloorGuardFn(preflightCtx, rc, sshPool, plannedDatabases); err != nil {
		return preflight.Err(err)
	}
	preflight.Stop()

	branchesRun := []string{}
	branchesWithItems := []string{}
	branchErrors := map[string]error{}

	runBranch := func(engine string, branch func(context.Context) error) {
		branchCtx, w := watchdog.Start(context.Background(), engine+" migrations", migrateStallWindow)
		defer w.Stop()
		if err := w.Err(branch(branchCtx)); err != nil {
			branchErrors[engine] = err
		}
	}
	runBranch("postgres", func(ctx context.Context) error {
		return runMigratePostgresBranch(ctx, cmd, rc, sshPool, dryRun, phase, target, plannedDatabases, &branchesRun, &branchesWithItems)
	})
	runBranch("clickhouse", func(ctx context.Context) error {
		return runMigrateClickHouseBranch(ctx, cmd, rc, sshPool, dryRun, phase, target, &branchesRun, &branchesWithItems)
	})

	if len(branchesRun) == 0 {
		fmt.Fprintln(out, "No migration-capable databases configured in manifest.")
		return nil
	}
	if len(branchErrors) > 0 {
		messages := make([]string, 0, len(branchErrors))
		for branch, err := range branchErrors {
			messages = append(messages, fmt.Sprintf("%s: %v", branch, err))
		}
		return fmt.Errorf("migrate failed: %s", strings.Join(messages, "; "))
	}

	if dryRun {
		// Migration files exist for these databases; which of them are still unapplied is what each branch's
		// check run above reports (a recap with changed=0 has none pending).
		fmt.Fprintln(out, "Dry-run complete; the check output above lists any unapplied migrations (changed=0 means none are pending)")
		return nil
	}
	if len(branchesWithItems) == 0 {
		fmt.Fprintf(out, "No pending %s migrations across configured databases at or below %s.\n", phase, target)
		return nil
	}
	ux.Success(out, "Migrations applied")
	ux.PrintNextSteps(out, []ux.NextStep{
		{Cmd: "frameworks cluster status", Why: "Verify deployed services are healthy after schema changes."},
		{Cmd: "frameworks cluster upgrade --all --dry-run", Why: "Preview service rollout after expand-compatible migrations."},
		{Why: "Review release notes for required data migrations or verification before flipping reads or applying contract migrations."},
	})
	return nil
}

// runBelowFloorGuardExcluding refuses the migration if any manifest database is
// missing a migration that was folded below the baseline floor — those are no
// longer offered, so an existing cluster lacking them would be silently stranded.
// Read-only; fail-closed (a ledger-read failure blocks rather than risk an unsafe
// upgrade). excluded names physical Postgres databases a dry-run would create:
// they do not exist yet, and they are born from the current baseline whose marker
// folds every below-floor migration.
func runBelowFloorGuardExcluding(ctx context.Context, rc *resolvedCluster, sshPool *ssh.Pool, excluded map[string]struct{}) error {
	if hasUnscopedDatabaseDeployments(rc.Manifest) {
		first := true
		return forEachDatabaseDeployment(rc.Manifest, func(view *inventory.Manifest) error {
			if !first {
				view.Infrastructure.ClickHouse = nil
			}
			first = false
			return runBelowFloorGuardExcluding(ctx, rc.withDatabaseManifest(view), sshPool, excluded)
		})
	}

	manifest := rc.Manifest

	if pg := manifest.Infrastructure.Postgres; pg != nil && pg.Enabled {
		hosts := postgresCandidateHosts(manifest, pg)
		if len(hosts) > 0 {
			pgHost := hosts[0]
			if pg.IsYugabyte() {
				if h, ok := firstHealthyYugabyteHost(ctx, sshPool, hosts, pg); ok {
					pgHost = h
				}
			}
			databases := yugabyteSchemaDatabases(pg.Databases, manifest)
			databases = excludeSchemaDatabases(databases, excluded)
			if len(databases) > 0 {
				var sharedEnv map[string]string
				if pg.IsYugabyte() && pg.Password == "" {
					env, sErr := rc.SharedEnv()
					if sErr != nil {
						return fmt.Errorf("load manifest env_files: %w", sErr)
					}
					sharedEnv = env
				}
				password, pErr := resolveYugabytePassword(pg, sharedEnv)
				if pErr != nil {
					return pErr
				}
				gap, gErr := postgresBelowFloorGapFn(ctx, sshPool, pgHost, pg, password, databases)
				if gErr != nil {
					return fmt.Errorf("baseline-floor check (postgres): %w", gErr)
				}
				if len(gap) > 0 {
					return fmt.Errorf("%s", provisioner.FormatBelowFloorRefusal("postgres", gap))
				}
			}
		}
	}

	if ch := manifest.Infrastructure.ClickHouse; ch != nil && ch.Enabled && len(ch.Databases) > 0 {
		if host, ok := manifest.GetHost(ch.CoordinatorHost()); ok {
			host.Name = firstNonEmpty(host.Name, ch.CoordinatorHost())
			env, sErr := rc.SharedEnv()
			if sErr != nil {
				return fmt.Errorf("load manifest env_files: %w", sErr)
			}
			gap, gErr := clickHouseBelowFloorGapFn(ctx, sshPool, host, ch.EffectivePort(), env["CLICKHOUSE_PASSWORD"], ch.Databases)
			if gErr != nil {
				return fmt.Errorf("baseline-floor check (clickhouse): %w", gErr)
			}
			if len(gap) > 0 {
				return fmt.Errorf("%s", provisioner.FormatBelowFloorRefusal("clickhouse", gap))
			}
		}
	}
	return nil
}

// runMigratePostgresBranch applies one phase of Postgres migrations. plannedDatabases
// names service databases a dry-run would create first; they do not exist yet, so
// their ledgers are not read.
func runMigratePostgresBranch(ctx context.Context, cmd *cobra.Command, rc *resolvedCluster, sshPool *ssh.Pool, dryRun bool, phase, target string, plannedDatabases map[string]struct{}, branchesRun, branchesWithItems *[]string) error {
	if hasUnscopedDatabaseDeployments(rc.Manifest) {
		return forEachDatabaseDeployment(rc.Manifest, func(view *inventory.Manifest) error {
			return runMigratePostgresBranch(ctx, cmd, rc.withDatabaseManifest(view), sshPool, dryRun, phase, target, plannedDatabases, branchesRun, branchesWithItems)
		})
	}
	out := cmd.OutOrStdout()
	manifest := rc.Manifest
	pg := manifest.Infrastructure.Postgres
	if pg == nil || !pg.Enabled {
		return nil
	}
	*branchesRun = append(*branchesRun, "postgres")

	// Target a live node rather than pinning Nodes[0]: migrations (DDL) are
	// cluster-replicated, so any healthy tserver applies them. For Yugabyte we
	// select by local YSQL health (below); this keeps `cluster migrate` working
	// when the first node is the dead one.
	hosts := postgresCandidateHosts(manifest, pg)
	if len(hosts) == 0 {
		return fmt.Errorf("no postgres/yugabyte hosts resolvable in manifest")
	}
	pgHost := hosts[0]
	if pg.IsYugabyte() {
		// Migrations run via ysqlsh against the chosen node's own YSQL listener, so pick a
		// tserver whose local YSQL actually answers, not merely one reachable
		// over SSH, and fail over to the next node otherwise.
		h, ok := firstHealthyYugabyteHost(ctx, sshPool, hosts, pg)
		if !ok {
			return fmt.Errorf("no yugabyte tserver with healthy local YSQL among %d candidate(s)", len(hosts))
		}
		pgHost = h
		if err := refuseDuringYugabyteRelayoutFn(ctx, sshPool, pgHost, pg); err != nil {
			return err
		}
	}

	databases := yugabyteSchemaDatabases(pg.Databases, manifest)
	engine := provisioner.SQLEnginePostgres
	if pg.IsYugabyte() {
		databases = yugabyteSchemaDatabases(pg.Databases, manifest)
		engine = provisioner.SQLEngineYugabyte
	}
	if len(databases) == 0 {
		fmt.Fprintln(out, "Postgres: no databases configured in manifest.")
		return nil
	}
	if dryRun && len(plannedDatabases) > 0 {
		databases = excludeSchemaDatabases(databases, plannedDatabases)
		fmt.Fprintf(out, "Postgres: skipping migration checks for database(s) created by this release: %s\n", strings.Join(sortedKeys(plannedDatabases), ", "))
		if len(databases) == 0 {
			return nil
		}
	}
	items, err := provisioner.BuildMigrationItemsForEngine(databases, phase, target, engine)
	if err != nil {
		return fmt.Errorf("collect postgres migrations: %w", err)
	}
	if len(items) == 0 {
		fmt.Fprintf(out, "Postgres: no %s migration files for configured databases at or below %s.\n", phase, target)
		return nil
	}
	*branchesWithItems = append(*branchesWithItems, "postgres")

	var sharedEnv map[string]string
	if pg.IsYugabyte() && pg.Password == "" {
		env, sErr := rc.SharedEnv()
		if sErr != nil {
			return fmt.Errorf("load manifest env_files: %w", sErr)
		}
		sharedEnv = env
	}
	password, err := resolveYugabytePassword(pg, sharedEnv)
	if err != nil {
		return err
	}

	serviceName := "postgres"
	itemsKey := "postgres_migrate_items"
	if pg.IsYugabyte() {
		serviceName = "yugabyte"
		itemsKey = "yugabyte_migrate_items"
	}

	migrator, err := migratorForFn(serviceName, sshPool)
	if err != nil {
		return err
	}

	cfg := provisioner.ServiceConfig{
		Port: pg.EffectivePort(),
		Metadata: map[string]any{
			"platform_channel":  manifest.ResolvedChannel(),
			"postgres_password": password,
			itemsKey:            items,
		},
	}
	if len(rc.ReleaseRepos) > 0 {
		repos := make([]string, len(rc.ReleaseRepos))
		copy(repos, rc.ReleaseRepos)
		cfg.Metadata["gitops_repositories"] = repos
	}

	if dryRun {
		fmt.Fprintf(out, "Postgres: running %s migrations in --check --diff mode (no writes)\n", phase)
	} else {
		fmt.Fprintf(out, "Postgres: applying pending %s migrations\n", phase)
	}

	if err := migrator.ApplyMigrations(ctx, pgHost, cfg, dryRun); err != nil {
		return fmt.Errorf("apply postgres migrations: %w", err)
	}
	if dryRun && phase == "expand" {
		recordDryRunExpandMigrations(rc.dryRunExpandMigrations, dryRunEnginePostgres, items)
	}
	return nil
}

// Engine labels of dryRunMigrationKey. PostgreSQL and YugabyteDB share one ledger shape and one gate branch.
const (
	dryRunEnginePostgres   = "postgres"
	dryRunEngineClickHouse = "clickhouse"
)

// migratorForFn resolves the migrate-role runner for an engine's provisioner. Tests substitute a recorder so the
// migrate branches run without ansible.
var migratorForFn = func(serviceName string, sshPool *ssh.Pool) (provisioner.Migrator, error) {
	prov, err := provisioner.GetProvisioner(serviceName, sshPool)
	if err != nil {
		return nil, err
	}
	migrator, ok := prov.(provisioner.Migrator)
	if !ok {
		return nil, fmt.Errorf("%s provisioner does not implement Migrator", serviceName)
	}
	return migrator, nil
}

// dryRunMigrationKey identifies one migration in one physical database by its ledger identity (version, phase,
// sequence), the same identity the pre-deploy gate's ledger diff reports.
func dryRunMigrationKey(engine, database, version, phase string, seq int) string {
	return fmt.Sprintf("%s|%s|%s|%s|%d", engine, database, version, phase, seq)
}

// recordDryRunExpandMigrations adds the migrate items a dry-run expand step submitted to its engine's role. The role
// applies each item absent from the ledger, so these are what the real expand step would apply. A nil set records
// nothing.
func recordDryRunExpandMigrations(set map[string]struct{}, engine string, items []map[string]any) {
	if set == nil {
		return
	}
	for _, item := range items {
		seq, _ := item["sequence"].(int) //nolint:errcheck // the provisioner item builders always set an int sequence
		set[dryRunMigrationKey(engine, fmt.Sprint(item["db"]), fmt.Sprint(item["version"]), fmt.Sprint(item["phase"]), seq)] = struct{}{}
	}
}

// splitDryRunExpandMigrations separates the missing expand migrations a dry-run expand step in this run would apply
// from those it would not. A checksum mismatch stays a refusal: the migrate role does not re-apply a ledgered
// migration. With no recorded set (every real apply) all missing migrations remain.
func splitDryRunExpandMigrations(missing []provisioner.MigrationKey, engine string, recorded map[string]struct{}) (remaining, wouldApply []provisioner.MigrationKey) {
	if len(recorded) == 0 {
		return missing, nil
	}
	for _, m := range missing {
		if m.MismatchedChecksum == "" {
			if _, ok := recorded[dryRunMigrationKey(engine, m.Database, m.Version, m.Phase, m.Seq)]; ok {
				wouldApply = append(wouldApply, m)
				continue
			}
		}
		remaining = append(remaining, m)
	}
	return remaining, wouldApply
}

// reportDryRunExpandMigrations prints the missing expand migrations a dry-run gate counts as applied.
func reportDryRunExpandMigrations(out io.Writer, serviceName string, wouldApply []provisioner.MigrationKey) {
	if len(wouldApply) == 0 {
		return
	}
	fmt.Fprintf(out, "[gate] %d expand migration(s) for %s would be applied by this release's expand step; dry-run counts them as applied:\n", len(wouldApply), serviceName)
	for _, m := range wouldApply {
		fmt.Fprintf(out, "    - %s\n", m.String())
	}
}

func runMigrateClickHouseBranch(ctx context.Context, cmd *cobra.Command, rc *resolvedCluster, sshPool *ssh.Pool, dryRun bool, phase, target string, branchesRun, branchesWithItems *[]string) error {
	out := cmd.OutOrStdout()
	manifest := rc.Manifest
	ch := manifest.Infrastructure.ClickHouse
	if ch == nil || !ch.Enabled {
		return nil
	}
	*branchesRun = append(*branchesRun, "clickhouse")

	if len(ch.Databases) == 0 {
		fmt.Fprintln(out, "ClickHouse: no databases configured in manifest.")
		return nil
	}

	host, ok := manifest.GetHost(ch.CoordinatorHost())
	if !ok {
		return fmt.Errorf("clickhouse host %s not found", ch.CoordinatorHost())
	}

	items, err := provisioner.BuildClickHouseMigrationItems(ch.Databases, phase, target)
	if err != nil {
		return fmt.Errorf("collect clickhouse migrations: %w", err)
	}
	if len(items) == 0 {
		fmt.Fprintf(out, "ClickHouse: no %s migration files for configured databases at or below %s.\n", phase, target)
		return nil
	}
	*branchesWithItems = append(*branchesWithItems, "clickhouse")

	chEnv, envErr := rc.SharedEnv()
	if envErr != nil {
		return fmt.Errorf("load manifest env_files: %w", envErr)
	}
	chPassword := chEnv["CLICKHOUSE_PASSWORD"]

	migrator, err := migratorForFn("clickhouse", sshPool)
	if err != nil {
		return err
	}

	chPort := ch.Port
	if chPort == 0 {
		chPort = 9000
	}
	cfg := provisioner.ServiceConfig{
		Port: chPort,
		Metadata: map[string]any{
			"platform_channel":         manifest.ResolvedChannel(),
			"clickhouse_password":      chPassword,
			"databases":                ch.Databases,
			"clickhouse_migrate_items": items,
		},
	}
	// Same Replicated-cluster metadata as provision/init so migrate.yml's
	// _migrations ledger uses ReplicatedReplacingMergeTree (keeper_nodes present),
	// not a plain per-node ledger. Migrations run on the coordinator.
	maps.Copy(cfg.Metadata, clickhouseClusterMetadata(manifest, ch, ch.CoordinatorHost(), 0))

	if dryRun {
		fmt.Fprintf(out, "ClickHouse: running %s migrations in --check --diff mode (no writes)\n", phase)
	} else {
		fmt.Fprintf(out, "ClickHouse: applying pending %s migrations\n", phase)
	}
	if err := migrator.ApplyMigrations(ctx, host, cfg, dryRun); err != nil {
		return fmt.Errorf("apply clickhouse migrations: %w", err)
	}
	if dryRun && phase == "expand" {
		recordDryRunExpandMigrations(rc.dryRunExpandMigrations, dryRunEngineClickHouse, items)
	}
	return nil
}

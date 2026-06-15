package cmd

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"frameworks/cli/internal/ux"
	"frameworks/cli/pkg/provisioner"
	"frameworks/cli/pkg/ssh"

	"github.com/spf13/cobra"
)

func newClusterMigrateCmd() *cobra.Command {
	var dryRun bool
	var phase string
	var yes bool
	var toVersion string
	var skipDataMigrationCheck bool

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
constraints that the currently running binaries can still tolerate. Background
data migrations, read/write flips, and destructive contract steps are
release-specific operations and are not run by this command.

The underlying Ansible role filters applied vs pending on the target; in
--dry-run mode the role runs under ansible-playbook --check --diff so
nothing is actually written.`,
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
			return runMigrate(cmd, rc, dryRun, phase, yes, toVersion, skipDataMigrationCheck)
		},
	}

	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Report pending migrations via ansible --check --diff without applying")
	cmd.Flags().StringVar(&phase, "phase", "expand", "Migration phase to apply (expand, postdeploy, or contract)")
	cmd.Flags().BoolVar(&yes, "yes", false, "Confirm contract migrations")
	cmd.Flags().StringVar(&toVersion, "to-version", "", "Concrete vX.Y.Z to migrate up to (defaults to cluster's resolved platform version)")
	cmd.Flags().BoolVar(&skipDataMigrationCheck, "skip-data-migration-check", false, "DANGEROUS: skip pre-postdeploy data migration gate")

	cmd.AddCommand(newClusterMigrateValidateCmd())

	return cmd
}

func newClusterMigrateValidateCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "validate",
		Short: "Validate embedded database migrations",
		Long: `Validate embedded PostgreSQL/YugabyteDB and ClickHouse migration files.

This checks the database/version/phase directory layout and conservative
expand-phase safety rules before migrations reach an operator cluster.`,
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

func runMigrate(cmd *cobra.Command, rc *resolvedCluster, dryRun bool, phase string, yes bool, toVersion string, skipDataMigrationCheck bool) error {
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

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	if phase == "postdeploy" || phase == "contract" {
		if err := runPhaseDataMigrationGate(ctx, cmd, rc, sshPool, phase, target, skipDataMigrationCheck); err != nil {
			return err
		}
	}

	branchesRun := []string{}
	branchesWithItems := []string{}
	branchErrors := map[string]error{}

	if pgErr := runMigratePostgresBranch(ctx, cmd, rc, sshPool, dryRun, phase, target, &branchesRun, &branchesWithItems); pgErr != nil {
		branchErrors["postgres"] = pgErr
	}

	if chErr := runMigrateClickHouseBranch(ctx, cmd, rc, sshPool, dryRun, phase, target, &branchesRun, &branchesWithItems); chErr != nil {
		branchErrors["clickhouse"] = chErr
	}

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
		fmt.Fprintln(out, "Dry-run complete; ansible check output above shows pending changes")
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

func runMigratePostgresBranch(ctx context.Context, cmd *cobra.Command, rc *resolvedCluster, sshPool *ssh.Pool, dryRun bool, phase, target string, branchesRun, branchesWithItems *[]string) error {
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
		// Migrations run via ysqlsh -h localhost on the chosen node, so pick a
		// tserver whose local YSQL actually answers, not merely one reachable
		// over SSH, and fail over to the next node otherwise.
		h, ok := firstHealthyYugabyteHost(ctx, sshPool, hosts, pg)
		if !ok {
			return fmt.Errorf("no yugabyte tserver with healthy local YSQL among %d candidate(s)", len(hosts))
		}
		pgHost = h
	}

	databases := schemaDatabasesFromConfigs(pg.Databases)
	if pg.IsYugabyte() {
		databases = yugabyteSchemaDatabases(pg.Databases, manifest)
	}
	if len(databases) == 0 {
		fmt.Fprintln(out, "Postgres: no databases configured in manifest.")
		return nil
	}
	items, err := provisioner.BuildMigrationItemsForDatabases(databases, phase, target)
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

	prov, err := provisioner.GetProvisioner(serviceName, sshPool)
	if err != nil {
		return err
	}
	migrator, ok := prov.(provisioner.Migrator)
	if !ok {
		return fmt.Errorf("%s provisioner does not implement Migrator", serviceName)
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
	return nil
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

	host, ok := manifest.GetHost(ch.Host)
	if !ok {
		return fmt.Errorf("clickhouse host %s not found", ch.Host)
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

	prov, err := provisioner.GetProvisioner("clickhouse", sshPool)
	if err != nil {
		return err
	}
	migrator, ok := prov.(provisioner.Migrator)
	if !ok {
		return fmt.Errorf("clickhouse provisioner does not implement Migrator")
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

	if dryRun {
		fmt.Fprintf(out, "ClickHouse: running %s migrations in --check --diff mode (no writes)\n", phase)
	} else {
		fmt.Fprintf(out, "ClickHouse: applying pending %s migrations\n", phase)
	}
	if err := migrator.ApplyMigrations(ctx, host, cfg, dryRun); err != nil {
		return fmt.Errorf("apply clickhouse migrations: %w", err)
	}
	return nil
}

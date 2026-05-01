package cmd

import (
	"context"
	"fmt"
	"time"

	"frameworks/cli/internal/ux"
	"frameworks/cli/pkg/inventory"
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
		Long: `Validate embedded PostgreSQL/YugabyteDB migration files.

This checks the database/version/phase directory layout and conservative
expand-phase safety rules before migrations reach an operator cluster.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := provisioner.ValidateEmbeddedMigrations(); err != nil {
				return err
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
	fmt.Fprintf(cmd.OutOrStdout(), "Target version: %s (phase: %s)\n", target, phase)

	manifest := rc.Manifest
	pg := manifest.Infrastructure.Postgres
	if pg == nil || !pg.Enabled {
		fmt.Fprintln(cmd.OutOrStdout(), "Postgres not enabled, nothing to migrate.")
		return nil
	}

	var pgHost inventory.Host
	var ok bool
	if pg.IsYugabyte() && len(pg.Nodes) > 0 {
		pgHost, ok = manifest.GetHost(pg.Nodes[0].Host)
		if !ok {
			return fmt.Errorf("yugabyte node host %s not found", pg.Nodes[0].Host)
		}
	} else {
		pgHost, ok = manifest.GetHost(pg.Host)
		if !ok {
			return fmt.Errorf("postgres host %s not found", pg.Host)
		}
	}

	databases := pg.Databases
	if len(databases) == 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "No databases configured in manifest.")
		return nil
	}
	dbNames := make([]string, 0, len(databases))
	for _, d := range databases {
		dbNames = append(dbNames, d.Name)
	}

	items, err := provisioner.BuildMigrationItems(dbNames, phase, target)
	if err != nil {
		return fmt.Errorf("collect migrations: %w", err)
	}
	if len(items) == 0 {
		fmt.Fprintf(cmd.OutOrStdout(), "No %s migration files for configured databases at or below %s.\n", phase, target)
		return nil
	}

	// Yugabyte-over-TCP requires a password; vanilla Postgres uses peer auth
	// and does not.
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

	sshKey := stringFlag(cmd, "ssh-key").Value
	sshPool := ssh.NewPool(30*time.Second, sshKey)
	defer sshPool.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

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
			"postgres_password": password,
			itemsKey:            items,
		},
	}

	if phase == "postdeploy" || phase == "contract" {
		if err := runPhaseDataMigrationGate(ctx, cmd, rc, sshPool, phase, target, skipDataMigrationCheck); err != nil {
			return err
		}
	}

	if dryRun {
		fmt.Fprintf(cmd.OutOrStdout(), "Running %s migrations in --check --diff mode (no writes)\n", phase)
	} else {
		fmt.Fprintf(cmd.OutOrStdout(), "Applying pending %s migrations\n", phase)
	}

	if err := migrator.ApplyMigrations(ctx, pgHost, cfg, dryRun); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}

	if dryRun {
		fmt.Fprintln(cmd.OutOrStdout(), "Dry-run complete; ansible check output above shows pending changes")
		return nil
	}
	ux.Success(cmd.OutOrStdout(), "Migrations applied")
	ux.PrintNextSteps(cmd.OutOrStdout(), []ux.NextStep{
		{Cmd: "frameworks cluster status", Why: "Verify deployed services are healthy after schema changes."},
		{Cmd: "frameworks cluster upgrade --all --dry-run", Why: "Preview service rollout after expand-compatible migrations."},
		{Why: "Review release notes for required data migrations or verification before flipping reads or applying contract migrations."},
	})
	return nil
}

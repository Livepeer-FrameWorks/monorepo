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

	cmd := &cobra.Command{
		Use:   "migrate",
		Short: "Apply pending database migrations",
		Long: `Apply versioned SQL migrations from pkg/database/sql/migrations/.

Migrations are tracked in a _migrations table per database.
Each migration runs inside a transaction and is recorded on success.
Safe to run multiple times — already-applied migrations are skipped.

The underlying Ansible role filters applied vs pending on the target; in
--dry-run mode the role runs under ansible-playbook --check --diff so
nothing is actually written.`,
		Example: `  frameworks cluster migrate --dry-run
  frameworks cluster migrate`,
		RunE: func(cmd *cobra.Command, args []string) error {
			rc, err := resolveClusterManifest(cmd)
			if err != nil {
				return err
			}
			defer rc.Cleanup()
			return runMigrate(cmd, rc, dryRun)
		},
	}

	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Report pending migrations via ansible --check --diff without applying")

	return cmd
}

func runMigrate(cmd *cobra.Command, rc *resolvedCluster, dryRun bool) error {
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

	items, err := provisioner.BuildMigrationItems(dbNames)
	if err != nil {
		return fmt.Errorf("collect migrations: %w", err)
	}
	if len(items) == 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "No migration files found under pkg/database/sql/migrations.")
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

	if dryRun {
		fmt.Fprintln(cmd.OutOrStdout(), "Running migrate in --check --diff mode (no writes)")
	} else {
		fmt.Fprintln(cmd.OutOrStdout(), "Applying pending migrations")
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
	})
	return nil
}

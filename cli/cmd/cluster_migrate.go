package cmd

import (
	"context"
	"fmt"
	"time"

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
Safe to run multiple times — already-applied migrations are skipped.`,
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

	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "List pending migrations without applying")

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

	port := pg.EffectivePort()
	dbUser := "postgres"
	if pg.IsYugabyte() {
		dbUser = "yugabyte"
	}

	sshPool := ssh.NewPool(30 * time.Second)
	defer sshPool.Close()

	// Only decrypt manifest env_files when Yugabyte actually needs a password.
	// Vanilla Postgres uses peer auth and needs no secret.
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
	exec, err := newSQLExecutor(pg.SQLAccess, pgHost, sshPool, pg.IsYugabyte(), password)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	databases := pg.Databases
	if len(databases) == 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "No databases configured in manifest.")
		return nil
	}

	totalApplied := 0
	for _, db := range databases {
		conn := provisioner.ConnParams{Host: pgHost.ExternalIP, Port: port, User: dbUser, Database: db.Name}

		results, err := provisioner.RunPostgresMigrations(ctx, exec, conn, dryRun)
		if err != nil {
			return fmt.Errorf("migrate %s: %w", db.Name, err)
		}

		if len(results) == 0 {
			fmt.Fprintf(cmd.OutOrStdout(), "  %s: up to date\n", db.Name)
			continue
		}

		verb := "Applied"
		if dryRun {
			verb = "Pending"
		}
		fmt.Fprintf(cmd.OutOrStdout(), "  %s: %s %d migration(s)\n", db.Name, verb, len(results))
		for _, r := range results {
			fmt.Fprintf(cmd.OutOrStdout(), "    %s %s/%s\n", verb, r.Version, r.Filename)
		}
		totalApplied += len(results)
	}

	if dryRun {
		fmt.Fprintf(cmd.OutOrStdout(), "\n%d pending migration(s) across %d database(s)\n", totalApplied, len(databases))
	} else {
		fmt.Fprintf(cmd.OutOrStdout(), "\n✓ Applied %d migration(s)\n", totalApplied)
	}

	return nil
}

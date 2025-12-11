package cmd

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/ssh"

	"github.com/spf13/cobra"
)

// newClusterRestoreCmd creates the restore command
func newClusterRestoreCmd() *cobra.Command {
	var manifestPath string
	var backupPath string
	var skipValidation bool
	var yes bool

	cmd := &cobra.Command{
		Use:   "restore <component>",
		Short: "Restore cluster components from backup",
		Long: `Restore cluster components from backup files.

Supported components:
  postgres    - Restore PostgreSQL databases from SQL dump
  clickhouse  - Restore ClickHouse databases from TSV files
  volumes     - Restore Docker volumes from tar.gz archive
  config      - Restore configuration files from tar.gz archive

WARNING: Restore operations will STOP services and OVERWRITE existing data!
Always backup current state before restoring.`,
		Example: `  # Restore Postgres from backup
  frameworks cluster restore postgres --from /backups/postgres-20250117-143000.sql

  # Restore ClickHouse
  frameworks cluster restore clickhouse --from /backups/clickhouse-20250117-143000/

  # Restore volumes
  frameworks cluster restore volumes --from /backups/volumes-20250117-143000.tar.gz

  # Skip confirmation prompt
  frameworks cluster restore postgres --from backup.sql --yes`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runRestore(cmd, manifestPath, args[0], backupPath, skipValidation, yes)
		},
	}

	cmd.Flags().StringVar(&manifestPath, "manifest", "cluster.yaml", "Path to cluster manifest file")
	cmd.Flags().StringVar(&backupPath, "from", "", "Path to backup file or directory (required)")
	cmd.Flags().BoolVar(&skipValidation, "skip-validation", false, "Skip health validation after restore")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "Skip confirmation prompt (DANGEROUS)")

	cmd.MarkFlagRequired("from")

	return cmd
}

// runRestore executes the restore command
func runRestore(cmd *cobra.Command, manifestPath, component, backupPath string, skipValidation, yes bool) error {
	// Load manifest
	manifest, err := inventory.Load(manifestPath)
	if err != nil {
		return fmt.Errorf("failed to load manifest: %w", err)
	}

	fmt.Fprintf(cmd.OutOrStdout(), "⚠  WARNING: Restore will STOP services and OVERWRITE data!\n")
	fmt.Fprintf(cmd.OutOrStdout(), "Component: %s\n", component)
	fmt.Fprintf(cmd.OutOrStdout(), "Backup: %s\n\n", backupPath)

	// Require confirmation for destructive operation
	if !yes {
		fmt.Fprintf(os.Stderr, "Are you sure you want to restore %s from %s? [y/N]: ", component, backupPath)
		reader := bufio.NewReader(os.Stdin)
		response, err := reader.ReadString('\n')
		if err != nil {
			return fmt.Errorf("failed to read confirmation: %w", err)
		}
		response = strings.TrimSpace(strings.ToLower(response))
		if response != "y" && response != "yes" {
			fmt.Fprintln(cmd.OutOrStdout(), "Cancelled")
			return nil
		}
	}

	fmt.Fprintln(cmd.OutOrStdout(), "Starting restore...")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	// Create SSH pool
	sshPool := ssh.NewPool(30 * time.Second)
	defer sshPool.Close()

	// Execute restore based on component
	switch component {
	case "postgres":
		return restorePostgres(ctx, cmd, manifest, backupPath, skipValidation, sshPool)
	case "clickhouse":
		return restoreClickHouse(ctx, cmd, manifest, backupPath, skipValidation, sshPool)
	case "volumes":
		return restoreVolumes(ctx, cmd, manifest, backupPath, skipValidation, sshPool)
	case "config":
		return restoreConfig(ctx, cmd, manifest, backupPath, sshPool)
	default:
		return fmt.Errorf("unknown component: %s (must be postgres, clickhouse, volumes, or config)", component)
	}
}

// restorePostgres restores PostgreSQL from backup
func restorePostgres(ctx context.Context, cmd *cobra.Command, manifest *inventory.Manifest, backupPath string, skipValidation bool, pool *ssh.Pool) error {
	if !manifest.Infrastructure.Postgres.Enabled {
		return fmt.Errorf("postgres not enabled in manifest")
	}

	host, found := manifest.GetHost(manifest.Infrastructure.Postgres.Host)
	if !found {
		return fmt.Errorf("postgres host not found: %s", manifest.Infrastructure.Postgres.Host)
	}

	fmt.Fprintf(cmd.OutOrStdout(), "[1/4] Stopping Postgres...\n")

	// Get runner
	runner, err := getRunner(host, pool)
	if err != nil {
		return err
	}

	// Stop Postgres
	stopCmd := "cd /opt/frameworks/postgres && docker compose stop"
	if result, err := runner.Run(ctx, stopCmd); err != nil || result.ExitCode != 0 {
		return fmt.Errorf("failed to stop postgres: %v", err)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "  ✓ Postgres stopped\n")

	fmt.Fprintf(cmd.OutOrStdout(), "\n[2/4] Restoring from backup...\n")

	// Restore from backup
	restoreCmd := fmt.Sprintf(`
cd /opt/frameworks/postgres
docker compose up -d
sleep 5
cat %s | docker compose exec -T postgres psql -U postgres
`, backupPath)

	result, err := runner.Run(ctx, restoreCmd)
	if err != nil {
		return fmt.Errorf("restore failed: %w", err)
	}

	if result.ExitCode != 0 {
		return fmt.Errorf("restore failed: %s", result.Stderr)
	}

	fmt.Fprintf(cmd.OutOrStdout(), "  ✓ Data restored\n")

	fmt.Fprintf(cmd.OutOrStdout(), "\n[3/4] Starting Postgres...\n")
	// Already started above
	fmt.Fprintf(cmd.OutOrStdout(), "  ✓ Postgres started\n")

	if !skipValidation {
		fmt.Fprintf(cmd.OutOrStdout(), "\n[4/4] Validating...\n")
		// Simple connection test
		validateCmd := "docker compose -f /opt/frameworks/postgres/docker-compose.yml exec -T postgres psql -U postgres -c 'SELECT 1'"
		if result, err := runner.Run(ctx, validateCmd); err != nil || result.ExitCode != 0 {
			fmt.Fprintf(cmd.OutOrStderr(), "  ⚠ Validation failed: %v\n", err)
		} else {
			fmt.Fprintf(cmd.OutOrStdout(), "  ✓ Postgres is healthy\n")
		}
	}

	fmt.Fprintf(cmd.OutOrStdout(), "\n✓ Postgres restore complete!\n")
	return nil
}

// restoreClickHouse restores ClickHouse from backup
func restoreClickHouse(ctx context.Context, cmd *cobra.Command, manifest *inventory.Manifest, backupPath string, skipValidation bool, pool *ssh.Pool) error {
	if !manifest.Infrastructure.ClickHouse.Enabled {
		return fmt.Errorf("clickhouse not enabled in manifest")
	}

	host, found := manifest.GetHost(manifest.Infrastructure.ClickHouse.Host)
	if !found {
		return fmt.Errorf("clickhouse host not found: %s", manifest.Infrastructure.ClickHouse.Host)
	}

	fmt.Fprintf(cmd.OutOrStdout(), "[1/4] Stopping ClickHouse...\n")

	// Get runner
	runner, err := getRunner(host, pool)
	if err != nil {
		return err
	}

	// Stop ClickHouse
	stopCmd := "cd /opt/frameworks/clickhouse && docker compose stop"
	if result, err := runner.Run(ctx, stopCmd); err != nil || result.ExitCode != 0 {
		return fmt.Errorf("failed to stop clickhouse: %v", err)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "  ✓ ClickHouse stopped\n")

	fmt.Fprintf(cmd.OutOrStdout(), "\n[2/4] Restoring from backup...\n")

	// Restore from TSV files
	restoreCmd := fmt.Sprintf(`
cd /opt/frameworks/clickhouse
docker compose up -d
sleep 5
# Restore each database and table from TSV files
for db in %s/*; do
  dbname=$(basename $db)
  docker compose exec -T clickhouse-server clickhouse-client --query="CREATE DATABASE IF NOT EXISTS $dbname"
  for table in $db/*.tsv; do
    tablename=$(basename $table .tsv)
    docker compose exec -T clickhouse-server clickhouse-client --database=$dbname --query="INSERT INTO $tablename FORMAT TSV" < $table
  done
done
`, backupPath)

	result, err := runner.Run(ctx, restoreCmd)
	if err != nil {
		return fmt.Errorf("restore failed: %w", err)
	}

	if result.ExitCode != 0 {
		return fmt.Errorf("restore failed: %s", result.Stderr)
	}

	fmt.Fprintf(cmd.OutOrStdout(), "  ✓ Data restored\n")

	fmt.Fprintf(cmd.OutOrStdout(), "\n[3/4] Starting ClickHouse...\n")
	// Already started above
	fmt.Fprintf(cmd.OutOrStdout(), "  ✓ ClickHouse started\n")

	if !skipValidation {
		fmt.Fprintf(cmd.OutOrStdout(), "\n[4/4] Validating...\n")
		validateCmd := "docker compose -f /opt/frameworks/clickhouse/docker-compose.yml exec -T clickhouse-server clickhouse-client --query='SELECT 1'"
		if result, err := runner.Run(ctx, validateCmd); err != nil || result.ExitCode != 0 {
			fmt.Fprintf(cmd.OutOrStderr(), "  ⚠ Validation failed: %v\n", err)
		} else {
			fmt.Fprintf(cmd.OutOrStdout(), "  ✓ ClickHouse is healthy\n")
		}
	}

	fmt.Fprintf(cmd.OutOrStdout(), "\n✓ ClickHouse restore complete!\n")
	return nil
}

// restoreVolumes restores Docker volumes from tar.gz
func restoreVolumes(ctx context.Context, cmd *cobra.Command, manifest *inventory.Manifest, backupPath string, skipValidation bool, pool *ssh.Pool) error {
	// Restore to first host
	var host inventory.Host
	for _, h := range manifest.Hosts {
		host = h
		break
	}

	fmt.Fprintf(cmd.OutOrStdout(), "[1/3] Stopping all services...\n")

	// Get runner
	runner, err := getRunner(host, pool)
	if err != nil {
		return err
	}

	// Stop all services
	stopCmd := "cd /opt/frameworks && for dir in */; do (cd $dir && docker compose down 2>/dev/null || true); done"
	if result, err := runner.Run(ctx, stopCmd); err != nil || result.ExitCode != 0 {
		fmt.Fprintf(cmd.OutOrStderr(), "  ⚠ Some services may not have stopped: %v\n", err)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "  ✓ Services stopped\n")

	fmt.Fprintf(cmd.OutOrStdout(), "\n[2/3] Restoring volumes...\n")

	// Restore volumes
	restoreCmd := fmt.Sprintf(`
docker run --rm -v /var/lib/docker/volumes:/volumes -v $(dirname %s):/backup alpine tar xzf /backup/$(basename %s) -C /volumes
`, backupPath, backupPath)

	result, err := runner.Run(ctx, restoreCmd)
	if err != nil {
		return fmt.Errorf("restore failed: %w", err)
	}

	if result.ExitCode != 0 {
		return fmt.Errorf("restore failed: %s", result.Stderr)
	}

	fmt.Fprintf(cmd.OutOrStdout(), "  ✓ Volumes restored\n")

	fmt.Fprintf(cmd.OutOrStdout(), "\n[3/3] Starting services...\n")
	// User should manually start services they need
	fmt.Fprintf(cmd.OutOrStdout(), "  ℹ  Use 'frameworks cluster provision' to start services\n")

	fmt.Fprintf(cmd.OutOrStdout(), "\n✓ Volume restore complete!\n")
	return nil
}

// restoreConfig restores configuration files
func restoreConfig(ctx context.Context, cmd *cobra.Command, manifest *inventory.Manifest, backupPath string, pool *ssh.Pool) error {
	// Restore to first host
	var host inventory.Host
	for _, h := range manifest.Hosts {
		host = h
		break
	}

	fmt.Fprintf(cmd.OutOrStdout(), "Restoring config files on %s...\n", host.Address)

	// Get runner
	runner, err := getRunner(host, pool)
	if err != nil {
		return err
	}

	// Restore config files
	restoreCmd := fmt.Sprintf("tar xzf %s -C /", backupPath)

	result, err := runner.Run(ctx, restoreCmd)
	if err != nil {
		return fmt.Errorf("restore failed: %w", err)
	}

	if result.ExitCode != 0 {
		return fmt.Errorf("restore failed: %s", result.Stderr)
	}

	fmt.Fprintf(cmd.OutOrStdout(), "✓ Config restore complete!\n")
	fmt.Fprintln(cmd.OutOrStdout(), "ℹ  Restart services to apply configuration changes")
	return nil
}

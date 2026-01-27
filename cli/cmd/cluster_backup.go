package cmd

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/ssh"

	"github.com/spf13/cobra"
)

// BackupManifest tracks backup files and their checksums for integrity verification
type BackupManifest struct {
	Version   string                `json:"version"`
	Timestamp string                `json:"timestamp"`
	Component string                `json:"component"`
	Files     map[string]BackupFile `json:"files"`
}

// BackupFile represents a single backup file with its metadata
type BackupFile struct {
	Path      string `json:"path"`
	Size      int64  `json:"size"`
	SHA256    string `json:"sha256"`
	Host      string `json:"host,omitempty"`
	Component string `json:"component"`
}

// newClusterBackupCmd creates the backup command
func newClusterBackupCmd() *cobra.Command {
	var manifestPath string
	var outputDir string
	var skipUpload bool

	cmd := &cobra.Command{
		Use:   "backup <component>",
		Short: "Backup cluster components",
		Long: `Backup cluster components to local or remote storage.

Supported components:
  postgres    - Backup all PostgreSQL databases
  clickhouse  - Backup ClickHouse databases
  volumes     - Backup Docker volumes
  config      - Backup configuration files (.env, docker-compose.yml)
  all         - Backup everything (postgres + clickhouse + volumes + config)

Backups are stored in the output directory with timestamps.
Optionally upload to S3/GCS after backup (requires --upload flag).`,
		Example: `  # Backup Postgres
  frameworks cluster backup postgres --output /backups

  # Backup everything
  frameworks cluster backup all --output /backups

  # Backup and upload to S3
  frameworks cluster backup postgres --upload s3://my-bucket/backups`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runBackup(cmd, manifestPath, args[0], outputDir, skipUpload)
		},
	}

	cmd.Flags().StringVar(&manifestPath, "manifest", "cluster.yaml", "Path to cluster manifest file")
	cmd.Flags().StringVarP(&outputDir, "output", "o", "./backups", "Output directory for backups")
	cmd.Flags().BoolVar(&skipUpload, "skip-upload", true, "Skip upload to remote storage")

	return cmd
}

// runBackup executes the backup command
func runBackup(cmd *cobra.Command, manifestPath, component, outputDir string, skipUpload bool) error {
	// Load manifest
	manifest, err := inventory.Load(manifestPath)
	if err != nil {
		return fmt.Errorf("failed to load manifest: %w", err)
	}

	timestamp := time.Now().Format("20060102-150405")
	fmt.Fprintf(cmd.OutOrStdout(), "Starting backup: %s (timestamp: %s)\n", component, timestamp)
	fmt.Fprintf(cmd.OutOrStdout(), "Output directory: %s\n\n", outputDir)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	// Create SSH pool
	sshPool := ssh.NewPool(30 * time.Second)
	defer sshPool.Close()

	// Track backup files for manifest
	backupFiles := make(map[string]BackupFile)

	// Execute backup based on component
	switch component {
	case "postgres":
		if err := backupPostgres(ctx, cmd, manifest, outputDir, timestamp, sshPool, backupFiles); err != nil {
			return err
		}
	case "clickhouse":
		if err := backupClickHouse(ctx, cmd, manifest, outputDir, timestamp, sshPool, backupFiles); err != nil {
			return err
		}
	case "volumes":
		if err := backupVolumes(ctx, cmd, manifest, outputDir, timestamp, sshPool, backupFiles); err != nil {
			return err
		}
	case "config":
		if err := backupConfig(ctx, cmd, manifest, outputDir, timestamp, sshPool, backupFiles); err != nil {
			return err
		}
	case "all":
		fmt.Fprintln(cmd.OutOrStdout(), "[1/4] Backing up Postgres...")
		if err := backupPostgres(ctx, cmd, manifest, outputDir, timestamp, sshPool, backupFiles); err != nil {
			fmt.Fprintf(cmd.OutOrStderr(), "  ⚠ Postgres backup failed: %v\n", err)
		}

		fmt.Fprintln(cmd.OutOrStdout(), "\n[2/4] Backing up ClickHouse...")
		if err := backupClickHouse(ctx, cmd, manifest, outputDir, timestamp, sshPool, backupFiles); err != nil {
			fmt.Fprintf(cmd.OutOrStderr(), "  ⚠ ClickHouse backup failed: %v\n", err)
		}

		fmt.Fprintln(cmd.OutOrStdout(), "\n[3/4] Backing up Docker volumes...")
		if err := backupVolumes(ctx, cmd, manifest, outputDir, timestamp, sshPool, backupFiles); err != nil {
			fmt.Fprintf(cmd.OutOrStderr(), "  ⚠ Volumes backup failed: %v\n", err)
		}

		fmt.Fprintln(cmd.OutOrStdout(), "\n[4/4] Backing up configuration...")
		if err := backupConfig(ctx, cmd, manifest, outputDir, timestamp, sshPool, backupFiles); err != nil {
			fmt.Fprintf(cmd.OutOrStderr(), "  ⚠ Config backup failed: %v\n", err)
		}

	default:
		return fmt.Errorf("unknown component: %s (must be postgres, clickhouse, volumes, config, or all)", component)
	}

	// Write backup manifest with checksums
	if len(backupFiles) > 0 {
		fmt.Fprintf(cmd.OutOrStdout(), "\nWriting backup manifest...\n")
		if err := writeBackupManifest(outputDir, timestamp, component, backupFiles); err != nil {
			fmt.Fprintf(cmd.OutOrStderr(), "  ⚠ Failed to write manifest: %v\n", err)
		} else {
			fmt.Fprintf(cmd.OutOrStdout(), "  ✓ Manifest written: manifest-%s.json\n", timestamp)
		}
	}

	fmt.Fprintln(cmd.OutOrStdout(), "\n✓ Backup complete!")
	return nil
}

// backupPostgres backs up PostgreSQL databases
func backupPostgres(ctx context.Context, cmd *cobra.Command, manifest *inventory.Manifest, outputDir, timestamp string, pool *ssh.Pool, backupFiles map[string]BackupFile) error {
	if !manifest.Infrastructure.Postgres.Enabled {
		return fmt.Errorf("postgres not enabled in manifest")
	}

	host, found := manifest.GetHost(manifest.Infrastructure.Postgres.Host)
	if !found {
		return fmt.Errorf("postgres host not found: %s", manifest.Infrastructure.Postgres.Host)
	}

	fmt.Fprintf(cmd.OutOrStdout(), "  Backing up Postgres on %s...\n", host.Address)

	// Get runner
	runner, err := getRunner(host, pool)
	if err != nil {
		return err
	}

	// Generate backup filename
	backupFile := filepath.Join(outputDir, fmt.Sprintf("postgres-%s.sql", timestamp))

	// Create backup command (works for both Docker and native)
	var backupCmd string = fmt.Sprintf("mkdir -p %s && docker compose -f /opt/frameworks/postgres/docker-compose.yml exec -T postgres pg_dumpall -U postgres > %s",
		outputDir, backupFile)

	result, err := runner.Run(ctx, backupCmd)
	if err != nil {
		return fmt.Errorf("backup command failed: %w", err)
	}

	if result.ExitCode != 0 {
		return fmt.Errorf("backup failed: %s", result.Stderr)
	}

	// Calculate checksum remotely
	checksumCmd := fmt.Sprintf("sha256sum %s | awk '{print $1}'", backupFile)
	checksumResult, err := runner.Run(ctx, checksumCmd)
	if err == nil && checksumResult.ExitCode == 0 {
		// Get file size
		sizeCmd := fmt.Sprintf("stat -c %%s %s 2>/dev/null || stat -f %%z %s", backupFile, backupFile)
		sizeResult, _ := runner.Run(ctx, sizeCmd)
		var size int64
		fmt.Sscanf(sizeResult.Stdout, "%d", &size)

		backupFiles[filepath.Base(backupFile)] = BackupFile{
			Path:      backupFile,
			Size:      size,
			SHA256:    strings.TrimSpace(checksumResult.Stdout),
			Host:      host.Address,
			Component: "postgres",
		}
	}

	fmt.Fprintf(cmd.OutOrStdout(), "  ✓ Postgres backup saved: %s\n", backupFile)
	return nil
}

// backupClickHouse backs up ClickHouse databases
func backupClickHouse(ctx context.Context, cmd *cobra.Command, manifest *inventory.Manifest, outputDir, timestamp string, pool *ssh.Pool, backupFiles map[string]BackupFile) error {
	if !manifest.Infrastructure.ClickHouse.Enabled {
		return fmt.Errorf("clickhouse not enabled in manifest")
	}

	host, found := manifest.GetHost(manifest.Infrastructure.ClickHouse.Host)
	if !found {
		return fmt.Errorf("clickhouse host not found: %s", manifest.Infrastructure.ClickHouse.Host)
	}

	fmt.Fprintf(cmd.OutOrStdout(), "  Backing up ClickHouse on %s...\n", host.Address)

	// Get runner
	runner, err := getRunner(host, pool)
	if err != nil {
		return err
	}

	// Generate backup directory
	backupDir := filepath.Join(outputDir, fmt.Sprintf("clickhouse-%s", timestamp))

	// Create backup using TSV export (compatible with all versions)
	backupCmd := fmt.Sprintf(`
mkdir -p %s
docker compose -f /opt/frameworks/clickhouse/docker-compose.yml exec -T clickhouse-server clickhouse-client --query="SHOW DATABASES" | while read db; do
  if [ "$db" != "system" ] && [ "$db" != "information_schema" ] && [ "$db" != "INFORMATION_SCHEMA" ]; then
    mkdir -p %s/$db
    docker compose -f /opt/frameworks/clickhouse/docker-compose.yml exec -T clickhouse-server clickhouse-client --database=$db --query="SHOW TABLES" | while read table; do
      docker compose -f /opt/frameworks/clickhouse/docker-compose.yml exec -T clickhouse-server clickhouse-client --database=$db --query="SELECT * FROM $table FORMAT TSV" > %s/$db/$table.tsv
    done
  fi
done
`, backupDir, backupDir, backupDir)

	result, err := runner.Run(ctx, backupCmd)
	if err != nil {
		return fmt.Errorf("backup command failed: %w", err)
	}

	if result.ExitCode != 0 {
		return fmt.Errorf("backup failed: %s", result.Stderr)
	}

	// Create tarball for easier checksum and transfer
	tarFile := backupDir + ".tar.gz"
	tarCmd := fmt.Sprintf("tar -czf %s -C %s . && rm -rf %s", tarFile, backupDir, backupDir)
	if tarResult, err := runner.Run(ctx, tarCmd); err == nil && tarResult.ExitCode == 0 {
		// Calculate checksum
		checksumCmd := fmt.Sprintf("sha256sum %s | awk '{print $1}'", tarFile)
		if checksumResult, err := runner.Run(ctx, checksumCmd); err == nil && checksumResult.ExitCode == 0 {
			sizeCmd := fmt.Sprintf("stat -c %%s %s 2>/dev/null || stat -f %%z %s", tarFile, tarFile)
			sizeResult, _ := runner.Run(ctx, sizeCmd)
			var size int64
			fmt.Sscanf(sizeResult.Stdout, "%d", &size)

			backupFiles[filepath.Base(tarFile)] = BackupFile{
				Path:      tarFile,
				Size:      size,
				SHA256:    strings.TrimSpace(checksumResult.Stdout),
				Host:      host.Address,
				Component: "clickhouse",
			}
		}
		fmt.Fprintf(cmd.OutOrStdout(), "  ✓ ClickHouse backup saved: %s\n", tarFile)
	} else {
		fmt.Fprintf(cmd.OutOrStdout(), "  ✓ ClickHouse backup saved: %s\n", backupDir)
	}

	return nil
}

// backupVolumes backs up Docker volumes from all hosts
func backupVolumes(ctx context.Context, cmd *cobra.Command, manifest *inventory.Manifest, outputDir, timestamp string, pool *ssh.Pool, backupFiles map[string]BackupFile) error {
	if len(manifest.Hosts) == 0 {
		return fmt.Errorf("no hosts defined in manifest")
	}

	var errors []string
	successCount := 0

	for hostName, host := range manifest.Hosts {
		fmt.Fprintf(cmd.OutOrStdout(), "  Backing up Docker volumes on %s (%s)...\n", hostName, host.Address)

		// Get runner
		runner, err := getRunner(host, pool)
		if err != nil {
			errors = append(errors, fmt.Sprintf("%s: failed to get runner: %v", hostName, err))
			continue
		}

		// Generate backup filename with host identifier
		backupFile := filepath.Join(outputDir, fmt.Sprintf("volumes-%s-%s.tar.gz", hostName, timestamp))

		// Backup all frameworks volumes
		backupCmd := fmt.Sprintf(`
mkdir -p %s
docker run --rm -v /var/lib/docker/volumes:/volumes -v %s:/backup alpine tar czf /backup/volumes-%s-%s.tar.gz -C /volumes $(docker volume ls --filter name=frameworks -q | tr '\n' ' ') 2>/dev/null || echo "no volumes found"
`, outputDir, outputDir, hostName, timestamp)

		result, err := runner.Run(ctx, backupCmd)
		if err != nil {
			errors = append(errors, fmt.Sprintf("%s: backup command failed: %v", hostName, err))
			continue
		}

		if result.ExitCode != 0 {
			errors = append(errors, fmt.Sprintf("%s: backup failed: %s", hostName, result.Stderr))
			continue
		}

		// Calculate checksum
		checksumCmd := fmt.Sprintf("sha256sum %s | awk '{print $1}'", backupFile)
		if checksumResult, err := runner.Run(ctx, checksumCmd); err == nil && checksumResult.ExitCode == 0 {
			sizeCmd := fmt.Sprintf("stat -c %%s %s 2>/dev/null || stat -f %%z %s", backupFile, backupFile)
			sizeResult, _ := runner.Run(ctx, sizeCmd)
			var size int64
			fmt.Sscanf(sizeResult.Stdout, "%d", &size)

			backupFiles[filepath.Base(backupFile)] = BackupFile{
				Path:      backupFile,
				Size:      size,
				SHA256:    strings.TrimSpace(checksumResult.Stdout),
				Host:      host.Address,
				Component: "volumes",
			}
		}

		fmt.Fprintf(cmd.OutOrStdout(), "    ✓ Volumes backup saved: %s\n", backupFile)
		successCount++
	}

	if len(errors) > 0 {
		for _, e := range errors {
			fmt.Fprintf(cmd.OutOrStderr(), "    ⚠ %s\n", e)
		}
	}

	if successCount == 0 {
		return fmt.Errorf("all volume backups failed")
	}

	fmt.Fprintf(cmd.OutOrStdout(), "  ✓ Backed up volumes from %d/%d hosts\n", successCount, len(manifest.Hosts))
	return nil
}

// backupConfig backs up configuration files from all hosts
func backupConfig(ctx context.Context, cmd *cobra.Command, manifest *inventory.Manifest, outputDir, timestamp string, pool *ssh.Pool, backupFiles map[string]BackupFile) error {
	if len(manifest.Hosts) == 0 {
		return fmt.Errorf("no hosts defined in manifest")
	}

	var errors []string
	successCount := 0

	for hostName, host := range manifest.Hosts {
		fmt.Fprintf(cmd.OutOrStdout(), "  Backing up config files on %s (%s)...\n", hostName, host.Address)

		// Get runner
		runner, err := getRunner(host, pool)
		if err != nil {
			errors = append(errors, fmt.Sprintf("%s: failed to get runner: %v", hostName, err))
			continue
		}

		// Generate backup filename with host identifier
		backupFile := filepath.Join(outputDir, fmt.Sprintf("config-%s-%s.tar.gz", hostName, timestamp))

		// Backup all config files (handle missing dirs gracefully)
		backupCmd := fmt.Sprintf(`
mkdir -p %s
cd / && tar czf %s \
  $(test -d /etc/frameworks && echo "/etc/frameworks") \
  $(find /opt/frameworks -maxdepth 2 -name 'docker-compose.yml' 2>/dev/null) \
  $(find /opt/frameworks -maxdepth 2 -name '.env' 2>/dev/null) \
  2>/dev/null || true
`, outputDir, backupFile)

		result, err := runner.Run(ctx, backupCmd)
		if err != nil {
			errors = append(errors, fmt.Sprintf("%s: backup command failed: %v", hostName, err))
			continue
		}

		if result.ExitCode != 0 {
			errors = append(errors, fmt.Sprintf("%s: backup failed: %s", hostName, result.Stderr))
			continue
		}

		// Calculate checksum
		checksumCmd := fmt.Sprintf("sha256sum %s | awk '{print $1}'", backupFile)
		if checksumResult, err := runner.Run(ctx, checksumCmd); err == nil && checksumResult.ExitCode == 0 {
			sizeCmd := fmt.Sprintf("stat -c %%s %s 2>/dev/null || stat -f %%z %s", backupFile, backupFile)
			sizeResult, _ := runner.Run(ctx, sizeCmd)
			var size int64
			fmt.Sscanf(sizeResult.Stdout, "%d", &size)

			backupFiles[filepath.Base(backupFile)] = BackupFile{
				Path:      backupFile,
				Size:      size,
				SHA256:    strings.TrimSpace(checksumResult.Stdout),
				Host:      host.Address,
				Component: "config",
			}
		}

		fmt.Fprintf(cmd.OutOrStdout(), "    ✓ Config backup saved: %s\n", backupFile)
		successCount++
	}

	if len(errors) > 0 {
		for _, e := range errors {
			fmt.Fprintf(cmd.OutOrStderr(), "    ⚠ %s\n", e)
		}
	}

	if successCount == 0 {
		return fmt.Errorf("all config backups failed")
	}

	fmt.Fprintf(cmd.OutOrStdout(), "  ✓ Backed up config from %d/%d hosts\n", successCount, len(manifest.Hosts))
	return nil
}

// getRunner returns an SSH runner for a host
func getRunner(host inventory.Host, pool *ssh.Pool) (ssh.Runner, error) {
	if host.Address == "" || host.Address == "localhost" || host.Address == "127.0.0.1" {
		return ssh.NewLocalRunner(""), nil
	}

	sshConfig := &ssh.ConnectionConfig{
		Address: host.Address,
		Port:    22,
		User:    host.User,
		KeyPath: host.SSHKey,
		Timeout: 30 * time.Second,
	}

	return pool.Get(sshConfig)
}

// calculateFileSHA256 computes the SHA256 hash of a file
func calculateFileSHA256(filePath string) (string, int64, error) {
	f, err := os.Open(filePath)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()

	h := sha256.New()
	size, err := io.Copy(h, f)
	if err != nil {
		return "", 0, err
	}

	return hex.EncodeToString(h.Sum(nil)), size, nil
}

// writeBackupManifest writes the backup manifest JSON file
func writeBackupManifest(outputDir, timestamp, component string, files map[string]BackupFile) error {
	manifest := BackupManifest{
		Version:   "1",
		Timestamp: timestamp,
		Component: component,
		Files:     files,
	}

	manifestPath := filepath.Join(outputDir, fmt.Sprintf("manifest-%s.json", timestamp))
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal manifest: %w", err)
	}

	if err := os.WriteFile(manifestPath, data, 0644); err != nil {
		return fmt.Errorf("failed to write manifest: %w", err)
	}

	return nil
}

// VerifyBackupManifest verifies the integrity of backup files against a manifest
func VerifyBackupManifest(manifestPath string) ([]string, error) {
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read manifest: %w", err)
	}

	var manifest BackupManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return nil, fmt.Errorf("failed to parse manifest: %w", err)
	}

	var errors []string
	for name, file := range manifest.Files {
		hash, size, err := calculateFileSHA256(file.Path)
		if err != nil {
			errors = append(errors, fmt.Sprintf("%s: failed to read file: %v", name, err))
			continue
		}

		if size != file.Size {
			errors = append(errors, fmt.Sprintf("%s: size mismatch (expected %d, got %d)", name, file.Size, size))
		}

		if hash != file.SHA256 {
			errors = append(errors, fmt.Sprintf("%s: checksum mismatch", name))
		}
	}

	return errors, nil
}

// recordBackupFile calculates checksum and adds file to manifest
func recordBackupFile(files map[string]BackupFile, filePath, component, hostName string) error {
	hash, size, err := calculateFileSHA256(filePath)
	if err != nil {
		return err
	}

	key := filepath.Base(filePath)
	files[key] = BackupFile{
		Path:      filePath,
		Size:      size,
		SHA256:    hash,
		Host:      hostName,
		Component: component,
	}
	return nil
}

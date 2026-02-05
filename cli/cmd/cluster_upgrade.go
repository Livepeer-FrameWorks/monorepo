package cmd

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"frameworks/cli/pkg/detect"
	"frameworks/cli/pkg/gitops"
	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/provisioner"
	"frameworks/cli/pkg/ssh"

	"github.com/spf13/cobra"
)

// newClusterUpgradeCmd creates the upgrade command
func newClusterUpgradeCmd() *cobra.Command {
	var manifestPath string
	var version string
	var dryRun bool
	var skipValidation bool
	var yes bool
	var noRollback bool

	cmd := &cobra.Command{
		Use:   "upgrade <service>",
		Short: "Upgrade a service to a new version",
		Long: `Upgrade a service to a new version using GitOps manifests.

The upgrade process:
  1. Detect current version and mode (Docker or native)
  2. Fetch new version from GitOps manifest
  3. Stop the service
  4. Pull new image (Docker) or download new binary (native)
  5. Start the service with new version
  6. Validate health (unless --skip-validation)
  7. On health failure, rollback to previous version (unless --no-rollback)

Upgrade is currently all-at-once. Future support for rolling upgrades
when replica counts are added to cluster manifests.

Version can be:
  - Specific version: v0.0.0-rc1, v1.2.3
  - Channel: stable, rc (uses latest from channel)
  - Default: stable (if not specified)

Environment variables:
  FRAMEWORKS_GITOPS_REPO - Override GitOps repository URL or local path`,
		Example: `  # Upgrade quartermaster to latest stable
  frameworks cluster upgrade quartermaster

  # Upgrade to specific version
  frameworks cluster upgrade commodore --version v0.0.0-rc2

  # Dry run to see what would happen
  frameworks cluster upgrade bridge --version rc --dry-run

  # Upgrade without health validation (faster but riskier)
  frameworks cluster upgrade foghorn --skip-validation

  # Skip confirmation prompt
  frameworks cluster upgrade bridge --yes`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runUpgrade(cmd, manifestPath, args[0], version, dryRun, skipValidation, yes, noRollback)
		},
	}

	cmd.Flags().StringVar(&manifestPath, "manifest", "cluster.yaml", "Path to cluster manifest file")
	cmd.Flags().StringVar(&version, "version", "stable", "Version to upgrade to (stable, rc, v1.2.3)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Show what would be upgraded without executing")
	cmd.Flags().BoolVar(&skipValidation, "skip-validation", false, "Skip health validation after upgrade")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "Skip confirmation prompt")
	cmd.Flags().BoolVar(&noRollback, "no-rollback", false, "Skip automatic rollback on health check failure")

	return cmd
}

// runUpgrade executes the upgrade command
func runUpgrade(cmd *cobra.Command, manifestPath, serviceName, version string, dryRun, skipValidation, yes, noRollback bool) error {
	// Load cluster manifest
	manifest, err := inventory.Load(manifestPath)
	if err != nil {
		return fmt.Errorf("failed to load manifest: %w", err)
	}

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

	// Find host for service
	var host inventory.Host
	var found bool

	// Check infrastructure
	if serviceName == "postgres" && manifest.Infrastructure.Postgres.Enabled {
		host, found = manifest.GetHost(manifest.Infrastructure.Postgres.Host)
	} else if serviceName == "kafka" && manifest.Infrastructure.Kafka.Enabled {
		if len(manifest.Infrastructure.Kafka.Brokers) > 0 {
			host, found = manifest.GetHost(manifest.Infrastructure.Kafka.Brokers[0].Host)
		}
	} else if serviceName == "clickhouse" && manifest.Infrastructure.ClickHouse.Enabled {
		host, found = manifest.GetHost(manifest.Infrastructure.ClickHouse.Host)
	}

	// Check application services
	if !found {
		if svcConfig, ok := manifest.Services[serviceName]; ok {
			if svcConfig.Enabled {
				host, found = manifest.GetHost(svcConfig.Host)
			}
		}
	}

	// Check interfaces
	if !found {
		if ifaceConfig, ok := manifest.Interfaces[serviceName]; ok {
			if ifaceConfig.Enabled {
				host, found = manifest.GetHost(ifaceConfig.Host)
			}
		}
	}

	// Check observability
	if !found {
		if obsConfig, ok := manifest.Observability[serviceName]; ok {
			if obsConfig.Enabled {
				host, found = manifest.GetHost(obsConfig.Host)
			}
		}
	}

	if !found {
		return fmt.Errorf("service %s not found or not enabled in manifest", serviceName)
	}

	fmt.Fprintf(cmd.OutOrStdout(), "Upgrading %s on %s to version: %s\n", serviceName, host.Address, version)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	// Create SSH pool
	sshPool := ssh.NewPool(30 * time.Second)
	defer sshPool.Close()

	// Detect current state
	fmt.Fprintf(cmd.OutOrStdout(), "\n[1/6] Detecting current state...\n")
	detector := detect.NewDetector(host)
	state, err := detector.Detect(ctx, deployName)
	if err != nil {
		return fmt.Errorf("failed to detect service: %w", err)
	}

	if !state.Exists {
		return fmt.Errorf("service %s does not exist on %s (cannot upgrade non-existent service)", serviceName, host.Address)
	}

	// Store previous version for potential rollback
	previousVersion := state.Version
	previousMode := state.Mode

	fmt.Fprintf(cmd.OutOrStdout(), "  Current: %s (mode: %s, running: %v)\n", state.Version, state.Mode, state.Running)

	// Fetch GitOps manifest for new version
	fmt.Fprintf(cmd.OutOrStdout(), "\n[2/6] Fetching GitOps manifest...\n")
	channel, resolvedVersion := gitops.ResolveVersion(version)
	fmt.Fprintf(cmd.OutOrStdout(), "  Channel: %s, Version: %s\n", channel, resolvedVersion)

	// Use default remote GitOps repository (https://raw.githubusercontent.com/Livepeer-FrameWorks/gitops/main)
	fetcher, err := gitops.NewFetcher(gitops.FetchOptions{})
	if err != nil {
		return fmt.Errorf("failed to create gitops fetcher: %w", err)
	}

	gitopsManifest, err := fetcher.Fetch(channel, resolvedVersion)
	if err != nil {
		return fmt.Errorf("failed to fetch gitops manifest: %w", err)
	}

	svcInfo, err := gitopsManifest.GetServiceInfo(deployName)
	if err != nil {
		return fmt.Errorf("service %s not found in GitOps manifest: %w", deployName, err)
	}

	fmt.Fprintf(cmd.OutOrStdout(), "  New version: %s\n", svcInfo.Version)
	if state.Mode == "docker" {
		fmt.Fprintf(cmd.OutOrStdout(), "  New image: %s\n", svcInfo.FullImage)
	}

	// Check if already at target version
	if state.Version == svcInfo.Version && !dryRun {
		fmt.Fprintf(cmd.OutOrStdout(), "\n✓ Already at version %s, nothing to do\n", svcInfo.Version)
		return nil
	}

	if dryRun {
		fmt.Fprintf(cmd.OutOrStdout(), "\n[DRY-RUN] Would upgrade %s from %s to %s\n", serviceName, state.Version, svcInfo.Version)
		fmt.Fprintf(cmd.OutOrStdout(), "  Mode: %s\n", state.Mode)
		if state.Mode == "docker" {
			fmt.Fprintf(cmd.OutOrStdout(), "  New image: %s\n", svcInfo.FullImage)
		}
		fmt.Fprintln(cmd.OutOrStdout(), "\nDry-run complete. Use without --dry-run to execute.")
		return nil
	}

	// Require confirmation for upgrade (destructive operation)
	if !yes {
		fmt.Fprintf(os.Stderr, "\nUpgrade %s from %s to %s? [y/N]: ", serviceName, previousVersion, svcInfo.Version)
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

	// Stop service
	fmt.Fprintf(cmd.OutOrStdout(), "\n[3/6] Stopping service...\n")
	if errStop := stopService(ctx, host, deployName, state.Mode, sshPool); errStop != nil {
		return fmt.Errorf("failed to stop service: %w", errStop)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "  ✓ Service stopped\n")

	// Get provisioner and re-provision with new version
	fmt.Fprintf(cmd.OutOrStdout(), "\n[4/6] Deploying new version...\n")
	prov, err := provisioner.GetProvisioner(deployName, sshPool)
	if err != nil {
		return fmt.Errorf("failed to get provisioner: %w", err)
	}

	portCfg := inventory.ServiceConfig{}
	if svcCfg, ok := manifest.Services[serviceName]; ok {
		portCfg = svcCfg
	} else if ifaceCfg, ok := manifest.Interfaces[serviceName]; ok {
		portCfg = ifaceCfg
	}
	port, err := resolvePort(serviceName, portCfg)
	if err != nil {
		return fmt.Errorf("failed to resolve port for %s: %w", serviceName, err)
	}

	config := provisioner.ServiceConfig{
		Mode:     state.Mode,
		Version:  version,
		Port:     port,
		Metadata: make(map[string]interface{}),
	}

	// Provision (this pulls new image or downloads new binary and starts)
	if err := prov.Provision(ctx, host, config); err != nil {
		return fmt.Errorf("failed to provision new version: %w", err)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "  ✓ New version deployed\n")

	// Validate health
	if !skipValidation {
		fmt.Fprintf(cmd.OutOrStdout(), "\n[5/6] Validating health...\n")

		// Wait a moment for service to start
		time.Sleep(3 * time.Second)

		if err := prov.Validate(ctx, host, config); err != nil {
			fmt.Fprintf(cmd.OutOrStderr(), "  ✗ Health check failed: %v\n", err)

			// Attempt rollback unless --no-rollback is set
			if !noRollback {
				fmt.Fprintf(cmd.OutOrStdout(), "\n[ROLLBACK] Reverting to previous version %s...\n", previousVersion)

				rollbackConfig := provisioner.ServiceConfig{
					Mode:     previousMode,
					Version:  previousVersion,
					Port:     port,
					Metadata: make(map[string]interface{}),
				}

				if rollbackErr := prov.Provision(ctx, host, rollbackConfig); rollbackErr != nil {
					fmt.Fprintf(cmd.OutOrStderr(), "  ✗ Rollback failed: %v\n", rollbackErr)
					fmt.Fprintln(cmd.OutOrStderr(), "\nCRITICAL: Service may be in broken state!")
					fmt.Fprintln(cmd.OutOrStderr(), "Manual intervention required. Check logs with: frameworks cluster logs "+serviceName)
					return fmt.Errorf("upgrade failed and rollback failed: %w", rollbackErr)
				}

				fmt.Fprintf(cmd.OutOrStdout(), "  ✓ Rolled back to %s\n", previousVersion)
				return fmt.Errorf("upgrade failed, rolled back to %s", previousVersion)
			}

			fmt.Fprintln(cmd.OutOrStderr(), "\nWARNING: Service upgraded but health check failed!")
			fmt.Fprintln(cmd.OutOrStderr(), "Check service logs with: frameworks cluster logs "+serviceName)
			return fmt.Errorf("health validation failed")
		}
		fmt.Fprintf(cmd.OutOrStdout(), "  ✓ Service is healthy\n")
	}

	fmt.Fprintf(cmd.OutOrStdout(), "\n[6/6] Upgrade complete!\n")
	fmt.Fprintf(cmd.OutOrStdout(), "✓ %s upgraded from %s to %s\n", serviceName, previousVersion, svcInfo.Version)

	return nil
}

// stopService stops a service based on its mode
func stopService(ctx context.Context, host inventory.Host, serviceName, mode string, pool *ssh.Pool) error {
	var stopCmd string
	switch mode {
	case "docker":
		stopCmd = fmt.Sprintf("cd /opt/frameworks/%s && docker compose stop", serviceName)
	case "native":
		stopCmd = fmt.Sprintf("systemctl stop frameworks-%s", serviceName)
	default:
		return fmt.Errorf("unknown service mode: %s", mode)
	}

	// Get runner
	var runner ssh.Runner
	if host.Address == "" || host.Address == "localhost" || host.Address == "127.0.0.1" {
		runner = ssh.NewLocalRunner("")
	} else {
		sshConfig := &ssh.ConnectionConfig{
			Address: host.Address,
			Port:    22,
			User:    host.User,
			KeyPath: host.SSHKey,
			Timeout: 30 * time.Second,
		}
		var err error
		runner, err = pool.Get(sshConfig)
		if err != nil {
			return fmt.Errorf("failed to connect to host: %w", err)
		}
	}

	result, err := runner.Run(ctx, stopCmd)
	if err != nil {
		return fmt.Errorf("failed to execute stop command: %w", err)
	}

	if result.ExitCode != 0 {
		return fmt.Errorf("stop command failed: %s", result.Stderr)
	}

	return nil
}

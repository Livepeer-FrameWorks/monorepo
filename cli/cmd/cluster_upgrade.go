package cmd

import (
	"bufio"
	"context"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"time"

	"frameworks/cli/internal/ux"
	"frameworks/cli/pkg/detect"
	"frameworks/cli/pkg/gitops"
	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/orchestrator"
	"frameworks/cli/pkg/provisioner"
	"frameworks/cli/pkg/ssh"

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

Use --all to upgrade all enabled services in dependency order.`,
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
				return runUpgradeAll(cmd, rc, version, dryRun, skipValidation, yes, noRollback)
			}
			return runUpgrade(cmd, rc, args[0], version, dryRun, skipValidation, yes, noRollback)
		},
	}

	cmd.Flags().StringVar(&version, "version", "", "Version to upgrade to (stable, rc, v1.2.3); defaults to cluster channel")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Show what would be upgraded without executing")
	cmd.Flags().BoolVar(&skipValidation, "skip-validation", false, "Skip health validation after upgrade")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "Skip confirmation prompt")
	cmd.Flags().BoolVar(&noRollback, "no-rollback", false, "Skip automatic rollback on health check failure")
	cmd.Flags().BoolVar(&all, "all", false, "Upgrade all enabled services in dependency order")

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

// runUpgrade executes the upgrade command against an already-resolved manifest.
func runUpgrade(cmd *cobra.Command, rc *resolvedCluster, serviceName, version string, dryRun, skipValidation, yes, noRollback bool) error {
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

	// Find host for service
	var host inventory.Host
	var found bool

	// Check infrastructure — every role-backed infra service resolves to a
	// single primary host for single-node upgrades. Multi-host infra
	// (ensemble Yugabyte, Kafka KRaft) upgrades one host at a time via
	// Provision's idempotent role-run; the CLI currently targets the first
	// node in each case.
	switch serviceName {
	case "postgres":
		if pg := manifest.Infrastructure.Postgres; pg != nil && pg.Enabled {
			host, found = manifest.GetHost(pg.Host)
		}
	case "yugabyte":
		if pg := manifest.Infrastructure.Postgres; pg != nil && pg.Enabled && pg.IsYugabyte() && len(pg.Nodes) > 0 {
			host, found = manifest.GetHost(pg.Nodes[0].Host)
		}
	case "kafka":
		if k := manifest.Infrastructure.Kafka; k != nil && k.Enabled && len(k.Brokers) > 0 {
			host, found = manifest.GetHost(k.Brokers[0].Host)
		}
	case "kafka-controller":
		if k := manifest.Infrastructure.Kafka; k != nil && k.Enabled && len(k.Controllers) > 0 {
			host, found = manifest.GetHost(k.Controllers[0].Host)
		}
	case "zookeeper":
		if z := manifest.Infrastructure.Zookeeper; z != nil && z.Enabled && len(z.Ensemble) > 0 {
			host, found = manifest.GetHost(z.Ensemble[0].Host)
		}
	case "clickhouse":
		if ch := manifest.Infrastructure.ClickHouse; ch != nil && ch.Enabled {
			host, found = manifest.GetHost(ch.Host)
		}
	case "redis":
		if r := manifest.Infrastructure.Redis; r != nil && r.Enabled && len(r.Instances) > 0 {
			host, found = manifest.GetHost(r.Instances[0].Host)
		}
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

	ux.Heading(cmd.OutOrStdout(), fmt.Sprintf("Upgrading %s on %s to version: %s", serviceName, host.ExternalIP, version))

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	// Create SSH pool
	sshKey := stringFlag(cmd, "ssh-key").Value
	sshPool := ssh.NewPool(30*time.Second, sshKey)
	defer sshPool.Close()

	// Detect current state
	fmt.Fprintf(cmd.OutOrStdout(), "\n[1/6] Detecting current state...\n")
	detector := detect.NewDetector(sshPool, host)
	state, err := detector.Detect(ctx, deployName)
	if err != nil {
		return fmt.Errorf("failed to detect service: %w", err)
	}

	if !state.Exists {
		return fmt.Errorf("service %s does not exist on %s (cannot upgrade non-existent service)", serviceName, host.ExternalIP)
	}

	// Store previous version for potential rollback
	previousVersion := state.Version
	previousMode := state.Mode

	fmt.Fprintf(cmd.OutOrStdout(), "  Current: %s (mode: %s, running: %v)\n", state.Version, state.Mode, state.Running)

	// Fetch GitOps manifest for new version
	fmt.Fprintf(cmd.OutOrStdout(), "\n[2/6] Fetching GitOps manifest...\n")
	channel, resolvedVersion := gitops.ResolveVersion(version)
	fmt.Fprintf(cmd.OutOrStdout(), "  Channel: %s, Version: %s\n", channel, resolvedVersion)

	gitopsManifest, err := gitops.FetchFromRepositories(gitops.FetchOptions{}, rc.ReleaseRepos, channel, resolvedVersion)
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
		ux.Success(cmd.OutOrStdout(), fmt.Sprintf("Already at version %s, nothing to do", svcInfo.Version))
		return nil
	}

	// Build the same ServiceConfig the real provision flow uses — role vars
	// builders depend on env + manifest-derived metadata, not just
	// Mode/Version/Port. A synthetic orchestrator.Task feeds buildTaskConfig.
	task := &orchestrator.Task{
		Name:       serviceName,
		Type:       deployName,
		ServiceID:  serviceName,
		InstanceID: "",
		Host:       host.Name,
		Phase:      orchestrator.PhaseApplications,
		Idempotent: true,
	}
	manifestDir := filepath.Dir(rc.ManifestPath)
	sharedEnv, envErr := rc.SharedEnv()
	if envErr != nil {
		// Upgrades for services that rely on SOPS-backed secrets fail
		// loud; non-secret services still work because their vars
		// builders tolerate missing env values.
		fmt.Fprintf(cmd.OutOrStderr(), "  warning: shared env decrypt failed: %v\n", envErr)
		sharedEnv = nil
	}
	config, err := buildTaskConfig(task, manifest, map[string]interface{}{}, true, manifestDir, sharedEnv, rc.ReleaseRepos)
	if err != nil {
		return fmt.Errorf("build upgrade config: %w", err)
	}
	// Override the resolved version with the one requested by the upgrade
	// command; buildTaskConfig pulls from the manifest, which may be pinned
	// to a different version than the user is moving to.
	config.Version = version
	config.Mode = state.Mode
	rc.applyReleaseMetadata(config.Metadata)

	if dryRun {
		fmt.Fprintf(cmd.OutOrStdout(), "\n[DRY-RUN] Would upgrade %s from %s to %s\n", serviceName, state.Version, svcInfo.Version)
		fmt.Fprintf(cmd.OutOrStdout(), "  Mode: %s\n", state.Mode)
		if state.Mode == "docker" {
			fmt.Fprintf(cmd.OutOrStdout(), "  New image: %s\n", svcInfo.FullImage)
		}
		prov, provErr := provisioner.GetProvisioner(deployName, sshPool)
		if provErr != nil {
			return fmt.Errorf("failed to get provisioner: %w", provErr)
		}
		checker, ok := prov.(provisioner.CheckDiffer)
		if !ok {
			fmt.Fprintln(cmd.OutOrStdout(), "\n(provisioner does not support --check --diff; preview above is the summary)")
			return nil
		}
		fmt.Fprintln(cmd.OutOrStdout(), "\nRunning ansible-playbook --check --diff against the target...")
		if checkErr := checker.CheckDiff(ctx, host, config); checkErr != nil {
			return fmt.Errorf("dry-run: %w", checkErr)
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

	// Role-backed services handle stop/restart via handlers notified on
	// binary/config change — explicit stop between install phases would
	// only duplicate work the role already does.
	fmt.Fprintf(cmd.OutOrStdout(), "\n[3/6] Getting provisioner...\n")
	prov, err := provisioner.GetProvisioner(deployName, sshPool)
	if err != nil {
		return fmt.Errorf("failed to get provisioner: %w", err)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "  ✓ Provisioner ready\n")

	// Deploy new version
	fmt.Fprintf(cmd.OutOrStdout(), "\n[4/6] Deploying new version...\n")

	// Provision (this pulls new image or downloads new binary and starts)
	if err := prov.Provision(ctx, host, config); err != nil {
		return fmt.Errorf("failed to provision new version: %w", err)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "  ✓ New version deployed\n")

	if err := prov.Initialize(ctx, host, config); err != nil {
		return fmt.Errorf("failed to initialize %s: %w", serviceName, err)
	}

	// Validate health
	if !skipValidation {
		fmt.Fprintf(cmd.OutOrStdout(), "\n[5/6] Validating health...\n")

		if err := waitForHealth(ctx, func() error {
			return prov.Validate(ctx, host, config)
		}, 5*time.Second, 90*time.Second); err != nil {
			fmt.Fprintf(cmd.OutOrStderr(), "  ✗ Health check failed: %v\n", err)

			// Attempt rollback unless --no-rollback is set
			if !noRollback {
				fmt.Fprintf(cmd.OutOrStdout(), "\n[ROLLBACK] Reverting to previous version %s...\n", previousVersion)

				// Rollback uses the same config surface but pinned to the
				// previous version/mode the host was running before upgrade.
				rollbackConfig := config
				rollbackConfig.Version = previousVersion
				rollbackConfig.Mode = previousMode
				rollbackConfig.Force = true
				rollbackConfig.Metadata = copyMetadata(config.Metadata)
				rc.applyReleaseMetadata(rollbackConfig.Metadata)

				if cleanupErr := prov.Cleanup(ctx, host, rollbackConfig); cleanupErr != nil {
					fmt.Fprintf(cmd.OutOrStderr(), "  ⚠ Rollback cleanup warning: %v\n", cleanupErr)
				}

				if rollbackErr := prov.Provision(ctx, host, rollbackConfig); rollbackErr != nil {
					fmt.Fprintf(cmd.OutOrStderr(), "  ✗ Rollback failed: %v\n", rollbackErr)
					fmt.Fprintln(cmd.OutOrStderr(), "\nCRITICAL: Service may be in broken state!")
					fmt.Fprintln(cmd.OutOrStderr(), "Manual intervention required. Check logs with: frameworks cluster logs "+serviceName)
					return fmt.Errorf("upgrade failed and rollback failed: %w", rollbackErr)
				}

				if err := prov.Initialize(ctx, host, rollbackConfig); err != nil {
					fmt.Fprintf(cmd.OutOrStderr(), "  ✗ Rollback initialization failed: %v\n", err)
					return fmt.Errorf("upgrade failed, rollback initialization failed: %w", err)
				}

				if err := waitForHealth(ctx, func() error {
					return prov.Validate(ctx, host, rollbackConfig)
				}, 5*time.Second, 90*time.Second); err != nil {
					fmt.Fprintf(cmd.OutOrStderr(), "  ✗ Rollback health check failed: %v\n", err)
					return fmt.Errorf("upgrade failed, rollback health check failed: %w", err)
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

	ux.Success(cmd.OutOrStdout(), fmt.Sprintf("%s upgraded from %s to %s", serviceName, previousVersion, svcInfo.Version))

	// Persist the new version back to the cluster manifest. The resolver
	// hands us the on-disk path of whichever source it chose; writing
	// into a github-sourced tempdir is harmless (discarded on Cleanup).
	saveUpgradedVersion(manifest, serviceName, svcInfo.Version, manifestPath, cmd)

	return nil
}

// saveUpgradedVersion updates the service version in the manifest and writes it back.
func saveUpgradedVersion(manifest *inventory.Manifest, serviceName, newVersion, manifestPath string, cmd *cobra.Command) {
	updated := false
	if svc, ok := manifest.Services[serviceName]; ok {
		svc.Version = newVersion
		manifest.Services[serviceName] = svc
		updated = true
	} else if iface, ok := manifest.Interfaces[serviceName]; ok {
		iface.Version = newVersion
		manifest.Interfaces[serviceName] = iface
		updated = true
	} else if obs, ok := manifest.Observability[serviceName]; ok {
		obs.Version = newVersion
		manifest.Observability[serviceName] = obs
		updated = true
	}
	if updated {
		if err := inventory.Save(manifestPath, manifest); err != nil {
			fmt.Fprintf(cmd.OutOrStderr(), "Warning: could not save manifest: %v\n", err)
		}
	}
}

// runUpgradeAll upgrades all enabled services in dependency order.
func runUpgradeAll(cmd *cobra.Command, rc *resolvedCluster, version string, dryRun, skipValidation, yes, noRollback bool) error {
	manifest := rc.Manifest
	var err error
	version = resolveUpgradeVersion(cmd, manifest, version)

	// Build dependency-ordered execution plan
	planner := orchestrator.NewPlanner(manifest)
	plan, err := planner.Plan(context.Background(), orchestrator.ProvisionOptions{
		Phase: orchestrator.PhaseAll,
	})
	if err != nil {
		return fmt.Errorf("failed to build execution plan: %w", err)
	}

	services := collectUpgradeableServices(plan)

	if len(services) == 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "No upgradeable services found in manifest.")
		return nil
	}

	ux.Heading(cmd.OutOrStdout(), fmt.Sprintf("Upgrading %d services (channel: %s, version: %s)", len(services), manifest.ResolvedChannel(), version))
	fmt.Fprintf(cmd.OutOrStdout(), "Order: %s\n\n", strings.Join(services, " -> "))

	if dryRun {
		for _, svc := range services {
			fmt.Fprintf(cmd.OutOrStdout(), "  [DRY-RUN] Would upgrade: %s\n", svc)
		}
		fmt.Fprintln(cmd.OutOrStdout(), "\nDry-run complete. Use without --dry-run to execute.")
		return nil
	}

	if !yes {
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
		if err := runUpgrade(cmd, rc, svc, version, false, skipValidation, true, noRollback); err != nil {
			fmt.Fprintf(cmd.OutOrStderr(), "  ✗ %s failed: %v\n", svc, err)
			failed = append(failed, svc)
			fmt.Fprintf(cmd.OutOrStderr(), "\nStopping upgrade sequence. Succeeded: %v, Failed: %v, Remaining: %v\n",
				succeeded, failed, services[i+1:])
			return fmt.Errorf("upgrade --all stopped: %s failed", svc)
		}
		succeeded = append(succeeded, svc)
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

// collectUpgradeableServices extracts deduplicated service IDs from an
// execution plan, stripping the @host suffix that the planner appends for
// multi-host replicated services. Infrastructure is included now that
// role-based provisioners own their own restart semantics via notified
// handlers — `cluster upgrade --all` no longer needs to carve it out.
func collectUpgradeableServices(plan *orchestrator.ExecutionPlan) []string {
	seen := make(map[string]bool)
	var services []string
	for _, batch := range plan.Batches {
		for _, task := range batch {
			svcID := task.ServiceID
			if !seen[svcID] {
				seen[svcID] = true
				services = append(services, svcID)
			}
		}
	}
	return services
}

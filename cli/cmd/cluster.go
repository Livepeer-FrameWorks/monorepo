package cmd

import (
	"context"
	"fmt"
	"time"

	"frameworks/cli/pkg/detect"
	"frameworks/cli/pkg/health"
	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/servicedefs"

	"github.com/spf13/cobra"
)

// newClusterCmd returns the cluster command group for central/regional infrastructure management
func newClusterCmd() *cobra.Command {
	cluster := &cobra.Command{
		Use:   "cluster",
		Short: "Cluster infrastructure management (central/regional control planes)",
		Long: `Manage central and regional FrameWorks clusters including:
  - Infrastructure tier (Postgres, Kafka, Zookeeper, ClickHouse)
  - Application services (Quartermaster, Commodore, Bridge, Periscope, etc.)
  - Interface services (Nginx/Caddy, Chartroom, Foredeck, Logbook)`,
	}

	cluster.AddCommand(newClusterDetectCmd())
	cluster.AddCommand(newClusterDoctorCmd())
	cluster.AddCommand(newClusterStatusCmd())
	cluster.AddCommand(newClusterProvisionCmd())
	cluster.AddCommand(newClusterInitCmd())
	cluster.AddCommand(newClusterLogsCmd())
	cluster.AddCommand(newClusterRestartCmd())
	cluster.AddCommand(newClusterUpgradeCmd())
	cluster.AddCommand(newClusterBackupCmd())
	cluster.AddCommand(newClusterRestoreCmd())
	cluster.AddCommand(newClusterDiagnoseCmd())
	cluster.AddCommand(newClusterSyncGeoIPCmd())
	cluster.AddCommand(newClusterSetChannelCmd())

	// Future commands
	// cluster.AddCommand(newClusterPlanCmd())
	// cluster.AddCommand(newClusterScaleCmd())

	// Export/Integration commands
	// cluster.AddCommand(newClusterExportCmd())

	return cluster
}

// newClusterDetectCmd implements: frameworks cluster detect --manifest cluster.yaml
func newClusterDetectCmd() *cobra.Command {
	var manifestPath string

	cmd := &cobra.Command{
		Use:   "detect",
		Short: "Detect current state of all services in the cluster",
		Long: `Scan the cluster and detect the current state of all services:
  - Which services are running (docker, native, or unknown)
  - Service versions
  - Health status
  - Configuration state

This command uses multi-method detection:
  1. Check inventory file (/etc/frameworks/inventory.json)
  2. Check Docker containers
  3. Check systemd services
  4. Check listening ports
  5. Try health endpoints
  6. Try direct connections (DB, Kafka, etc.)`,
		Example: `  # Detect all services
  frameworks cluster detect --manifest cluster.yaml

  # Detect with verbose output
  frameworks cluster detect --manifest cluster.yaml --verbose`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runDetect(cmd, manifestPath)
		},
	}

	cmd.Flags().StringVar(&manifestPath, "manifest", "cluster.yaml", "Path to cluster manifest file")
	return cmd
}

// newClusterDoctorCmd implements: frameworks cluster doctor --manifest cluster.yaml
func newClusterDoctorCmd() *cobra.Command {
	var manifestPath string

	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Comprehensive health check of all cluster services",
		Long: `Run comprehensive health checks across the entire cluster:

Infrastructure Health:
  - Postgres: connection, databases, query performance, replication
  - Kafka: cluster health, topics, consumer lag, broker disk
  - Zookeeper: quorum, leader election
  - ClickHouse: connection, tables, query performance

Application Services:
  - Health endpoints (/health)
  - Database connectivity
  - Kafka consumer status
  - Active connections/subscriptions

Networking & Connectivity:
  - WireGuard mesh (if configured)
  - /etc/hosts overrides
  - Service discovery (can services reach dependencies)

Data Initialization:
  - Postgres schemas and tables
  - Kafka topics and partitions
  - ClickHouse tables

Reports critical issues and provides actionable recommendations.`,
		Example: `  # Run full health check
  frameworks cluster doctor --manifest cluster.yaml

  # Verbose output with detailed checks
  frameworks cluster doctor --manifest cluster.yaml --verbose`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runDoctor(cmd, manifestPath)
		},
	}

	cmd.Flags().StringVar(&manifestPath, "manifest", "cluster.yaml", "Path to cluster manifest file")
	return cmd
}

// runDetect executes the detect command
func runDetect(cmd *cobra.Command, manifestPath string) error {
	// Load manifest
	manifest, err := inventory.Load(manifestPath)
	if err != nil {
		return fmt.Errorf("failed to load manifest: %w", err)
	}

	fmt.Fprintf(cmd.OutOrStdout(), "Detecting cluster state from manifest: %s\n", manifestPath)
	fmt.Fprintf(cmd.OutOrStdout(), "Cluster type: %s, Profile: %s\n", manifest.Type, manifest.Profile)
	fmt.Fprintf(cmd.OutOrStdout(), "Hosts: %d\n\n", len(manifest.Hosts))

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Detect infrastructure services
	if manifest.Infrastructure.Postgres != nil && manifest.Infrastructure.Postgres.Enabled {
		detectService(ctx, cmd, manifest, "postgres", "postgres", manifest.Infrastructure.Postgres.Host)
	}

	if manifest.Infrastructure.ClickHouse != nil && manifest.Infrastructure.ClickHouse.Enabled {
		detectService(ctx, cmd, manifest, "clickhouse", "clickhouse", manifest.Infrastructure.ClickHouse.Host)
	}

	if manifest.Infrastructure.Kafka != nil && manifest.Infrastructure.Kafka.Enabled {
		for _, broker := range manifest.Infrastructure.Kafka.Brokers {
			serviceName := fmt.Sprintf("kafka-broker-%d", broker.ID)
			detectService(ctx, cmd, manifest, serviceName, "kafka", broker.Host)
		}
	}

	if manifest.Infrastructure.Zookeeper != nil && manifest.Infrastructure.Zookeeper.Enabled {
		for _, node := range manifest.Infrastructure.Zookeeper.Ensemble {
			serviceName := fmt.Sprintf("zookeeper-%d", node.ID)
			detectService(ctx, cmd, manifest, serviceName, "zookeeper", node.Host)
		}
	}

	// Detect application services
	for name, svc := range manifest.Services {
		if !svc.Enabled {
			continue
		}
		if svc.Host != "" {
			deploy, err := resolveDeployName(name, svc)
			if err != nil {
				fmt.Fprintf(cmd.OutOrStdout(), "✗ %s: %v\n", name, err)
				continue
			}
			detectService(ctx, cmd, manifest, name, deploy, svc.Host)
		} else if len(svc.Hosts) > 0 {
			for i, hostName := range svc.Hosts {
				deploy, err := resolveDeployName(name, svc)
				if err != nil {
					fmt.Fprintf(cmd.OutOrStdout(), "✗ %s: %v\n", name, err)
					continue
				}
				serviceName := fmt.Sprintf("%s-%d", name, i+1)
				detectService(ctx, cmd, manifest, serviceName, deploy, hostName)
			}
		}
	}

	// Detect interface services
	for name, iface := range manifest.Interfaces {
		if !iface.Enabled {
			continue
		}
		if iface.Host != "" {
			deploy, err := resolveDeployName(name, iface)
			if err != nil {
				fmt.Fprintf(cmd.OutOrStdout(), "✗ %s: %v\n", name, err)
				continue
			}
			detectService(ctx, cmd, manifest, name, deploy, iface.Host)
		}
	}

	// Detect observability services
	for name, obs := range manifest.Observability {
		if !obs.Enabled {
			continue
		}
		if obs.Host != "" {
			deploy, err := resolveDeployName(name, obs)
			if err != nil {
				fmt.Fprintf(cmd.OutOrStdout(), "✗ %s: %v\n", name, err)
				continue
			}
			detectService(ctx, cmd, manifest, name, deploy, obs.Host)
		}
	}

	return nil
}

// detectService detects a single service on a host
func detectService(ctx context.Context, cmd *cobra.Command, manifest *inventory.Manifest, serviceName, deployName, hostName string) {
	host, ok := manifest.GetHost(hostName)
	if !ok {
		fmt.Fprintf(cmd.OutOrStdout(), "✗ %s: host '%s' not found\n", serviceName, hostName)
		return
	}

	detector := detect.NewDetector(host)
	state, err := detector.Detect(ctx, deployName)

	if err != nil {
		fmt.Fprintf(cmd.OutOrStdout(), "✗ %s (%s): detection error: %v\n", serviceName, hostName, err)
		return
	}

	if !state.Exists {
		fmt.Fprintf(cmd.OutOrStdout(), "✗ %s (%s): not found\n", serviceName, hostName)
		return
	}

	// Print detection results
	status := "✓"
	if !state.Running {
		status = "⚠"
	}

	modeStr := state.Mode
	if state.Version != "" {
		modeStr = fmt.Sprintf("%s, v%s", state.Mode, state.Version)
	}

	runningStr := "stopped"
	if state.Running {
		runningStr = "running"
	}

	fmt.Fprintf(cmd.OutOrStdout(), "%s %s (%s): %s [%s, detected by: %s]\n",
		status, serviceName, hostName, runningStr, modeStr, state.DetectedBy)

	// Show metadata if verbose
	if verbose && len(state.Metadata) > 0 {
		for k, v := range state.Metadata {
			fmt.Fprintf(cmd.OutOrStdout(), "    %s: %s\n", k, v)
		}
	}
}

// runDoctor executes comprehensive health checks
func runDoctor(cmd *cobra.Command, manifestPath string) error {
	// Load manifest
	manifest, err := inventory.Load(manifestPath)
	if err != nil {
		return fmt.Errorf("failed to load manifest: %w", err)
	}

	fmt.Fprintf(cmd.OutOrStdout(), "Running cluster health checks\n")
	fmt.Fprintf(cmd.OutOrStdout(), "Manifest: %s (type: %s, profile: %s)\n\n", manifestPath, manifest.Type, manifest.Profile)

	totalChecks := 0
	passedChecks := 0

	// Infrastructure Health Checks
	fmt.Fprintln(cmd.OutOrStdout(), "Infrastructure Health:")
	fmt.Fprintln(cmd.OutOrStdout(), "")

	// Check Postgres/YugabyteDB
	if manifest.Infrastructure.Postgres != nil && manifest.Infrastructure.Postgres.Enabled {
		host, ok := manifest.GetHost(manifest.Infrastructure.Postgres.Host)
		if !ok {
			fmt.Fprintf(cmd.OutOrStdout(), "✗ Postgres: host '%s' not found\n", manifest.Infrastructure.Postgres.Host)
		} else {
			totalChecks++
			checker := &health.PostgresChecker{
				User:     "postgres", // TODO: Get from config
				Password: "",
				Database: "postgres",
			}
			result := checker.Check(host.Address, manifest.Infrastructure.Postgres.Port)
			printHealthResult(cmd, "Postgres/Yugabyte", result)
			if result.OK {
				passedChecks++
			}
		}
	}

	// Check ClickHouse
	if manifest.Infrastructure.ClickHouse != nil && manifest.Infrastructure.ClickHouse.Enabled {
		host, ok := manifest.GetHost(manifest.Infrastructure.ClickHouse.Host)
		if !ok {
			fmt.Fprintf(cmd.OutOrStdout(), "✗ ClickHouse: host '%s' not found\n", manifest.Infrastructure.ClickHouse.Host)
		} else {
			totalChecks++
			checker := &health.ClickHouseChecker{
				User:     "default", // TODO: Get from config
				Password: "",
				Database: "default",
			}
			result := checker.Check(host.Address, manifest.Infrastructure.ClickHouse.Port)
			printHealthResult(cmd, "ClickHouse", result)
			if result.OK {
				passedChecks++
			}
		}
	}

	// Check Kafka brokers
	if manifest.Infrastructure.Kafka != nil && manifest.Infrastructure.Kafka.Enabled {
		for _, broker := range manifest.Infrastructure.Kafka.Brokers {
			host, ok := manifest.GetHost(broker.Host)
			if !ok {
				fmt.Fprintf(cmd.OutOrStdout(), "✗ Kafka broker %d: host '%s' not found\n", broker.ID, broker.Host)
				continue
			}
			totalChecks++
			checker := &health.KafkaChecker{}
			result := checker.Check(host.Address, broker.Port)
			printHealthResult(cmd, fmt.Sprintf("Kafka Broker %d", broker.ID), result)
			if result.OK {
				passedChecks++
			}
		}
	}

	fmt.Fprintln(cmd.OutOrStdout(), "")

	// Application Services Health Checks
	fmt.Fprintln(cmd.OutOrStdout(), "Application Services:")
	fmt.Fprintln(cmd.OutOrStdout(), "")

	for name, svc := range manifest.Services {
		if !svc.Enabled {
			continue
		}

		hostName := svc.Host
		if hostName == "" && len(svc.Hosts) > 0 {
			hostName = svc.Hosts[0] // Check first replica
		}

		if hostName == "" {
			continue
		}

		host, ok := manifest.GetHost(hostName)
		if !ok {
			fmt.Fprintf(cmd.OutOrStdout(), "✗ %s: host '%s' not found\n", name, hostName)
			continue
		}

		port, err := resolvePort(name, svc)
		if err != nil {
			fmt.Fprintf(cmd.OutOrStdout(), "✗ %s: %v\n", name, err)
			continue
		}
		totalChecks++
		path := "/health"
		if def, ok := servicedefs.Lookup(name); ok && def.HealthPath != "" {
			path = def.HealthPath
		}
		checker := &health.HTTPChecker{
			Path:    path,
			Timeout: 5 * time.Second,
		}
		result := checker.Check(host.Address, port)
		printHealthResult(cmd, name, result)
		if result.OK {
			passedChecks++
		}
	}

	fmt.Fprintln(cmd.OutOrStdout(), "")

	// Interface Services Health Checks
	fmt.Fprintln(cmd.OutOrStdout(), "Interface Services:")
	fmt.Fprintln(cmd.OutOrStdout(), "")

	for name, svc := range manifest.Interfaces {
		if !svc.Enabled {
			continue
		}
		if svc.Host == "" {
			continue
		}

		host, ok := manifest.GetHost(svc.Host)
		if !ok {
			fmt.Fprintf(cmd.OutOrStdout(), "✗ %s: host '%s' not found\n", name, svc.Host)
			continue
		}

		port, err := resolvePort(name, svc)
		if err != nil {
			fmt.Fprintf(cmd.OutOrStdout(), "✗ %s: %v\n", name, err)
			continue
		}

		totalChecks++
		path := "/health"
		if def, ok := servicedefs.Lookup(name); ok && def.HealthPath != "" {
			path = def.HealthPath
		}
		checker := &health.HTTPChecker{
			Path:    path,
			Timeout: 5 * time.Second,
		}
		result := checker.Check(host.Address, port)
		printHealthResult(cmd, name, result)
		if result.OK {
			passedChecks++
		}
	}

	fmt.Fprintln(cmd.OutOrStdout(), "")

	// Observability Services Health Checks
	if len(manifest.Observability) > 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "Observability Services:")
		fmt.Fprintln(cmd.OutOrStdout(), "")
		for name, svc := range manifest.Observability {
			if !svc.Enabled {
				continue
			}
			if svc.Host == "" {
				continue
			}
			host, ok := manifest.GetHost(svc.Host)
			if !ok {
				fmt.Fprintf(cmd.OutOrStdout(), "✗ %s: host '%s' not found\n", name, svc.Host)
				continue
			}
			port, err := resolvePort(name, svc)
			if err != nil {
				fmt.Fprintf(cmd.OutOrStdout(), "✗ %s: %v\n", name, err)
				continue
			}
			totalChecks++
			path := "/health"
			if def, ok := servicedefs.Lookup(name); ok && def.HealthPath != "" {
				path = def.HealthPath
			}
			checker := &health.HTTPChecker{
				Path:    path,
				Timeout: 5 * time.Second,
			}
			result := checker.Check(host.Address, port)
			printHealthResult(cmd, name, result)
			if result.OK {
				passedChecks++
			}
		}
		fmt.Fprintln(cmd.OutOrStdout(), "")
	}

	// Summary
	fmt.Fprintf(cmd.OutOrStdout(), "Summary: %d/%d checks passed\n", passedChecks, totalChecks)

	if passedChecks < totalChecks {
		fmt.Fprintln(cmd.OutOrStdout(), "\nRecommendations:")
		fmt.Fprintln(cmd.OutOrStdout(), "  - Check failed services with: frameworks cluster detect")
		fmt.Fprintln(cmd.OutOrStdout(), "  - Review service logs for errors")
		fmt.Fprintln(cmd.OutOrStdout(), "  - Verify network connectivity between hosts")
	}

	return nil
}

// printHealthResult prints a health check result
func printHealthResult(cmd *cobra.Command, serviceName string, result *health.CheckResult) {
	mark := "✗"
	if result.OK {
		mark = "✓"
	} else if result.Status == "degraded" {
		mark = "⚠"
	}

	statusStr := result.Status
	if result.Message != "" {
		statusStr = result.Message
	} else if result.Error != "" {
		statusStr = result.Error
	}

	fmt.Fprintf(cmd.OutOrStdout(), "%s %-20s: %s\n", mark, serviceName, statusStr)

	// Show metadata if verbose
	if verbose && len(result.Metadata) > 0 {
		for k, v := range result.Metadata {
			fmt.Fprintf(cmd.OutOrStdout(), "    %s: %s\n", k, v)
		}
	}
}

package cmd

import (
	"context"
	"fmt"
	"time"

	"frameworks/cli/pkg/detect"
	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/provisioner"
	"frameworks/cli/pkg/ssh"

	"github.com/spf13/cobra"
)

// newClusterRestartCmd creates the restart command
func newClusterRestartCmd() *cobra.Command {
	var manifestPath string
	var validate bool

	cmd := &cobra.Command{
		Use:   "restart <service>",
		Short: "Restart a service",
		Long: `Restart a service running on the cluster.

For Docker mode:
  - Uses 'docker compose restart'

For native mode (systemd):
  - Uses 'systemctl restart frameworks-<service>'

The restart command automatically detects the service mode and uses
the appropriate restart method.

After restart, the command can optionally validate that the service
is healthy using health checks.`,
		Example: `  # Restart quartermaster
  frameworks cluster restart quartermaster

  # Restart and validate health
  frameworks cluster restart bridge --validate`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runRestart(cmd, manifestPath, args[0], validate)
		},
	}

	cmd.Flags().StringVar(&manifestPath, "manifest", "cluster.yaml", "Path to cluster manifest file")
	cmd.Flags().BoolVar(&validate, "validate", false, "Validate service health after restart")

	return cmd
}

// runRestart executes the restart command
func runRestart(cmd *cobra.Command, manifestPath, serviceName string, validate bool) error {
	// Load manifest
	manifest, err := inventory.Load(manifestPath)
	if err != nil {
		return fmt.Errorf("failed to load manifest: %w", err)
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

	if !found {
		return fmt.Errorf("service %s not found or not enabled in manifest", serviceName)
	}

	fmt.Fprintf(cmd.OutOrStdout(), "Restarting %s on %s...\n", serviceName, host.Address)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	// Create SSH pool
	sshPool := ssh.NewPool(30 * time.Second)
	defer sshPool.Close()

	// Detect service mode
	detector := detect.NewDetector(host)
	state, err := detector.Detect(ctx, serviceName)
	if err != nil {
		return fmt.Errorf("failed to detect service: %w", err)
	}

	if !state.Exists {
		return fmt.Errorf("service %s does not exist on %s", serviceName, host.Address)
	}

	// Build restart command based on mode
	var restartCmd string
	switch state.Mode {
	case "docker":
		restartCmd = fmt.Sprintf("cd /opt/frameworks/%s && docker compose restart", serviceName)

	case "native":
		restartCmd = fmt.Sprintf("systemctl restart frameworks-%s", serviceName)

	default:
		return fmt.Errorf("unknown service mode: %s (cannot determine restart method)", state.Mode)
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
		runner, err = sshPool.Get(sshConfig)
		if err != nil {
			return fmt.Errorf("failed to connect to host: %w", err)
		}
	}

	// Execute restart command
	result, err := runner.Run(ctx, restartCmd)
	if err != nil {
		return fmt.Errorf("failed to restart service: %w", err)
	}

	if result.ExitCode != 0 {
		fmt.Fprintf(cmd.OutOrStderr(), "Error restarting service: %s\n", result.Stderr)
		return fmt.Errorf("restart command exited with code %d", result.ExitCode)
	}

	fmt.Fprintf(cmd.OutOrStdout(), "✓ %s restarted successfully\n", serviceName)

	// Validate if requested
	if validate {
		fmt.Fprintf(cmd.OutOrStdout(), "Validating service health...\n")

		// Wait a moment for service to start
		time.Sleep(3 * time.Second)

		// Get provisioner and validate
		prov, err := provisioner.GetProvisioner(serviceName, sshPool)
		if err != nil {
			fmt.Fprintf(cmd.OutOrStderr(), "  ⚠ Warning: Cannot validate (unknown service type)\n")
			return nil
		}

		config := provisioner.ServiceConfig{
			Mode: state.Mode,
			Port: provisioner.ServicePorts[serviceName],
		}

		if err := prov.Validate(ctx, host, config); err != nil {
			fmt.Fprintf(cmd.OutOrStderr(), "  ✗ Validation failed: %v\n", err)
			return fmt.Errorf("service restarted but health check failed")
		}

		fmt.Fprintf(cmd.OutOrStdout(), "  ✓ Service is healthy\n")
	}

	return nil
}

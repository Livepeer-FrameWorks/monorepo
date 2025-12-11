package cmd

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"frameworks/cli/pkg/detect"
	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/ssh"

	"github.com/spf13/cobra"
)

// newClusterLogsCmd creates the logs command
func newClusterLogsCmd() *cobra.Command {
	var manifestPath string
	var follow bool
	var tail int

	cmd := &cobra.Command{
		Use:   "logs <service>",
		Short: "Show logs from a service",
		Long: `Show logs from a service running on the cluster.

For Docker mode:
  - Uses 'docker compose logs'

For native mode (systemd):
  - Uses 'journalctl -u frameworks-<service>'

The logs command automatically detects the service mode and uses
the appropriate log viewing method.`,
		Example: `  # Show last 50 lines of quartermaster logs
  frameworks cluster logs quartermaster

  # Follow logs in real-time
  frameworks cluster logs quartermaster --follow

  # Show last 100 lines and follow
  frameworks cluster logs bridge --tail 100 --follow`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runLogs(cmd, manifestPath, args[0], follow, tail)
		},
	}

	cmd.Flags().StringVar(&manifestPath, "manifest", "cluster.yaml", "Path to cluster manifest file")
	cmd.Flags().BoolVarP(&follow, "follow", "f", false, "Follow log output")
	cmd.Flags().IntVarP(&tail, "tail", "n", 50, "Number of lines to show from the end")

	return cmd
}

// runLogs executes the logs command
func runLogs(cmd *cobra.Command, manifestPath, serviceName string, follow bool, tail int) error {
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

	fmt.Fprintf(cmd.OutOrStdout(), "Fetching logs for %s on %s...\n\n", serviceName, host.Address)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Handle Ctrl+C gracefully
	if follow {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
		go func() {
			<-sigCh
			fmt.Fprintln(cmd.OutOrStderr(), "\nStopping log stream...")
			cancel()
		}()
	}

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

	// Build log command based on mode
	var logCmd string
	switch state.Mode {
	case "docker":
		logCmd = fmt.Sprintf("cd /opt/frameworks/%s && docker compose logs", serviceName)
		if tail > 0 {
			logCmd += fmt.Sprintf(" --tail=%d", tail)
		}
		if follow {
			logCmd += " --follow"
		}

	case "native":
		logCmd = fmt.Sprintf("journalctl -u frameworks-%s", serviceName)
		if tail > 0 {
			logCmd += fmt.Sprintf(" -n %d", tail)
		}
		if follow {
			logCmd += " -f"
		}

	default:
		return fmt.Errorf("unknown service mode: %s (cannot determine log location)", state.Mode)
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

	// Execute log command
	result, err := runner.Run(ctx, logCmd)
	if err != nil {
		return fmt.Errorf("failed to fetch logs: %w", err)
	}

	if result.ExitCode != 0 {
		fmt.Fprintf(cmd.OutOrStderr(), "Error fetching logs: %s\n", result.Stderr)
		return fmt.Errorf("log command exited with code %d", result.ExitCode)
	}

	// Print logs
	fmt.Fprint(cmd.OutOrStdout(), result.Stdout)

	return nil
}

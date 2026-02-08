package cmd

import (
	"context"
	"fmt"
	"sort"
	"time"

	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/servicedefs"
	"frameworks/cli/pkg/ssh"

	"github.com/spf13/cobra"
)

// newClusterDiagnoseCmd creates the diagnose command
func newClusterDiagnoseCmd() *cobra.Command {
	var manifestPath string

	cmd := &cobra.Command{
		Use:   "diagnose <component>",
		Short: "Run diagnostics on cluster components",
		Long: `Run diagnostic checks on cluster components.

Supported diagnostics:
  network    - Test network connectivity between hosts
  resources  - Check CPU, memory, disk usage on all hosts
  ports      - Check for port conflicts
  kafka      - Check Kafka cluster health, topic lag, broker status

Diagnostics help troubleshoot issues and identify problems before they
cause outages.`,
		Example: `  # Check network connectivity
  frameworks cluster diagnose network

  # Check resource usage
  frameworks cluster diagnose resources

  # Check Kafka health
  frameworks cluster diagnose kafka`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runDiagnose(cmd, manifestPath, args[0])
		},
	}

	cmd.Flags().StringVar(&manifestPath, "manifest", "cluster.yaml", "Path to cluster manifest file")

	return cmd
}

// runDiagnose executes diagnostic checks
func runDiagnose(cmd *cobra.Command, manifestPath, component string) error {
	// Load manifest
	manifest, err := inventory.Load(manifestPath)
	if err != nil {
		return fmt.Errorf("failed to load manifest: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	// Create SSH pool
	sshPool := ssh.NewPool(30 * time.Second)
	defer sshPool.Close()

	// Execute diagnostic based on component
	switch component {
	case "network":
		return diagnoseNetwork(ctx, cmd, manifest, sshPool)
	case "resources":
		return diagnoseResources(ctx, cmd, manifest, sshPool)
	case "ports":
		return diagnosePorts(ctx, cmd, manifest, sshPool)
	case "kafka":
		return diagnoseKafka(ctx, cmd, manifest, sshPool)
	default:
		return fmt.Errorf("unknown component: %s (must be network, resources, ports, or kafka)", component)
	}
}

// diagnoseNetwork tests network connectivity
func diagnoseNetwork(ctx context.Context, cmd *cobra.Command, manifest *inventory.Manifest, pool *ssh.Pool) error {
	fmt.Fprintln(cmd.OutOrStdout(), "Network Connectivity Diagnostics")

	hosts := make([]inventory.Host, 0, len(manifest.Hosts))
	for _, h := range manifest.Hosts {
		hosts = append(hosts, h)
	}

	// Test connectivity from each host to every other host
	for i, sourceHost := range hosts {
		runner, err := getRunner(sourceHost, pool)
		if err != nil {
			fmt.Fprintf(cmd.OutOrStderr(), "✗ Cannot connect to %s: %v\n", sourceHost.Address, err)
			continue
		}

		for j, targetHost := range hosts {
			if i == j {
				continue // Skip self-ping
			}

			// Ping test
			pingCmd := fmt.Sprintf("ping -c 1 -W 2 %s", targetHost.Address)
			result, err := runner.Run(ctx, pingCmd)

			if err != nil || result.ExitCode != 0 {
				fmt.Fprintf(cmd.OutOrStderr(), "✗ %s → %s: FAILED (no response)\n", sourceHost.Address, targetHost.Address)
			} else {
				fmt.Fprintf(cmd.OutOrStdout(), "✓ %s → %s: OK\n", sourceHost.Address, targetHost.Address)
			}
		}
	}

	return nil
}

// diagnoseResources checks resource usage on all hosts
func diagnoseResources(ctx context.Context, cmd *cobra.Command, manifest *inventory.Manifest, pool *ssh.Pool) error {
	fmt.Fprintln(cmd.OutOrStdout(), "Resource Usage Diagnostics")

	for hostname, host := range manifest.Hosts {
		fmt.Fprintf(cmd.OutOrStdout(), "Host: %s (%s)\n", hostname, host.Address)

		runner, err := getRunner(host, pool)
		if err != nil {
			fmt.Fprintf(cmd.OutOrStderr(), "  ✗ Cannot connect: %v\n\n", err)
			continue
		}

		// CPU usage
		cpuCmd := "top -bn1 | grep 'Cpu(s)' | awk '{print $2}'"
		if result, err := runner.Run(ctx, cpuCmd); err == nil && result.ExitCode == 0 {
			fmt.Fprintf(cmd.OutOrStdout(), "  CPU: %s%% used\n", result.Stdout)
		}

		// Memory usage
		memCmd := "free -h | awk 'NR==2{printf \"  Memory: %s / %s (%.2f%%)\\n\", $3, $2, $3*100/$2}'"
		if result, err := runner.Run(ctx, memCmd); err == nil && result.ExitCode == 0 {
			fmt.Fprint(cmd.OutOrStdout(), result.Stdout)
		}

		// Disk usage
		diskCmd := "df -h / | awk 'NR==2{printf \"  Disk: %s / %s (%s used)\\n\", $3, $2, $5}'"
		if result, err := runner.Run(ctx, diskCmd); err == nil && result.ExitCode == 0 {
			fmt.Fprint(cmd.OutOrStdout(), result.Stdout)
		}

		// Load average
		loadCmd := "uptime | awk -F'load average:' '{print $2}'"
		if result, err := runner.Run(ctx, loadCmd); err == nil && result.ExitCode == 0 {
			fmt.Fprintf(cmd.OutOrStdout(), "  Load:%s\n", result.Stdout)
		}

		fmt.Fprintln(cmd.OutOrStdout(), "")
	}

	return nil
}

// diagnosePorts checks for port conflicts
func diagnosePorts(ctx context.Context, cmd *cobra.Command, manifest *inventory.Manifest, pool *ssh.Pool) error {
	fmt.Fprintln(cmd.OutOrStdout(), "Port Conflict Diagnostics")

	// Check standard ports on each host
	standardPorts := buildStandardPorts()

	for hostname, host := range manifest.Hosts {
		fmt.Fprintf(cmd.OutOrStdout(), "Host: %s (%s)\n", hostname, host.Address)

		runner, err := getRunner(host, pool)
		if err != nil {
			fmt.Fprintf(cmd.OutOrStderr(), "  ✗ Cannot connect: %v\n\n", err)
			continue
		}

		for port, service := range standardPorts {
			checkCmd := fmt.Sprintf("netstat -tuln | grep ':%d ' || echo 'free'", port)
			result, err := runner.Run(ctx, checkCmd)
			if err == nil && result.ExitCode == 0 {
				if result.Stdout == "free\n" {
					fmt.Fprintf(cmd.OutOrStdout(), "  Port %d (%s): FREE\n", port, service)
				} else {
					fmt.Fprintf(cmd.OutOrStdout(), "  Port %d (%s): IN USE\n", port, service)
				}
			}
		}

		fmt.Fprintln(cmd.OutOrStdout(), "")
	}

	return nil
}

func buildStandardPorts() map[int]string {
	standardPorts := map[int]string{
		5353:  "privateer-dns",
		18019: "foghorn-control",
	}

	ids := make([]string, 0, len(servicedefs.Services))
	for id := range servicedefs.Services {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	for _, id := range ids {
		svc := servicedefs.Services[id]
		if svc.DefaultPort != 0 {
			if _, exists := standardPorts[svc.DefaultPort]; !exists {
				standardPorts[svc.DefaultPort] = id
			}
		}
		if grpcPort, ok := servicedefs.DefaultGRPCPort(id); ok {
			if _, exists := standardPorts[grpcPort]; !exists {
				standardPorts[grpcPort] = fmt.Sprintf("%s-grpc", id)
			}
		}
	}

	return standardPorts
}

// diagnoseKafka checks Kafka cluster health
func diagnoseKafka(ctx context.Context, cmd *cobra.Command, manifest *inventory.Manifest, pool *ssh.Pool) error {
	if !manifest.Infrastructure.Kafka.Enabled {
		return fmt.Errorf("kafka not enabled in manifest")
	}

	fmt.Fprintln(cmd.OutOrStdout(), "Kafka Diagnostics")

	// Check first broker
	if len(manifest.Infrastructure.Kafka.Brokers) == 0 {
		return fmt.Errorf("no kafka brokers configured")
	}

	broker := manifest.Infrastructure.Kafka.Brokers[0]
	host, found := manifest.GetHost(broker.Host)
	if !found {
		return fmt.Errorf("broker host not found: %s", broker.Host)
	}

	runner, err := getRunner(host, pool)
	if err != nil {
		return err
	}

	// List topics
	fmt.Fprintln(cmd.OutOrStdout(), "Topics:")
	topicsCmd := "docker compose -f /opt/frameworks/kafka/docker-compose.yml exec -T kafka kafka-topics --bootstrap-server localhost:9092 --list"
	if result, err := runner.Run(ctx, topicsCmd); err == nil && result.ExitCode == 0 {
		fmt.Fprint(cmd.OutOrStdout(), result.Stdout)
	} else {
		fmt.Fprintf(cmd.OutOrStderr(), "  ✗ Failed to list topics: %v\n", err)
	}

	// Check consumer groups
	fmt.Fprintln(cmd.OutOrStdout(), "\nConsumer Groups:")
	groupsCmd := "docker compose -f /opt/frameworks/kafka/docker-compose.yml exec -T kafka kafka-consumer-groups --bootstrap-server localhost:9092 --list"
	if result, err := runner.Run(ctx, groupsCmd); err == nil && result.ExitCode == 0 {
		fmt.Fprint(cmd.OutOrStdout(), result.Stdout)
	} else {
		fmt.Fprintf(cmd.OutOrStderr(), "  ✗ Failed to list consumer groups: %v\n", err)
	}

	// Check broker config
	fmt.Fprintln(cmd.OutOrStdout(), "\nBroker Status:")
	brokerCmd := "docker compose -f /opt/frameworks/kafka/docker-compose.yml exec -T kafka kafka-broker-api-versions --bootstrap-server localhost:9092"
	if result, err := runner.Run(ctx, brokerCmd); err == nil && result.ExitCode == 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "  ✓ Broker is responding")
	} else {
		fmt.Fprintf(cmd.OutOrStderr(), "  ✗ Broker is not responding: %v\n", err)
	}

	return nil
}

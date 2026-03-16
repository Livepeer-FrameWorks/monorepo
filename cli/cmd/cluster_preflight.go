package cmd

import (
	"fmt"
	"runtime"

	"frameworks/cli/internal/preflight"
	"frameworks/cli/pkg/inventory"

	"github.com/spf13/cobra"
)

func newClusterPreflightCmd() *cobra.Command {
	var (
		domain       string
		manifestPath string
	)

	cmd := &cobra.Command{
		Use:   "preflight",
		Short: "Check host readiness for cluster provisioning",
		Long: `Validate that the local host meets requirements for running cluster services:
  - Docker / Docker Compose availability
  - Service manager (systemctl/launchctl)
  - Disk space on / and /var/lib
  - Port availability (80, 443)
  - System limits (ulimit, sysctl, /dev/shm)
  - DNS resolution (optional)
  - Infrastructure connectivity (Postgres, ClickHouse, Kafka, Redis) when --manifest is provided`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			var results []preflight.Check

			// Host checks
			fmt.Fprintln(cmd.OutOrStdout(), "Host checks:")
			if domain != "" {
				results = append(results, preflight.DNSResolution(ctx, domain))
			}
			results = append(results, preflight.HasDocker(ctx)...)
			results = append(results, preflight.HasServiceManager())
			if runtime.GOOS == "linux" {
				results = append(results, preflight.LinuxSysctlChecks()...)
				results = append(results, preflight.ShmSize())
			}
			results = append(results, preflight.UlimitNoFile())
			results = append(results, preflight.PortChecks(ctx)...)
			results = append(results, preflight.DiskSpace("/", minDiskFreeBytes, minDiskFreePercent))
			if runtime.GOOS == "linux" {
				results = append(results, preflight.DiskSpace("/var/lib", minDiskFreeBytes, minDiskFreePercent))
			} else {
				results = append(results, preflight.DiskSpace("/usr/local", minDiskFreeBytes, minDiskFreePercent))
			}

			// Infrastructure connectivity checks (when manifest provided)
			if manifestPath != "" {
				manifest, err := inventory.Load(manifestPath)
				if err != nil {
					return fmt.Errorf("failed to load manifest: %w", err)
				}

				fmt.Fprintln(cmd.OutOrStdout(), "\nInfrastructure connectivity:")
				if pg := manifest.Infrastructure.Postgres; pg != nil && pg.Enabled {
					pgHost := pg.Host
					if host, ok := manifest.GetHost(pg.Host); ok {
						pgHost = host.ExternalIP
					}
					results = append(results, preflight.PostgresConnectivity(ctx, pgHost, pg.EffectivePort()))
				}
				if ch := manifest.Infrastructure.ClickHouse; ch != nil && ch.Enabled {
					chHost := ch.Host
					if host, ok := manifest.GetHost(ch.Host); ok {
						chHost = host.ExternalIP
					}
					port := ch.Port
					if port == 0 {
						port = 9000
					}
					results = append(results, preflight.ClickHouseConnectivity(ctx, chHost, port))
				}
				if k := manifest.Infrastructure.Kafka; k != nil && k.Enabled {
					var brokers []string
					for _, b := range k.Brokers {
						bHost := b.Host
						if host, ok := manifest.GetHost(b.Host); ok {
							bHost = host.ExternalIP
						}
						port := b.Port
						if port == 0 {
							port = 9092
						}
						brokers = append(brokers, fmt.Sprintf("%s:%d", bHost, port))
					}
					results = append(results, preflight.KafkaBrokerHealth(ctx, brokers))
				}
				if r := manifest.Infrastructure.Redis; r != nil && r.Enabled {
					for _, inst := range r.Instances {
						rHost := inst.Host
						if host, ok := manifest.GetHost(inst.Host); ok {
							rHost = host.ExternalIP
						}
						port := inst.Port
						if port == 0 {
							port = 6379
						}
						results = append(results, preflight.RedisConnectivity(ctx, rHost, port))
					}
				}
			}

			// Print results
			okCount := 0
			for _, r := range results {
				mark := "✗"
				if r.OK {
					mark = "✓"
					okCount++
				}
				if r.Error != "" {
					fmt.Fprintf(cmd.OutOrStdout(), " %s %-18s %-40s (%s)\n", mark, r.Name+":", r.Detail, r.Error)
				} else {
					fmt.Fprintf(cmd.OutOrStdout(), " %s %-18s %s\n", mark, r.Name+":", r.Detail)
				}
			}
			fmt.Fprintf(cmd.OutOrStdout(), "\nSummary: %d/%d checks passed\n", okCount, len(results))
			if okCount < len(results) {
				return fmt.Errorf("%d preflight check(s) failed", len(results)-okCount)
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&domain, "domain", "", "Domain to validate DNS resolution")
	cmd.Flags().StringVar(&manifestPath, "manifest", "", "Path to cluster manifest (enables infrastructure connectivity checks)")
	return cmd
}

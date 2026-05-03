package cmd

import (
	"fmt"
	"io"
	"os"
	"runtime"

	fwcfg "frameworks/cli/internal/config"
	"frameworks/cli/internal/preflight"
	"frameworks/cli/internal/ux"
	"frameworks/cli/pkg/clusterderive"
	"frameworks/cli/pkg/inventory"
	pkgdns "frameworks/pkg/dns"

	"github.com/spf13/cobra"
)

func newClusterPreflightCmd() *cobra.Command {
	var domain string

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
  - Infrastructure connectivity (Postgres, ClickHouse, Kafka, Redis) when a
    manifest source is configured (via persistent cluster flags or the
    active context's gitops defaults)`,
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

			if anyManifestSourceConfigured(cmd) {
				rc, err := resolveClusterManifest(cmd)
				if err != nil {
					return err
				}
				defer rc.Cleanup()
				manifest := rc.Manifest

				warnUnassignedClusterScopedBunny(cmd.OutOrStdout(), manifest)

				fmt.Fprintln(cmd.OutOrStdout(), "\nInfrastructure connectivity:")
				if pg := manifest.Infrastructure.Postgres; pg != nil && pg.Enabled {
					pgHost := resolvePostgresConnectivityHost(pg)
					if host, ok := manifest.GetHost(pgHost); ok {
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

			okCount := 0
			for _, r := range results {
				label := r.Name + ":"
				line := fmt.Sprintf("%-18s %s", label, r.Detail)
				if r.Error != "" {
					line = fmt.Sprintf("%-18s %-40s (%s)", label, r.Detail, r.Error)
				}
				switch {
				case r.OK:
					ux.Success(cmd.OutOrStdout(), line)
					okCount++
				default:
					ux.Fail(cmd.OutOrStdout(), line)
				}
			}
			fmt.Fprintf(cmd.OutOrStdout(), "\nSummary: %d/%d checks passed\n", okCount, len(results))

			if okCount < len(results) {
				ux.PrintNextSteps(cmd.OutOrStdout(), []ux.NextStep{
					{Cmd: "frameworks cluster preflight --domain <your.domain>", Why: "Fix the reported items and re-run."},
				})
				return fmt.Errorf("%d preflight check(s) failed", len(results)-okCount)
			}
			ux.PrintNextSteps(cmd.OutOrStdout(), []ux.NextStep{
				{Cmd: "frameworks cluster provision --ready", Why: "All checks passed — provision the cluster and chain init+seed."},
			})
			return nil
		},
	}

	cmd.Flags().StringVar(&domain, "domain", "", "Domain to validate DNS resolution")
	return cmd
}

// warnUnassignedClusterScopedBunny prints a warning for each enabled
// cluster-scoped Bunny service (foghorn, chandler, livepeer-gateway) that
// resolves to zero logical media clusters. Triggered by manifests with no
// media/edge cluster, or with multiple media clusters and no `default: true`
// flag, since the assignment reconciler then has no target to pick.
func warnUnassignedClusterScopedBunny(out io.Writer, manifest *inventory.Manifest) {
	if manifest == nil {
		return
	}
	any := false
	for name, svc := range manifest.Services {
		if !svc.Enabled {
			continue
		}
		if !pkgdns.IsPoolAssignedServiceType(name) {
			continue
		}
		if len(clusterderive.LogicalServiceClusterIDs(name, svc, manifest)) == 0 {
			if !any {
				fmt.Fprintln(out, "\nMedia-cluster assignment warnings:")
				any = true
			}
			ux.Fail(out, fmt.Sprintf("%s has no logical media-cluster target — set services.%s.cluster(s) or mark a media cluster default: true", name, name))
		}
	}
}

func resolvePostgresConnectivityHost(pg *inventory.PostgresConfig) string {
	if pg == nil {
		return ""
	}
	if hosts := pg.AllHosts(); len(hosts) > 0 {
		return hosts[0]
	}
	return pg.Host
}

// anyManifestSourceConfigured reports whether resolveClusterManifest
// would succeed for the given flags/env/context. Preflight uses this to
// decide whether to run infrastructure-connectivity checks (which need a
// manifest) — preflight itself does not require one.
func anyManifestSourceConfigured(cmd *cobra.Command) bool {
	cfg, _ := fwcfg.Load() //nolint:errcheck
	rt := fwcfg.GetRuntimeOverrides()
	ctxCfg, _ := fwcfg.MaybeActiveContext(rt, fwcfg.OSEnv{}, cfg) //nolint:errcheck

	cwd, _ := os.Getwd() //nolint:errcheck
	in := inventory.ResolveInput{
		Manifest:    stringFlag(cmd, "manifest"),
		GitopsDir:   stringFlag(cmd, "gitops-dir"),
		GithubRepo:  stringFlag(cmd, "github-repo"),
		GithubRef:   stringFlag(cmd, "github-ref"),
		Cluster:     stringFlag(cmd, "cluster"),
		AgeKey:      stringFlag(cmd, "age-key"),
		GithubAppID: int64Flag(cmd, "github-app-id"),
		GithubInst:  int64Flag(cmd, "github-installation-id"),
		GithubKey:   stringFlag(cmd, "github-private-key"),
		Env:         fwcfg.OSEnv{},
		Context:     ctxCfg,
		GitHubCfg:   cfg.GitHub,
		Cwd:         cwd,
		Stdout:      io.Discard,
		Ctx:         cmd.Context(),
	}
	if rm, err := inventory.ResolveManifestSource(in); err == nil {
		if rm.Cleanup != nil {
			rm.Cleanup()
		}
		return true
	}
	if ctxCfg.LastManifestPath != "" {
		if _, statErr := os.Stat(ctxCfg.LastManifestPath); statErr == nil {
			return true
		}
	}
	return false
}

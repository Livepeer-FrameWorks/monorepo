package cmd

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	"frameworks/cli/internal/ux"
	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/orchestrator"
	"frameworks/cli/pkg/provisioner"
	"frameworks/cli/pkg/ssh"

	"github.com/spf13/cobra"
)

// newClusterRestartCmd creates the restart command
func newClusterRestartCmd() *cobra.Command {
	var validate bool

	cmd := &cobra.Command{
		Use:   "restart <service>",
		Short: "Restart a service",
		Long: `Restart a service running on the cluster via its Ansible role.

Each role's tasks/restart.yml knows the correct unit or compose names for
its managed service(s): clickhouse-server, postgresql, yb-master + yb-tserver,
caddy, frameworks-kafka, docker-compose stacks, etc. Cluster restart
type-asserts the service's provisioner to Restarter and delegates there.

After restart, pass --validate to run the role's tasks/validate.yml (port
probes, /health check, etc.) to confirm the service came back healthy.`,
		Example: `  frameworks cluster restart quartermaster
  frameworks cluster restart bridge --validate`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			rc, err := resolveClusterManifest(cmd)
			if err != nil {
				return err
			}
			defer rc.Cleanup()
			return runRestart(cmd, rc, args[0], validate)
		},
	}

	cmd.Flags().BoolVar(&validate, "validate", false, "Validate service health after restart via the role's validate tag")

	return cmd
}

// runRestart executes the restart via the service's Restarter interface so
// the role picks the right systemd unit or compose project without the CLI
// having to guess frameworks-<service>.
func runRestart(cmd *cobra.Command, rc *resolvedCluster, serviceName string, validate bool) error {
	manifest := rc.Manifest
	var err error

	var deployName string
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
	} else {
		deployName = serviceName // infrastructure services use canonical IDs
	}

	host, found := resolveServiceHost(manifest, serviceName)
	if !found {
		return fmt.Errorf("service %s not found or not enabled in manifest", serviceName)
	}

	ux.Heading(cmd.OutOrStdout(), fmt.Sprintf("Restarting %s on %s", serviceName, host.ExternalIP))

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	sshKey := stringFlag(cmd, "ssh-key").Value
	sshPool := ssh.NewPool(30*time.Second, sshKey)
	defer sshPool.Close()

	prov, err := provisioner.GetProvisioner(deployName, sshPool)
	if err != nil {
		return fmt.Errorf("get provisioner for %s: %w", deployName, err)
	}
	restarter, ok := prov.(provisioner.Restarter)
	if !ok {
		return fmt.Errorf("provisioner for %s does not support role-based restart", deployName)
	}

	// Build the same ServiceConfig surface provision would pass so the
	// role's restart.yml has access to env-derived unit names, ports, etc.
	task := &orchestrator.Task{
		Name:       serviceName,
		Type:       deployName,
		ServiceID:  serviceName,
		Host:       host.Name,
		Phase:      orchestrator.PhaseApplications,
		Idempotent: true,
	}
	manifestDir := filepath.Dir(rc.ManifestPath)
	sharedEnv, envErr := rc.SharedEnv()
	if envErr != nil {
		fmt.Fprintf(cmd.OutOrStderr(), "  warning: shared env decrypt failed: %v\n", envErr)
		sharedEnv = nil
	}
	config, cfgErr := buildTaskConfig(task, manifest, map[string]any{}, false, manifestDir, sharedEnv, rc.ReleaseRepos)
	if cfgErr != nil {
		return fmt.Errorf("build restart config: %w", cfgErr)
	}
	rc.applyReleaseMetadata(config.Metadata)

	if err := restarter.Restart(ctx, host, config); err != nil {
		return fmt.Errorf("restart %s: %w", serviceName, err)
	}

	ux.Success(cmd.OutOrStdout(), fmt.Sprintf("%s restarted", serviceName))

	if validate {
		fmt.Fprintln(cmd.OutOrStdout(), "Validating service health...")
		time.Sleep(3 * time.Second)
		if err := prov.Validate(ctx, host, config); err != nil {
			ux.Fail(cmd.ErrOrStderr(), fmt.Sprintf("Validation failed: %v", err))
			return fmt.Errorf("service restarted but health check failed")
		}
		fmt.Fprintf(cmd.OutOrStdout(), "  ✓ Service is healthy\n")
	}

	return nil
}

// resolveServiceHost walks the manifest in the same order as upgrade +
// provision: infrastructure first, then application services, interfaces,
// observability.
func resolveServiceHost(manifest *inventory.Manifest, serviceName string) (inventory.Host, bool) {
	switch serviceName {
	case "postgres":
		if pg := manifest.Infrastructure.Postgres; pg != nil && pg.Enabled {
			return manifest.GetHost(pg.Host)
		}
	case "yugabyte":
		if pg := manifest.Infrastructure.Postgres; pg != nil && pg.Enabled && pg.IsYugabyte() && len(pg.Nodes) > 0 {
			return manifest.GetHost(pg.Nodes[0].Host)
		}
	case "kafka":
		if k := manifest.Infrastructure.Kafka; k != nil && k.Enabled && len(k.Brokers) > 0 {
			return manifest.GetHost(k.Brokers[0].Host)
		}
	case "kafka-controller":
		if k := manifest.Infrastructure.Kafka; k != nil && k.Enabled && len(k.Controllers) > 0 {
			return manifest.GetHost(k.Controllers[0].Host)
		}
	case "zookeeper":
		if z := manifest.Infrastructure.Zookeeper; z != nil && z.Enabled && len(z.Ensemble) > 0 {
			return manifest.GetHost(z.Ensemble[0].Host)
		}
	case "clickhouse":
		if ch := manifest.Infrastructure.ClickHouse; ch != nil && ch.Enabled {
			return manifest.GetHost(ch.Host)
		}
	case "redis":
		if r := manifest.Infrastructure.Redis; r != nil && r.Enabled && len(r.Instances) > 0 {
			return manifest.GetHost(r.Instances[0].Host)
		}
	}

	if svc, ok := manifest.Services[serviceName]; ok && svc.Enabled {
		if h, found := manifest.GetHost(svc.Host); found {
			return h, true
		}
	}
	if iface, ok := manifest.Interfaces[serviceName]; ok && iface.Enabled {
		if h, found := manifest.GetHost(iface.Host); found {
			return h, true
		}
	}
	if obs, ok := manifest.Observability[serviceName]; ok && obs.Enabled {
		if h, found := manifest.GetHost(obs.Host); found {
			return h, true
		}
	}
	return inventory.Host{}, false
}

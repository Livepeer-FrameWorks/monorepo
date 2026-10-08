package cmd

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	"frameworks/cli/internal/ux"
	"frameworks/cli/pkg/detect"
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
	if (serviceName == "yugabyte" || serviceName == "postgres") && hasUnscopedDatabaseDeployments(rc.Manifest) {
		return forEachDatabaseDeployment(rc.Manifest, func(view *inventory.Manifest) error {
			if (serviceName == "yugabyte") != view.Infrastructure.Postgres.IsYugabyte() {
				return nil
			}
			return runRestart(cmd, rc.withDatabaseManifest(view), serviceName, validate)
		})
	}
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

	if serviceName == "yugabyte" {
		return runYugabyteRollingRestart(cmd, rc, deployName, validate)
	}

	hosts, err := resolveRestartHosts(manifest, serviceName)
	if err != nil {
		return err
	}
	for _, host := range hosts {
		if err := restartServiceOnHost(cmd, rc, serviceName, deployName, host, validate); err != nil {
			return err
		}
	}
	return nil
}

func restartServiceOnHost(cmd *cobra.Command, rc *resolvedCluster, serviceName, deployName string, host inventory.Host, validate bool) error {
	ux.Heading(cmd.OutOrStdout(), fmt.Sprintf("Restarting %s on %s", serviceName, host.ExternalIP))

	ctx, cancel := context.WithTimeout(cmd.Context(), restartCommandTimeout)
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

	targets, err := buildServiceRoleTargets(cmd, rc, serviceName, deployName, host)
	if err != nil {
		return fmt.Errorf("restart %s: %w", serviceName, err)
	}

	for _, target := range targets {
		label := serviceName
		if len(targets) > 1 {
			label = target.task.Name
		}
		config := target.config
		if validate {
			// Restart deploys nothing, so the gate must probe the release already on the host, not the channel's newest.
			state, detectErr := provisioner.DetectWithConfig(ctx, prov, host, config)
			if detectErr != nil {
				return fmt.Errorf("detect installed %s before restart: %w", label, detectErr)
			}
			config = probeInstalledRelease(config, state)
		}

		if err := restarter.Restart(ctx, host, config); err != nil {
			return fmt.Errorf("restart %s: %w", label, err)
		}

		ux.Success(cmd.OutOrStdout(), fmt.Sprintf("%s restarted", label))

		if validate {
			fmt.Fprintln(cmd.OutOrStdout(), "Validating service health...")
			time.Sleep(3 * time.Second)
			if err := prov.Validate(ctx, host, config); err != nil {
				ux.Fail(cmd.ErrOrStderr(), fmt.Sprintf("Validation failed: %v", err))
				return fmt.Errorf("%s restarted but health check failed", label)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "  ✓ Service is healthy\n")
		}
	}

	return nil
}

func resolveRestartHosts(manifest *inventory.Manifest, serviceName string) ([]inventory.Host, error) {
	for _, group := range []map[string]inventory.ServiceConfig{manifest.Services, manifest.Interfaces, manifest.Observability} {
		if svc, ok := group[serviceName]; ok && svc.Enabled {
			names := svc.Hosts
			if len(names) == 0 && svc.Host != "" {
				names = []string{svc.Host}
			}
			if len(names) == 0 {
				return nil, fmt.Errorf("service %s has no configured hosts", serviceName)
			}
			var hosts []inventory.Host
			seen := map[string]bool{}
			for _, name := range names {
				if seen[name] {
					continue
				}
				host, ok := manifest.GetHost(name)
				if !ok {
					return nil, fmt.Errorf("service %s: unknown host %s", serviceName, name)
				}
				host.Name = firstNonEmpty(host.Name, name)
				hosts = append(hosts, host)
				seen[name] = true
			}
			return hosts, nil
		}
	}
	if host, found := resolveServiceHost(manifest, serviceName); found {
		return []inventory.Host{host}, nil
	}
	return nil, fmt.Errorf("service %s not found or not enabled in manifest", serviceName)
}

// restartCommandTimeout bounds cluster restart: the role restart, host
// detection, the settle pause, and a validate that may wait the full rollout
// readiness window.
const restartCommandTimeout = provisioner.RolloutReadinessTimeout + 2*time.Minute

// probeInstalledRelease makes config's rollout gate follow the release
// detected on the host. A missing detection leaves InstalledVersion empty,
// which gates on liveness.
func probeInstalledRelease(config provisioner.ServiceConfig, state *detect.ServiceState) provisioner.ServiceConfig {
	config.ProbeInstalled = true
	config.InstalledVersion = ""
	if state != nil {
		config.InstalledVersion = state.Version
	}
	return config
}

// serviceRoleTarget is one planned deployment of a service on a host and the ServiceConfig provision renders for it.
type serviceRoleTarget struct {
	task   *orchestrator.Task
	config provisioner.ServiceConfig
}

// buildServiceRoleTargets renders, for every planner task that deploys the service on the host, the ServiceConfig
// provision passes, so a role's restart, stop and validate tasks see the same cluster, cell, instance, unit names and
// ports. A host with several instances of one infrastructure service yields one target per instance.
func buildServiceRoleTargets(cmd *cobra.Command, rc *resolvedCluster, serviceName, deployName string, host inventory.Host) ([]serviceRoleTarget, error) {
	manifest := rc.Manifest
	tasks, err := plannedServiceTasks(manifest, serviceName, deployName, host.Name)
	if err != nil {
		return nil, err
	}
	if len(tasks) == 0 {
		return nil, fmt.Errorf("the manifest plans no %s (%s) task on %s", serviceName, deployName, host.Name)
	}
	manifestDir := filepath.Dir(rc.ManifestPath)
	sharedEnv, envErr := rc.PreparedSharedEnv()
	if envErr != nil {
		return nil, fmt.Errorf("prepare shared environment: %w", envErr)
	}
	clusterEnvs, clusterEnvsErr := rc.ClusterEnvs()
	if clusterEnvsErr != nil {
		fmt.Fprintf(cmd.OutOrStderr(), "  warning: cluster env decrypt failed: %v\n", clusterEnvsErr)
		clusterEnvs = nil
	}
	targets := make([]serviceRoleTarget, 0, len(tasks))
	for _, task := range tasks {
		config, cfgErr := renderTaskConfig(task, manifest, false, map[string]any{}, manifestDir, sharedEnv, clusterEnvs, rc.ReleaseRepos)
		if cfgErr != nil {
			return nil, fmt.Errorf("build role config for %s: %w", task.Name, cfgErr)
		}
		if missing := missingRequiredExternalEnv(deployName, config.EnvVars); len(missing) > 0 {
			return nil, requiredEnvPreflightError([]requiredEnvGap{{Target: fmt.Sprintf("%s on %s", task.Name, host.Name), Missing: missing}})
		}
		rc.applyReleaseMetadata(config.Metadata)
		targets = append(targets, serviceRoleTarget{task: task, config: config})
	}
	return targets, nil
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
	case "clickhouse":
		if ch := manifest.Infrastructure.ClickHouse; ch != nil && ch.Enabled {
			return manifest.GetHost(ch.CoordinatorHost())
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

// yugabyteRollingRestartDeadline bounds a rolling restart of nodes nodes. Each node may wait for the masters to allow
// its restart and then for its own recovery, each up to the roll's recovery timeout, on top of the restart itself.
func yugabyteRollingRestartDeadline(nodes int) time.Duration {
	return time.Duration(nodes) * (2*yugabyteRollRecoveryTimeout + 5*time.Minute)
}

// runYugabyteRollingRestart restarts every Yugabyte node, one at a time, through the roll's gate: nodes that are down
// first, each serving node only once the masters confirm the universe survives losing it, and the next node only after
// the restarted one has recovered. The first failure stops the restart.
func runYugabyteRollingRestart(cmd *cobra.Command, rc *resolvedCluster, deployName string, validate bool) error {
	manifest := rc.Manifest
	sshKey := stringFlag(cmd, "ssh-key").Value
	sshPool := ssh.NewPool(30*time.Second, sshKey)
	defer sshPool.Close()

	roll := newYugabyteGate(cmd.OutOrStdout(), manifest, sshPool)
	if roll == nil {
		return fmt.Errorf("service yugabyte not found or not enabled in manifest")
	}
	ctx, cancel := context.WithTimeout(context.Background(), yugabyteRollingRestartDeadline(len(roll.hosts)))
	defer cancel()

	prov, err := provisioner.GetProvisioner(deployName, sshPool)
	if err != nil {
		return fmt.Errorf("get provisioner for %s: %w", deployName, err)
	}
	restarter, ok := prov.(provisioner.Restarter)
	if !ok {
		return fmt.Errorf("provisioner for %s does not support role-based restart", deployName)
	}
	manifestDir := filepath.Dir(rc.ManifestPath)
	sharedEnv, envErr := rc.PreparedSharedEnv()
	if envErr != nil {
		return fmt.Errorf("prepare shared environment: %w", envErr)
	}
	clusterEnvs, clusterEnvsErr := rc.ClusterEnvs()
	if clusterEnvsErr != nil {
		fmt.Fprintf(cmd.OutOrStderr(), "  warning: cluster env decrypt failed: %v\n", clusterEnvsErr)
		clusterEnvs = nil
	}

	names := make([]string, 0, len(roll.hosts))
	for _, host := range roll.hosts {
		roll.serving[host.Name] = roll.u.ServesYSQL(ctx, host)
		names = append(names, host.Name)
	}
	roll.setOrder(names)
	ux.Heading(cmd.OutOrStdout(), fmt.Sprintf("Restarting yugabyte on %d node(s), one at a time", len(roll.hosts)))
	for _, name := range roll.order {
		host, found := manifest.GetHost(name)
		if !found {
			return fmt.Errorf("yugabyte node %s not found in manifest", name)
		}
		// The planner's node task carries the node id the role derives its placement from.
		tasks, planErr := plannedServiceTasks(manifest, "yugabyte", deployName, host.Name)
		if planErr != nil {
			return planErr
		}
		if len(tasks) != 1 {
			return fmt.Errorf("the manifest plans %d yugabyte node task(s) on %s, want 1", len(tasks), host.Name)
		}
		config, cfgErr := renderTaskConfig(tasks[0], manifest, false, map[string]any{}, manifestDir, sharedEnv, clusterEnvs, rc.ReleaseRepos)
		if cfgErr != nil {
			return fmt.Errorf("build restart config for %s: %w", host.Name, cfgErr)
		}
		rc.applyReleaseMetadata(config.Metadata)
		fmt.Fprintf(cmd.OutOrStdout(), "\n--- %s ---\n", host.Name)
		err := roll.change(ctx, host, func(gate func() error) error {
			if gateErr := gate(); gateErr != nil {
				return gateErr
			}
			if restartErr := restarter.Restart(ctx, host, config); restartErr != nil {
				return fmt.Errorf("restart yugabyte on %s: %w", host.Name, restartErr)
			}
			if validate {
				if validateErr := prov.Validate(ctx, host, config); validateErr != nil {
					return fmt.Errorf("yugabyte on %s restarted but health check failed: %w", host.Name, validateErr)
				}
			}
			return nil
		})
		if err != nil {
			return err
		}
	}
	ux.Success(cmd.OutOrStdout(), fmt.Sprintf("yugabyte restarted on %d node(s)", len(roll.hosts)))
	return nil
}

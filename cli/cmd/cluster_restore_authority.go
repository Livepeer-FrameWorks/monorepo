package cmd

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/ssh"
	"github.com/spf13/cobra"
)

type authorityRestoreReplica struct {
	Service string
	Host    inventory.Host
}

// Resolve every replica before stopping any of them. An incomplete manifest
// is not safe to use for a database replacement.
func authorityRestoreReplicas(manifest *inventory.Manifest) ([]authorityRestoreReplica, error) {
	var replicas []authorityRestoreReplica
	for name, service := range manifest.Services {
		if !service.Enabled {
			continue
		}
		deploy, err := resolveDeployName(name, service)
		if err != nil {
			return nil, err
		}
		if deploy != "foghorn" {
			continue
		}
		if service.Mode != "" && service.Mode != "native" {
			return nil, fmt.Errorf("restore fencing requires native Foghorn units; %s uses %s", name, service.Mode)
		}
		hosts := serviceHosts(service)
		if len(hosts) == 0 {
			return nil, fmt.Errorf("foghorn %s has no replica hosts", name)
		}
		for _, hostname := range hosts {
			host, ok := manifest.GetHost(hostname)
			if !ok {
				return nil, fmt.Errorf("foghorn %s replica host %s is absent", name, hostname)
			}
			host.Name = hostname
			replicas = append(replicas, authorityRestoreReplica{Service: name, Host: host})
		}
	}
	sort.Slice(replicas, func(i, j int) bool {
		if replicas[i].Service != replicas[j].Service {
			return replicas[i].Service < replicas[j].Service
		}
		return replicas[i].Host.Name < replicas[j].Host.Name
	})
	return replicas, nil
}

func authorityRestoreReplicaCommand(action string) string {
	// The go_service role names the unit after the deploy ID, not the
	// manifest alias (for example foghorn-us).
	unit := ssh.ShellQuote("frameworks-foghorn.service")
	command := "set -e\n"
	command += "test \"$(systemctl show " + unit + " -p LoadState --value)\" = loaded\n"
	if action == "stop" || action == "start" {
		command += "if [ \"$(id -u)\" = 0 ]; then systemctl " + action + " " + unit + "; else sudo -n systemctl " + action + " " + unit + "; fi\n"
	}
	if action != "start" {
		command += "test \"$(systemctl show " + unit + " -p ActiveState --value)\" = inactive\n"
		command += "test \"$(systemctl show " + unit + " -p MainPID --value)\" = 0\n"
	}
	return command
}

func operateAuthorityRestoreReplicas(ctx context.Context, replicas []authorityRestoreReplica, pool *ssh.Pool, action string) error {
	for _, replica := range replicas {
		runner, err := getRunner(replica.Host, pool)
		if err != nil {
			return err
		}
		result, err := runner.Run(ctx, authorityRestoreReplicaCommand(action))
		if err != nil || result == nil || result.ExitCode != 0 {
			return fmt.Errorf("restore %s %s on %s: %s; inspect replica states before proceeding", action, replica.Service, replica.Host.Name, diagnosticCommandError(result, err))
		}
	}
	return nil
}

func executeAuthorityRestorePhase(phase string, replicas func(string) error, fence func() error) error {
	action := "stop"
	if phase == "complete" {
		action = "check"
	} else if phase != "prepare" {
		return fmt.Errorf("invalid restore phase %q", phase)
	}
	if err := replicas(action); err != nil {
		return err
	}
	if err := fence(); err != nil {
		return err
	}
	if phase == "complete" {
		return replicas("start")
	}
	return nil
}

func newClusterRestoreFenceCmd() *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "restore-fence <prepare|complete>",
		Short: "Stop cell replicas and safely fence a native PostgreSQL/Yugabyte restore",
		Long: `Use the complete manifest containing every Foghorn replica sharing the restored databases.
prepare stops all Foghorn replicas and raises their durable authority fences.
Perform the database-native restore while all replicas remain stopped.
complete verifies every replica is stopped, fences the restored databases, then starts all replicas.
Any failure before all fences are installed leaves replicas stopped. Never restart them manually between these steps.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if args[0] != "prepare" && args[0] != "complete" {
				return fmt.Errorf("expected prepare or complete")
			}
			if !yes {
				return fmt.Errorf("restore fencing interrupts all Foghorn replicas; review the manifest and pass --yes")
			}
			rc, err := resolveClusterManifest(cmd)
			if err != nil {
				return err
			}
			defer rc.Cleanup()
			replicas, err := authorityRestoreReplicas(rc.Manifest)
			if err != nil {
				return err
			}
			if len(replicas) == 0 {
				return fmt.Errorf("no Foghorn replicas found in manifest")
			}
			targets, err := postgresSnapshotTargets(rc)
			if err != nil {
				return err
			}
			probes := make(map[int][]mediaAuthorityDatabaseProbe)
			for i, target := range targets {
				for _, database := range target.Databases {
					if database == "foghorn" || strings.HasPrefix(database, "foghorn_") {
						// Unlike a mixed legacy dump, a named cell database must have
						// the table: a missing migration must fail before any restart.
						probes[i] = append(probes[i], mediaAuthorityDatabaseProbe{Database: database, SQL: `INSERT INTO foghorn.media_authority_restore_fence (singleton, fenced_at) VALUES (TRUE, clock_timestamp()) ON CONFLICT (singleton) DO UPDATE SET fenced_at = clock_timestamp();`})
					}
				}
			}
			if len(probes) == 0 {
				return fmt.Errorf("no Foghorn databases found in manifest")
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 5*time.Minute)
			defer cancel()
			pool := ssh.NewPool(30*time.Second, stringFlag(cmd, "ssh-key").Value)
			defer pool.Close()
			err = executeAuthorityRestorePhase(args[0], func(action string) error {
				return operateAuthorityRestoreReplicas(ctx, replicas, pool, action)
			}, func() error {
				for i, target := range targets {
					if len(probes[i]) == 0 {
						continue
					}
					runner, runnerErr := getRunner(target.Host, pool)
					if runnerErr != nil {
						return runnerErr
					}
					result, runErr := runner.RunScript(ctx, mediaAuthorityDatabaseScript(target, probes[i]))
					if runErr != nil || result == nil || result.ExitCode != 0 {
						return fmt.Errorf("fence restored databases on %s: %s; Foghorn remains stopped", target.HostName, diagnosticCommandError(result, runErr))
					}
				}
				return nil
			})
			if err != nil {
				return err
			}
			if args[0] == "complete" {
				fmt.Fprintln(cmd.OutOrStdout(), "Restored databases fenced; Foghorn replicas started. Run cluster doctor to check recovery convergence.")
			} else {
				fmt.Fprintln(cmd.OutOrStdout(), "All Foghorn replicas stopped and fenced. Perform the database restore, then run cluster restore-fence complete --yes with this same manifest.")
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "Confirm the complete replica manifest and service interruption")
	return cmd
}

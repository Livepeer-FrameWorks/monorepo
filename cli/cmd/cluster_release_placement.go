package cmd

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"frameworks/cli/pkg/orchestrator"
	"frameworks/cli/pkg/remoteaccess"
	"frameworks/cli/pkg/ssh"

	"github.com/spf13/cobra"
)

// reconcileReleasePlacements runs after every desired release replica is healthy. This ordering lets a host move
// overlap old and new service instances during deployment, then removes the stale managed placement only after the
// replacement is serving. A dry-run reads the same authoritative registry and reports the cleanup without mutating it.
func reconcileReleasePlacements(ctx context.Context, cmd *cobra.Command, rc *resolvedCluster, installed map[string]struct{}, dryRun bool) error {
	sharedEnv, err := rc.PreparedSharedEnv()
	if err != nil {
		return fmt.Errorf("load manifest env_files: %w", err)
	}
	serviceToken := strings.TrimSpace(sharedEnv["SERVICE_TOKEN"])
	if serviceToken == "" {
		return fmt.Errorf("SERVICE_TOKEN missing from manifest env_files")
	}

	sshKey := stringFlag(cmd, "ssh-key").Value
	sess, err := remoteaccess.OpenSession(remoteaccess.Options{
		Manifest:      rc.Manifest,
		SSHKeyPath:    sshKey,
		AllowInsecure: isDevProfile(rc.Manifest),
	})
	if err != nil {
		return fmt.Errorf("open remote-access session: %w", err)
	}
	defer sess.Close()

	runtimeData := map[string]any{"service_token": serviceToken}
	client, err := newQuartermasterClient(ctx, rc.Manifest, runtimeData, sess)
	if err != nil {
		return fmt.Errorf("connect Quartermaster: %w", err)
	}
	defer client.Close()

	placements, err := removedServicePlacements(ctx, rc.Manifest, orchestrator.PhaseAll, client)
	if err != nil {
		return err
	}

	if dryRun {
		if len(installed) > 0 {
			fmt.Fprintln(cmd.OutOrStdout(), "New service placement plan:")
			services := make([]string, 0, len(installed))
			for serviceName := range installed {
				services = append(services, serviceName)
			}
			sort.Strings(services)
			for _, serviceName := range services {
				fmt.Fprintf(cmd.OutOrStdout(), "  - %s: would install on its desired host set before retiring stale placements\n", serviceName)
			}
			fmt.Fprintln(cmd.OutOrStdout())
		}
		writeRemovedServicePlacementDryRunPlan(cmd.OutOrStdout(), placements)
		return nil
	}

	if len(placements) == 0 {
		return nil
	}

	pool := ssh.NewPool(30*time.Second, sshKey)
	defer pool.Close()
	cleanup := func(cleanupCtx context.Context, placement removedServicePlacement) error {
		return cleanupRemovedServicePlacement(cleanupCtx, rc.Manifest, pool, placement)
	}
	return reconcileRemovedServicePlacementsWithClient(ctx, cmd.OutOrStdout(), rc.Manifest, orchestrator.PhaseAll, client, cleanup)
}

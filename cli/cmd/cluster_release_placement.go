package cmd

import (
	"context"
	"fmt"
	"io"
	"sort"
	"time"

	"frameworks/cli/pkg/orchestrator"
	"frameworks/cli/pkg/ssh"

	"github.com/spf13/cobra"
)

// reconcileReleasePlacements runs after every desired release replica is healthy. This ordering lets a host move
// overlap old and new service instances during deployment, then removes the stale managed placement only after the
// replacement is serving. A dry-run reads the same authoritative registry and reports the cleanup without mutating it.
func reconcileReleasePlacements(ctx context.Context, cmd *cobra.Command, rc *resolvedCluster, installed map[string]struct{}, dryRun bool) error {
	sshKey := stringFlag(cmd, "ssh-key").Value
	return retryReleasePlacementReconciliation(ctx, cmd.OutOrStdout(), func() error {
		return reconcileReleasePlacementsOnce(ctx, cmd, rc, installed, dryRun, sshKey)
	})
}

func reconcileReleasePlacementsOnce(ctx context.Context, cmd *cobra.Command, rc *resolvedCluster, installed map[string]struct{}, dryRun bool, sshKey string) error {
	// Production service certificates chain to the internal CA, so this path must use the same resolved endpoint and
	// trust bundle as the rest of the release executor.
	client, _, cleanupSession, err := buildReconcileQM(ctx, rc)
	if err != nil {
		return fmt.Errorf("connect Quartermaster: %w", err)
	}
	defer cleanupSession()
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
	cleanupPlacement := func(cleanupCtx context.Context, placement removedServicePlacement) error {
		return cleanupRemovedServicePlacement(cleanupCtx, rc.Manifest, pool, placement)
	}
	return reconcileRemovedServicePlacementsWithClient(ctx, cmd.OutOrStdout(), rc.Manifest, orchestrator.PhaseAll, client, cleanupPlacement)
}

func retryReleasePlacementReconciliation(ctx context.Context, out io.Writer, fn func() error) error {
	return retryReleasePlacementReconciliationWithBackoff(ctx, out, 6, 5*time.Second, fn)
}

func retryReleasePlacementReconciliationWithBackoff(ctx context.Context, out io.Writer, attempts int, delay time.Duration, fn func() error) error {
	var err error
	for attempt := 1; attempt <= attempts; attempt++ {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		err = fn()
		if err == nil || !isRetryableControlPlaneRPCError(err) || attempt == attempts {
			return err
		}
		fmt.Fprintf(out, "  Retrying service placement reconciliation with a fresh control-plane connection (%d/%d): %v\n", attempt, attempts-1, err)
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	return err
}

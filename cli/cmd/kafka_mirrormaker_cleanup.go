package cmd

import (
	"context"
	"fmt"
	"io"
	"slices"
	"sort"

	"frameworks/cli/internal/ux"
	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/orchestrator"
	"frameworks/cli/pkg/provisioner"
	"frameworks/cli/pkg/ssh"

	infra "github.com/Livepeer-FrameWorks/monorepo/pkg/models"
)

// kafkaMirrorMakerWorkerProbe reports whether host runs a MirrorMaker2 worker.
type kafkaMirrorMakerWorkerProbe func(ctx context.Context, host inventory.Host) (bool, error)

// kafkaMirrorMakerWorkerRemover stops and removes the worker on host.
type kafkaMirrorMakerWorkerRemover func(ctx context.Context, host inventory.Host) error

// staleKafkaMirrorMakerWorkerCandidates returns the hosts that may still run a
// MirrorMaker2 worker the manifest no longer declares: every non-edge host that
// is not a link worker, or every non-edge host when MirrorMaker2 is disabled.
// Quartermaster does not register MirrorMaker2 workers, so the hosts are probed
// rather than read from the service registry. A manifest without Kafka has no
// candidates.
func staleKafkaMirrorMakerWorkerCandidates(manifest *inventory.Manifest) []string {
	if manifest == nil || manifest.Infrastructure.Kafka == nil {
		return nil
	}
	desired := orchestrator.KafkaMirrorMakerHosts(manifest)
	var candidates []string
	for name, host := range manifest.Hosts {
		if slices.Contains(host.Roles, infra.NodeTypeEdge) || slices.Contains(desired, name) {
			continue
		}
		candidates = append(candidates, name)
	}
	sort.Strings(candidates)
	return candidates
}

// findStaleKafkaMirrorMakerWorkers probes every candidate host and returns the
// ones that run a worker. A probe failure fails the scan: an unreachable host
// may still be replicating under a link the manifest removed.
func findStaleKafkaMirrorMakerWorkers(ctx context.Context, manifest *inventory.Manifest, probe kafkaMirrorMakerWorkerProbe) ([]string, error) {
	var stale []string
	for _, name := range staleKafkaMirrorMakerWorkerCandidates(manifest) {
		host, ok := manifest.GetHost(name)
		if !ok {
			continue
		}
		present, err := probe(ctx, host)
		if err != nil {
			return nil, fmt.Errorf("probe kafka-mirrormaker worker on %s: %w", name, err)
		}
		if present {
			stale = append(stale, name)
		}
	}
	return stale, nil
}

// reconcileStaleKafkaMirrorMakerWorkers removes MirrorMaker2 workers from hosts
// the manifest no longer lists as link workers. It runs after the desired
// workers are converged, so a link moving between hosts starts on the new
// hosts before the old ones stop. With dryRun it prints the removals only.
func reconcileStaleKafkaMirrorMakerWorkers(ctx context.Context, out io.Writer, manifest *inventory.Manifest, probe kafkaMirrorMakerWorkerProbe, remove kafkaMirrorMakerWorkerRemover, dryRun bool) error {
	stale, err := findStaleKafkaMirrorMakerWorkers(ctx, manifest, probe)
	if err != nil {
		return err
	}
	if dryRun {
		fmt.Fprintln(out, "Removed kafka-mirrormaker worker plan:")
		if len(stale) == 0 {
			fmt.Fprintln(out, "  - no kafka-mirrormaker workers outside the declared links")
		}
		for _, name := range stale {
			fmt.Fprintf(out, "  - kafka-mirrormaker on %s: would stop and remove the worker\n", name)
		}
		fmt.Fprintln(out)
		return nil
	}
	for _, name := range stale {
		host, _ := manifest.GetHost(name)
		if err := remove(ctx, host); err != nil {
			return fmt.Errorf("remove kafka-mirrormaker worker on %s: %w", name, err)
		}
		ux.Success(out, fmt.Sprintf("kafka-mirrormaker removed from %s", name))
	}
	return nil
}

// reconcileStaleKafkaMirrorMakerWorkersOverSSH wires the scan to the
// kafka-mirrormaker role: its detector for presence and its cleanup playbook
// for removal.
func reconcileStaleKafkaMirrorMakerWorkersOverSSH(ctx context.Context, out io.Writer, manifest *inventory.Manifest, pool *ssh.Pool, dryRun bool) error {
	if len(staleKafkaMirrorMakerWorkerCandidates(manifest)) == 0 {
		return nil
	}
	prov, err := provisioner.GetProvisioner("kafka-mirrormaker", pool)
	if err != nil {
		return err
	}
	cleanupConfig := provisioner.ServiceConfig{
		Mode:       "native",
		DeployName: "kafka-mirrormaker",
		Metadata:   map[string]any{"_cleanup_only": true},
	}
	probe := func(probeCtx context.Context, host inventory.Host) (bool, error) {
		state, detectErr := provisioner.DetectWithConfig(probeCtx, prov, host, cleanupConfig)
		if detectErr != nil {
			return false, detectErr
		}
		return state != nil && state.Exists, nil
	}
	remove := func(removeCtx context.Context, host inventory.Host) error {
		return prov.Cleanup(removeCtx, host, cleanupConfig)
	}
	return reconcileStaleKafkaMirrorMakerWorkers(ctx, out, manifest, probe, remove, dryRun)
}

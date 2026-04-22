package cmd

import (
	"context"
	"fmt"
	"strings"
	"time"

	"frameworks/cli/internal/compare"
	"frameworks/cli/pkg/artifacts"
	"frameworks/cli/pkg/gitops"
	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/orchestrator"
	fwssh "frameworks/cli/pkg/ssh"

	"github.com/spf13/cobra"
)

// buildDryRunTaskCompare returns a per-task annotation function that
// compares desired artifacts against the host. Returns a no-op annotator
// plus a nil cleanup if resources can't be set up — dry-run never blocks
// on compare failures.
func buildDryRunTaskCompare(ctx context.Context, cmd *cobra.Command, rc *resolvedCluster, manifest *inventory.Manifest, manifestDir string, sharedEnv map[string]string) (func(*orchestrator.Task) string, func()) {
	sshKey := stringFlag(cmd, "ssh-key").Value
	sshPool := fwssh.NewPool(30*time.Second, sshKey)

	var gitopsManifest *gitops.Manifest
	gitopsFetchFailed := false
	if manifest != nil {
		channel := manifest.ResolvedChannel()
		gopsChannel, gopsVersion := gitops.ResolveVersion(channel)
		fetched, fetchErr := gitops.FetchFromRepositories(gitops.FetchOptions{}, rc.ReleaseRepos, gopsChannel, gopsVersion)
		if fetchErr != nil {
			gitopsFetchFailed = true
			fmt.Fprintf(cmd.ErrOrStderr(), "warning: dry-run config diff degraded — failed to fetch gitops manifest: %v\n", fetchErr)
		} else {
			gitopsManifest = fetched
		}
	}

	annotate := func(task *orchestrator.Task) string {
		target := clusterDriftTarget{
			Host:    task.Host,
			Display: task.ServiceID,
			Deploy:  task.Type,
			Role:    task.InstanceID,
		}
		host, ok := manifest.GetHost(target.Host)
		if !ok {
			return " | inconclusive: host not in manifest"
		}
		cfg, buildErr := buildClusterTargetConfig(target, host, manifest, manifestDir, sharedEnv, rc.ReleaseRepos)
		if buildErr != nil {
			return fmt.Sprintf(" | inconclusive: config resolve failed: %v", buildErr)
		}
		imageRef := cfg.Image
		if imageRef == "" && gitopsManifest != nil {
			if info, infoErr := gitopsManifest.GetServiceInfo(target.Deploy); infoErr == nil {
				imageRef = info.FullImage
			}
		}
		if imageRef == "" && cfg.Mode == "docker" && gitopsFetchFailed {
			return " | inconclusive: gitops fetch failed; docker compose not compared"
		}
		arts := ClusterArtifactsFor(target, host, cfg, imageRef)
		if len(arts) == 0 {
			return " | inconclusive: no artifacts defined for this service"
		}
		runner := compare.NewSSHRunner(sshPool, &fwssh.ConnectionConfig{
			Address:  host.ExternalIP,
			User:     host.User,
			HostName: host.Name,
			KeyPath:  sshKey,
		})
		diff := compare.CompareTarget(ctx, artifacts.TargetID{Host: target.Host, Display: target.Display, Deploy: target.Deploy}, arts, runner)
		return summarizeDryRunDiff(diff)
	}
	return annotate, func() { _ = sshPool.Close() }
}

// summarizeDryRunDiff returns a human-readable annotation appended to the
// plan task line. Empty string means no diff to surface (either matches or
// nothing comparable).
func summarizeDryRunDiff(diff artifacts.ConfigDiff) string {
	if diff.Divergences() == 0 {
		if len(diff.Entries) == 0 {
			return ""
		}
		return " | no-op (state matches)"
	}
	var fileDiffs, envDiffs, missing, probeErrors int
	for _, e := range diff.Entries {
		switch e.Status {
		case artifacts.StatusMissingOnHost:
			missing++
		case artifacts.StatusProbeError:
			probeErrors++
		case artifacts.StatusDiffer:
			if e.Env != nil {
				envDiffs++
			} else {
				fileDiffs++
			}
		}
	}
	parts := []string{}
	if fileDiffs > 0 {
		parts = append(parts, fmt.Sprintf("%d file(s) differ", fileDiffs))
	}
	if envDiffs > 0 {
		parts = append(parts, fmt.Sprintf("%d env diff(s)", envDiffs))
	}
	if missing > 0 {
		parts = append(parts, fmt.Sprintf("%d missing", missing))
	}
	if probeErrors > 0 {
		parts = append(parts, fmt.Sprintf("%d probe err(s)", probeErrors))
	}
	return " | would change: " + strings.Join(parts, ", ")
}

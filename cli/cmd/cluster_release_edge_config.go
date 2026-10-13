package cmd

import (
	"fmt"
	"strings"
	"time"

	fwcfg "frameworks/cli/internal/config"
	"frameworks/cli/internal/ux"

	"github.com/spf13/cobra"
)

// Release apply finishes at the edges. Foghorn rolls the edge components the
// release pins (Helmsman, MistServer, Caddy) but never touches an edge's
// rendered configuration, so once every edge reports the release, release
// apply re-renders this CLI's edge config on the operator-managed edges listed
// in the edge manifest, through the same rollout `edge provision` runs: every
// enrolled node is checked in Ansible check mode, a node without drift is
// skipped, and drifted nodes are applied one at a time, drained first when the
// change restarts MistServer or Caddy and another routable node can take its
// sessions. Customer-enrolled edges are not in that manifest; `cluster doctor`
// lists any edge whose config predates the CLI.

var (
	releaseSyncEdgeTargetFn            = syncClusterEdgeReleaseTargetPinned
	releaseConvergeEdgeConfigFn        = convergeReleaseEdgeConfig
	releaseEdgeProvisionFromManifestFn = runEdgeProvisionFromManifest
)

// releaseEdgeManifestPath is the edge manifest release apply converges: the
// --edge-manifest flag, else edge.yaml beside the cluster manifest, else "".
func releaseEdgeManifestPath(rc *resolvedCluster, flag string) string {
	if path := strings.TrimSpace(flag); path != "" {
		return path
	}
	if rc == nil {
		return ""
	}
	return defaultEdgeManifestPath(rc.ManifestPath)
}

// releaseEdgeConfigCommand is the standalone command that re-renders the edge
// config for platformVersion.
func releaseEdgeConfigCommand(edgeManifestPath, platformVersion string) string {
	if edgeManifestPath == "" {
		edgeManifestPath = "<edge-manifest>"
	}
	return fmt.Sprintf("frameworks edge provision --manifest %s --version %s", edgeManifestPath, platformVersion)
}

// runReleaseEdgeStage pins the edge release target, waits for the edges to
// report the release, and then converges their rendered config. The config
// renders against the release's edge artifacts, so it waits for the fleet: a
// fleet still rolling when the wait ends gets the exact command to run once it
// converged instead.
func runReleaseEdgeStage(cmd *cobra.Command, rc *resolvedCluster, opts releaseApplyOptions, releaseChannel, platformVersion, version string) error {
	out := cmd.OutOrStdout()
	// The target only tells Foghorn's reconciler what to roll out; the edges
	// have the release once they report it, which the wait below checks.
	ux.Subheading(out, "Syncing edge release target")
	if err := releaseSyncEdgeTargetFn(cmd, rc, releaseChannel, platformVersion, nil); err != nil {
		return fmt.Errorf("edge release target sync: %w", err)
	}
	ux.Subheading(out, "Waiting for edges to report the release")
	edgesConverged, edgeErr := releaseVerifyEdgeRolloutFn(cmd, rc, opts.edgeWait)
	if edgeErr != nil {
		return fmt.Errorf("release %s applied to the control plane, but the edge rollout failed: %w", version, edgeErr)
	}
	edgeManifest := releaseEdgeManifestPath(rc, opts.edgeManifest)
	if !edgesConverged {
		ux.Warn(out, fmt.Sprintf("Release %s applied to the control plane; the edge fleet has not converged yet", version))
		fmt.Fprintf(out, "  Edge config was not re-rendered. Once `frameworks cluster edges rollout status --wait 15m` reports every edge on %s, run `%s`.\n",
			platformVersion, releaseEdgeConfigCommand(edgeManifest, platformVersion))
		return nil
	}
	ux.Subheading(out, "Converging edge config")
	if err := releaseConvergeEdgeConfigFn(cmd, rc, edgeManifest, platformVersion); err != nil {
		return fmt.Errorf("release %s applied and every edge runs it, but the edge config did not converge: %w; after fixing the cause, run `%s`",
			version, err, releaseEdgeConfigCommand(edgeManifest, platformVersion))
	}
	ux.Success(out, fmt.Sprintf("Release %s applied", version))
	return nil
}

// convergeReleaseEdgeConfig re-renders this CLI's edge config on the enrolled
// nodes of the edge manifest, rendering the release's pinned artifacts. The
// release apply already pinned the edge release target, so the provision does
// not sync it again, and a node in the manifest that was never installed is
// left for `edge provision`.
func convergeReleaseEdgeConfig(cmd *cobra.Command, rc *resolvedCluster, edgeManifestPath, platformVersion string) error {
	out := cmd.OutOrStdout()
	if edgeManifestPath == "" {
		ux.Warn(out, "No edge manifest: no --edge-manifest and no edge.yaml beside the cluster manifest, so no edge config was re-rendered.")
		fmt.Fprintf(out, "  Operator-managed edges take this CLI's edge config with `%s`; `frameworks cluster doctor` lists every edge whose config predates this CLI.\n",
			releaseEdgeConfigCommand("", platformVersion))
		return nil
	}
	cfg, err := fwcfg.Load()
	if err != nil {
		return fmt.Errorf("load CLI config: %w", err)
	}
	cliCtx, err := fwcfg.MaybeActiveContext(fwcfg.GetRuntimeOverrides(), fwcfg.OSEnv{}, cfg)
	if err != nil {
		return err
	}
	return releaseEdgeProvisionFromManifestFn(cmd, cliCtx, edgeManifestProvisionOptions{
		ManifestPath:            edgeManifestPath,
		ClusterManifestOverride: rc.ManifestPath,
		SSHKey:                  stringFlag(cmd, "ssh-key").Value,
		Parallel:                1,
		Timeout:                 3 * time.Minute,
		Version:                 platformVersion,
		VersionSet:              true,
		ONNXProfile:             "auto",
		AgeKeyFile:              rc.AgeKey,
		ReleaseTargetPinned:     true,
		EnrolledOnly:            true,
	})
}

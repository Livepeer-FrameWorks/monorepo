package cmd

import (
	"fmt"

	"frameworks/cli/internal/ux"
	"frameworks/cli/pkg/inventory"

	"github.com/spf13/cobra"
)

var validChannels = []string{"stable", "rc"}

func newClusterSetChannelCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "set-channel <channel>",
		Short: "Set the release channel for this cluster",
		Long: `Set the release channel recorded in the cluster manifest.

Valid channels:
  stable  - Production releases (default)
  rc      - Release candidates (pre-production)

The channel controls which release track 'frameworks cluster upgrade' uses
when no explicit version is given.`,
		Example: `  frameworks cluster set-channel rc
  frameworks cluster set-channel stable --manifest /etc/frameworks/cluster.yaml`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			rc, err := resolveClusterManifest(cmd)
			if err != nil {
				return err
			}
			defer rc.Cleanup()
			return runSetChannel(cmd, rc, args[0])
		},
	}

	return cmd
}

func runSetChannel(cmd *cobra.Command, rc *resolvedCluster, channel string) error {
	if !isValidChannel(channel) {
		return fmt.Errorf("invalid channel %q: must be one of %v", channel, validChannels)
	}

	manifest := rc.Manifest
	manifestPath := rc.ManifestPath
	out := cmd.OutOrStdout()
	current := manifest.ResolvedChannel()
	if current == channel {
		ux.Success(out, fmt.Sprintf("Already on channel: %s", channel))
		return nil
	}

	// Refuse to report success against a source we cannot actually persist to. A GitHub-fetched manifest lives in a
	// temporary checkout that Cleanup removes, so a patch there would be silently discarded — the operator must edit
	// the channel in the source repo (and commit) or run against a local gitops checkout / manifest file.
	if !rc.SourcePersistsManifest {
		return fmt.Errorf("cannot persist the channel: this manifest was resolved from a non-writable source (%s) — "+
			"a GitHub-fetched manifest is a temporary checkout. Set `channel: %s` in the source gitops repository and "+
			"commit it, or run set-channel against a local gitops directory / manifest file", rc.Source, channel)
	}

	// Persist ONLY the channel by patching the SOURCE manifest in place (comments and key order preserved). The
	// resolved manifest in memory has SOPS-decrypted host inventory merged in; serializing it would leak host
	// IPs/users/key-paths into the plaintext manifest, so never Save the resolved struct here.
	if err := inventory.PatchManifestField(manifestPath, "channel", channel); err != nil {
		return fmt.Errorf("failed to update channel in %s: %w", manifestPath, err)
	}
	manifest.Channel = channel

	ux.Success(out, fmt.Sprintf("Channel updated in %s: %s -> %s", manifestPath, current, channel))
	if channel == "rc" {
		ux.Warn(out, "Release candidates are pre-production — review the changelog before upgrading.")
	}
	ux.PrintNextSteps(out, []ux.NextStep{
		{Cmd: "frameworks cluster release plan", Why: "Review the complete release selected by the new channel."},
		{Cmd: "frameworks cluster release apply --dry-run", Why: "Run live release gates before applying it."},
	})
	return nil
}

func isValidChannel(channel string) bool {
	for _, c := range validChannels {
		if c == channel {
			return true
		}
	}
	return false
}

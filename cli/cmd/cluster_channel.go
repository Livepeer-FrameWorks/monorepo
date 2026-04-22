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
			return runSetChannel(cmd, rc.Manifest, rc.ManifestPath, args[0])
		},
	}

	return cmd
}

func runSetChannel(cmd *cobra.Command, manifest *inventory.Manifest, manifestPath, channel string) error {
	if !isValidChannel(channel) {
		return fmt.Errorf("invalid channel %q: must be one of %v", channel, validChannels)
	}

	out := cmd.OutOrStdout()
	current := manifest.ResolvedChannel()
	if current == channel {
		ux.Success(out, fmt.Sprintf("Already on channel: %s", channel))
		return nil
	}

	manifest.Channel = channel

	if err := inventory.Save(manifestPath, manifest); err != nil {
		return fmt.Errorf("failed to save manifest: %w", err)
	}

	ux.Success(out, fmt.Sprintf("Channel updated: %s -> %s", current, channel))
	if channel == "rc" {
		ux.Warn(out, "Release candidates are pre-production — review the changelog before upgrading.")
	}
	ux.PrintNextSteps(out, []ux.NextStep{
		{Cmd: "frameworks cluster upgrade --all", Why: "Roll services to the new channel."},
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

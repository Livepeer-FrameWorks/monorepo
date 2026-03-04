package cmd

import (
	"fmt"
	"frameworks/cli/pkg/selfupdate"
	fwv "frameworks/pkg/version"

	"github.com/spf13/cobra"
)

func newUpdateCmd() *cobra.Command {
	var checkOnly bool

	cmd := &cobra.Command{
		Use:   "update",
		Short: "Update the frameworks CLI to the latest version",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			current := fwv.Version

			fmt.Fprintf(cmd.OutOrStdout(), "Current version: %s\n", current)
			fmt.Fprintln(cmd.OutOrStdout(), "Checking for updates...")

			release, err := selfupdate.CheckLatest(ctx)
			if err != nil {
				return fmt.Errorf("failed to check for updates: %w", err)
			}

			if release.TagName == current {
				fmt.Fprintln(cmd.OutOrStdout(), "Already up to date.")
				return nil
			}

			fmt.Fprintf(cmd.OutOrStdout(), "Update available: %s -> %s\n", current, release.TagName)

			if checkOnly {
				return nil
			}

			fmt.Fprintln(cmd.OutOrStdout(), "Downloading...")

			result, err := selfupdate.Update(ctx, release)
			if err != nil {
				return fmt.Errorf("update failed: %w", err)
			}

			fmt.Fprintf(cmd.OutOrStdout(), "Updated to %s\n", result.NewVersion)
			return nil
		},
	}

	cmd.Flags().BoolVar(&checkOnly, "check", false, "Only check for updates, don't install")
	return cmd
}

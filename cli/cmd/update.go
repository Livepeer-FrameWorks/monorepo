package cmd

import (
	"fmt"
	"os"
	"strings"

	"frameworks/cli/internal/ux"
	"frameworks/cli/pkg/installer"
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
				ux.Success(cmd.OutOrStdout(), "Already up to date")
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

			ux.Success(cmd.OutOrStdout(), fmt.Sprintf("Updated %s -> %s", current, result.NewVersion))

			if majorVersion(current) != majorVersion(result.NewVersion) {
				ux.Warn(cmd.OutOrStdout(), "major version change — review the changelog for breaking changes")
			}

			// Record install state for lifecycle tracking
			execPath, err := os.Executable()
			if err != nil {
				execPath = ""
			}
			if err := installer.RecordInstall(result.NewVersion, execPath); err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "Warning: failed to record install state: %v\n", err)
			}

			return nil
		},
	}

	cmd.Flags().BoolVar(&checkOnly, "check", false, "Only check for updates, don't install")
	return cmd
}

func majorVersion(v string) string {
	v = strings.TrimPrefix(v, "v")
	if idx := strings.Index(v, "."); idx != -1 {
		return v[:idx]
	}
	return v
}

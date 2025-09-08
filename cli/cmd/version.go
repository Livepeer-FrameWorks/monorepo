package cmd

import (
	"fmt"
	fwv "frameworks/pkg/version"
	"github.com/spf13/cobra"
)

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print CLI and platform version info",
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Fprintf(cmd.OutOrStdout(), "Frameworks CLI\n")
			fmt.Fprintf(cmd.OutOrStdout(), " - platform version: %s\n", fwv.Version)
			fmt.Fprintf(cmd.OutOrStdout(), " - component: %s %s\n", fwv.ComponentName, fwv.ComponentVersion)
			fmt.Fprintf(cmd.OutOrStdout(), " - git: %s\n", fwv.GitCommit)
			return nil
		},
	}
}

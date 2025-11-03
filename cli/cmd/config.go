package cmd

import "github.com/spf13/cobra"

func newConfigCmd() *cobra.Command {
	cfg := &cobra.Command{Use: "config", Short: "Configuration helpers"}
	cfg.AddCommand(newConfigEnvCmd())
	return cfg
}

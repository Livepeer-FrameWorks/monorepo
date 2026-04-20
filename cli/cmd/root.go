package cmd

import (
	"fmt"
	"os"

	fwcfg "frameworks/cli/internal/config"

	"github.com/mattn/go-isatty"
	"github.com/spf13/cobra"
)

var (
	cfgFile     string
	output      string
	verbose     bool
	contextName string
)

// NewRootCmd returns the root command for the Frameworks CLI
func NewRootCmd() *cobra.Command {
	rootCmd := &cobra.Command{
		Use:           "frameworks",
		Short:         "Frameworks CLI — unified operator tool",
		Long:          "Frameworks CLI — manage Edge nodes, services, and infrastructure across the Frameworks platform.",
		SilenceUsage:  true,
		SilenceErrors: true,
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			// Parse flags + env once into typed RuntimeOverrides so the
			// config / credentials / inventory packages don't need to
			// know about cobra.
			fwcfg.SetRuntimeOverrides(fwcfg.RuntimeOverrides{
				ContextName:        contextName,
				ContextExplicit:    cmd.Flags().Changed("context"),
				ConfigPath:         cfgFile,
				ConfigPathExplicit: cmd.Flags().Changed("config"),
				OutputJSON:         output == "json",
				NoHints:            os.Getenv("CI") != "" || os.Getenv("FRAMEWORKS_NO_HINTS") != "",
			})
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			_ = cmd.Help()
			if isatty.IsTerminal(os.Stdout.Fd()) {
				fmt.Fprintln(cmd.OutOrStdout(), "\nTip: run 'frameworks setup' to configure the CLI, or 'frameworks menu' for an interactive start.")
			}
			return nil
		},
	}

	rootCmd.PersistentFlags().StringVar(&cfgFile, "config", "", "path to config.yaml (default: $XDG_CONFIG_HOME/frameworks/config.yaml)")
	rootCmd.PersistentFlags().StringVar(&output, "output", "", "output format: json|text (default: text)")
	rootCmd.PersistentFlags().BoolVarP(&verbose, "verbose", "v", false, "enable verbose logging")
	rootCmd.PersistentFlags().StringVar(&contextName, "context", "", "context name to use for this invocation (overrides the saved current context)")

	// Subcommands (groups)
	rootCmd.AddCommand(newMenuCmd())
	rootCmd.AddCommand(newEdgeCmd())
	rootCmd.AddCommand(newClusterCmd())
	rootCmd.AddCommand(newServicesCmd())
	rootCmd.AddCommand(newContextCmd())
	rootCmd.AddCommand(newLoginCmd())
	rootCmd.AddCommand(newLogoutCmd())
	rootCmd.AddCommand(newSetupCmd())
	rootCmd.AddCommand(newAdminCmd())
	rootCmd.AddCommand(newVersionCmd())
	rootCmd.AddCommand(newConfigCmd())
	rootCmd.AddCommand(newDNSCmd())
	rootCmd.AddCommand(newMeshCmd())
	rootCmd.AddCommand(newUpdateCmd())
	rootCmd.AddCommand(newLivepeerCmd())
	return rootCmd
}

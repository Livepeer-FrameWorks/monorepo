package cmd

import (
	"fmt"
	"os"

	fwcfg "frameworks/cli/internal/config"

	"github.com/mattn/go-isatty"
	"github.com/spf13/cobra"
)

var (
	output  string
	verbose bool
)

// NewRootCmd returns the root command for the Frameworks CLI
func NewRootCmd() *cobra.Command {
	var (
		rootCfgFile     string
		rootOutput      string
		rootVerbose     bool
		rootContextName string
	)

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
			output = rootOutput
			verbose = rootVerbose
			fwcfg.SetRuntimeOverrides(fwcfg.RuntimeOverrides{
				ContextName:        rootContextName,
				ContextExplicit:    cmd.Flags().Changed("context"),
				ConfigPath:         rootCfgFile,
				ConfigPathExplicit: cmd.Flags().Changed("config"),
				OutputJSON:         rootOutput == "json",
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

	rootCmd.PersistentFlags().StringVar(&rootCfgFile, "config", "", "path to config.yaml (default: $XDG_CONFIG_HOME/frameworks/config.yaml)")
	rootCmd.PersistentFlags().StringVar(&rootOutput, "output", "", "output format: json|text (default: text)")
	rootCmd.PersistentFlags().BoolVarP(&rootVerbose, "verbose", "v", false, "enable verbose logging")
	rootCmd.PersistentFlags().StringVar(&rootContextName, "context", "", "context name to use for this invocation (overrides the saved current context)")

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
	rootCmd.AddCommand(newCommandsCmd())
	rootCmd.AddCommand(newDNSCmd())
	rootCmd.AddCommand(newMeshCmd())
	rootCmd.AddCommand(newUpdateCmd())
	rootCmd.AddCommand(newLivepeerCmd())
	return rootCmd
}

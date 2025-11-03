package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
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
		RunE: func(cmd *cobra.Command, args []string) error {
			// Default action: show help and hint about interactive menu
			_ = cmd.Help()
			fmt.Fprintln(cmd.OutOrStdout(), "\nTip: run 'frameworks menu' for an interactive start.")
			return nil
		},
	}

	rootCmd.PersistentFlags().StringVar(&cfgFile, "config", "", "config file (default is $HOME/.frameworks/config.yaml)")
	rootCmd.PersistentFlags().StringVar(&output, "output", "", "output format: json|text (default: text)")
	rootCmd.PersistentFlags().BoolVarP(&verbose, "verbose", "v", false, "enable verbose logging")
	rootCmd.PersistentFlags().StringVar(&contextName, "context", "default", "context name (e.g., default|dev|staging|prod)")

	cobra.OnInitialize(initConfig)

	// Subcommands (groups)
	rootCmd.AddCommand(newMenuCmd())
	rootCmd.AddCommand(newEdgeCmd())
	rootCmd.AddCommand(newServicesCmd())
	rootCmd.AddCommand(newContextCmd())
	rootCmd.AddCommand(newLoginCmd())
	rootCmd.AddCommand(newAdminCmd())
	rootCmd.AddCommand(newVersionCmd())
	rootCmd.AddCommand(newConfigCmd())

	return rootCmd
}

func initConfig() {
	if cfgFile != "" {
		viper.SetConfigFile(cfgFile)
	} else {
		home, err := os.UserHomeDir()
		if err == nil {
			viper.AddConfigPath(home + "/.frameworks")
			viper.SetConfigName("config")
		}
		viper.SetConfigType("yaml")
	}

	viper.SetEnvPrefix("FRAMEWORKS")
	viper.AutomaticEnv()

	// Ignore missing config
	_ = viper.ReadInConfig()
}

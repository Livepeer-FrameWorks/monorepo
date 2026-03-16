package cmd

import (
	"fmt"
	"strconv"

	fwcfg "frameworks/cli/internal/config"

	"github.com/spf13/cobra"
)

func newConfigCmd() *cobra.Command {
	cfg := &cobra.Command{Use: "config", Short: "Configuration helpers"}
	cfg.AddCommand(newConfigInitCmd())
	cfg.AddCommand(newConfigEnvCmd())
	cfg.AddCommand(newConfigSetCmd())
	cfg.AddCommand(newConfigGetCmd())
	return cfg
}

func newConfigInitCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "init",
		Short: "Create default configuration file",
		Long:  `Create ~/.frameworks/config.yaml with default settings if it does not exist.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, cfgPath, err := fwcfg.Load()
			if err != nil {
				return err
			}
			if err := fwcfg.Save(cfg, cfgPath); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Config ready at %s\n", cfgPath)
			return nil
		},
	}
}

func newConfigSetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "set <key> <value>",
		Short: "Set a configuration value",
		Long: `Set a configuration value using dot-separated keys.

Supported keys:
  github.app-id            GitHub App ID
  github.installation-id   GitHub App Installation ID
  github.private-key       Path to GitHub App private key PEM
  github.repo              GitHub repo (owner/repo)
  github.ref               Git ref for manifest fetch (default: main)`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			key, value := args[0], args[1]

			cfg, cfgPath, err := fwcfg.Load()
			if err != nil {
				return err
			}

			if cfg.GitHub == nil {
				cfg.GitHub = &fwcfg.GitHubApp{}
			}

			switch key {
			case "github.app-id":
				v, err := strconv.ParseInt(value, 10, 64)
				if err != nil {
					return fmt.Errorf("app-id must be a number: %w", err)
				}
				cfg.GitHub.AppID = v
			case "github.installation-id":
				v, err := strconv.ParseInt(value, 10, 64)
				if err != nil {
					return fmt.Errorf("installation-id must be a number: %w", err)
				}
				cfg.GitHub.InstallationID = v
			case "github.private-key":
				cfg.GitHub.PrivateKeyPath = value
			case "github.repo":
				cfg.GitHub.Repo = value
			case "github.ref":
				cfg.GitHub.Ref = value
			default:
				return fmt.Errorf("unknown key: %s", key)
			}

			if err := fwcfg.Save(cfg, cfgPath); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Set %s = %s\n", key, value)
			return nil
		},
	}
}

func newConfigGetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "get <key>",
		Short: "Get a configuration value",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			key := args[0]

			cfg, _, err := fwcfg.Load()
			if err != nil {
				return err
			}

			gh := cfg.GitHub
			if gh == nil {
				gh = &fwcfg.GitHubApp{}
			}

			var value string
			switch key {
			case "github.app-id":
				value = strconv.FormatInt(gh.AppID, 10)
			case "github.installation-id":
				value = strconv.FormatInt(gh.InstallationID, 10)
			case "github.private-key":
				value = gh.PrivateKeyPath
			case "github.repo":
				value = gh.Repo
			case "github.ref":
				value = gh.Ref
			default:
				return fmt.Errorf("unknown key: %s", key)
			}

			fmt.Fprintln(cmd.OutOrStdout(), value)
			return nil
		},
	}
}

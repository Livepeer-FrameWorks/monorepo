package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	fwcfg "frameworks/cli/internal/config"
	fwcredentials "frameworks/cli/internal/credentials"

	"github.com/spf13/cobra"
)

func newConfigCmd() *cobra.Command {
	cfg := &cobra.Command{Use: "config", Short: "Configuration helpers"}
	cfg.AddCommand(newConfigEnvCmd())
	cfg.AddCommand(newConfigSetCmd())
	cfg.AddCommand(newConfigGetCmd())
	cfg.AddCommand(newConfigPathCmd())
	return cfg
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
  github.ref               Git ref for manifest fetch (default: main)

For per-context gitops defaults (manifest source, cluster, age key),
use 'frameworks context set-gitops-*' instead.`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			key, value := args[0], args[1]

			cfg, err := fwcfg.Load()
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

			if err := fwcfg.Save(cfg); err != nil {
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

			cfg, err := fwcfg.Load()
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

func newConfigPathCmd() *cobra.Command {
	var kind string
	cmd := &cobra.Command{
		Use:   "path",
		Short: "Print the canonical location for CLI state",
		Long: `Print the canonical location for a given state kind.

  config       Filesystem path to config.yaml.
  credentials  Backend + identifier for the credential store. On macOS
               this is 'keychain:<service>'; on other platforms it is a
               filesystem path with mode 0600. JSON output carries both
               'backend' and 'location' fields.

The location is returned whether or not the underlying file exists yet.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			rt := fwcfg.GetRuntimeOverrides()
			switch kind {
			case "config":
				path, err := fwcfg.ConfigPath()
				if err != nil {
					return err
				}
				return emitPath(cmd, rt, map[string]string{
					"kind":     kind,
					"backend":  "file",
					"location": path,
					"path":     path,
				}, path)
			case "credentials":
				backend, location := credentialsLocation()
				display := location
				if backend == "keychain" {
					display = "keychain:" + location
				}
				return emitPath(cmd, rt, map[string]string{
					"kind":     kind,
					"backend":  backend,
					"location": location,
					"path":     display,
				}, display)
			default:
				return fmt.Errorf("unknown --kind %q (want config|credentials)", kind)
			}
		},
	}
	cmd.Flags().StringVar(&kind, "kind", "config", "which location to print: config|credentials")
	return cmd
}

func emitPath(cmd *cobra.Command, rt fwcfg.RuntimeOverrides, payload map[string]string, textOut string) error {
	if rt.OutputJSON {
		b, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		fmt.Fprintln(cmd.OutOrStdout(), string(b))
		return nil
	}
	fmt.Fprintln(cmd.OutOrStdout(), textOut)
	return nil
}

// credentialsLocation returns the backend name and its identifier. For
// keychain the identifier is the service name; for file it's the
// filesystem path.
func credentialsLocation() (backend, location string) {
	store := fwcredentials.DefaultStore()
	switch store.Name() {
	case "keychain":
		return "keychain", fwcredentials.ServiceName
	default:
		base := strings.TrimSpace(os.Getenv("XDG_DATA_HOME"))
		if base == "" {
			if home, err := os.UserHomeDir(); err == nil {
				base = filepath.Join(home, ".local", "share")
			}
		}
		return store.Name(), filepath.Join(base, "frameworks", "credentials")
	}
}

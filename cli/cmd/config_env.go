package cmd

import (
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"frameworks/pkg/configgen"
	"github.com/spf13/cobra"
)

func newConfigEnvCmd() *cobra.Command {
	var base string
	var secrets string
	var output string
	var context string

	cmd := &cobra.Command{Use: "env", Short: "Generate merged env file"}
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		if base == "" {
			base = "config/env/base.env"
		}
		if secrets == "" {
			secrets = "config/env/secrets.env"
		}
		if context == "" {
			context = "dev"
		}

		base = filepath.Clean(base)
		secrets = filepath.Clean(secrets)

		opts := configgen.Options{
			BaseFile:    base,
			SecretsFile: secrets,
			Context:     context,
		}

		if strings.TrimSpace(output) == "" {
			opts.OutputFile = ".env"
		} else if output == "-" {
			opts.OutputFile = ""
		} else {
			opts.OutputFile = filepath.Clean(output)
		}

		env, err := configgen.Generate(opts)
		if err != nil {
			return err
		}

		if opts.OutputFile == "" {
			// Write to stdout in sorted order
			keys := make([]string, 0, len(env))
			for k := range env {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				fmt.Fprintf(cmd.OutOrStdout(), "%s=%s\n", k, strconv.Quote(env[k]))
			}
			return nil
		}

		abs, _ := filepath.Abs(opts.OutputFile)
		fmt.Fprintf(cmd.OutOrStdout(), "wrote %s\n", abs)
		return nil
	}

	cmd.Flags().StringVar(&base, "base", "", "path to base env file (default: config/env/base.env)")
	cmd.Flags().StringVar(&secrets, "secrets", "", "path to secrets env file (default: config/env/secrets.env)")
	cmd.Flags().StringVar(&output, "output", "", "output path (default: .env, use '-' for stdout)")
	cmd.Flags().StringVar(&context, "context", "dev", "value for ENV_CONTEXT")
	return cmd
}

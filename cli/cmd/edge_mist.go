package cmd

import (
	"fmt"

	"frameworks/cli/pkg/mistdiag"
	"github.com/spf13/cobra"
)

func newEdgeMistCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "mist",
		Short: "MistServer diagnostics",
	}
	cores := &cobra.Command{
		Use:   "cores",
		Short: "Collect and analyze MistServer core dumps",
	}
	cores.AddCommand(newEdgeMistCoresCollectCmd())
	cores.AddCommand(newEdgeMistCoresAnalyzeCmd())
	cmd.AddCommand(cores)
	return cmd
}

func newEdgeMistCoresCollectCmd() *cobra.Command {
	var opts mistdiag.CoreCollectOptions
	cmd := &cobra.Command{
		Use:   "collect <ssh-target>",
		Short: "Collect the latest MistController core dump from an edge over SSH",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			opts.Target = args[0]
			path, err := mistdiag.CollectMistControllerCore(cmd.Context(), opts)
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Wrote %s\n", path)
			return nil
		},
	}
	cmd.Flags().StringVar(&opts.KeyPath, "ssh-key", "", "SSH private key path")
	cmd.Flags().StringVarP(&opts.Output, "out", "o", "", "local output tarball path")
	cmd.Flags().StringVar(&opts.Since, "since", "2 hours ago", "journalctl time window to include")
	cmd.Flags().StringVar(&opts.Service, "service", "frameworks-mistserver", "systemd unit to include in logs")
	return cmd
}

func newEdgeMistCoresAnalyzeCmd() *cobra.Command {
	var opts mistdiag.CoreAnalyzeOptions
	cmd := &cobra.Command{
		Use:   "analyze <bundle.tar.gz>",
		Short: "Analyze a collected MistController core bundle with matching debug symbols",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			opts.BundlePath = args[0]
			result, err := mistdiag.AnalyzeMistControllerCore(cmd.Context(), opts)
			if err != nil {
				if result != nil {
					fmt.Fprintf(cmd.ErrOrStderr(), "Bundle extracted to %s\n", result.BundleDir)
				}
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Bundle: %s\n", result.BundleDir)
			fmt.Fprintf(cmd.OutOrStdout(), "Binary: %s\n", result.BinaryPath)
			fmt.Fprintf(cmd.OutOrStdout(), "Core: %s\n", result.CorePath)
			if result.DebugArtifact != "" {
				fmt.Fprintf(cmd.OutOrStdout(), "Debug symbols: %s\n", result.DebugArtifact)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Debugger: %s\n\n%s", result.Debugger, result.Backtrace)
			return nil
		},
	}
	cmd.Flags().StringVar(&opts.CacheDir, "cache-dir", "", "debug symbol cache directory")
	return cmd
}

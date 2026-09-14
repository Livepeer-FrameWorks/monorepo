package cmd

import (
	"fmt"
	"os"

	"frameworks/cli/pkg/gitops"

	"github.com/spf13/cobra"
)

func newValidateReleaseManifestCmd() *cobra.Command {
	return &cobra.Command{
		Use:    "validate-release-manifest <path>",
		Short:  "Strictly validate an assembled release manifest",
		Hidden: true,
		Args:   cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			data, err := os.ReadFile(args[0])
			if err != nil {
				return fmt.Errorf("read release manifest: %w", err)
			}
			manifest, err := gitops.ParseManifest(data)
			if err != nil {
				return err
			}
			if err := manifest.ValidateServiceArtifacts(); err != nil {
				return fmt.Errorf("validate release artifacts: %w", err)
			}
			return nil
		},
	}
}

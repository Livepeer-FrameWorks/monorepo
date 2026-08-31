package cmd

import (
	"fmt"
	"slices"
	"strings"

	"frameworks/cli/pkg/credentials"

	"github.com/spf13/cobra"
)

func newClusterSecretsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "secrets",
		Short: "Generate cluster secret material for an operator-owned secret store",
	}
	cmd.AddCommand(newClusterSecretsGenerateSharedCmd())
	cmd.AddCommand(newClusterSecretsGenerateMediaAuthorityCmd())
	return cmd
}

func newClusterSecretsGenerateSharedCmd() *cobra.Command {
	var outputPath string
	cmd := &cobra.Command{
		Use:   "generate-shared",
		Short: "Generate a complete production shared-secret file",
		Long:  "Writes every shared platform secret required by production validation to a new mode-0600 dotenv file. Import it into the operator-owned SOPS secret store, then securely remove the plaintext fragment.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if strings.TrimSpace(outputPath) == "" {
				return fmt.Errorf("--out is required")
			}
			values, err := credentials.GenerateSharedDeploymentMaterial()
			if err != nil {
				return err
			}
			if err := writeSecretFragment(outputPath, "Complete production shared-secret set.", values); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Production shared secrets written to %s (mode 0600).\n", outputPath)
			return nil
		},
	}
	cmd.Flags().StringVar(&outputPath, "out", "", "new dotenv fragment path (required; never overwritten)")
	return cmd
}

func newClusterSecretsGenerateMediaAuthorityCmd() *cobra.Command {
	var outputPath string
	cmd := &cobra.Command{
		Use:   "generate-media-authority",
		Short: "Generate media-authority signing, sealing, and state-encryption roots",
		Long:  "Writes a new dotenv fragment with mode 0600 and refuses to overwrite an existing file. Import all values into the production SOPS secret file, then securely remove the fragment.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if strings.TrimSpace(outputPath) == "" {
				return fmt.Errorf("--out is required")
			}
			values, err := credentials.GenerateMediaAuthorityDeploymentMaterial()
			if err != nil {
				return err
			}
			if err := writeSecretFragment(outputPath, "Media-authority upgrade values; merge with the existing shared-secret set.", values); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Media-authority deployment secrets written to %s (mode 0600).\n", outputPath)
			return nil
		},
	}
	cmd.Flags().StringVar(&outputPath, "out", "", "new dotenv fragment path (required; never overwritten)")
	return cmd
}

func writeSecretFragment(outputPath, description string, values map[string]string) error {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	var payload strings.Builder
	fmt.Fprintf(&payload, "# %s\n", description)
	payload.WriteString("# Import into the production SOPS-managed shared secret file.\n")
	for _, key := range keys {
		fmt.Fprintf(&payload, "%s=%s\n", key, values[key])
	}
	return writeExclusiveFile(outputPath, []byte(payload.String()), 0o600)
}

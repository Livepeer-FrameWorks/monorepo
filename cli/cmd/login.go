package cmd

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	fwcfg "frameworks/cli/internal/config"

	"github.com/spf13/cobra"
	"golang.org/x/term"
)

func newLoginCmd() *cobra.Command {
	var serviceAccount bool

	cmd := &cobra.Command{
		Use:   "login",
		Short: "Authenticate with the FrameWorks platform",
		Long: `Authenticate by providing your API token or service token.

Tokens are stored securely in ~/.frameworks/.env and never appear in shell history.

For user authentication, use the web dashboard to generate an API token.
For service/automation, use the SERVICE_TOKEN from your cluster configuration.

Examples:
  frameworks login                    # Prompt for API token
  frameworks login --service-account  # Prompt for service token
`,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Determine which token type to prompt for
			tokenType := "API token"
			envKey := "FW_API_TOKEN"
			if serviceAccount {
				tokenType = "service token"
				envKey = "SERVICE_TOKEN"
			}

			// Check if already logged in
			envMap, _ := fwcfg.LoadEnvFile()
			existingToken := fwcfg.GetEnvValue(envKey, envMap)
			if existingToken != "" {
				fmt.Fprintf(cmd.OutOrStdout(), "Already logged in (%s is set).\n", envKey)
				fmt.Fprint(cmd.OutOrStdout(), "Replace existing token? [y/N]: ")
				reader := bufio.NewReader(os.Stdin)
				confirm, _ := reader.ReadString('\n')
				if strings.ToLower(strings.TrimSpace(confirm)) != "y" {
					fmt.Fprintln(cmd.OutOrStdout(), "Keeping existing credentials.")
					return nil
				}
			}

			// Prompt for token (hidden input)
			fmt.Fprintf(cmd.OutOrStdout(), "Enter %s: ", tokenType)
			tokenBytes, err := term.ReadPassword(int(os.Stdin.Fd()))
			fmt.Fprintln(cmd.OutOrStdout()) // newline after hidden input
			if err != nil {
				// Fallback to regular input if terminal doesn't support hidden
				reader := bufio.NewReader(os.Stdin)
				tokenStr, _ := reader.ReadString('\n')
				tokenBytes = []byte(strings.TrimSpace(tokenStr))
			}

			token := strings.TrimSpace(string(tokenBytes))
			if token == "" {
				return fmt.Errorf("no token provided")
			}

			// Validate token format
			if !serviceAccount && !strings.HasPrefix(token, "fw_") {
				fmt.Fprintln(cmd.OutOrStderr(), "Warning: API tokens typically start with 'fw_'")
			}

			// Save to .env file
			if err := fwcfg.SaveEnvValue(envKey, token); err != nil {
				return fmt.Errorf("failed to save credentials: %w", err)
			}

			fmt.Fprintf(cmd.OutOrStdout(), "Credentials saved to ~/.frameworks/.env\n")
			return nil
		},
	}

	cmd.Flags().BoolVar(&serviceAccount, "service-account", false, "Login as a service account (use SERVICE_TOKEN)")

	return cmd
}

// RequireAuth checks if the user is authenticated and prompts for login if not.
// Returns the token value or an error.
func RequireAuth(tokenType string) (string, error) {
	envKey := "FW_API_TOKEN"
	if tokenType == "service" {
		envKey = "SERVICE_TOKEN"
	}

	envMap, _ := fwcfg.LoadEnvFile()
	token := fwcfg.GetEnvValue(envKey, envMap)
	if token != "" {
		return token, nil
	}

	// Not authenticated - prompt user
	fmt.Printf("This command requires authentication.\n")
	fmt.Printf("Run 'frameworks login' to authenticate, or set %s in ~/.frameworks/.env\n", envKey)
	return "", fmt.Errorf("not authenticated")
}

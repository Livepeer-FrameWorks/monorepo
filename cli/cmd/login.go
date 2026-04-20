package cmd

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"frameworks/cli/internal/credentials"

	"github.com/spf13/cobra"
	"golang.org/x/term"
)

func newLoginCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "login",
		Short: "Authenticate with the FrameWorks platform",
		Long: `Authenticate by providing your user session token.

The session token is stored in the OS credential store (macOS Keychain,
or an XDG data-dir file with mode 0600 on other platforms). It never
lands in shell history or plaintext config files. The tray reads the
same Keychain entry on macOS.

Use the web dashboard to generate a session token.

The platform SERVICE_TOKEN is not a user credential — it is loaded from
your manifest env_files (gitops). There is no 'login --service-account'.
`,
		RunE: func(cmd *cobra.Command, args []string) error {
			store := credentials.DefaultStore()
			account := credentials.AccountUserSession

			existing, err := store.Get(account)
			if err != nil {
				return fmt.Errorf("read credential store (%s): %w", store.Name(), err)
			}
			if existing != "" {
				fmt.Fprintf(cmd.OutOrStdout(), "Already logged in (%s is set in %s).\n", account, store.Name())
				fmt.Fprint(cmd.OutOrStdout(), "Replace existing credential? [y/N]: ")
				reader := bufio.NewReader(os.Stdin)
				confirm, _ := reader.ReadString('\n')
				if strings.ToLower(strings.TrimSpace(confirm)) != "y" {
					fmt.Fprintln(cmd.OutOrStdout(), "Keeping existing credential.")
					return nil
				}
			}

			fmt.Fprint(cmd.OutOrStdout(), "Enter user session token: ")
			tokenBytes, err := term.ReadPassword(int(os.Stdin.Fd()))
			fmt.Fprintln(cmd.OutOrStdout())
			if err != nil {
				reader := bufio.NewReader(os.Stdin)
				tokenStr, _ := reader.ReadString('\n')
				tokenBytes = []byte(strings.TrimSpace(tokenStr))
			}

			token := strings.TrimSpace(string(tokenBytes))
			if token == "" {
				return fmt.Errorf("no token provided")
			}

			if err := store.Set(account, token); err != nil {
				return fmt.Errorf("save credential (%s): %w", store.Name(), err)
			}

			fmt.Fprintf(cmd.OutOrStdout(), "Saved %s to %s (service=%s).\n", account, store.Name(), credentials.ServiceName)
			return nil
		},
	}

	return cmd
}

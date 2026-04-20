package cmd

import (
	"fmt"

	"frameworks/cli/internal/credentials"

	"github.com/spf13/cobra"
)

func newLogoutCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "logout",
		Short: "Clear stored user-session credentials",
		Long: `Delete the stored user session and refresh tokens.

The refresh token is cleared alongside the session so a running tray
cannot silently repopulate user_session via its refresh cycle. This
only affects the CLI credential store; FW_USER_TOKEN is not touched.

The platform SERVICE_TOKEN is not stored here — it lives in your
manifest env_files (gitops).`,
		RunE: func(cmd *cobra.Command, args []string) error {
			store := credentials.DefaultStore()
			for _, account := range []string{credentials.AccountUserSession, credentials.AccountUserRefresh} {
				if err := store.Delete(account); err != nil {
					return fmt.Errorf("delete %s from %s: %w", account, store.Name(), err)
				}
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Removed user session from %s.\n", store.Name())
			return nil
		},
	}
}

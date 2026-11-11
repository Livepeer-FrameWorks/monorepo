package cmd

import (
	"fmt"

	"frameworks/cli/internal/credentials"
	"frameworks/cli/internal/ux"

	"github.com/spf13/cobra"
)

func newLogoutCmd() *cobra.Command {
	var localOnly bool
	command := &cobra.Command{
		Use:   "logout",
		Short: "Sign out the stored CLI session",
		Long: `Revoke the stored CLI session on its login Bridge, then clear its credentials.

This only affects the CLI credential store; the Mac tray's credentials and
FW_USER_TOKEN are not touched. Older platforms without named sessions support
local sign-out only. Use --local to clear credentials without contacting Bridge.

The platform SERVICE_TOKEN is not stored here — it lives in your
manifest env_files (gitops).`,
		RunE: func(cmd *cobra.Command, args []string) error {
			store := credentials.DefaultStore()
			if localOnly {
				if err := credentials.ClearSession(cmd.Context(), store); err != nil {
					return err
				}
			} else {
				revoked, err := credentials.LogoutSession(cmd.Context(), store)
				if err != nil {
					return err
				}
				if revoked {
					ux.Success(cmd.OutOrStdout(), "Revoked the CLI session on Bridge")
				} else {
					ux.Warn(cmd.OutOrStdout(), "No named session was revoked on Bridge; clearing local credentials only")
				}
			}
			ux.Success(cmd.OutOrStdout(), fmt.Sprintf("Removed user session from %s", store.Name()))
			ux.PrintNextSteps(cmd.OutOrStdout(), []ux.NextStep{
				{Cmd: "frameworks login", Why: "Log back in when you're ready."},
			})
			return nil
		},
	}
	command.Flags().BoolVar(&localOnly, "local", false, "clear local credentials without revoking the server session")
	return command
}

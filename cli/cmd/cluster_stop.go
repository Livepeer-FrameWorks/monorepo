package cmd

import (
	"fmt"
	"time"

	"frameworks/cli/pkg/ssh"
	"github.com/spf13/cobra"
)

func newClusterStopCmd() *cobra.Command {
	return &cobra.Command{
		Use: "stop <service>", Short: "Stop all replicas of an application service for maintenance",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			rc, err := resolveClusterManifest(cmd)
			if err != nil {
				return err
			}
			defer rc.Cleanup()
			if err := requirePlatformIfImplicitManifest(rc, cmd.OutOrStdout()); err != nil {
				return err
			}
			pool := ssh.NewPool(30*time.Second, stringFlag(cmd, "ssh-key").Value)
			defer pool.Close()
			control := newRestoreServiceControlFn(cmd, rc, pool)
			if err := control.Stop(cmd.Context(), args[0]); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s stopped; it remains stopped until explicitly restarted or deployed.\n", args[0])
			return nil
		},
	}
}

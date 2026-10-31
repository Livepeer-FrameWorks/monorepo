package cmd

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
	"github.com/spf13/cobra"
)

type tierUpgradeRecoveryClient interface {
	AdminRecoverTierUpgrade(context.Context, *purserpb.AdminRecoverTierUpgradeRequest) (*purserpb.AdminRecoverTierUpgradeResponse, error)
}

func runTierUpgradeRecovery(cmd *cobra.Command, p tierUpgradeRecoveryClient, jwt string, r *purserpb.AdminRecoverTierUpgradeRequest, yes, jsonOutput bool) error {
	if err := validateUUID(r.GetTenantId()); err != nil {
		return err
	}
	if err := validateUUID(r.GetUpgradeId()); err != nil {
		return err
	}
	if strings.TrimSpace(r.GetReason()) == "" {
		return fmt.Errorf("--reason is required")
	}
	ctx, cancel := adminRPCContextTimeout(cmd.Context(), jwt, 40*time.Second)
	defer cancel()
	r.Confirmed = false
	preview, err := p.AdminRecoverTierUpgrade(ctx, r)
	if err != nil {
		return err
	}
	fmt.Fprintf(cmd.ErrOrStderr(), "Upgrade %s to tier %s: %s (%s). %s\n", preview.GetUpgradeId(), preview.GetTierId(), preview.GetState(), preview.GetProvider(), preview.GetDetail())
	if !yes {
		fmt.Fprint(cmd.ErrOrStderr(), "Type yes to confirm: ")
		answer, readErr := bufio.NewReader(cmd.InOrStdin()).ReadString('\n')
		if readErr != nil || !strings.EqualFold(strings.TrimSpace(answer), "yes") {
			return fmt.Errorf("tier upgrade recovery confirmation declined")
		}
	}
	r.Confirmed = true
	confirmCtx, confirmCancel := adminRPCContextTimeout(cmd.Context(), jwt, 40*time.Second)
	defer confirmCancel()
	result, err := p.AdminRecoverTierUpgrade(confirmCtx, r)
	if err != nil {
		return err
	}
	return writeTierUpgradeRecovery(cmd.OutOrStdout(), result, jsonOutput)
}

func writeTierUpgradeRecovery(w io.Writer, r *purserpb.AdminRecoverTierUpgradeResponse, jsonOutput bool) error {
	if jsonOutput {
		return writeProtoJSON(w, r)
	}
	_, err := fmt.Fprintf(w, "Tier upgrade recovery: %s (upgrade %s, operator %s)\n%s\n", r.GetState(), r.GetUpgradeId(), r.GetRequestedBy(), r.GetDetail())
	return err
}

func newAdminTierUpgradeRecoveryCmd() *cobra.Command {
	req := &purserpb.AdminRecoverTierUpgradeRequest{}
	var yes bool
	cmd := &cobra.Command{Use: "upgrade", Short: "Inspect and reconcile an accepted tier upgrade without starting another purchase", RunE: func(cmd *cobra.Command, _ []string) error {
		p, cfg, cleanup, err := purserGRPCClientFromContext(cmd.Context())
		if err != nil {
			return err
		}
		defer cleanup()
		defer p.Close()
		return runTierUpgradeRecovery(cmd, p, cfg.Auth.JWT, req, yes, output == "json")
	}}
	cmd.Flags().StringVar(&req.TenantId, "tenant-id", "", "owning tenant UUID (required)")
	cmd.Flags().StringVar(&req.UpgradeId, "upgrade-id", "", "purchase UUID from recovery list (required)")
	cmd.Flags().StringVar(&req.Reason, "reason", "", "operator reason retained with the request (required)")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "confirm the displayed recovery without prompting")
	return cmd
}

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

type tierDowngradeRecoveryClient interface {
	AdminRecoverTierDowngrade(context.Context, *purserpb.AdminRecoverTierDowngradeRequest) (*purserpb.AdminRecoverTierDowngradeResponse, error)
}

// A confirmed request closes the tenant's billing period, which can rate usage,
// read analytics and charge a provider before it returns.
const tierDowngradeRecoveryTimeout = 2 * time.Minute

func runTierDowngradeRecovery(cmd *cobra.Command, p tierDowngradeRecoveryClient, jwt string, r *purserpb.AdminRecoverTierDowngradeRequest, yes, jsonOutput bool) error {
	if err := validateUUID(r.GetTenantId()); err != nil {
		return err
	}
	if strings.TrimSpace(r.GetReason()) == "" {
		return fmt.Errorf("--reason is required")
	}
	ctx, cancel := adminRPCContextTimeout(cmd.Context(), jwt, 10*time.Second)
	defer cancel()
	r.Confirmed, r.TargetTierId = false, ""
	preview, err := p.AdminRecoverTierDowngrade(ctx, r)
	if err != nil {
		return err
	}
	held := preview.GetDowngrade()
	fmt.Fprintf(cmd.ErrOrStderr(), "Downgrade to %s, effective %s, closing invoice %s (%s): %s. %s\n",
		held.GetTargetTierName(), recoveryTimestamp(held.GetEffectiveAt()), dashIfEmpty(held.GetInvoiceId()), dashIfEmpty(held.GetInvoiceStatus()), preview.GetStatus(), preview.GetDetail())
	fmt.Fprintln(cmd.ErrOrStderr(), "Confirming closes the tenant's billing period now and applies the downgrade if nothing still holds it; your identity and reason are recorded.")
	if !yes {
		fmt.Fprint(cmd.ErrOrStderr(), "Type yes to confirm: ")
		answer, readErr := bufio.NewReader(cmd.InOrStdin()).ReadString('\n')
		if readErr != nil || !strings.EqualFold(strings.TrimSpace(answer), "yes") {
			return fmt.Errorf("downgrade recovery confirmation declined; use --yes for non-interactive confirmation")
		}
	}
	r.Confirmed, r.TargetTierId = true, held.GetTargetTierId()
	confirmCtx, confirmCancel := adminRPCContextTimeout(cmd.Context(), jwt, tierDowngradeRecoveryTimeout)
	defer confirmCancel()
	result, err := p.AdminRecoverTierDowngrade(confirmCtx, r)
	if err != nil {
		return err
	}
	return writeTierDowngradeRecovery(cmd.OutOrStdout(), result, jsonOutput)
}

func writeTierDowngradeRecovery(w io.Writer, r *purserpb.AdminRecoverTierDowngradeResponse, jsonOutput bool) error {
	if jsonOutput {
		return writeProtoJSON(w, r)
	}
	_, err := fmt.Fprintf(w, "Downgrade recovery: %s (tenant %s, target %s)\n%s\n", r.GetStatus(), r.GetDowngrade().GetTenantId(), r.GetDowngrade().GetTargetTierName(), r.GetDetail())
	return err
}

// writeHeldDowngrades prints the downgrades PurserTierDowngradeHeld alerts on.
func writeHeldDowngrades(w io.Writer, held []*purserpb.HeldTierDowngrade, hasMore bool) {
	fmt.Fprintln(w)
	if len(held) == 0 {
		fmt.Fprintln(w, "No held downgrades found.")
		return
	}
	fmt.Fprintln(w, "Held downgrades (PurserTierDowngradeHeld):")
	fmt.Fprintln(w, "TENANT  TARGET  EFFECTIVE  INVOICE  INVOICE STATUS")
	for _, h := range held {
		fmt.Fprintf(w, "%s  %s  %s  %s  %s\n", h.GetTenantId(), h.GetTargetTierName(), recoveryTimestamp(h.GetEffectiveAt()), dashIfEmpty(h.GetInvoiceId()), dashIfEmpty(h.GetInvoiceStatus()))
	}
	if hasMore {
		fmt.Fprintln(w, "More held downgrades exist than --limit lists; raise --limit or pass --tenant-id.")
	}
	fmt.Fprintln(w, "Inspect and release a held downgrade with admin billing recovery downgrade --tenant-id <tenant> --reason <reason>.")
}

func newAdminTierDowngradeRecoveryCmd() *cobra.Command {
	req := &purserpb.AdminRecoverTierDowngradeRequest{}
	var yes bool
	cmd := &cobra.Command{Use: "downgrade", Short: "Inspect a held downgrade and close its billing period again to apply it", RunE: func(cmd *cobra.Command, _ []string) error {
		p, cfg, cleanup, err := purserGRPCClientFromContext(cmd.Context())
		if err != nil {
			return err
		}
		defer cleanup()
		defer p.Close()
		return runTierDowngradeRecovery(cmd, p, cfg.Auth.JWT, req, yes, output == "json")
	}}
	cmd.Flags().StringVar(&req.TenantId, "tenant-id", "", "tenant UUID from recovery list (required)")
	cmd.Flags().StringVar(&req.Reason, "reason", "", "operator reason recorded on the billing audit event (required)")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "confirm the displayed recovery without prompting")
	return cmd
}

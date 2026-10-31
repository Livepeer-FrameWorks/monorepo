package cmd

import (
	"bufio"
	"context"
	"fmt"
	"strings"
	"time"

	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
	"github.com/spf13/cobra"
)

type fallbackNoticeRecoveryClient interface {
	AdminResendFreeFallbackNotice(context.Context, *purserpb.AdminResendFreeFallbackNoticeRequest) (*purserpb.AdminResendFreeFallbackNoticeResponse, error)
}

func runFallbackNoticeRecovery(cmd *cobra.Command, p fallbackNoticeRecoveryClient, jwt string, r *purserpb.AdminResendFreeFallbackNoticeRequest, yes, jsonOutput bool) error {
	if err := validateUUID(r.GetTenantId()); err != nil {
		return err
	}
	if strings.TrimSpace(r.GetReason()) == "" {
		return fmt.Errorf("--reason is required")
	}
	ctx, cancel := adminRPCContextTimeout(cmd.Context(), jwt, 10*time.Second)
	defer cancel()
	r.Confirmed = false
	preview, err := p.AdminResendFreeFallbackNotice(ctx, r)
	if err != nil {
		return err
	}
	fmt.Fprintf(cmd.ErrOrStderr(), "Fallback notification to %s: %s. %s\nThis requeues email delivery only.\n", preview.GetRecipient(), preview.GetStatus(), preview.GetDetail())
	if !yes {
		fmt.Fprint(cmd.ErrOrStderr(), "Type yes to confirm: ")
		answer, readErr := bufio.NewReader(cmd.InOrStdin()).ReadString('\n')
		if readErr != nil || !strings.EqualFold(strings.TrimSpace(answer), "yes") {
			return fmt.Errorf("fallback notification retry declined")
		}
	}
	r.Confirmed, r.ScheduledAt, r.Moved = true, preview.GetScheduledAt(), preview.GetMoved()
	confirmCtx, confirmCancel := adminRPCContextTimeout(cmd.Context(), jwt, 10*time.Second)
	defer confirmCancel()
	result, err := p.AdminResendFreeFallbackNotice(confirmCtx, r)
	if err != nil {
		return err
	}
	if jsonOutput {
		return writeProtoJSON(cmd.OutOrStdout(), result)
	}
	_, err = fmt.Fprintf(cmd.OutOrStdout(), "Fallback notification: %s (recipient %s)\n%s\n", result.GetStatus(), result.GetRecipient(), result.GetDetail())
	return err
}

func newAdminFallbackNoticeRecoveryCmd() *cobra.Command {
	req := &purserpb.AdminResendFreeFallbackNoticeRequest{}
	var yes bool
	cmd := &cobra.Command{Use: "notice", Short: "Preview and requeue an abandoned Free-fallback email", RunE: func(cmd *cobra.Command, _ []string) error {
		p, cfg, cleanup, err := purserGRPCClientFromContext(cmd.Context())
		if err != nil {
			return err
		}
		defer cleanup()
		defer p.Close()
		return runFallbackNoticeRecovery(cmd, p, cfg.Auth.JWT, req, yes, output == "json")
	}}
	cmd.Flags().StringVar(&req.TenantId, "tenant-id", "", "owning tenant UUID (required)")
	cmd.Flags().StringVar(&req.Reason, "reason", "", "operator reason retained in the billing audit (required)")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "confirm the displayed notification retry without prompting")
	return cmd
}

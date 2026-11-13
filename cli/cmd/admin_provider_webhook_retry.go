package cmd

import (
	"bufio"
	"context"
	"fmt"
	"strings"

	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
	"github.com/spf13/cobra"
)

type providerWebhookRetryClient interface {
	AdminRetryProviderWebhook(context.Context, *purserpb.AdminRetryProviderWebhookRequest) (*purserpb.AdminRetryProviderWebhookResponse, error)
}

func runProviderWebhookRetry(cmd *cobra.Command, p providerWebhookRetryClient, jwt string, r *purserpb.AdminRetryProviderWebhookRequest, yes, jsonOutput bool) error {
	if err := validateUUID(r.GetTenantId()); err != nil {
		return err
	}
	if err := validateUUID(r.GetWebhookId()); err != nil {
		return err
	}
	if strings.TrimSpace(r.GetReason()) == "" {
		return fmt.Errorf("--reason is required")
	}
	ctx, cancel := adminRPCContext(cmd.Context(), jwt)
	defer cancel()
	r.Confirmed = false
	r.ExpectedState = ""
	preview, err := p.AdminRetryProviderWebhook(ctx, r)
	if err != nil {
		return err
	}
	fmt.Fprintf(cmd.ErrOrStderr(), "Provider callback %s: %s payment %s, status %s, error %s.\n", r.GetWebhookId(), preview.GetProvider(), preview.GetProviderPaymentId(), preview.GetStatus(), preview.GetErrorCode())
	if !preview.GetEligible() {
		if preview.GetProvider() == "mollie" && preview.GetErrorCode() == "evidence_conflict" {
			return fmt.Errorf("mollie settlement conflict requires provider-evidence reconciliation; replaying its payment callback cannot recheck the settlement")
		}
		return fmt.Errorf("callback is not blocked provider work eligible for retry")
	}
	fmt.Fprintln(cmd.ErrOrStderr(), "Retrying reprocesses the existing callback and records your identity and reason. Conflicting payment evidence remains refused.")
	if !yes {
		fmt.Fprint(cmd.ErrOrStderr(), "Type yes to confirm: ")
		answer, readErr := bufio.NewReader(cmd.InOrStdin()).ReadString('\n')
		if readErr != nil || !strings.EqualFold(strings.TrimSpace(answer), "yes") {
			return fmt.Errorf("provider callback retry declined")
		}
	}
	r.Confirmed = true
	r.ExpectedState = preview.GetExpectedState()
	confirmCtx, confirmCancel := adminRPCContext(cmd.Context(), jwt)
	defer confirmCancel()
	result, err := p.AdminRetryProviderWebhook(confirmCtx, r)
	if err != nil {
		return err
	}
	if jsonOutput {
		return writeProtoJSON(cmd.OutOrStdout(), result)
	}
	_, err = fmt.Fprintf(cmd.OutOrStdout(), "Provider callback retry requested: %t (audit %s)\n", result.GetRetryRequested(), result.GetAuditId())
	return err
}

func newAdminProviderWebhookRetryCmd() *cobra.Command {
	req := &purserpb.AdminRetryProviderWebhookRequest{}
	var yes bool
	cmd := &cobra.Command{Use: "retry-webhook", Short: "Preview and request an audited retry of one blocked provider callback", RunE: func(cmd *cobra.Command, _ []string) error {
		p, cfg, cleanup, err := purserGRPCClientFromContext(cmd.Context())
		if err != nil {
			return err
		}
		defer cleanup()
		defer p.Close()
		return runProviderWebhookRetry(cmd, p, cfg.Auth.JWT, req, yes, output == "json")
	}}
	cmd.Flags().StringVar(&req.TenantId, "tenant-id", "", "tenant UUID from recovery list (required)")
	cmd.Flags().StringVar(&req.WebhookId, "webhook-id", "", "blocked callback UUID from recovery list (required)")
	cmd.Flags().StringVar(&req.Reason, "reason", "", "operator reason for retrying after repair (required)")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "confirm the displayed retry without prompting")
	return cmd
}

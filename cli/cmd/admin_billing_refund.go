package cmd

import (
	"context"
	"fmt"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
	"github.com/google/uuid"
	"github.com/spf13/cobra"
	"io"
	"strings"
	"time"
)

type billingRefundRequest struct {
	Inspect                                     bool
	Target                                      billingTenantTarget
	InvoiceID, ReferenceID, Reason, Fingerprint string
}

func runBillingRefund(ctx context.Context, w io.Writer, p adminBillingClient, lookup adminUserLookupClient, jwt string, req billingRefundRequest, outputJSON bool) error {
	if err := req.Target.validate(); err != nil {
		return err
	}
	if req.Inspect {
		if _, err := uuid.Parse(req.ReferenceID); err != nil {
			return fmt.Errorf("--reference-id must be a UUID")
		}
		tenant, _, err := req.Target.resolve(ctx, lookup, jwt)
		if err != nil {
			return err
		}
		cctx, cancel := adminRPCContextTimeout(ctx, jwt, 30*time.Second)
		defer cancel()
		out, err := p.AdminGetInvoiceRefund(cctx, &purserpb.AdminGetInvoiceRefundRequest{TenantId: tenant, RequestId: req.ReferenceID})
		if err != nil {
			return err
		}
		return writeProtoJSON(w, out)
	}
	for _, value := range []string{req.InvoiceID, req.ReferenceID} {
		if _, err := uuid.Parse(value); err != nil {
			return fmt.Errorf("--invoice-id and stable --reference-id must be UUIDs; reuse the reference for confirmation and retries")
		}
	}
	if strings.TrimSpace(req.Reason) == "" {
		return fmt.Errorf("--reason is required")
	}
	if req.Fingerprint != "" && len(req.Fingerprint) != 64 {
		return fmt.Errorf("--confirm must be the exact preview fingerprint")
	}
	tenant, _, err := req.Target.resolve(ctx, lookup, jwt)
	if err != nil {
		return err
	}
	cctx, cancel := adminRPCContextTimeout(ctx, jwt, 30*time.Second)
	defer cancel()
	out, err := p.AdminRefundInvoice(cctx, &purserpb.AdminRefundInvoiceRequest{TenantId: tenant, InvoiceId: req.InvoiceID, RequestId: req.ReferenceID, Reason: strings.TrimSpace(req.Reason), Confirmed: req.Fingerprint != "", ExpectedFingerprint: req.Fingerprint})
	if err != nil {
		return fmt.Errorf("refund request %s: %w; inspect this same reference before retrying", req.ReferenceID, err)
	}
	if out == nil {
		return fmt.Errorf("missing refund receipt for %s; inspect the same reference", req.ReferenceID)
	}
	if outputJSON {
		return writeProtoJSON(w, out)
	}
	fmt.Fprintf(w, "Refund reference: %s\nInvoice: %s\nAmount: %d minor units %s\nProvider: %s\nStatus: %s\n%s\n", out.GetRequestId(), out.GetInvoiceId(), out.GetAmountMinor(), out.GetCurrency(), out.GetProvider(), out.GetStatus(), out.GetDetail())
	if out.GetStatus() == "preview" {
		fmt.Fprintf(w, "Confirm with --confirm %s and the same tenant, invoice, reason and --reference-id.\n", out.GetFingerprint())
	}
	return nil
}
func newAdminBillingRefundInvoiceCmd() *cobra.Command {
	var req billingRefundRequest
	cmd := &cobra.Command{Use: "refund-invoice", Short: "Preview and queue a full original-payment invoice refund", Long: "Preview by default. Confirmation requires its exact --confirm fingerprint and the same stable --reference-id. Returns the original currency to the original card, issues an original-tax credit note after settlement, and holds collection while pending. It does not credit the wallet or cancel the subscription. Partial, split, previously reversed or credited invoices require manual reconciliation.", RunE: func(cmd *cobra.Command, _ []string) error {
		if err := req.Target.validate(); err != nil {
			return err
		}
		p, lookup, jwt, cleanup, err := billingTargetClients(cmd.Context(), req.Target)
		if err != nil {
			return err
		}
		defer cleanup()
		return runBillingRefund(cmd.Context(), cmd.OutOrStdout(), p, lookup, jwt, req, output == "json")
	}}
	cmd.Flags().StringVar(&req.Target.TenantID, "tenant-id", "", "target tenant UUID")
	cmd.Flags().StringVar(&req.Target.Email, "email", "", "email of a user in the target tenant")
	cmd.Flags().BoolVar(&req.Inspect, "inspect", false, "read the existing request receipt; no invoice ID, reason or confirmation is required")
	cmd.Flags().StringVar(&req.InvoiceID, "invoice-id", "", "paid invoice UUID")
	cmd.Flags().StringVar(&req.ReferenceID, "reference-id", "", "stable request UUID, required for preview, confirmation and retries")
	cmd.Flags().StringVar(&req.Reason, "reason", "", "audit reason")
	cmd.Flags().StringVar(&req.Fingerprint, "confirm", "", "exact preview fingerprint; omitted performs only a preview")
	return cmd
}
func newAdminBillingAutomaticTopupCmd() *cobra.Command {
	root := &cobra.Command{Use: "automatic-topup", Short: "Inspect or disable a tenant's explicitly consented automatic top-ups"}
	for _, disable := range []bool{false, true} {
		var target billingTenantTarget
		var reason string
		name := "inspect"
		if disable {
			name = "disable"
		}
		cmd := &cobra.Command{Use: name, RunE: func(cmd *cobra.Command, _ []string) error {
			if err := target.validate(); err != nil {
				return err
			}
			if disable && strings.TrimSpace(reason) == "" {
				return fmt.Errorf("--reason is required")
			}
			p, lookup, jwt, cleanup, err := billingTargetClients(cmd.Context(), target)
			if err != nil {
				return err
			}
			defer cleanup()
			tenant, _, err := target.resolve(cmd.Context(), lookup, jwt)
			if err != nil {
				return err
			}
			ctx, cancel := adminRPCContextTimeout(cmd.Context(), jwt, 30*time.Second)
			defer cancel()
			var out *purserpb.AutomaticTopupSettings
			if disable {
				out, err = p.AdminDisableAutomaticTopup(ctx, &purserpb.AdminDisableAutomaticTopupRequest{TenantId: tenant, Reason: reason})
			} else {
				out, err = p.GetAutomaticTopup(ctx, &purserpb.GetAutomaticTopupRequest{TenantId: tenant})
			}
			if err != nil {
				return err
			}
			if output == "json" {
				return writeProtoJSON(cmd.OutOrStdout(), out)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Tenant: %s\nEnabled: %t\nMonthly reserved/spent: %d %s minor units\nMonthly cash limit: %d %s minor units\nPaused: %s\n", tenant, out.GetEnabled(), out.GetMonthlyReservedOrSpentMinor(), out.GetCurrency(), out.GetMonthlyGrossLimitMinor(), out.GetCurrency(), out.GetPausedReason())
			if disable {
				fmt.Fprintln(cmd.OutOrStdout(), "A payment already in progress may still settle.")
			}
			return nil
		}}
		cmd.Flags().StringVar(&target.TenantID, "tenant-id", "", "target tenant UUID")
		cmd.Flags().StringVar(&target.Email, "email", "", "email of a user in the tenant")
		if disable {
			cmd.Flags().StringVar(&reason, "reason", "", "audit reason")
		}
		root.AddCommand(cmd)
	}
	return root
}

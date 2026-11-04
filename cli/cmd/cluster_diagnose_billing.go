package cmd

import (
	"context"
	"fmt"
	"io"
	"strings"

	"frameworks/cli/internal/ux"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
	"github.com/spf13/cobra"
)

type billingDiagnosisClient interface {
	DiagnoseBilling(context.Context, string) (*purserpb.DiagnoseBillingResponse, error)
}

func newClusterDiagnoseBillingCmd() *cobra.Command {
	var tenant string
	cmd := &cobra.Command{Use: "billing --tenant-id <uuid>", Short: "Inspect a tenant's subscription, payments, invoices, and provider backlog", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			tenant = strings.TrimSpace(tenant)
			if err := validateUUID(tenant); err != nil {
				return fmt.Errorf("--tenant-id: %w", err)
			}
			if err := recordDiagnosticRead(cmd.Context(), "billing", tenant, "purser"); err != nil {
				return err
			}
			p, cfg, cleanup, err := purserGRPCClientFromContext(cmd.Context())
			if err != nil {
				return err
			}
			defer cleanup()
			defer p.Close()
			return runBillingDiagnose(cmd.Context(), cmd.OutOrStdout(), p, cfg.Auth.JWT, tenant, output == "json")
		}}
	cmd.Flags().StringVar(&tenant, "tenant-id", "", "tenant UUID (required)")
	return cmd
}

func runBillingDiagnose(ctx context.Context, w io.Writer, p billingDiagnosisClient, jwt, tenant string, jsonOutput bool) error {
	if err := requireOperatorSession(jwt); err != nil {
		return err
	}
	ctx, cancel := adminRPCContext(ctx, jwt)
	defer cancel()
	d, err := p.DiagnoseBilling(ctx, tenant)
	if err != nil {
		return fmt.Errorf("diagnose billing: %w", err)
	}
	if jsonOutput {
		return writeProtoJSON(w, d)
	}
	ux.Heading(w, "Billing for "+d.GetTenantId())
	fmt.Fprintf(w, "  subscription=%s tier=%s model=%s period=%s to %s\n", d.GetStatus(), d.GetTier(), d.GetBillingModel(), dashIfEmpty(d.GetPeriodStart()), dashIfEmpty(d.GetPeriodEnd()))
	fmt.Fprintf(w, "  provider=%s stripe_customer=%s stripe_subscription=%s tier_stripe_price=%s mollie_subscription=%s\n", dashIfEmpty(d.GetProvider()), dashIfEmpty(d.GetStripeCustomerId()), dashIfEmpty(d.GetStripeSubscriptionId()), dashIfEmpty(d.GetStripePriceId()), dashIfEmpty(d.GetMollieSubscriptionId()))
	fmt.Fprintf(w, "  pending_tier=%s effective=%s provider_confirmed_tier=%s fallback=%s reason=%s\n", dashIfEmpty(d.GetPendingTierId()), dashIfEmpty(d.GetPendingEffectiveAt()), dashIfEmpty(d.GetPendingProviderTierId()), dashIfEmpty(d.GetFreeFallbackAt()), dashIfEmpty(d.GetFreeFallbackReason()))
	for _, b := range d.GetBalances() {
		fmt.Fprintf(w, "  balance=%d cents currency=%s\n", b.GetBalanceCents(), b.GetCurrency())
	}
	fmt.Fprintf(w, "\nPayment intents (%d of %d):\n", len(d.GetIntents()), d.GetIntentCount())
	for _, p := range d.GetIntents() {
		fmt.Fprintf(w, "  %s provider=%s purpose=%s status=%s submission=%s amount_cents=%d currency=%s attempts=%d has_error=%t\n", p.GetId(), p.GetProvider(), p.GetPurpose(), p.GetStatus(), p.GetSubmissionState(), p.GetAmountCents(), p.GetCurrency(), p.GetAttempts(), p.GetHasError())
	}
	fmt.Fprintf(w, "\nInvoices (%d of %d):\n", len(d.GetInvoices()), d.GetInvoiceCount())
	for _, i := range d.GetInvoices() {
		fmt.Fprintf(w, "  %s %s status=%s amount=%s %s due=%s\n", i.GetId(), i.GetNumber(), i.GetStatus(), i.GetAmount(), i.GetCurrency(), i.GetDueAt())
	}
	fmt.Fprintf(w, "\nUnresolved provider work (%d, more=%t):\n", len(d.GetPendingWebhooks()), d.GetPendingWebhooksHasMore())
	for _, i := range d.GetPendingWebhooks() {
		fmt.Fprintf(w, "  %s provider=%s status=%s attempts=%d next=%s\n", i.GetId(), i.GetProvider(), i.GetStatus(), i.GetAttempts(), formatOptionalTime(i.GetNextAttemptAt()))
	}
	return nil
}

package cmd

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"frameworks/cli/internal/ux"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
	"github.com/spf13/cobra"
)

// prepaidDoubleChargeClient is the Purser surface of the prepaid double-charge
// report.
type prepaidDoubleChargeClient interface {
	AdminListPrepaidDoubleCharges(ctx context.Context, req *purserpb.AdminListPrepaidDoubleChargesRequest) (*purserpb.AdminListPrepaidDoubleChargesResponse, error)
}

// runBillingPrepaidDoubleCharges prints the invoices that charged prepaid
// tenants again for usage their prepaid balance had already paid, and what to
// refund for each. It only reads.
func runBillingPrepaidDoubleCharges(ctx context.Context, w io.Writer, p prepaidDoubleChargeClient, jwt, tenantID string, limit int32, outputJSON bool) error {
	tenantID = strings.TrimSpace(tenantID)
	if tenantID != "" {
		if err := validateUUID(tenantID); err != nil {
			return fmt.Errorf("--tenant-id: %w", err)
		}
	}
	if limit < 0 {
		return fmt.Errorf("--limit must not be negative")
	}
	cctx, cancel := adminRPCContextTimeout(ctx, jwt, 60*time.Second)
	defer cancel()
	resp, err := p.AdminListPrepaidDoubleCharges(cctx, &purserpb.AdminListPrepaidDoubleChargesRequest{TenantId: tenantID, Limit: limit})
	if err != nil {
		return fmt.Errorf("list prepaid double charges: %w", err)
	}
	if outputJSON {
		return writeProtoJSON(w, resp)
	}
	if len(resp.GetCharges()) == 0 {
		ux.Success(w, "No invoice charged a prepaid tenant again for usage its prepaid balance paid")
		return nil
	}
	ux.Heading(w, fmt.Sprintf("Invoices that charged prepaid usage twice (%d, %s to refund)",
		len(resp.GetCharges()), formatEURCents(resp.GetTotalDoubleChargedCents())))
	for _, charge := range resp.GetCharges() {
		_, _ = fmt.Fprintf(w, " - tenant=%s invoice=%s (%s) status=%s period=%s..%s refund=%s usage_on_invoice=%s paid_from_balance=%s invoice_credit=%s charged=%s",
			charge.GetTenantId(), charge.GetInvoiceNumber(), charge.GetInvoiceId(), charge.GetStatus(),
			charge.GetPeriodStart().AsTime().UTC().Format(time.DateOnly), charge.GetPeriodEnd().AsTime().UTC().Format(time.DateOnly),
			formatEURCents(charge.GetDoubleChargedCents()), formatEURCents(charge.GetInvoiceUsageCents()),
			formatEURCents(charge.GetPrepaidUsagePaidCents()), formatEURCents(charge.GetInvoiceCreditCents()),
			formatEURCents(charge.GetInvoiceAmountCents()))
		if currency := charge.GetPresentmentCurrency(); currency != "" && currency != "EUR" {
			_, _ = fmt.Fprintf(w, " (%s %d.%02d)", currency, charge.GetPresentmentAmountCents()/100, charge.GetPresentmentAmountCents()%100)
		}
		_, _ = fmt.Fprintln(w)
	}
	if resp.GetTruncated() {
		_, _ = fmt.Fprintln(w, "The list stopped at --limit; raise it to see the rest.")
	}
	return nil
}

func newAdminBillingPrepaidDoubleChargesCmd() *cobra.Command {
	var tenantID string
	var limit int32
	cmd := &cobra.Command{
		Use:   "prepaid-double-charges",
		Short: "List invoices that charged prepaid tenants twice for the same usage (read-only)",
		Long: `List finalized usage invoices that rated usage again which the tenant's
prepaid balance had already paid as it was reported. Before prepaid periods
closed with statements, month-end finalization invoiced prepaid tenants, and a
tenant that moved from prepaid to postpaid mid-period was invoiced for the
prepaid part of the period as well. The invoice took its total from the
prepaid balance as invoice credit, or charged it to the card on file.

For each invoice the report shows the usage it charged, what prepaid
settlements took from the balance for the same period, the invoice credit,
and the amount charged twice: the invoice's usage up to what the balance had
already paid. That is the amount to refund. The command only reads; refund
with 'frameworks admin billing credit' or through the payment provider.

Needs a platform-operator session or the manifest service token.`,
		Example: `  frameworks admin billing prepaid-double-charges
  frameworks admin billing prepaid-double-charges --tenant-id <uuid> --output json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			p, ctxCfg, cleanup, err := purserGRPCClientFromContext(cmd.Context())
			if err != nil {
				return err
			}
			defer func() {
				_ = p.Close()
				cleanup()
			}()
			return runBillingPrepaidDoubleCharges(cmd.Context(), cmd.OutOrStdout(), p, ctxCfg.Auth.JWT, tenantID, limit, output == "json")
		},
	}
	cmd.Flags().StringVar(&tenantID, "tenant-id", "", "only this tenant (UUID)")
	cmd.Flags().Int32Var(&limit, "limit", 0, "at most this many invoices (default 500)")
	return cmd
}

package handlers

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"frameworks/api_billing/internal/database/purserdb"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/billing"
	"github.com/shopspring/decimal"
)

type nativeMollieUpgradeCredit struct {
	invoiceID string
	gross     int64
}

// Mollie payments identify their supply but contain no service-period fields.
// The accepted upgrade receipt supplies the unused net share and exact payment
// IDs; original invoices supply the tax, including after the buyer moves.
func planNativeMollieUpgradeCredit(ctx context.Context, q *purserdb.Queries, tenant string, receipt billing.TierUpgradeReceipt) (previousBaseFee, []nativeMollieUpgradeCredit, error) {
	unique := map[string]bool{}
	for _, id := range strings.Split(receipt.PaidReceiptID, ",") {
		if id = strings.TrimSpace(id); id != "" {
			unique[id] = true
		}
	}
	ids := make([]string, 0, len(unique))
	for id := range unique {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	if len(ids) == 0 {
		if len(receipt.TaxDocumentPaymentIDs) > 0 {
			return previousBaseFee{}, nil, fmt.Errorf("native Mollie upgrade lacks required payment identities")
		}
		return previousBaseFee{}, nil, nil
	}
	for _, id := range receipt.TaxDocumentPaymentIDs {
		if !unique[id] {
			return previousBaseFee{}, nil, fmt.Errorf("native Mollie tax document identity differs from paid receipt")
		}
	}
	rows, err := q.LockNativeMollieUpgradeSources(ctx, purserdb.LockNativeMollieUpgradeSourcesParams{TenantID: tenant, PaymentIds: ids})
	if err != nil {
		return previousBaseFee{}, nil, err
	}
	if len(rows) == 0 {
		if len(receipt.TaxDocumentPaymentIDs) > 0 {
			return previousBaseFee{}, nil, fmt.Errorf("native Mollie upgrade awaits original tax documents")
		}
		return previousBaseFee{}, nil, nil
	}
	if len(rows) != len(ids) {
		return previousBaseFee{}, nil, fmt.Errorf("native Mollie upgrade has incomplete original invoice evidence")
	}
	var totalNet int64
	for _, row := range rows {
		if row.TaxStatus != "ready" || row.NetMinor <= 0 || row.GrossMinor <= 0 || row.CreditedNet > row.NetMinor || row.CreditedGross > row.GrossMinor {
			return previousBaseFee{}, nil, fmt.Errorf("native Mollie upgrade source %s has incomplete tax or credit evidence", row.ID)
		}
		if !row.Consumed {
			totalNet += row.NetMinor - row.CreditedNet
		}
	}
	unusedNet := min(max(receipt.UnusedPaidCents, 0), totalNet)
	plan := previousBaseFee{found: true, taxReady: true, creditCurrency: "EUR", credit: baseFeeCredit{providerPaymentID: receipt.PaidReceiptID, periodStart: receipt.PaidPeriodStart, periodEnd: receipt.PaidPeriodEnd}}
	var credits []nativeMollieUpgradeCredit
	var seenNet, allocatedNet int64
	for _, row := range rows {
		if row.Consumed {
			continue
		}
		net := row.NetMinor - row.CreditedNet
		seenNet += net
		share := int64(0)
		if totalNet > 0 {
			cumulative := decimal.NewFromInt(unusedNet).Mul(decimal.NewFromInt(seenNet)).Div(decimal.NewFromInt(totalNet)).Round(0).IntPart()
			share, allocatedNet = cumulative-allocatedNet, cumulative
		}
		gross := int64(0)
		if net > 0 {
			gross = decimal.NewFromInt(row.GrossMinor - row.CreditedGross).Mul(decimal.NewFromInt(share)).Div(decimal.NewFromInt(net)).Round(0).IntPart()
		}
		credits = append(credits, nativeMollieUpgradeCredit{invoiceID: row.ID, gross: gross})
		plan.credit.cents += gross
	}
	plan.creditOriginalMinor, plan.sourceGrossEUR, plan.sourceGrossOriginal = plan.credit.cents, plan.credit.cents, plan.credit.cents
	return plan, credits, nil
}

func consumeNativeMollieUpgradeCredit(ctx context.Context, q *purserdb.Queries, tenant, replacement string, receipt billing.TierUpgradeReceipt, sources []nativeMollieUpgradeCredit, applied int64) error {
	var total, seen, allocated int64
	for _, source := range sources {
		total += source.gross
	}
	for _, source := range sources {
		seen += source.gross
		share := int64(0)
		if total > 0 {
			cumulative := decimal.NewFromInt(applied).Mul(decimal.NewFromInt(seen)).Div(decimal.NewFromInt(total)).Round(0).IntPart()
			share, allocated = cumulative-allocated, cumulative
		}
		if err := issueUnusedBaseFeeCredit(ctx, q, tenant, source.invoiceID, share, "EUR", receipt.PeriodStart); err != nil {
			return err
		}
		previous := previousBaseFee{found: true, credit: baseFeeCredit{invoiceID: source.invoiceID}}
		credit := baseFeeCredit{cents: share, forfeitedCents: source.gross - share}
		if err := consumePreviousBaseFee(ctx, q, tenant, previous, credit, replacement, receipt.PeriodStart); err != nil {
			return err
		}
	}
	return nil
}

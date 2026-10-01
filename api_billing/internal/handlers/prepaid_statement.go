package handlers

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	billingpkg "frameworks/api_billing/internal/billing"
	"frameworks/api_billing/internal/database/purserdb"
	"frameworks/api_billing/internal/operator"
	"frameworks/api_billing/internal/pricing"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/billing"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
)

// prepaidSettledUsageExcludedKey marks the usage_details of a usage invoice
// rated without the usage that prepaid settlements paid from the balance.
const prepaidSettledUsageExcludedKey = "prepaid_settled_usage_excluded"

// prepaidStatementUsageToleranceMicro is how far a statement's rated usage may
// sit from what the balance paid before the difference is logged: rated lines
// round to the cent and settlements carry sub-cent remainders.
const prepaidStatementUsageToleranceMicro = 10_000

// PrepaidStatementDetails is what a prepaid statement states besides its line
// items. It is stored as usage_details.statement. Amounts are EUR cents.
type PrepaidStatementDetails struct {
	// ClosesPrepaidPhase is set on the statement a switch to postpaid wrote.
	ClosesPrepaidPhase bool `json:"closes_prepaid_phase"`
	// RatedUsageCents is the period's usage at rated prices, without fees.
	RatedUsageCents int64 `json:"rated_usage_cents"`
	// PaidFromBalanceCents is what prepaid usage settlements took from the
	// balance for the period's usage as it was reported. It is what the
	// usage cost; RatedUsageCents only restates it.
	PaidFromBalanceCents int64 `json:"paid_from_balance_cents"`
	PaidFromBalanceMicro int64 `json:"paid_from_balance_micro"`
	// UsageDifferenceCents is RatedUsageCents less PaidFromBalanceCents.
	UsageDifferenceCents int64 `json:"usage_difference_cents"`
	// SettledWithStatementCents is the part of PaidFromBalanceCents the
	// statement itself took from the balance (negative: returned): the
	// phase's usage no settlement had paid when the statement was written,
	// such as a report processed under the model the tenant left.
	SettledWithStatementCents int64 `json:"settled_with_statement_cents"`
	// PeriodFeesCents is the period's base and monthly cluster fees, charged
	// to the balance with this statement.
	PeriodFeesCents int64 `json:"period_fees_cents"`
	// OpeningBalanceCents and PeriodEndBalanceCents are the balance at the
	// period's start and end; the movements between them are top-ups, usage
	// deductions and other movements posted inside the period.
	OpeningBalanceCents   int64 `json:"opening_balance_cents"`
	TopupCents            int64 `json:"topup_cents"`
	Topups                int64 `json:"topups"`
	UsagePostedCents      int64 `json:"usage_posted_cents"`
	OtherMovementsCents   int64 `json:"other_movements_cents"`
	PeriodEndBalanceCents int64 `json:"period_end_balance_cents"`
	// ClosingBalanceCents is the period-end balance less the period fees and
	// what the statement settled.
	ClosingBalanceCents int64 `json:"closing_balance_cents"`
	// AmountDueCents is always zero: the balance paid everything stated.
	AmountDueCents int64 `json:"amount_due_cents"`
}

// prepaidStatementRun is a prepaid period rated for its statement, outside
// the transaction that writes it.
type prepaidStatementRun struct {
	jobs         *JobManager
	tenantID     string
	billingEmail string
	periodStart  time.Time
	periodEnd    time.Time
	// phase is the usage phase the statement closes; its split period's
	// bounds are the ones its prepaid settlements rated against.
	phase       usagePhase
	tier        *billingpkg.EffectiveTier
	rating      *clusterRatingResult
	usageTotals map[string]float64
	enrichment  map[string]interface{}
	closesPhase bool
	// ratedUsage is the usage lines' total; periodFees the base and monthly
	// cluster fee lines' total.
	ratedUsage decimal.Decimal
	periodFees decimal.Decimal
}

// prepaidStatementResult is what writing a statement did.
type prepaidStatementResult struct {
	statementID   string
	number        string
	details       PrepaidStatementDetails
	balanceBefore int64
	balanceAfter  int64
	feeCharged    bool
	// settled is what the statement settled of the phase's usage.
	settled         prepaidPhaseSettlement
	returnedCredit  invoiceCreditChange
	emailQueued     bool
	usageDifference int64
}

// ratePrepaidStatement rates a prepaid tenant's usage of the prepaid phase
// [phase.start, phase.end), the records its prepaid settlements rated, at the
// prices they used, with the period's monthly fees. Rating the statement
// never touches the balance.
func (jm *JobManager) ratePrepaidStatement(ctx context.Context, tier *billingpkg.EffectiveTier, tenantID, billingEmail string, phase usagePhase, baseProviderManaged, closesPhase bool) (*prepaidStatementRun, error) {
	if tier == nil {
		return nil, errors.New("rate prepaid statement: no tier")
	}
	if tier.Currency != billing.LedgerCurrency {
		return nil, fmt.Errorf("prepaid tier %s prices in %s, not the %s prepaid balance", tier.TierName, tier.Currency, billing.LedgerCurrency)
	}
	periodStart, periodEnd := phase.start, phase.end
	perClusterUsage, perClusterDimensioned, err := collectPhaseUsage(ctx, jm.db, tenantID, phase)
	if err != nil {
		return nil, fmt.Errorf("collect statement usage: %w", err)
	}
	result, err := jm.rateInvoiceForTenant(ctx, tenantID, periodStart, periodEnd, tier, true, baseProviderManaged, perClusterUsage, perClusterDimensioned)
	if err != nil {
		return nil, fmt.Errorf("rate statement usage: %w", err)
	}
	if len(result.ManualReviewReasons) > 0 {
		return nil, fmt.Errorf("statement cluster pricing is unresolved: %s", strings.Join(result.ManualReviewReasons, "; "))
	}
	applyBaseFeeShare(result, phase)
	run := &prepaidStatementRun{
		jobs: jm, tenantID: tenantID, billingEmail: billingEmail,
		periodStart: periodStart, periodEnd: periodEnd, phase: phase,
		tier: tier, rating: result, usageTotals: flattenUsageAcrossClusters(perClusterUsage),
		closesPhase: closesPhase, periodFees: result.BaseAmount,
	}
	for _, line := range result.UsageLines {
		if line.PricingSource == pricing.SourceClusterMonthly {
			run.periodFees = run.periodFees.Add(line.Amount)
			continue
		}
		run.ratedUsage = run.ratedUsage.Add(line.Amount)
	}
	if jm.periscopeClient != nil {
		enrichCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		run.enrichment = jm.enrichInvoiceFromPeriscope(enrichCtx, tenantID, periodStart, periodEnd)
		cancel()
	}
	return run, nil
}

// prepaidPhaseSettlement is what a statement settled of its phase's usage.
type prepaidPhaseSettlement struct {
	micro                       int64
	balanceBefore, balanceAfter int64
	applied                     bool
}

// cents is what the settlement took from the balance, in cents.
func (s prepaidPhaseSettlement) cents() int64 { return s.balanceBefore - s.balanceAfter }

// settlePrepaidPhaseUsageTx settles from the prepaid balance what the usage
// of a prepaid phase still owes when its statement is written: the phase's
// cumulative usage rated the way processPrepaidUsage rates it, less what its
// prepaid settlements took. Usage that reached Purser in the phase but was
// processed under the model the tenant was leaving, or after the last
// settlement, is paid here and nowhere else; a settlement that took usage
// the phase does not hold is returned. The caller holds the subscription row
// lock, so no usage of the phase is written or settled meanwhile. The
// settlement is recorded like a report's, under the statement's phase.
func (run *prepaidStatementRun) settlePrepaidPhaseUsageTx(ctx context.Context, tx *sql.Tx) (prepaidPhaseSettlement, error) {
	var settlement prepaidPhaseSettlement
	if run.jobs == nil || run.tier == nil || !run.tier.MeteringEnabled {
		return settlement, nil
	}
	queries := purserdb.New(tx)
	tenantID := run.tenantID
	if err := queries.EnsurePrepaidBalance(ctx, purserdb.EnsurePrepaidBalanceParams{TenantID: tenantID, Currency: billing.LedgerCurrency}); err != nil {
		return settlement, fmt.Errorf("ensure prepaid balance: %w", err)
	}
	locked, err := queries.LockPrepaidBalance(ctx, purserdb.LockPrepaidBalanceParams{TenantID: tenantID, Currency: billing.LedgerCurrency})
	if err != nil {
		return settlement, fmt.Errorf("lock prepaid balance: %w", err)
	}
	settlement.balanceBefore, settlement.balanceAfter = locked.BalanceCents, locked.BalanceCents
	perCluster, err := collectPhaseDimensionedUsage(ctx, queries, tenantID, run.phase)
	if err != nil {
		return settlement, fmt.Errorf("collect the phase's usage: %w", err)
	}
	// Prepaid settlements rate against the prepaid period the phase started,
	// which runs to the end of the split period.
	ratedUntil := run.phase.splitEnd()
	desired, err := run.jobs.rateCumulativePrepaidUsage(ctx, tenantID, run.periodStart, ratedUntil, run.tier, perCluster)
	if err != nil {
		return settlement, fmt.Errorf("rate the phase's usage: %w", err)
	}
	desiredMicro := desired.Shift(6).Round(0).IntPart()
	settled, err := queries.SumPrepaidUsageSettlementsForStatement(ctx, purserdb.SumPrepaidUsageSettlementsForStatementParams{
		TenantID: tenantID, PeriodStart: run.periodStart, PeriodEnd: run.periodEnd,
	})
	if err != nil {
		return settlement, fmt.Errorf("sum prepaid usage settlements: %w", err)
	}
	settlement.micro = desiredMicro - settled.AmountMicro
	if settlement.micro == 0 {
		return settlement, nil
	}
	reportID := "statement-" + uuid.NewSHA1(uuid.NameSpaceOID, []byte(fmt.Sprintf("prepaid-phase:%s:%s:%s", tenantID,
		run.periodStart.UTC().Format(time.RFC3339Nano), run.periodEnd.UTC().Format(time.RFC3339Nano)))).String()
	description := fmt.Sprintf("Usage %s to %s settled with its prepaid statement",
		run.periodStart.UTC().Format(time.RFC3339), run.periodEnd.UTC().Format(time.RFC3339))
	settlement.balanceBefore, settlement.balanceAfter, settlement.applied, err = applyPrepaidBalanceForUsageMicroLocked(
		ctx, queries, tenantID, settlement.micro, description, uuid.NewSHA1(uuid.NameSpaceOID, []byte(reportID)),
		locked.BalanceCents, locked.BalanceRemainderMicro,
	)
	if err != nil {
		return settlement, fmt.Errorf("settle the phase's usage from the prepaid balance: %w", err)
	}
	if !settlement.applied {
		return settlement, fmt.Errorf("the phase's usage settlement %s was already posted", reportID)
	}
	rows, err := queries.InsertPrepaidUsageSettlement(ctx, purserdb.InsertPrepaidUsageSettlementParams{
		ReportID: reportID, TenantID: tenantID,
		BillingPeriodStart: run.periodStart, BillingPeriodEnd: ratedUntil,
		AmountMicro: settlement.micro, CumulativeAmountMicro: desiredMicro, Currency: billing.LedgerCurrency,
	})
	if err != nil {
		return settlement, fmt.Errorf("record the phase's usage settlement: %w", err)
	}
	if rows != 1 {
		return settlement, fmt.Errorf("the phase's usage settlement %s was already recorded", reportID)
	}
	return settlement, nil
}

// writePrepaidStatementTx writes the statement of a rated prepaid period: it
// settles what the phase's usage still owes the balance
// (settlePrepaidPhaseUsageTx), then writes its header with nothing due, its
// line items, its email, and the charge of the period's monthly fees to the
// prepaid balance. Usage the prepaid settlements paid is never charged again;
// the statement states it. An open draft left by postpaid billing becomes the
// statement, and any invoice credit that draft held returns to the balance
// first. draftPeriodStart is the draft's period start, zero without a draft.
func writePrepaidStatementTx(ctx context.Context, tx *sql.Tx, run *prepaidStatementRun, draftInvoiceID string, draftPeriodStart, finalizedAt time.Time) (prepaidStatementResult, error) {
	var result prepaidStatementResult
	queries := purserdb.New(tx)
	tenantID := run.tenantID

	creditPeriodStart := run.periodStart
	if draftInvoiceID != "" && !draftPeriodStart.IsZero() {
		creditPeriodStart = draftPeriodStart
	}
	returned, err := reconcileInvoicePrepaidCreditTx(ctx, tx, tenantID, creditPeriodStart, 0)
	if err != nil {
		return result, fmt.Errorf("return invoice credit of the period: %w", err)
	}
	result.returnedCredit = returned
	if result.settled, err = run.settlePrepaidPhaseUsageTx(ctx, tx); err != nil {
		return result, err
	}
	if err = queries.EnsurePrepaidBalance(ctx, purserdb.EnsurePrepaidBalanceParams{TenantID: tenantID, Currency: billing.LedgerCurrency}); err != nil {
		return result, fmt.Errorf("ensure prepaid balance: %w", err)
	}
	locked, err := queries.LockPrepaidBalance(ctx, purserdb.LockPrepaidBalanceParams{TenantID: tenantID, Currency: billing.LedgerCurrency})
	if err != nil {
		return result, fmt.Errorf("lock prepaid balance: %w", err)
	}
	ledger, err := queries.GetPrepaidStatementLedger(ctx, purserdb.GetPrepaidStatementLedgerParams{
		TenantID: tenantID, PeriodStart: run.periodStart, PeriodEnd: run.periodEnd,
	})
	if err != nil {
		return result, fmt.Errorf("read statement ledger: %w", err)
	}
	settled, err := queries.SumPrepaidUsageSettlementsForStatement(ctx, purserdb.SumPrepaidUsageSettlementsForStatementParams{
		TenantID: tenantID, PeriodStart: run.periodStart, PeriodEnd: run.periodEnd,
	})
	if err != nil {
		return result, fmt.Errorf("sum prepaid usage settlements: %w", err)
	}

	feesCents := run.periodFees.Shift(2).Round(0).IntPart()
	ratedMicro := run.ratedUsage.Shift(6).Round(0).IntPart()
	details := PrepaidStatementDetails{
		ClosesPrepaidPhase:    run.closesPhase,
		RatedUsageCents:       run.ratedUsage.Shift(2).Round(0).IntPart(),
		PaidFromBalanceMicro:  settled.AmountMicro,
		PaidFromBalanceCents:  decimal.New(settled.AmountMicro, -4).Round(0).IntPart(),
		PeriodFeesCents:       feesCents,
		OpeningBalanceCents:   locked.BalanceCents - ledger.SincePeriodStartCents,
		TopupCents:            ledger.TopupCents,
		Topups:                ledger.Topups,
		UsagePostedCents:      -ledger.UsageCents,
		PeriodEndBalanceCents: locked.BalanceCents - ledger.SincePeriodEndCents,
	}
	details.UsageDifferenceCents = details.RatedUsageCents - details.PaidFromBalanceCents
	details.SettledWithStatementCents = result.settled.cents()
	details.OtherMovementsCents = details.PeriodEndBalanceCents - details.OpeningBalanceCents - ledger.TopupCents - ledger.UsageCents
	details.ClosingBalanceCents = details.PeriodEndBalanceCents - feesCents - details.SettledWithStatementCents
	result.details = details
	result.usageDifference = ratedMicro - settled.AmountMicro

	usageDetails := map[string]interface{}{
		"period_start": run.periodStart,
		"period_end":   run.periodEnd,
		"tier_info": map[string]interface{}{
			"tier_id":          run.tier.TierID,
			"tier_name":        run.tier.TierName,
			"base_price":       run.tier.BasePrice.String(),
			"metering_enabled": run.tier.MeteringEnabled,
		},
	}
	for key, value := range run.usageTotals {
		usageDetails[key] = value
	}
	for key, value := range run.enrichment {
		usageDetails[key] = value
	}
	usageDetails["statement"] = details
	usageJSON, err := json.Marshal(usageDetails)
	if err != nil {
		return result, fmt.Errorf("marshal statement details: %w", err)
	}

	baseAmount := run.rating.BaseAmount.Round(2).String()
	meteredAmount := run.rating.UsageAmount.Round(2).String()
	grossMetered := run.rating.GrossUsageAmount.Round(2).String()
	if draftInvoiceID != "" {
		row, convertErr := queries.ConvertDraftToPrepaidStatement(ctx, purserdb.ConvertDraftToPrepaidStatementParams{
			FinalizedAt: finalizedAt, BaseAmount: baseAmount, MeteredAmount: meteredAmount,
			GrossMeteredAmount: grossMetered, UsageDetails: usageJSON, PeriodStart: run.periodStart, PeriodEnd: run.periodEnd,
			InvoiceID: draftInvoiceID, TenantID: tenantID,
		})
		if errors.Is(convertErr, sql.ErrNoRows) {
			return result, fmt.Errorf("draft %s of the period is no longer open", draftInvoiceID)
		}
		if convertErr != nil {
			return result, fmt.Errorf("turn the period's draft into its statement: %w", convertErr)
		}
		result.statementID, result.number = row.ID, row.InvoiceNumber
	} else {
		row, insertErr := queries.InsertPrepaidStatement(ctx, purserdb.InsertPrepaidStatementParams{
			TenantID: tenantID, FinalizedAt: finalizedAt, BaseAmount: baseAmount, MeteredAmount: meteredAmount,
			GrossMeteredAmount: grossMetered, UsageDetails: usageJSON,
			PeriodStart: run.periodStart, PeriodEnd: run.periodEnd,
		})
		if errors.Is(insertErr, sql.ErrNoRows) {
			return result, fmt.Errorf("the period starting %s already has a billing document", run.periodStart.Format(time.RFC3339))
		}
		if insertErr != nil {
			return result, fmt.Errorf("insert statement: %w", insertErr)
		}
		result.statementID, result.number = row.ID, row.InvoiceNumber
	}
	if err = persistInvoiceLineItems(ctx, tx, result.statementID, tenantID, run.rating); err != nil {
		return result, err
	}
	if err = queries.MarkUsageAdjustmentsAppliedToInvoice(ctx, purserdb.MarkUsageAdjustmentsAppliedToInvoiceParams{
		InvoiceID: result.statementID, TenantID: tenantID, PeriodStart: run.periodStart, PeriodEnd: run.periodEnd,
		ChainFrom: run.phase.splitStart(), ReceivedBefore: run.phase.receivedBefore,
	}); err != nil {
		return result, fmt.Errorf("mark usage adjustments applied to statement: %w", err)
	}
	// Marketplace operators earn their share of usage the balance paid.
	if err = operator.ComputeAndPersistCredits(ctx, tx, result.statementID, "paid"); err != nil {
		return result, fmt.Errorf("persist operator credits: %w", err)
	}

	result.balanceBefore, result.balanceAfter = locked.BalanceCents+result.settled.cents(), locked.BalanceCents
	if feesCents > 0 {
		result.balanceAfter = locked.BalanceCents - feesCents
		if err = queries.InsertPrepaidStatementFeeTransaction(ctx, purserdb.InsertPrepaidStatementFeeTransactionParams{
			TenantID: tenantID, AmountCents: -feesCents, BalanceAfterCents: result.balanceAfter,
			Description: sql.NullString{String: fmt.Sprintf("Monthly fees %s to %s (prepaid statement %s)",
				run.periodStart.UTC().Format(time.DateOnly), run.periodEnd.UTC().Format(time.DateOnly), result.number), Valid: true},
			StatementID: result.statementID,
		}); err != nil {
			return result, fmt.Errorf("record monthly fees charged to the prepaid balance: %w", err)
		}
		if err = queries.UpdatePrepaidBalance(ctx, purserdb.UpdatePrepaidBalanceParams{
			BalanceCents: result.balanceAfter, TenantID: tenantID, Currency: billing.LedgerCurrency,
		}); err != nil {
			return result, fmt.Errorf("charge monthly fees to the prepaid balance: %w", err)
		}
		result.feeCharged = true
	}
	if recipient := strings.TrimSpace(run.billingEmail); recipient != "" {
		if err = queries.EnqueuePrepaidStatementEmail(ctx, purserdb.EnqueuePrepaidStatementEmailParams{
			InvoiceID: result.statementID, TenantID: tenantID, Recipient: recipient,
		}); err != nil {
			return result, fmt.Errorf("enqueue statement email: %w", err)
		}
		result.emailQueued = true
	}
	return result, nil
}

// logPrepaidStatement records a committed statement, the monthly fees it
// charged, and a difference between its rated usage and what the balance paid.
func logPrepaidStatement(logger logging.Logger, run *prepaidStatementRun, result prepaidStatementResult) {
	logInvoiceCreditChange(logger, run.tenantID, result.statementID, run.periodStart, result.returnedCredit)
	fields := logging.Fields{
		"tenant_id":                run.tenantID,
		"statement_id":             result.statementID,
		"statement_number":         result.number,
		"period_start":             run.periodStart.UTC().Format(time.RFC3339),
		"period_end":               run.periodEnd.UTC().Format(time.RFC3339),
		"closes_prepaid_phase":     run.closesPhase,
		"rated_usage_cents":        result.details.RatedUsageCents,
		"paid_from_balance_cents":  result.details.PaidFromBalanceCents,
		"period_fees_cents":        result.details.PeriodFeesCents,
		"settled_with_statement":   result.details.SettledWithStatementCents,
		"opening_balance_cents":    result.details.OpeningBalanceCents,
		"period_end_balance_cents": result.details.PeriodEndBalanceCents,
		"closing_balance_cents":    result.details.ClosingBalanceCents,
		"email_queued":             result.emailQueued,
	}
	logger.WithFields(fields).Info("Created prepaid statement")
	if result.settled.applied {
		logger.WithFields(logging.Fields{
			"tenant_id":      run.tenantID,
			"statement_id":   result.statementID,
			"period_start":   run.periodStart.UTC().Format(time.RFC3339),
			"period_end":     run.periodEnd.UTC().Format(time.RFC3339),
			"amount_micro":   result.settled.micro,
			"balance_before": result.settled.balanceBefore,
			"balance_after":  result.settled.balanceAfter,
		}).Info("Settled the prepaid phase's unpaid usage from the balance with its statement")
	}
	if result.feeCharged {
		logger.WithFields(logging.Fields{
			"tenant_id":        run.tenantID,
			"statement_id":     result.statementID,
			"statement_number": result.number,
			"period_start":     run.periodStart.UTC().Format(time.RFC3339),
			"period_end":       run.periodEnd.UTC().Format(time.RFC3339),
			"fees_cents":       result.details.PeriodFeesCents,
			"balance_before":   result.balanceBefore,
			"balance_after":    result.balanceAfter,
		}).Info("Charged monthly fees to the prepaid balance")
	}
	if result.usageDifference > prepaidStatementUsageToleranceMicro || result.usageDifference < -prepaidStatementUsageToleranceMicro {
		logger.WithFields(logging.Fields{
			"tenant_id":               run.tenantID,
			"statement_id":            result.statementID,
			"period_start":            run.periodStart.UTC().Format(time.RFC3339),
			"period_end":              run.periodEnd.UTC().Format(time.RFC3339),
			"rated_usage_cents":       result.details.RatedUsageCents,
			"paid_from_balance_micro": result.details.PaidFromBalanceMicro,
			"difference_micro":        result.usageDifference,
		}).Warn("Prepaid statement usage differs from what the balance paid; the statement states the balance deductions")
	}
}

// finalizePrepaidStatementPeriod closes a prepaid tenant's ended period with
// its statement and moves the subscription to its next period, in one
// transaction. It reports whether it wrote the statement.
func (jm *JobManager) finalizePrepaidStatementPeriod(ctx context.Context, subscription purserdb.ListSubscriptionsDueForInvoiceRow, periodStart, periodEnd time.Time, draftInvoiceID string) bool {
	tenantID := subscription.TenantID
	tier, err := billingpkg.LoadEffectiveTier(ctx, jm.db, tenantID)
	if err != nil {
		jm.logger.WithError(err).WithField("tenant_id", tenantID).Error("Failed to load tier for prepaid statement")
		return false
	}
	baseFeeInvoiced, err := purserdb.New(jm.db).BaseFeeInvoiceExistsForPeriod(ctx, purserdb.BaseFeeInvoiceExistsForPeriodParams{
		TenantID: tenantID, PeriodStart: periodStart,
	})
	if err != nil {
		jm.logger.WithError(err).WithField("tenant_id", tenantID).Error("Failed to check advance base-fee invoice for prepaid statement")
		return false
	}
	baseProviderManaged := subscription.StripeSubscriptionID.Valid || subscription.MollieSubscriptionID.Valid || baseFeeInvoiced
	// A tenant whose period switches split closes only the last phase of the
	// split period here.
	phase, err := prepaidUsagePhase(ctx, jm.db, tenantID, periodStart, periodEnd)
	if err != nil {
		jm.logger.WithError(err).WithField("tenant_id", tenantID).Error("Failed to look up the phases of the period for the prepaid statement")
		return false
	}
	run, err := jm.ratePrepaidStatement(ctx, tier, tenantID, subscription.BillingEmail.String, phase, baseProviderManaged, false)
	if err != nil {
		jm.logger.WithError(err).WithField("tenant_id", tenantID).Error("Failed to rate prepaid statement; retrying next run")
		return false
	}
	nextPeriodEnd := nextBillingPeriodEnd(phase.chainFrom, periodEnd)
	var result prepaidStatementResult
	err = withTx(ctx, jm.db, func(tx *sql.Tx) error {
		// The subscription row lock comes before the balance's, in the order
		// usage receipt, settlement and switches take them.
		if txErr := purserdb.New(tx).LockSubscriptionForPeriodClose(ctx, tenantID); txErr != nil {
			return fmt.Errorf("lock subscription: %w", txErr)
		}
		var txErr error
		result, txErr = writePrepaidStatementTx(ctx, tx, run, draftInvoiceID, periodStart, time.Now().UTC())
		if txErr != nil {
			return txErr
		}
		rows, txErr := purserdb.New(tx).AdvanceSubscriptionBillingPeriod(ctx, purserdb.AdvanceSubscriptionBillingPeriodParams{
			NextBillingDate:    sql.NullTime{Time: nextPeriodEnd, Valid: true},
			BillingPeriodStart: sql.NullTime{Time: periodEnd, Valid: true},
			BillingPeriodEnd:   sql.NullTime{Time: nextPeriodEnd, Valid: true},
			TenantID:           tenantID,
		})
		if txErr != nil {
			return fmt.Errorf("advance subscription period: %w", txErr)
		}
		if rows == 0 {
			return fmt.Errorf("advance subscription period: no subscription row for tenant %s", tenantID)
		}
		return nil
	})
	if err != nil {
		jm.logger.WithError(err).WithField("tenant_id", tenantID).Error("Failed to create prepaid statement")
		return false
	}
	logPrepaidStatement(jm.logger, run, result)
	if (result.feeCharged || result.settled.applied) && jm.thresholdEnforcer != nil {
		if thresholdErr := jm.thresholdEnforcer.EnforcePrepaidThresholds(ctx, tenantID, result.balanceBefore, result.balanceAfter); thresholdErr != nil {
			jm.logger.WithError(thresholdErr).WithField("tenant_id", tenantID).Warn("Failed to enforce prepaid thresholds after the statement's charges")
		}
	}
	jm.applyPendingDowngrade(ctx, tenantID)
	return true
}

// ErrPrepaidPhaseChanged means the subscription is no longer the prepaid one
// its closing statement was rated for, or usage of the phase reached Purser
// meanwhile; the switch is retried.
var ErrPrepaidPhaseChanged = errors.New("the prepaid subscription or its usage changed while its closing statement was prepared")

// PrepaidPhaseClose closes a prepaid tenant's prepaid phase when it switches
// to postpaid. Its usage was paid from the balance as it was reported, so it
// belongs on a prepaid statement and never on the postpaid invoice: the
// statement covers the period's start up to the switch, and the postpaid
// period starts at the switch and ends where the prepaid period would have.
// Rating happens before the switch transaction, writing inside it.
type PrepaidPhaseClose struct {
	logger        logging.Logger
	tenantID      string
	periodStart   sql.NullTime
	periodEnd     sql.NullTime
	mollieNext    sql.NullTime
	switchAt      time.Time
	nextPeriodEnd time.Time
	// received is the count of usage records and corrections the statement
	// was rated from; CountPhaseUsageReceivedBefore at commit must still
	// return it.
	received int64
	// run is nil when the switch falls on the period start, which leaves no
	// prepaid phase to state.
	run    *prepaidStatementRun
	result prepaidStatementResult
	closed bool
}

// currentBillingPeriod is the subscription's current period the way month-end
// finalization and postpaid drafts read it: ending on Mollie's next payment
// date when Mollie collects the subscription (mollieAnchoredPeriod), else the
// stored period, else the calendar month of at.
func currentBillingPeriod(start, end, mollieNext sql.NullTime, at time.Time) (time.Time, time.Time) {
	if mollieNext.Valid {
		periodStart, periodEnd := mollieAnchoredPeriod(mollieNext.Time, start)
		return periodStart.UTC(), periodEnd.UTC()
	}
	return prepaidPhasePeriod(start, end, at)
}

// countPhaseReceived counts the usage records and corrections of phase that
// reached Purser before it closes.
func countPhaseReceived(ctx context.Context, queries *purserdb.Queries, tenantID string, phase usagePhase) (int64, error) {
	received, err := queries.CountPhaseUsageReceivedBefore(ctx, purserdb.CountPhaseUsageReceivedBeforeParams{
		TenantID: tenantID, WindowStart: phase.start, WindowEnd: phase.end,
		ChainFrom: phase.splitStart(), ReceivedBefore: phase.receivedBefore.Time,
	})
	if err != nil {
		return 0, fmt.Errorf("count usage of the phase: %w", err)
	}
	return received, nil
}

// PreparePrepaidPhaseClose rates the closing statement of the tenant's
// prepaid phase for a switch to postpaid now. It returns nil when the tenant
// is not prepaid. svc supplies cluster pricing and may be nil for tenants
// without cluster usage.
//
// The switch time is the database clock's: the postpaid invoice later tells
// the usage that reached Purser after the switch by its records' receipt
// time, which the database clock wrote.
func PreparePrepaidPhaseClose(ctx context.Context, db *sql.DB, logger logging.Logger, svc *Service, tenantID string) (*PrepaidPhaseClose, error) {
	subscription, err := purserdb.New(db).GetSubscriptionPrepaidPhase(ctx, tenantID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load subscription: %w", err)
	}
	if subscription.BillingModel != "prepaid" {
		return nil, nil
	}
	switchAt := subscription.DatabaseNow.UTC().Truncate(time.Microsecond)
	phaseStart, phaseEnd := currentBillingPeriod(subscription.BillingPeriodStart, subscription.BillingPeriodEnd, subscription.MollieNextPaymentDate, switchAt)
	closing := &PrepaidPhaseClose{
		logger: logger, tenantID: tenantID,
		periodStart: subscription.BillingPeriodStart, periodEnd: subscription.BillingPeriodEnd,
		mollieNext: subscription.MollieNextPaymentDate, switchAt: switchAt, nextPeriodEnd: phaseEnd,
	}
	if !switchAt.After(phaseStart) {
		return closing, nil
	}
	if !switchAt.Before(phaseEnd) {
		closing.nextPeriodEnd = switchAt.AddDate(0, 1, 0)
	}
	tier, err := billingpkg.LoadSubscriptionEffectiveTier(ctx, db, tenantID)
	if err != nil {
		return nil, fmt.Errorf("load prepaid tier: %w", err)
	}
	baseFeeInvoiced, err := purserdb.New(db).BaseFeeInvoiceExistsForPeriod(ctx, purserdb.BaseFeeInvoiceExistsForPeriodParams{
		TenantID: tenantID, PeriodStart: phaseStart,
	})
	if err != nil {
		return nil, fmt.Errorf("check advance base-fee invoice: %w", err)
	}
	if svc == nil {
		svc = &Service{db: db, logger: logger}
	}
	jobs := &JobManager{db: db, logger: logger, billing: svc}
	providerManaged := subscription.StripeSubscriptionID.Valid || subscription.MollieSubscriptionID.Valid || baseFeeInvoiced
	phase, err := prepaidUsagePhase(ctx, db, tenantID, phaseStart, switchAt)
	if err != nil {
		return nil, err
	}
	phase.periodEnd = phaseEnd
	phase.receivedBefore = sql.NullTime{Time: switchAt, Valid: true}
	if closing.received, err = countPhaseReceived(ctx, purserdb.New(db), tenantID, phase); err != nil {
		return nil, err
	}
	closing.run, err = jobs.ratePrepaidStatement(ctx, tier, tenantID, subscription.BillingEmail.String, phase, providerManaged, true)
	if err != nil {
		return nil, err
	}
	return closing, nil
}

// prepaidPhasePeriod is the period prepaid usage settles against: the
// subscription's period, or the calendar month of at without one.
func prepaidPhasePeriod(start, end sql.NullTime, at time.Time) (time.Time, time.Time) {
	if start.Valid && end.Valid && end.Time.After(start.Time) {
		return start.Time.UTC(), end.Time.UTC()
	}
	monthStart := time.Date(at.Year(), at.Month(), 1, 0, 0, 0, 0, time.UTC)
	return monthStart, monthStart.AddDate(0, 1, 0)
}

// CommitTx writes the closing statement and starts the postpaid period at the
// switch, inside the switch's transaction and before it changes the billing
// model. The caller holds the subscription row lock. It reports whether it
// closed a phase; without one the caller keeps its own period handling. A
// subscription that changed since the statement was rated, or usage of the
// phase that reached Purser meanwhile, returns ErrPrepaidPhaseChanged. The
// statement settles what the phase's usage still owes the balance. The open
// draft overlapping the phase becomes the statement. It may run again when
// the transaction retries.
func (c *PrepaidPhaseClose) CommitTx(ctx context.Context, tx *sql.Tx) (bool, error) {
	if c == nil {
		return false, nil
	}
	c.closed, c.result = false, prepaidStatementResult{}
	queries := purserdb.New(tx)
	subscription, err := queries.GetSubscriptionPrepaidPhase(ctx, c.tenantID)
	if err != nil {
		return false, fmt.Errorf("load subscription: %w", err)
	}
	if subscription.BillingModel != "prepaid" || !sameNullTime(subscription.BillingPeriodStart, c.periodStart) ||
		!sameNullTime(subscription.BillingPeriodEnd, c.periodEnd) || !sameNullTime(subscription.MollieNextPaymentDate, c.mollieNext) {
		return false, ErrPrepaidPhaseChanged
	}
	if c.run == nil {
		return false, nil
	}
	received, err := countPhaseReceived(ctx, queries, c.tenantID, c.run.phase)
	if err != nil {
		return false, err
	}
	if received != c.received {
		return false, ErrPrepaidPhaseChanged
	}
	draft, err := queries.GetOpenUsageDraftForPhase(ctx, purserdb.GetOpenUsageDraftForPhaseParams{
		TenantID: c.tenantID, PhaseStart: c.run.periodStart, PhaseEnd: c.run.periodEnd,
	})
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return false, fmt.Errorf("look up the period's draft: %w", err)
	}
	c.result, err = writePrepaidStatementTx(ctx, tx, c.run, draft.ID, draft.PeriodStart, time.Now().UTC())
	if err != nil {
		return false, err
	}
	rows, err := queries.StartBillingPhase(ctx, purserdb.StartBillingPhaseParams{
		PeriodStart: c.switchAt, PeriodEnd: c.nextPeriodEnd, TenantID: c.tenantID,
	})
	if err != nil {
		return false, fmt.Errorf("start the postpaid period: %w", err)
	}
	if rows != 1 {
		return false, fmt.Errorf("start the postpaid period: no subscription for tenant %s", c.tenantID)
	}
	c.closed = true
	return true, nil
}

// Log records the committed closing statement.
func (c *PrepaidPhaseClose) Log() {
	if c == nil || !c.closed || c.run == nil {
		return
	}
	logPrepaidStatement(c.logger, c.run, c.result)
	c.logger.WithFields(logging.Fields{
		"tenant_id":           c.tenantID,
		"statement_id":        c.result.statementID,
		"postpaid_from":       c.switchAt.Format(time.RFC3339Nano),
		"postpaid_period_end": c.nextPeriodEnd.Format(time.RFC3339),
	}).Info("Closed prepaid phase at switch to postpaid")
}

// StatementID is the closing statement written by CommitTx, empty when none.
func (c *PrepaidPhaseClose) StatementID() string {
	if c == nil || !c.closed {
		return ""
	}
	return c.result.statementID
}

func sameNullTime(a, b sql.NullTime) bool {
	return a.Valid == b.Valid && (!a.Valid || a.Time.Equal(b.Time))
}

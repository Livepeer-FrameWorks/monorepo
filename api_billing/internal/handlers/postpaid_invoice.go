package handlers

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	billingpkg "frameworks/api_billing/internal/billing"
	"frameworks/api_billing/internal/database/purserdb"
	"frameworks/api_billing/internal/fx"
	"frameworks/api_billing/internal/operator"
	billingstripe "frameworks/api_billing/internal/stripe"
	billingtax "frameworks/api_billing/internal/tax"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/billing"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
)

// closesPostpaidPhaseKey marks the usage_details of the invoice a switch to
// prepaid finalized for the postpaid phase it closed.
const closesPostpaidPhaseKey = "closes_postpaid_phase"

// postpaidInvoiceRun is a postpaid usage invoice rated outside the
// transaction that writes it.
type postpaidInvoiceRun struct {
	tenantID            string
	billingEmail        string
	tierName            string
	currency            string
	meteringEnabled     bool
	presentmentCurrency string
	// collectionProvider is the provider that collects the invoice off
	// session, empty when the tenant pays it by bank transfer.
	collectionProvider string
	periodStart        time.Time
	periodEnd          time.Time
	// phase is the usage phase the invoice rates.
	phase          usagePhase
	draftInvoiceID string
	// invoiceID is the id a new invoice gets when the period has no draft.
	invoiceID    string
	rating       *clusterRatingResult
	usageDetails map[string]interface{}
}

// postpaidInvoiceResult is what writing a postpaid invoice did.
type postpaidInvoiceResult struct {
	invoiceID    string
	status       string
	total        decimal.Decimal
	credit       decimal.Decimal
	presentment  fx.Record
	creditChange invoiceCreditChange
}

// ratePostpaidInvoice rates the postpaid invoice of a phase: its usage, the
// base fee unless a provider subscription or an advance base-fee invoice
// collects it, and the monthly cluster fees. A phase that switches between
// prepaid and postpaid split off its period, such as one an invoice
// closesPhase finalizes at a switch to prepaid, pays the base fee for its
// share of the split period (applyBaseFeeShare).
func (jm *JobManager) ratePostpaidInvoice(ctx context.Context, subscription purserdb.ListSubscriptionsDueForInvoiceRow, tier *billingpkg.EffectiveTier, phase usagePhase, draftInvoiceID string, closesPhase bool) (*postpaidInvoiceRun, error) {
	if tier == nil {
		return nil, errors.New("rate postpaid invoice: no tier")
	}
	tenantID := subscription.TenantID
	periodStart, periodEnd := phase.start, phase.end

	// Aggregate canonical usage metrics for the phase. SUM handles flow/delta
	// meters; MAX handles peak gauges; unique counts are skipped here and come
	// from Periscope enrichment because scalar windows cannot be summed into
	// unique users. A query failure aborts the invoice: rating against an
	// empty or partial usage map underbills. Usage of other phases, which
	// prepaid documents and settlements bill, is not rated.
	perClusterUsage, perClusterDimensioned, err := collectPhaseUsage(ctx, jm.db, tenantID, phase)
	if err != nil {
		return nil, fmt.Errorf("collect usage: %w", err)
	}

	// The base fee is outside the period's usage invoice when a provider
	// subscription collects it or Purser charged it in advance on a base-fee
	// invoice for this period.
	baseFeeInvoiced, err := purserdb.New(jm.db).BaseFeeInvoiceExistsForPeriod(ctx, purserdb.BaseFeeInvoiceExistsForPeriodParams{
		TenantID: tenantID, PeriodStart: periodStart,
	})
	if err != nil {
		return nil, fmt.Errorf("check advance base-fee invoice: %w", err)
	}
	baseProviderManaged := subscription.StripeSubscriptionID.Valid || subscription.MollieSubscriptionID.Valid || baseFeeInvoiced
	collectionProvider, err := resolveInvoiceCollectionProvider(subscription.PaymentMethod.String,
		subscription.StripeSubscriptionID.Valid, subscription.MollieSubscriptionID.Valid,
		subscription.StripeCustomerID.Valid && subscription.StripeCustomerID.String != "", subscription.HasMollieCustomer)
	if err != nil {
		return nil, billingBoundaryConditionf("invoice finalization blocked by ambiguous collection provider: %v; select the correct collection provider before retrying", err)
	}
	ratingResult, err := jm.rateInvoiceForTenant(ctx, tenantID, phase, tier, true, baseProviderManaged, perClusterUsage, perClusterDimensioned)
	if err != nil {
		return nil, fmt.Errorf("rate usage: %w", err)
	}
	applyBaseFeeShare(ratingResult, phase)
	if len(ratingResult.ManualReviewReasons) > 0 {
		jm.logger.WithFields(logging.Fields{
			"tenant_id": tenantID,
			"reasons":   strings.Join(ratingResult.ManualReviewReasons, "; "),
		}).Warn("Invoice routed to manual_review; finalization halted")
	}

	basePrice, _ := tier.BasePrice.Float64()
	// Flat usage_details: all metrics at top level for email and API.
	usageDetails := map[string]interface{}{
		"period_start": periodStart,
		"period_end":   periodEnd,
		"tier_info": map[string]interface{}{
			"tier_id":          subscription.TierID.String(),
			"tier_name":        subscription.TierName,
			"display_name":     subscription.DisplayName,
			"base_price":       basePrice,
			"metering_enabled": tier.MeteringEnabled,
		},
		// This invoice rates only its phase's usage, never usage the prepaid
		// balance paid; the prepaid double-charge diagnostic skips invoices
		// that carry it.
		prepaidSettledUsageExcludedKey: true,
	}
	if closesPhase {
		usageDetails[closesPostpaidPhaseKey] = true
	}
	for k, v := range flattenUsageAcrossClusters(perClusterUsage) {
		usageDetails[k] = v
	}
	// Accurate unique counts and geo come from Periscope; they cannot be
	// rolled up from 5-minute windows.
	enrichCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	if enrichment := jm.enrichInvoiceFromPeriscope(enrichCtx, tenantID, periodStart, periodEnd); enrichment != nil {
		for k, v := range enrichment {
			usageDetails[k] = v
		}
	}
	cancel()

	return &postpaidInvoiceRun{
		tenantID:            tenantID,
		billingEmail:        subscription.BillingEmail.String,
		tierName:            subscription.TierName,
		currency:            tier.Currency,
		meteringEnabled:     tier.MeteringEnabled,
		presentmentCurrency: strings.ToUpper(strings.TrimSpace(subscription.PresentmentCurrency)),
		collectionProvider:  collectionProvider,
		periodStart:         periodStart,
		periodEnd:           periodEnd,
		phase:               phase,
		draftInvoiceID:      draftInvoiceID,
		invoiceID:           uuid.New().String(),
		rating:              ratingResult,
		usageDetails:        usageDetails,
	}, nil
}

// writePostpaidInvoiceTx finalizes a rated postpaid invoice: it reconciles the
// period's prepaid invoice credit, applies the collection minimum, writes the
// header (the period's draft becomes the invoice) with its line items and
// presentment, and enqueues the operator credits, Stripe meter events, the
// invoice email and invoice_created. It may run again when the transaction
// retries; every value starts from the rated run.
func writePostpaidInvoiceTx(ctx context.Context, tx *sql.Tx, run *postpaidInvoiceRun) (postpaidInvoiceResult, error) {
	result := postpaidInvoiceResult{invoiceID: run.invoiceID, status: "pending"}
	queries := purserdb.New(tx)
	tenantID := run.tenantID
	ratingResult := run.rating
	usageDetails := maps.Clone(run.usageDetails)
	heldForReview := len(ratingResult.ManualReviewReasons) > 0
	if heldForReview {
		// manual_review holds the whole invoice: no payment captures, Stripe
		// meter pushes, ledger writes, or period advance until ops resolves
		// the cluster pricing and re-finalizes. Lines persist for visibility.
		result.status = "manual_review"
	}

	// Money stays in decimal.Decimal until the SQL boundary. The gross metered
	// amount is the would-have-cost usage total, for display only.
	issuedAt := time.Now().UTC()
	netDec := ratingResult.TotalAmount.Round(2)
	var taxAssessment invoiceTaxAssessment
	var taxReview *billingtax.ReviewError
	grossDec := netDec
	if !heldForReview {
		var taxErr error
		taxAssessment, taxErr = assessInvoiceTaxTx(ctx, tx, tenantID, netDec.Shift(2).IntPart(), issuedAt)
		if taxErr != nil {
			if !errors.As(taxErr, &taxReview) {
				return result, fmt.Errorf("assess invoice tax: %w", taxErr)
			}
			heldForReview = true
			result.status = "manual_review"
			usageDetails["tax_review"] = map[string]any{"code": taxReview.Code, "message": taxReview.Message}
		} else {
			grossDec = decimal.New(taxAssessment.Gross, -2)
		}
	}
	result.total = grossDec

	// A held invoice records no credit, so it reconciles the period's credit
	// to zero and anything an earlier draft took returns.
	creditTargetCents := int64(0)
	if !heldForReview {
		creditTargetCents = netDec.Shift(2).IntPart()
	}
	var err error
	result.creditChange, err = reconcileInvoicePrepaidCreditTx(ctx, tx, tenantID, run.periodStart, creditTargetCents)
	if err != nil {
		return result, err
	}
	var financialCredit, absorbedVAT int64
	var advanceLines []pricedLine
	var collectionDecision *invoiceCollectionDecision
	if !heldForReview {
		result.credit = decimal.NewFromInt(result.creditChange.AppliedCents).Div(decimal.NewFromInt(100))
		advanceLines, financialCredit, absorbedVAT, err = applyInvoiceAdvanceTaxTx(ctx, tx, tenantID, run.periodStart, result.creditChange.AppliedCents, &taxAssessment)
		if err != nil {
			return result, err
		}
		grossDec = decimal.New(taxAssessment.Gross, -2)
		result.total = grossDec.Sub(decimal.New(financialCredit+absorbedVAT, -2))
		if result.total.IsNegative() {
			result.total = decimal.Zero
		}
		result.total = result.total.Round(2)
		if run.collectionProvider != "" {
			decision, decisionErr := applyInvoiceCollectionMinimumTx(
				ctx, tx, tenantID, run.collectionProvider, billing.LedgerCurrency,
				result.total.Mul(decimal.NewFromInt(100)).IntPart(),
			)
			if decisionErr != nil {
				return result, fmt.Errorf("apply invoice collection minimum: %w", decisionErr)
			}
			collectionDecision = &decision
			usageDetails["collection"] = map[string]interface{}{
				"provider":              decision.Provider,
				"minimum_cents":         decision.MinimumCents,
				"opening_balance_cents": decision.OpeningBalanceCents,
				"current_charge_cents":  decision.CurrentChargeCents,
				"collected_cents":       decision.CollectedCents,
				"closing_balance_cents": decision.ClosingBalanceCents,
				"outcome":               decision.Outcome,
			}
			result.total = decimal.NewFromInt(decision.CollectedCents).Div(decimal.NewFromInt(100))
		}
		result.status = finalizedInvoiceStatus(result.total)
	} else {
		result.total = result.total.Round(2)
	}
	usageJSON, err := json.Marshal(usageDetails)
	if err != nil {
		return result, fmt.Errorf("marshal invoice usage details: %w", err)
	}

	// Decimals bind as strings into NUMERIC columns so no float64 rounding
	// reaches the SQL boundary.
	totalAmt := result.total.Round(2).String()
	baseAmt := ratingResult.BaseAmount.Round(2).String()
	meteredAmt := ratingResult.UsageAmount.Round(2).String()
	grossMeteredAmt := ratingResult.GrossUsageAmount.Round(2).String()
	creditAmt := result.credit.Round(2).String()
	dueDate := run.periodEnd.AddDate(0, 0, 14)
	if run.draftInvoiceID != "" {
		result.invoiceID, err = queries.UpdateDraftInvoice(ctx, purserdb.UpdateDraftInvoiceParams{
			Amount: totalAmt, BaseAmount: baseAmt, MeteredAmount: meteredAmt, PrepaidCreditApplied: creditAmt,
			Currency: run.currency, Status: result.status, DueDate: dueDate, UsageDetails: json.RawMessage(usageJSON),
			PeriodStart: sql.NullTime{Time: run.periodStart, Valid: true}, PeriodEnd: sql.NullTime{Time: run.periodEnd, Valid: true},
			GrossMeteredAmount: grossMeteredAmt, InvoiceID: run.draftInvoiceID, TenantID: tenantID,
		})
		if err != nil {
			return result, fmt.Errorf("update invoice: %w", err)
		}
	} else {
		result.invoiceID, err = queries.UpsertInvoiceForPeriod(ctx, purserdb.UpsertInvoiceForPeriodParams{
			InvoiceID: run.invoiceID, TenantID: tenantID, Amount: totalAmt, Currency: run.currency, Status: result.status,
			DueDate: dueDate, BaseAmount: baseAmt, MeteredAmount: meteredAmt, PrepaidCreditApplied: creditAmt,
			UsageDetails: json.RawMessage(usageJSON),
			PeriodStart:  sql.NullTime{Time: run.periodStart, Valid: true}, PeriodEnd: sql.NullTime{Time: run.periodEnd, Valid: true},
			GrossMeteredAmount: grossMeteredAmt,
		})
		if err != nil {
			return result, fmt.Errorf("upsert invoice: %w", err)
		}
	}
	invoiceRating := *ratingResult
	invoiceRating.UsageLines = append(append([]pricedLine(nil), ratingResult.UsageLines...), advanceLines...)
	if err = persistInvoiceLineItems(ctx, tx, result.invoiceID, tenantID, &invoiceRating); err != nil {
		return result, err
	}
	if taxReview != nil {
		evidence, marshalErr := json.Marshal(usageDetails["tax_review"])
		if marshalErr != nil {
			return result, marshalErr
		}
		if err = queries.RecordCryptoAccountingAnomaly(ctx, purserdb.RecordCryptoAccountingAnomalyParams{TenantID: tenantID, Kind: "invoice_tax_review", Network: "tax", ReferenceType: "invoice", ReferenceID: result.invoiceID, AmountCents: netDec.Shift(2).IntPart(), Detail: taxReview.Error(), EvidenceJson: evidence}); err != nil {
			return result, fmt.Errorf("record invoice tax review: %w", err)
		}
	}
	if financialCredit != 0 || absorbedVAT != 0 {
		if _, err = tx.ExecContext(ctx, `UPDATE purser.billing_invoices SET prepaid_financial_credit_eur_cents=$3,absorbed_vat_eur_cents=$4 WHERE tenant_id=$1::uuid AND id=$2::uuid`, tenantID, result.invoiceID, financialCredit, absorbedVAT); err != nil {
			return result, err
		}
	}
	// A finalized invoice is presented in the tenant's presentment currency at
	// the ECB rate of the finalization date. Without a usable rate the whole
	// finalization rolls back and is retried.
	if !heldForReview {
		result.presentment, err = finalizeInvoicePresentmentTx(ctx, tx, result.invoiceID, tenantID, run.presentmentCurrency, result.total, issuedAt)
		if err != nil {
			return result, err
		}
		if err = freezeInvoiceTaxTx(ctx, tx, tenantID, result.invoiceID, taxAssessment, result.presentment); err != nil {
			return result, err
		}
		if _, err = queries.ResolveInvoiceTaxReview(ctx, purserdb.ResolveInvoiceTaxReviewParams{TenantID: tenantID, InvoiceID: result.invoiceID}); err != nil {
			return result, fmt.Errorf("resolve invoice tax review: %w", err)
		}
	}
	if collectionDecision != nil {
		if err = persistInvoiceCollectionDecisionTx(ctx, tx, result.invoiceID, tenantID, *collectionDecision); err != nil {
			return result, err
		}
	}
	if !heldForReview {
		if err = queries.MarkUsageAdjustmentsAppliedToInvoice(ctx, purserdb.MarkUsageAdjustmentsAppliedToInvoiceParams{
			InvoiceID: result.invoiceID, TenantID: tenantID, PeriodEnd: run.periodEnd, PeriodStart: run.periodStart,
			ChainFrom: run.phase.splitStart(), ReceivedBefore: run.phase.receivedBefore,
		}); err != nil {
			return result, fmt.Errorf("mark usage adjustments applied to invoice: %w", err)
		}
	}
	// Operator credit accrual for marketplace lines commits with the invoice;
	// the helper skips manual_review invoices.
	if err = operator.ComputeAndPersistCredits(ctx, tx, result.invoiceID, result.status); err != nil {
		return result, fmt.Errorf("persist operator credits: %w", err)
	}
	// Stripe meter events go to the outbox; the flusher pushes them and a
	// rollback discards them.
	if err = billingstripe.EnqueueMeterEvents(ctx, tx, result.invoiceID, tenantID, result.status); err != nil {
		return result, fmt.Errorf("enqueue stripe meter events: %w", err)
	}
	if err = enqueueInvoiceEmailTx(ctx, tx, result.invoiceID, tenantID, run.billingEmail, result.status); err != nil {
		return result, fmt.Errorf("enqueue invoice email: %w", err)
	}
	if err = enqueueInvoiceCreatedTx(ctx, tx, tenantID, result.invoiceID, result.status,
		result.total.Round(2).Shift(2).IntPart(), run.periodStart, run.periodEnd, dueDate); err != nil {
		return result, fmt.Errorf("enqueue invoice_created: %w", err)
	}
	return result, nil
}

// afterPostpaidInvoiceCommit logs a committed postpaid invoice and starts its
// collection: Mollie payments that arrived before the invoice existed are
// attached, and the amount due is charged off session through the one
// selected provider. A charge that does not start leaves the invoice pending
// for the payment retry job and dunning.
func (jm *JobManager) afterPostpaidInvoiceCommit(ctx context.Context, run *postpaidInvoiceRun, result postpaidInvoiceResult, message string) {
	logInvoiceCreditChange(jm.logger, run.tenantID, result.invoiceID, run.periodStart, result.creditChange)
	jm.logger.WithFields(logging.Fields{
		"invoice_id":       result.invoiceID,
		"tenant_id":        run.tenantID,
		"tier_name":        run.tierName,
		"period_start":     run.periodStart.UTC().Format(time.RFC3339Nano),
		"period_end":       run.periodEnd.UTC().Format(time.RFC3339Nano),
		"base_amount":      run.rating.BaseAmount.Round(2).String(),
		"metered_amount":   run.rating.UsageAmount.Round(2).String(),
		"total_amount":     result.total.Round(2).String(),
		"currency":         run.currency,
		"status":           result.status,
		"metering_enabled": run.meteringEnabled,
	}).Info(message)
	if result.status != "pending" {
		return
	}
	if jm.billing != nil {
		if drainErr := jm.billing.drainMolliePaymentObservationsForInvoice(ctx, result.invoiceID); drainErr != nil {
			jm.logger.WithError(drainErr).WithFields(logging.Fields{
				"tenant_id": run.tenantID, "invoice_id": result.invoiceID,
			}).Warn("Failed to drain Mollie payment observations")
		}
	}
	if result.presentment.OriginalMinor > 0 {
		if chargeErr := jm.chargeInvoice(ctx, run.collectionProvider, run.tenantID, result.invoiceID, result.presentment.OriginalMinor, result.presentment.OriginalCurrency); chargeErr != nil {
			jm.logger.WithError(chargeErr).WithFields(logging.Fields{
				"tenant_id": run.tenantID, "invoice_id": result.invoiceID, "provider": run.collectionProvider,
			}).Warn("Failed to trigger off-session invoice charge")
		}
	}
}

// ErrPostpaidPhaseChanged means the postpaid subscription, its arrangement or
// its received usage changed while its closing invoice was rated; the caller
// is retried.
var ErrPostpaidPhaseChanged = errors.New("the postpaid subscription or its usage changed while its closing invoice was prepared")

// PostpaidPhaseClose finalizes a postpaid pricing/model phase at its boundary.
// The invoice covers the phase's usage, prorated base fee and monthly cluster
// fees. Rating runs before the transaction; CommitTx writes the document and
// starts the next phase in the same transaction as the arrangement change.
type PostpaidPhaseClose struct {
	// grantBoundary also fences the grant snapshot used to rate this phase.
	grantBoundary bool
	// monthlyCatchup advances Mollie's invoice anchor without charging a new term.
	monthlyCatchup bool
	// toPrepaid collects deferred charges and forfeits unused paid advance fees.
	toPrepaid    bool
	jobs         *JobManager
	tenantID     string
	subscription purserdb.GetSubscriptionForPhaseCloseRow
	tier         *billingpkg.EffectiveTier
	switchAt     time.Time
	phase        usagePhase
	// received is the count of usage records the invoice was rated from;
	// CountPhaseUsageReceivedBefore at commit must still return it.
	received      int64
	nextPeriodEnd time.Time
	// run is nil when the boundary falls on the period start, which leaves no
	// postpaid phase to invoice.
	run    *postpaidInvoiceRun
	result postpaidInvoiceResult
	// carriedCents is what the collection minimum had deferred, charged to the
	// prepaid balance when moving to prepaid; balanceAfter is the resulting balance.
	carriedCents int64
	balanceAfter int64
	// releasedCredit is the credit an open draft of the phase that started
	// elsewhere than the phase held under its own key, returned before the
	// closing invoice takes the credit it uses.
	releasedCredit invoiceCreditChange
	// baseFeeDisposition records the reduction or forfeiture of the advance
	// invoice's unused share at a move to prepaid.
	baseFeeDisposition unusedBaseFeeDisposition
	closed             bool
}

// settleCollectionCarryTx charges what the collection minimum deferred from
// postpaid invoices to the prepaid balance when a switch to prepaid closes
// the postpaid phase: no later postpaid invoice would collect it. It returns
// the amount charged and the balance after it.
func settleCollectionCarryTx(ctx context.Context, tx *sql.Tx, tenantID, invoiceID string) (int64, int64, error) {
	queries := purserdb.New(tx)
	carried, err := queries.LockBillingCollectionBalance(ctx, purserdb.LockBillingCollectionBalanceParams{
		TenantID: tenantID, Currency: billing.LedgerCurrency,
	})
	if errors.Is(err, sql.ErrNoRows) || (err == nil && carried <= 0) {
		return 0, 0, nil
	}
	if err != nil {
		return 0, 0, fmt.Errorf("lock collection balance: %w", err)
	}
	if err = queries.EnsurePrepaidBalance(ctx, purserdb.EnsurePrepaidBalanceParams{TenantID: tenantID, Currency: billing.LedgerCurrency}); err != nil {
		return 0, 0, fmt.Errorf("ensure prepaid balance: %w", err)
	}
	locked, err := queries.LockPrepaidBalance(ctx, purserdb.LockPrepaidBalanceParams{TenantID: tenantID, Currency: billing.LedgerCurrency})
	if err != nil {
		return 0, 0, fmt.Errorf("lock prepaid balance: %w", err)
	}
	balanceAfter := locked.BalanceCents - carried
	if err = queries.InsertCollectionCarryBalanceTransaction(ctx, purserdb.InsertCollectionCarryBalanceTransactionParams{
		TenantID: tenantID, AmountCents: -carried, BalanceAfterCents: balanceAfter,
		Description: sql.NullString{String: "Postpaid charges below the collection minimum, settled at the switch to prepaid", Valid: true},
		InvoiceID:   invoiceID,
	}); err != nil {
		return 0, 0, fmt.Errorf("record collection carry charged to the prepaid balance: %w", err)
	}
	if err = queries.UpdatePrepaidBalance(ctx, purserdb.UpdatePrepaidBalanceParams{
		BalanceCents: balanceAfter, TenantID: tenantID, Currency: billing.LedgerCurrency,
	}); err != nil {
		return 0, 0, fmt.Errorf("charge collection carry to the prepaid balance: %w", err)
	}
	if _, err = queries.UpdateBillingCollectionBalance(ctx, purserdb.UpdateBillingCollectionBalanceParams{
		BalanceCents: 0, TenantID: tenantID, Currency: billing.LedgerCurrency,
	}); err != nil {
		return 0, 0, fmt.Errorf("clear collection balance: %w", err)
	}
	return carried, balanceAfter, nil
}

// PreparePostpaidPhaseClose rates the invoice that closes the tenant's
// postpaid phase for a switch to prepaid now. It returns nil when the tenant
// is not postpaid. svc supplies cluster pricing and the payment providers
// that collect the invoice; it may be nil for tenants without either.
//
// The switch time is the database clock's: usage records belong to the phase
// in force when they reached Purser, by the receipt time the database clock
// wrote. Records that reach Purser after the switch go to the prepaid phase.
func PreparePostpaidPhaseClose(ctx context.Context, db *sql.DB, logger logging.Logger, svc *Service, tenantID string) (*PostpaidPhaseClose, error) {
	return preparePostpaidPhaseClose(ctx, db, logger, svc, tenantID, time.Time{}, true, false)
}

// PrepareProviderBillingBoundary rates usage received by preparation time for a
// provider tier/model change. It commits only after the provider lock confirms
// there is no outstanding grant; delayed usage for the closing phase is included.
func PrepareProviderBillingBoundary(ctx context.Context, db *sql.DB, logger logging.Logger, svc *Service, tenantID string, at time.Time) (*PostpaidPhaseClose, error) {
	closing, err := preparePostpaidPhaseClose(ctx, db, logger, svc, tenantID, at, false, false)
	if err != nil {
		return nil, err
	}
	if closing != nil && closing.run != nil && closing.tier.MeteringEnabled {
		if err = closing.jobs.assertMeteringComplete(ctx, tenantID, closing.phase.start, closing.phase.end); err != nil {
			return nil, fmt.Errorf("wait for complete metering before provider activation: %w", err)
		}
	}
	return closing, nil
}

// prepareAcceptedProviderBoundary closes a phase whose paid provider change an
// operator accepted on the usage recorded so far, without complete metering.
func prepareAcceptedProviderBoundary(ctx context.Context, db *sql.DB, logger logging.Logger, svc *Service, tenantID string, at time.Time) (*PostpaidPhaseClose, error) {
	return preparePostpaidPhaseClose(ctx, db, logger, svc, tenantID, at, false, false)
}

// PrepareFreeFallbackBoundary closes at most one stored monthly period. The
// final partial period ends at cutoff; complete months keep separate allowances.
func PrepareFreeFallbackBoundary(ctx context.Context, db *sql.DB, logger logging.Logger, svc *Service, tenantID string, cutoff time.Time) (*PostpaidPhaseClose, bool, error) {
	return prepareMonthlyPostpaidBoundary(ctx, db, logger, svc, tenantID, cutoff, false, false)
}

// PrepareRetainedProviderBoundary keeps overdue monthly allowances separate
// when changing a retained provider's billing phase. Only the final partial
// phase can switch billing model; a grant keeps its historical pricing cutoff.
func PrepareRetainedProviderBoundary(ctx context.Context, db *sql.DB, logger logging.Logger, svc *Service, tenantID string, cutoff time.Time, toPrepaid, grantBoundary bool) (*PostpaidPhaseClose, bool, error) {
	closing, final, err := prepareMonthlyPostpaidBoundary(ctx, db, logger, svc, tenantID, cutoff, toPrepaid, grantBoundary)
	if err != nil {
		return nil, false, err
	}
	if (toPrepaid || grantBoundary) && closing != nil && closing.run != nil && closing.tier.MeteringEnabled {
		if err = closing.jobs.assertMeteringComplete(ctx, tenantID, closing.phase.start, closing.phase.end); err != nil {
			return nil, false, fmt.Errorf("wait for complete metering before changing the retained provider billing phase: %w", err)
		}
	}
	return closing, final, nil
}

func prepareMonthlyPostpaidBoundary(ctx context.Context, db *sql.DB, logger logging.Logger, svc *Service, tenantID string, cutoff time.Time, toPrepaid, grantBoundary bool) (*PostpaidPhaseClose, bool, error) {
	subscription, err := purserdb.New(db).GetSubscriptionForPhaseClose(ctx, tenantID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, true, nil
	}
	if err != nil {
		return nil, false, err
	}
	start, end := currentBillingPeriod(subscription.BillingPeriodStart, subscription.BillingPeriodEnd, subscription.MollieNextPaymentDate, cutoff)
	boundary := cutoff
	final := true
	if end.After(start) && end.Before(cutoff) {
		boundary, final = end, false
	}
	var closing *PostpaidPhaseClose
	if grantBoundary || toPrepaid {
		closing, err = preparePostpaidPhaseClose(ctx, db, logger, svc, tenantID, boundary, toPrepaid && final, grantBoundary)
	} else {
		closing, err = PrepareProviderBillingBoundary(ctx, db, logger, svc, tenantID, boundary)
	}
	if err != nil || closing == nil {
		return closing, final, err
	}
	if !final {
		closing.monthlyCatchup = true
		closing.nextPeriodEnd = nextBillingPeriodEnd(closing.phase.chainFrom, boundary)
	}
	if closing.run != nil && boundary.Equal(end) {
		// A full month ends late-arrival lineage, including a pricing change
		// exactly on its anniversary. A midmonth split keeps that lineage.
		delete(closing.run.usageDetails, closesPostpaidPhaseKey)
	}
	return closing, final, nil
}

// PreparePostpaidGrantBoundary rates the closing phase at a grant change.
// toPrepaid also settles deferred collection and records the unused advance-fee disposition.
func PreparePostpaidGrantBoundary(ctx context.Context, db *sql.DB, logger logging.Logger, svc *Service, tenantID string, at time.Time, toPrepaid bool) (*PostpaidPhaseClose, error) {
	return preparePostpaidPhaseClose(ctx, db, logger, svc, tenantID, at, toPrepaid, true)
}

func preparePostpaidPhaseClose(ctx context.Context, db *sql.DB, logger logging.Logger, svc *Service, tenantID string, at time.Time, toPrepaid, grantBoundary bool) (*PostpaidPhaseClose, error) {
	queries := purserdb.New(db)
	clock, err := queries.GetSubscriptionPrepaidPhase(ctx, tenantID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load subscription: %w", err)
	}
	subscription, err := queries.GetSubscriptionForPhaseClose(ctx, tenantID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load subscription: %w", err)
	}
	if subscription.BillingModel != "postpaid" {
		return nil, nil
	}
	switchAt := clock.DatabaseNow.UTC().Truncate(time.Microsecond)
	if !at.IsZero() {
		switchAt = at.UTC().Truncate(time.Microsecond)
	}
	phaseStart, phaseEnd := currentBillingPeriod(subscription.BillingPeriodStart, subscription.BillingPeriodEnd, subscription.MollieNextPaymentDate, switchAt)
	if svc == nil {
		svc = &Service{db: db, logger: logger}
	}
	closing := &PostpaidPhaseClose{
		jobs:          &JobManager{db: db, logger: logger, billing: svc},
		grantBoundary: grantBoundary, toPrepaid: toPrepaid,
		tenantID: tenantID, subscription: subscription,
		switchAt: switchAt, nextPeriodEnd: phaseEnd,
	}
	if !switchAt.After(phaseStart) {
		return closing, nil
	}
	if !switchAt.Before(phaseEnd) {
		closing.nextPeriodEnd = billing.NextMonthlyAnniversary(switchAt)
	}
	chainFrom, err := splitPeriodStart(ctx, db, tenantID, phaseStart)
	if err != nil {
		return nil, err
	}
	closing.phase = usagePhase{
		start: phaseStart, end: switchAt, chainFrom: chainFrom, periodEnd: phaseEnd,
		receivedBefore: sql.NullTime{Time: switchAt, Valid: true},
	}
	if !grantBoundary || switchAt.Equal(phaseEnd) {
		// Complete months include all reports received before preparation.
		// Only a historic midmonth grant split needs the grant deadline cutoff.
		closing.phase.receivedBefore = sql.NullTime{Time: clock.DatabaseNow, Valid: true}
	}
	if closing.received, err = countPhaseReceived(ctx, queries, tenantID, closing.phase); err != nil {
		return nil, err
	}
	if grantBoundary {
		closing.tier, err = billingpkg.LoadSubscriptionEffectiveTierAt(ctx, db, tenantID, switchAt)
	} else {
		closing.tier, err = billingpkg.LoadSubscriptionEffectiveTier(ctx, db, tenantID)
	}
	if err != nil {
		return nil, fmt.Errorf("load postpaid tier: %w", err)
	}
	// Grant/model switches retain the calendar period. Provider activation starts
	// a new period, so its closing invoice must not extend the old phase chain.
	closing.run, err = closing.jobs.ratePostpaidInvoice(ctx, purserdb.ListSubscriptionsDueForInvoiceRow(subscription), closing.tier, closing.phase, "", grantBoundary || toPrepaid)
	if err != nil {
		return nil, err
	}
	if len(closing.run.rating.ManualReviewReasons) > 0 {
		return nil, billingBoundaryConditionf("the closing invoice's cluster pricing is unresolved: %s; reconcile pricing for the closing period before retrying", strings.Join(closing.run.rating.ManualReviewReasons, "; "))
	}
	return closing, nil
}

// unchangedSince reports whether the subscription and its effective tier are
// still those the closing invoice was rated for.
func (c *PostpaidPhaseClose) unchangedSince(current purserdb.GetSubscriptionForPhaseCloseRow, tier purserdb.LoadSubscriptionEffectiveTierRow) bool {
	rated := c.subscription
	if current.BillingModel != "postpaid" || current.TierID != rated.TierID ||
		!sameNullTime(current.BillingPeriodStart, rated.BillingPeriodStart) || !sameNullTime(current.BillingPeriodEnd, rated.BillingPeriodEnd) ||
		!sameNullTime(current.MollieNextPaymentDate, rated.MollieNextPaymentDate) ||
		current.StripeSubscriptionID != rated.StripeSubscriptionID || current.MollieSubscriptionID != rated.MollieSubscriptionID ||
		current.PaymentMethod != rated.PaymentMethod || current.StripeCustomerID != rated.StripeCustomerID ||
		current.HasMollieCustomer != rated.HasMollieCustomer || current.PresentmentCurrency != rated.PresentmentCurrency {
		return false
	}
	if c.tier == nil {
		return true
	}
	basePrice := tier.BasePrice
	if tier.GrantedBasePrice.Valid {
		basePrice = tier.GrantedBasePrice.String
	}
	base, err := decimal.NewFromString(basePrice)
	return err == nil && base.Equal(c.tier.BasePrice) && tier.WaiveUsage == c.tier.UsageWaived && tier.TierID.String() == c.tier.TierID
}

// CommitTx writes the closing invoice and starts the next billing phase inside
// the arrangement's transaction. The caller holds the subscription row lock.
// Changed subscription/pricing/received usage returns ErrPostpaidPhaseChanged.
// A move to prepaid settles the unused advance fee without wallet credit.
// A postpaid grant boundary retains it for postpaid fee netting.
func (c *PostpaidPhaseClose) CommitTx(ctx context.Context, tx *sql.Tx) (bool, error) {
	if c == nil {
		return false, nil
	}
	c.closed, c.result, c.releasedCredit = false, postpaidInvoiceResult{}, invoiceCreditChange{}
	c.carriedCents, c.balanceAfter, c.baseFeeDisposition = 0, 0, unusedBaseFeeDisposition{}
	queries := purserdb.New(tx)
	current, err := queries.GetSubscriptionForPhaseClose(ctx, c.tenantID)
	if err != nil {
		return false, fmt.Errorf("load subscription: %w", err)
	}
	if c.run == nil {
		if !c.unchangedSince(current, purserdb.LoadSubscriptionEffectiveTierRow{}) {
			return false, ErrPostpaidPhaseChanged
		}
		return false, nil
	}
	var tier purserdb.LoadSubscriptionEffectiveTierRow
	if c.grantBoundary {
		var row purserdb.LoadSubscriptionEffectiveTierAtRow
		row, err = queries.LoadSubscriptionEffectiveTierAt(ctx, purserdb.LoadSubscriptionEffectiveTierAtParams{TenantID: c.tenantID, CloseAt: c.switchAt})
		tier = purserdb.LoadSubscriptionEffectiveTierRow(row)
	} else {
		tier, err = queries.LoadSubscriptionEffectiveTier(ctx, c.tenantID)
	}
	if err != nil {
		return false, fmt.Errorf("load postpaid tier: %w", err)
	}
	if !c.unchangedSince(current, tier) {
		return false, ErrPostpaidPhaseChanged
	}
	received, err := countPhaseReceived(ctx, queries, c.tenantID, c.phase)
	if err != nil {
		return false, err
	}
	if received != c.received {
		return false, ErrPostpaidPhaseChanged
	}
	// The open draft overlapping the phase becomes the closing invoice, also
	// one written for another period start, so none stays open.
	draft, err := queries.GetOpenUsageDraftForPhase(ctx, purserdb.GetOpenUsageDraftForPhaseParams{
		TenantID: c.tenantID, PhaseStart: c.phase.start, PhaseEnd: c.phase.end,
	})
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return false, fmt.Errorf("look up the period's draft: %w", err)
	}
	if draft.ID != "" && !draft.PeriodStart.Equal(c.phase.start) {
		// That draft's credit is held under its own period start's key.
		if c.releasedCredit, err = reconcileInvoicePrepaidCreditTx(ctx, tx, c.tenantID, draft.PeriodStart, 0); err != nil {
			return false, fmt.Errorf("return the draft's invoice credit: %w", err)
		}
	}
	run := *c.run
	run.draftInvoiceID = draft.ID
	c.result, err = writePostpaidInvoiceTx(ctx, tx, &run)
	if err != nil {
		return false, err
	}
	if c.result.status == "manual_review" {
		return false, billingBoundaryConditionf("the closing invoice requires tax review; complete the billing details before changing the billing phase")
	}
	if c.toPrepaid {
		if c.carriedCents, c.balanceAfter, err = settleCollectionCarryTx(ctx, tx, c.tenantID, c.result.invoiceID); err != nil {
			return false, err
		}
	}
	if c.toPrepaid {
		if c.baseFeeDisposition, err = settleUnusedBaseFeeTx(ctx, tx, c.tenantID, c.switchAt, time.Now().UTC()); err != nil {
			return false, err
		}
	}
	var rows int64
	if c.monthlyCatchup {
		rows, err = queries.AdvanceSubscriptionBillingPeriod(ctx, purserdb.AdvanceSubscriptionBillingPeriodParams{
			BillingPeriodStart: sql.NullTime{Time: c.switchAt, Valid: true},
			BillingPeriodEnd:   sql.NullTime{Time: c.nextPeriodEnd, Valid: true},
			NextBillingDate:    sql.NullTime{Time: c.nextPeriodEnd, Valid: true}, TenantID: c.tenantID,
		})
	} else {
		rows, err = queries.StartBillingPhase(ctx, purserdb.StartBillingPhaseParams{
			PeriodStart: c.switchAt, PeriodEnd: c.nextPeriodEnd, TenantID: c.tenantID,
		})
	}
	if err != nil {
		return false, fmt.Errorf("start the next billing phase: %w", err)
	}
	if rows != 1 {
		return false, fmt.Errorf("start the next billing phase: no subscription for tenant %s", c.tenantID)
	}
	c.closed = true
	return true, nil
}

// AfterCommit records the committed closing invoice and starts its
// collection.
func (c *PostpaidPhaseClose) AfterCommit(ctx context.Context) {
	if c == nil || !c.closed || c.run == nil {
		return
	}
	logInvoiceCreditChange(c.jobs.logger, c.tenantID, c.result.invoiceID, c.phase.start, c.releasedCredit)
	c.jobs.afterPostpaidInvoiceCommit(ctx, c.run, c.result, "Finalized the invoice that closes the postpaid phase")
	if c.carriedCents > 0 {
		c.jobs.logger.WithFields(logging.Fields{
			"tenant_id":     c.tenantID,
			"invoice_id":    c.result.invoiceID,
			"carried_cents": c.carriedCents,
			"balance_after": c.balanceAfter,
		}).Info("Charged postpaid charges below the collection minimum to the prepaid balance at the switch to prepaid")
	}
	if c.baseFeeDisposition.invoiceID != "" {
		c.jobs.logger.WithFields(logging.Fields{
			"tenant_id":        c.tenantID,
			"base_fee_invoice": c.baseFeeDisposition.invoiceID,
			"reduced_cents":    c.baseFeeDisposition.reducedCents,
			"credited_cents":   int64(0),
			"forfeited_cents":  c.baseFeeDisposition.forfeitedCents,
		}).Info("Settled the unused advance base fee at the switch to prepaid")
	}
	c.jobs.logger.WithFields(logging.Fields{
		"tenant_id":        c.tenantID,
		"invoice_id":       c.result.invoiceID,
		"phase_from":       c.switchAt.Format(time.RFC3339Nano),
		"phase_period_end": c.nextPeriodEnd.Format(time.RFC3339),
	}).Info("Closed postpaid billing phase")
}

// InvoiceID is the closing invoice written by CommitTx, empty when none.
func (c *PostpaidPhaseClose) InvoiceID() string {
	if c == nil || !c.closed {
		return ""
	}
	return c.result.invoiceID
}

// BoundaryAt returns the instant separating the old and next pricing phases.
func (c *PostpaidPhaseClose) BoundaryAt() time.Time { return c.switchAt }

// sameInvoiceSubscription compares billing snapshots using instants for nullable times.
func sameInvoiceSubscription(a, b purserdb.ListSubscriptionsDueForInvoiceRow) bool {
	if !sameNullTime(a.BillingPeriodStart, b.BillingPeriodStart) || !sameNullTime(a.BillingPeriodEnd, b.BillingPeriodEnd) || !sameNullTime(a.MollieNextPaymentDate, b.MollieNextPaymentDate) {
		return false
	}
	a.BillingPeriodStart, a.BillingPeriodEnd, a.MollieNextPaymentDate = sql.NullTime{}, sql.NullTime{}, sql.NullTime{}
	b.BillingPeriodStart, b.BillingPeriodEnd, b.MollieNextPaymentDate = sql.NullTime{}, sql.NullTime{}, sql.NullTime{}
	return a == b
}

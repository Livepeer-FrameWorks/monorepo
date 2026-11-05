package handlers

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	billingpkg "frameworks/api_billing/internal/billing"
	"frameworks/api_billing/internal/database/purserdb"
	"frameworks/api_billing/internal/fx"
	"frameworks/api_billing/internal/pricing"
	"frameworks/api_billing/internal/rating"
	"frameworks/api_billing/internal/subscriptions"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/billing"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/shopspring/decimal"
)

// baseFeeDueDays is how long an advance base-fee invoice stays payable when
// its off-session charge does not settle.
const baseFeeDueDays = 14

// finalizeInvoicePresentmentTx converts a finalized invoice's EUR total into
// the tenant's presentment currency at the ECB rate for finalizedAt and stores
// the presentment amount, rate, reference date, and finalization time. A
// missing or stale rate returns an error so the caller's transaction rolls
// back and finalization is retried later.
func finalizeInvoicePresentmentTx(ctx context.Context, tx *sql.Tx, invoiceID, tenantID, presentmentCurrency string, totalEUR decimal.Decimal, finalizedAt time.Time) (fx.Record, error) {
	currency := strings.ToUpper(strings.TrimSpace(presentmentCurrency))
	if currency == "" {
		currency = billing.LedgerCurrency
	}
	totalCents := totalEUR.Round(2).Shift(2).IntPart()
	record, err := fx.QuoteFromEUR(ctx, tx, currency, totalCents, finalizedAt)
	if err != nil {
		return fx.Record{}, fmt.Errorf("present invoice %s in %s: %w", invoiceID, currency, err)
	}
	rows, err := purserdb.New(tx).SetInvoicePresentment(ctx, purserdb.SetInvoicePresentmentParams{
		PresentmentAmountCents:   record.OriginalMinor,
		PresentmentCurrency:      record.OriginalCurrency,
		PresentmentUnitsPerEur:   record.UnitsText(),
		PresentmentReferenceDate: record.ReferenceDate,
		FinalizedAt:              finalizedAt,
		SupplierSnapshot:         DocumentSupplierSnapshot(),
		InvoiceID:                invoiceID,
		TenantID:                 tenantID,
	})
	if err != nil {
		return fx.Record{}, fmt.Errorf("store invoice %s presentment: %w", invoiceID, err)
	}
	if rows != 1 {
		return fx.Record{}, fmt.Errorf("store invoice %s presentment: invoice is not finalized", invoiceID)
	}
	return record, nil
}

// activateAdvanceBilledTenant starts advance invoicing after payment-method setup.
// Setup cannot change a paid tier during its current period; that purchase
// uses the durable tier-upgrade boundary. Setting up another collection provider
// on the same advance-billed tier preserves its paid period. Activation commits before collection,
// and the invoice job retries a charge that cannot start immediately.
func (s *Service) activateAdvanceBilledTenant(ctx context.Context, tenantID, tierID, provider, stripeCustomerID string, now time.Time, receiptID string) error {
	return s.activateAdvanceBilledTenantWithMandate(ctx, tenantID, tierID, provider, stripeCustomerID, now, receiptID, nil)
}

func (s *Service) activateAdvanceBilledTenantWithMandate(ctx context.Context, tenantID, tierID, provider, stripeCustomerID string, now time.Time, receiptID string, mandate *purserdb.UpsertMollieMandateParams) error {
	if _, policyErr := subscriptions.TransitionProvider(ctx, s.db, subscriptions.ProviderChange{
		TenantID: tenantID, Operation: subscriptions.ProviderCheckout, ReceiptIntentID: receiptID,
		Advance: purserdb.ActivateAdvanceBilledSubscriptionParams{TierID: tierID, PaymentMethod: provider},
	}); policyErr != nil {
		return policyErr
	}
	periodStart := now.UTC().Truncate(time.Microsecond)
	profile, err := purserdb.New(s.db).GetTenantCollectionProfile(ctx, tenantID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("load collection profile before activation: %w", err)
	}
	var closing *PostpaidPhaseClose
	if err == nil && advanceBilledPeriodChanges(profile, tierID, periodStart) {
		var prepareErr error
		closing, prepareErr = PrepareProviderBillingBoundary(ctx, s.db, s.logger, s, tenantID, periodStart)
		if prepareErr != nil {
			return fmt.Errorf("prepare the current period before provider activation: %w", prepareErr)
		}
		if closing != nil {
			periodStart = closing.BoundaryAt()
		}
	}

	result, err := subscriptions.TransitionProvider(ctx, s.db, subscriptions.ProviderChange{
		TenantID: tenantID, Operation: subscriptions.AdvanceActivation, ReceiptIntentID: receiptID,
		MollieMandate: mandate, Boundary: closing, Advance: purserdb.ActivateAdvanceBilledSubscriptionParams{
			PaymentMethod: provider, StripeCustomerID: stripeCustomerID, TierID: tierID,
			PeriodStart: periodStart, PeriodEnd: billing.NextMonthlyAnniversary(periodStart), TenantID: tenantID,
		}})
	if err != nil {
		return fmt.Errorf("activate tenant %s without a provider subscription: %w", tenantID, err)
	}
	if result.Rows == 0 {
		return fmt.Errorf("no subscription without a provider subscription for tenant %s", tenantID)
	}
	if closing != nil {
		closing.AfterCommit(ctx)
	}
	if s.convergeTenantEntitlements != nil {
		if convergeErr := s.convergeTenantEntitlements(ctx, tenantID); convergeErr != nil {
			return fmt.Errorf("converge tenant entitlements after activation: %w", convergeErr)
		}
	}
	if s.chargeAdvanceBaseFee != nil {
		if chargeErr := s.chargeAdvanceBaseFee(ctx, tenantID); chargeErr != nil {
			s.logger.WithError(chargeErr).WithField("tenant_id", tenantID).Warn("Advance base-fee charge after activation failed; the invoice job retries it")
		}
	}
	return nil
}

// advanceBilledPeriodChanges reports whether activating tierID
// ends a running advance-billed period: the tenant is active and
// postpaid without a provider subscription, now falls inside its period, and
// the tier differs. An empty tierID keeps the current tier.
func advanceBilledPeriodChanges(profile purserdb.GetTenantCollectionProfileRow, tierID string, now time.Time) bool {
	if profile.Status != "active" || profile.BillingModel != "postpaid" ||
		profile.StripeSubscriptionID.Valid || profile.MollieSubscriptionID.Valid ||
		!profile.BillingPeriodStart.Valid || !profile.BillingPeriodEnd.Valid {
		return false
	}
	if !now.After(profile.BillingPeriodStart.Time) || !now.Before(profile.BillingPeriodEnd.Time) {
		return false
	}
	sameTier := tierID == "" || tierID == profile.TierID
	return !sameTier
}

// handleStripeSetupCheckoutCompleted saves the payment method a setup-mode
// Checkout Session collected as the customer's default and activates the tier
// the session was started for.
func (s *Service) handleStripeSetupCheckoutCompleted(ctx context.Context, sessionID, tenantID, tierID, customerID, setupIntentID string, receiptID string) error {
	if tenantID == "" || customerID == "" || setupIntentID == "" {
		return fmt.Errorf("setup checkout %s is missing tenant, customer, or setup intent", sessionID)
	}
	completed, completedErr := purserdb.New(s.db).CheckoutAlreadySettled(ctx, purserdb.CheckoutAlreadySettledParams{TenantID: tenantID, Provider: "stripe", SessionID: sessionID, PaymentID: ""})
	if completedErr != nil {
		return fmt.Errorf("check checkout outcome: %w", completedErr)
	}
	if completed {
		return nil
	}
	bound, bindingErr := purserdb.New(s.db).CheckoutReceiptBound(ctx, purserdb.CheckoutReceiptBoundParams{TenantID: tenantID, Provider: "stripe", SessionID: sessionID})
	if bindingErr != nil {
		return fmt.Errorf("read checkout response binding: %w", bindingErr)
	}
	if !bound {
		return errors.New("checkout response binding is pending; retry")
	}

	if s.stripeClient == nil {
		return errors.New("stripe client not configured for setup checkout")
	}
	if _, err := s.stripeClient.SetDefaultPaymentMethodFromSetupIntent(ctx, customerID, setupIntentID); err != nil {
		return err
	}
	if err := s.activateAdvanceBilledTenant(ctx, tenantID, tierID, "stripe", customerID, providerReceiptTime(ctx), receiptID); err != nil {
		if errors.Is(err, subscriptions.ErrCheckoutCompensationQueued) {
			return nil
		}
		return err
	}
	if err := purserdb.New(s.db).MarkStripeIntentSucceededBySession(ctx, purserdb.MarkStripeIntentSucceededBySessionParams{
		SubscriptionID: "", SessionID: sessionID, TenantID: tenantID,
	}); err != nil {
		return fmt.Errorf("mark setup checkout intent succeeded: %w", err)
	}
	s.logger.WithFields(logging.Fields{
		"tenant_id":   tenantID,
		"tier_id":     tierID,
		"customer_id": customerID,
		"session_id":  sessionID,
	}).Info("Activated tier from Stripe setup checkout")
	return nil
}

// chargeInvoice collects amountMinor of currency for an invoice off-session
// through the selected provider. An empty provider collects nothing.
func (jm *JobManager) chargeInvoice(ctx context.Context, provider, tenantID, invoiceID string, amountMinor int64, currency string) error {
	if amountMinor <= 0 {
		return nil
	}
	amount := decimal.New(amountMinor, -2)
	switch provider {
	case "stripe":
		return jm.chargeStripeOverage(ctx, tenantID, invoiceID, amount, currency)
	case "mollie":
		return jm.chargeMollieOverage(ctx, tenantID, invoiceID, amount, currency)
	default:
		return nil
	}
}

// chargeDueAdvanceBaseFees charges the base fee of every tenant whose current
// period has no base-fee invoice yet. It backs up the charge made at period
// rollover and at activation, so a failed charge or rate lookup is retried on
// the next run.
func (jm *JobManager) chargeDueAdvanceBaseFees(ctx context.Context, now time.Time) {
	candidates, err := purserdb.New(jm.db).ListAdvanceBaseFeeCandidates(ctx, now)
	if err != nil {
		jm.logger.WithError(err).Warn("List advance base-fee candidates failed")
		return
	}
	for _, candidate := range candidates {
		if chargeErr := jm.chargeAdvanceBaseFee(ctx, candidate.TenantID, now); chargeErr != nil {
			jm.logger.WithError(chargeErr).WithField("tenant_id", candidate.TenantID).Warn("Advance base-fee charge failed; retrying next run")
		}
	}
}

// ChargeAdvanceBaseFee is chargeAdvanceBaseFee for activation paths outside
// the invoice job.
func (jm *JobManager) ChargeAdvanceBaseFee(ctx context.Context, tenantID string) error {
	return jm.chargeAdvanceBaseFee(ctx, tenantID, time.Now())
}

// chargeAdvanceBaseFee creates the base-fee invoice for the tenant's current
// period and charges it off-session, when Purser collects the tenant's base
// fee: an active postpaid tenant without a provider subscription, presented
// outside EUR, with a card provider on file. When a tier change cut the
// previous base-fee period short, planPreviousBaseFee settles that invoice's
// unused share in the same transaction: a paid invoice credits the replacement,
// and an unpaid invoice is reduced to its used share. Credit is capped at the
// replacement fee; any excess is recorded as forfeited and never becomes funds.
// The net EUR amount is presented at the current ECB rate. A missing or stale rate, or a payment
// of the previous invoice still pending, creates nothing and returns an error.
// A period that already has a base-fee invoice is left alone, and the retry job
// collects an invoice whose charge did not start.
func (jm *JobManager) chargeAdvanceBaseFee(ctx context.Context, tenantID string, now time.Time) error {
	queries := purserdb.New(jm.db)
	profile, err := queries.GetTenantCollectionProfile(ctx, tenantID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("load collection profile: %w", err)
	}
	provider := profile.PaymentMethod.String
	currency := strings.ToUpper(strings.TrimSpace(profile.PresentmentCurrency))
	if profile.Status != "active" || profile.BillingModel != "postpaid" ||
		profile.StripeSubscriptionID.Valid || profile.MollieSubscriptionID.Valid ||
		currency == billing.LedgerCurrency || (provider != "stripe" && provider != "mollie") ||
		!profile.BillingPeriodStart.Valid || !profile.BillingPeriodEnd.Valid {
		return nil
	}
	periodStart, periodEnd := profile.BillingPeriodStart.Time, profile.BillingPeriodEnd.Time
	if now.Before(periodStart) || !now.Before(periodEnd) {
		return nil
	}
	exists, err := queries.BaseFeeInvoiceExistsForPeriod(ctx, purserdb.BaseFeeInvoiceExistsForPeriodParams{
		TenantID: tenantID, PeriodStart: periodStart,
	})
	if err != nil {
		return fmt.Errorf("check base-fee invoice: %w", err)
	}
	if exists {
		return nil
	}

	tier, err := billingpkg.LoadEffectiveTier(ctx, jm.db, tenantID)
	if err != nil {
		return fmt.Errorf("load effective tier: %w", err)
	}
	if tier.Currency != billing.LedgerCurrency {
		return fmt.Errorf("tier %s is priced in %s, not the %s price list", tier.TierName, tier.Currency, billing.LedgerCurrency)
	}
	// A period that a switch from prepaid started mid-way pays the base fee
	// for its share of the period the switches split.
	chainFrom, err := splitPeriodStart(ctx, jm.db, tenantID, periodStart)
	if err != nil {
		return err
	}
	baseEUR := usagePhase{start: periodStart, end: periodEnd, chainFrom: chainFrom}.baseFeeShare(tier.BasePrice.Round(2))
	billingEmail, emailErr := queries.GetTenantBillingEmail(ctx, tenantID)
	if emailErr != nil && !errors.Is(emailErr, sql.ErrNoRows) {
		return fmt.Errorf("load billing email: %w", emailErr)
	}

	var invoiceID string
	var record fx.Record
	var credit baseFeeCredit
	dueAt := now.AddDate(0, 0, baseFeeDueDays)
	err = withTx(ctx, jm.db, func(tx *sql.Tx) error {
		txQueries := purserdb.New(tx)
		if lockErr := txQueries.LockSubscriptionForPeriodClose(ctx, tenantID); lockErr != nil {
			return lockErr
		}
		if _, closureErr := txQueries.GetTenantBillingClosure(ctx, tenantID); closureErr == nil {
			return nil
		} else if !errors.Is(closureErr, sql.ErrNoRows) {
			return closureErr
		}
		previous, previousErr := planPreviousBaseFee(ctx, txQueries, tenantID, periodStart)
		if previousErr != nil {
			return previousErr
		}
		credit = cappedReplacementCredit(previous.credit, baseEUR.Shift(2).IntPart())
		if !baseEUR.IsPositive() && !previous.found {
			return nil
		}
		netCents := baseEUR.Shift(2).IntPart() - credit.cents
		if previous.taxReady {
			netCents = baseEUR.Shift(2).IntPart()
		}
		collectCents := max(netCents, 0)
		taxAssessment, taxErr := assessInvoiceTaxTx(ctx, tx, tenantID, collectCents, now)
		if taxErr != nil {
			return taxErr
		}
		collectCents = taxAssessment.Gross
		if previous.taxReady {
			credit, previous.creditOriginalMinor = frozenReplacementCredit(previous, collectCents)
			collectCents -= credit.cents
		}
		var quoteErr error
		record, quoteErr = fx.QuoteFromEUR(ctx, tx, currency, collectCents, now)
		if quoteErr != nil {
			return fmt.Errorf("present base fee in %s: %w", currency, quoteErr)
		}
		status := finalizedInvoiceStatus(decimal.New(collectCents, -2))
		usageDetails, detailsErr := baseFeeInvoiceDetails(tier, baseEUR, periodStart, periodEnd, credit)
		if detailsErr != nil {
			return detailsErr
		}
		id, insertErr := txQueries.InsertBaseFeeInvoice(ctx, purserdb.InsertBaseFeeInvoiceParams{
			TenantID:                 tenantID,
			Status:                   status,
			Amount:                   decimal.New(collectCents, -2).StringFixed(2),
			BaseAmount:               baseEUR.StringFixed(2),
			DueDate:                  dueAt,
			UsageDetails:             usageDetails,
			PeriodStart:              periodStart,
			PeriodEnd:                periodEnd,
			PresentmentAmountCents:   record.OriginalMinor,
			PresentmentCurrency:      record.OriginalCurrency,
			PresentmentUnitsPerEur:   record.UnitsText(),
			PresentmentReferenceDate: record.ReferenceDate,
			FinalizedAt:              now,
			SupplierSnapshot:         DocumentSupplierSnapshot(),
		})
		if errors.Is(insertErr, sql.ErrNoRows) {
			return nil
		}
		if insertErr != nil {
			return fmt.Errorf("insert base-fee invoice: %w", insertErr)
		}
		invoiceID = id
		lines := &clusterRatingResult{
			BaseLine: pricedLine{
				LineItem: rating.LineItem{
					LineKey:          rating.LineKeyBaseSubscription,
					Description:      fmt.Sprintf("Base subscription %s to %s", periodStart.Format(time.DateOnly), periodEnd.Format(time.DateOnly)),
					Quantity:         decimal.NewFromInt(1),
					IncludedQuantity: decimal.Zero,
					BillableQuantity: decimal.NewFromInt(1),
					UnitPrice:        baseEUR,
					Amount:           baseEUR,
					Currency:         billing.LedgerCurrency,
				},
				PricingSource: pricing.SourceTier,
			},
		}
		if credit.cents > 0 && !previous.taxReady {
			creditEUR := decimal.New(-credit.cents, -2)
			lines.UsageLines = append(lines.UsageLines, pricedLine{
				LineItem: rating.LineItem{
					LineKey: unusedBaseFeeCreditLineKey,
					Description: fmt.Sprintf("Credit for unused base subscription %s to %s",
						periodStart.Format(time.DateOnly), credit.periodEnd.Format(time.DateOnly)),
					Quantity:         decimal.NewFromInt(1),
					IncludedQuantity: decimal.Zero,
					BillableQuantity: decimal.NewFromInt(1),
					UnitPrice:        creditEUR,
					Amount:           creditEUR,
					Currency:         billing.LedgerCurrency,
				},
				PricingSource: pricing.SourceTier,
			})
		}
		if lineErr := persistInvoiceLineItems(ctx, tx, invoiceID, tenantID, lines); lineErr != nil {
			return lineErr
		}
		if taxErr = freezeInvoiceTaxTx(ctx, tx, tenantID, invoiceID, taxAssessment, record); taxErr != nil {
			return taxErr
		}
		if previous.reduction != nil {
			if reduceErr := reducePreviousBaseFeeInvoiceTx(ctx, tx, tenantID, *previous.reduction, now); reduceErr != nil {
				return reduceErr
			}
		}
		if previous.taxReady {
			if previous.reduction == nil && credit.cents > 0 {
				if creditErr := issueUnusedBaseFeeCredit(ctx, txQueries, tenantID, previous.credit.invoiceID, previous.creditOriginalMinor, previous.creditCurrency, periodStart); creditErr != nil {
					return creditErr
				}
			}
			if creditErr := txQueries.ApplyUnusedBaseFeeFinancialCredit(ctx, purserdb.ApplyUnusedBaseFeeFinancialCreditParams{TenantID: tenantID, InvoiceID: invoiceID, CreditEurCents: credit.cents}); creditErr != nil {
				return creditErr
			}
		}
		if consumeErr := consumePreviousBaseFee(ctx, txQueries, tenantID, previous, credit, invoiceID, periodStart); consumeErr != nil {
			return consumeErr
		}
		if emailErr := enqueueInvoiceEmailTx(ctx, tx, invoiceID, tenantID, billingEmail.String, status); emailErr != nil {
			return emailErr
		}
		return enqueueInvoiceCreatedTx(ctx, tx, tenantID, invoiceID, status, collectCents, periodStart, periodEnd, dueAt)
	})
	if err != nil || invoiceID == "" {
		return err
	}
	jm.logger.WithFields(logging.Fields{
		"tenant_id":         tenantID,
		"invoice_id":        invoiceID,
		"base_eur":          baseEUR.String(),
		"credit_eur_cents":  credit.cents,
		"presentment_minor": record.OriginalMinor,
		"currency":          record.OriginalCurrency,
		"reference_date":    record.ReferenceDate.Format(time.DateOnly),
	}).Info("Created advance base-fee invoice")
	return jm.chargeInvoice(ctx, provider, tenantID, invoiceID, record.OriginalMinor, record.OriginalCurrency)
}

// unusedBaseFeeCreditLineKey is the base-fee invoice line that credits the
// unused share of the previous period's base fee.
const unusedBaseFeeCreditLineKey = "unused_base_fee_credit"

type baseFeeCredit struct {
	cents                  int64
	forfeitedCents         int64
	invoiceID              string
	providerPaymentID      string
	periodStart, periodEnd time.Time
}

// cappedReplacementCredit consumes the available share once, including excess
// that cannot fund this purchase and must not survive as spendable credit.
func cappedReplacementCredit(credit baseFeeCredit, baseCents int64) baseFeeCredit {
	applied := min(max(credit.cents, 0), max(baseCents, 0))
	credit.forfeitedCents = max(credit.cents-applied, 0)
	credit.cents = applied
	return credit
}

// A capped paid-service credit reverses only the amount allocated to the new
// purchase. Retained fees keep their original tax instead of losing VAT twice.
func frozenReplacementCredit(previous previousBaseFee, limit int64) (baseFeeCredit, int64) {
	credit := cappedReplacementCredit(previous.credit, limit)
	original := previous.creditOriginalMinor
	if previous.reduction == nil && previous.credit.cents > 0 && credit.cents < previous.credit.cents {
		original = decimal.NewFromInt(original).Mul(decimal.NewFromInt(credit.cents)).Div(decimal.NewFromInt(previous.credit.cents)).Round(0).IntPart()
		for original > 0 && decimal.NewFromInt(previous.sourceGrossEUR).Mul(decimal.NewFromInt(original)).Div(decimal.NewFromInt(previous.sourceGrossOriginal)).Round(0).IntPart() > credit.cents {
			original--
		}
		credit.cents = decimal.NewFromInt(previous.sourceGrossEUR).Mul(decimal.NewFromInt(original)).Div(decimal.NewFromInt(previous.sourceGrossOriginal)).Round(0).IntPart()
		credit.forfeitedCents = max(previous.credit.cents-credit.cents, 0)
	}
	return credit, original
}

// unusedBaseFeeReductionLineKey is the line that takes the unused share of its
// period off an unpaid base-fee invoice a tier change cut short.
const unusedBaseFeeReductionLineKey = "unused_base_fee_reduction"

// errPreviousBaseFeePaymentPending means a payment of the base-fee invoice a
// tier change cut short has not resolved yet, so what that invoice still owes
// is unknown. The new period's base-fee invoice waits for it.
var errPreviousBaseFeePaymentPending = errors.New("a payment of the previous base-fee invoice is still pending")

// ErrBaseFeePaymentPending means a payment of the advance base-fee invoice
// whose unused share a switch to prepaid settles has not resolved yet, so
// what that invoice still owes is unknown; the switch waits for it.
var ErrBaseFeePaymentPending = errPreviousBaseFeePaymentPending

// unusedBaseFeeDisposition records reduction and forfeit of the advance
// base-fee invoice whose period it cut short.
type unusedBaseFeeDisposition struct {
	invoiceID      string
	reducedCents   int64
	forfeitedCents int64
}

// settleUnusedBaseFeeTx settles the old invoice when an operator moves to
// prepaid. Unpaid fees are reduced to their used share; retained paid fees are
// forfeited, recorded on the invoice, and never become spendable balance.
func settleUnusedBaseFeeTx(ctx context.Context, tx *sql.Tx, tenantID string, switchAt, now time.Time) (unusedBaseFeeDisposition, error) {
	queries := purserdb.New(tx)
	plan, err := planPreviousBaseFee(ctx, queries, tenantID, switchAt)
	if err != nil {
		return unusedBaseFeeDisposition{}, err
	}
	if !plan.found || plan.credit.invoiceID == "" {
		return unusedBaseFeeDisposition{}, nil
	}
	disposition := unusedBaseFeeDisposition{invoiceID: plan.credit.invoiceID, forfeitedCents: plan.credit.cents}
	if plan.reduction != nil {
		if err = reducePreviousBaseFeeInvoiceTx(ctx, tx, tenantID, *plan.reduction, now); err != nil {
			return unusedBaseFeeDisposition{}, err
		}
		disposition.reducedCents = plan.reduction.reducedCents
	}
	details, err := json.Marshal(map[string]any{
		"unused_from": switchAt,
		"reduced":     decimal.New(disposition.reducedCents, -2).StringFixed(2),
		"credited":    decimal.Zero.StringFixed(2),
		"forfeited":   decimal.New(disposition.forfeitedCents, -2).StringFixed(2),
	})
	if err != nil {
		return unusedBaseFeeDisposition{}, fmt.Errorf("marshal base-fee disposition: %w", err)
	}
	rows, err := queries.MarkBaseFeeUnusedShareReturned(ctx, purserdb.MarkBaseFeeUnusedShareReturnedParams{
		Details: details, InvoiceID: disposition.invoiceID, TenantID: tenantID,
	})
	if err != nil {
		return unusedBaseFeeDisposition{}, fmt.Errorf("record the base-fee disposition on invoice %s: %w", disposition.invoiceID, err)
	}
	if rows != 1 {
		return unusedBaseFeeDisposition{}, fmt.Errorf("record the base-fee disposition: base-fee invoice %s not found", disposition.invoiceID)
	}
	return disposition, nil
}

// previousBaseFee is what a new period's base-fee invoice does with the
// base-fee invoice whose period it cut short.
type previousBaseFee struct {
	// found reports a base-fee invoice overlapping the new period.
	found               bool
	taxReady            bool
	creditOriginalMinor int64
	sourceGrossEUR      int64
	sourceGrossOriginal int64
	creditCurrency      string
	// credit is the EUR the new base-fee invoice credits.
	credit baseFeeCredit
	// reduction, when set, lowers the unpaid previous invoice to its used share.
	reduction *baseFeeReduction
}

type baseFeeReduction struct {
	taxReady                  bool
	creditOriginalMinor       int64
	creditCurrency            string
	invoiceID                 string
	reducedCents, amountCents int64
	presentmentCents          int64
	unusedFrom, unusedUntil   time.Time
	// covered reports that confirmed payments already cover the reduced
	// presentment amount, so the invoice is paid once reduced.
	covered bool
}

// planPreviousBaseFee locks the base-fee invoice a period starting at
// periodStart cut short and decides how its unused share, the base fee times
// the remaining over the whole period duration rounded half away from zero to
// the cent, is settled:
//   - A paid invoice was collected in full, so the new base-fee invoice credits
//     the unused share.
//   - A pending or overdue invoice stays payable, under its due date and
//     dunning, for the share that was used: it is reduced by the unused share,
//     presented at the rate it was issued at. Only the part of the unused share
//     above what it still owed, and any payment above the reduced amount, is
//     credited to the new invoice.
//   - Any other invoice earns nothing.
//
// A payment of the previous invoice that has not resolved returns
// errPreviousBaseFeePaymentPending. A period that follows its predecessor gets
// nothing.
func planPreviousBaseFee(ctx context.Context, queries *purserdb.Queries, tenantID string, periodStart time.Time) (previousBaseFee, error) {
	previousID, err := queries.FindOverlappedBaseFeeInvoice(ctx, purserdb.FindOverlappedBaseFeeInvoiceParams{
		TenantID: tenantID, PeriodStart: periodStart,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return previousBaseFee{}, nil
	}
	if err != nil {
		return previousBaseFee{}, fmt.Errorf("find the base-fee invoice the new period cuts short: %w", err)
	}
	// The payment-creation lock comes before the row lock, in the order payment
	// creation and settlement take them, so no payment is created or settled
	// against the invoice while it is read and reduced.
	if lockErr := queries.LockInvoicePaymentCreation(ctx, previousID); lockErr != nil {
		return previousBaseFee{}, fmt.Errorf("lock payments of invoice %s: %w", previousID, lockErr)
	}
	previous, err := queries.LockOverlappedBaseFeeInvoice(ctx, purserdb.LockOverlappedBaseFeeInvoiceParams{
		InvoiceID: previousID, TenantID: tenantID,
	})
	if err != nil {
		return previousBaseFee{}, fmt.Errorf("lock the base-fee invoice the new period cuts short: %w", err)
	}
	plan := previousBaseFee{found: true}
	baseEUR, err := decimal.NewFromString(previous.BaseAmount)
	if err != nil {
		return previousBaseFee{}, fmt.Errorf("parse base amount of invoice %s: %w", previous.ID, err)
	}
	whole := previous.BaseFeePeriodEnd.Sub(previous.BaseFeePeriodStart).Microseconds()
	remaining := previous.BaseFeePeriodEnd.Sub(periodStart).Microseconds()
	if whole <= 0 || remaining <= 0 {
		return plan, nil
	}
	unusedCents := baseEUR.Shift(2).Mul(decimal.NewFromInt(remaining)).Div(decimal.NewFromInt(whole)).Round(0).IntPart()
	if previous.TaxStatus == "ready" && previous.DocumentGrossMinor.Valid && previous.GrossEurCents.Valid && previous.DocumentGrossMinor.Int64 > 0 {
		plan.taxReady = true
		plan.sourceGrossEUR = previous.GrossEurCents.Int64
		plan.sourceGrossOriginal = previous.DocumentGrossMinor.Int64
		plan.creditCurrency = previous.DocumentCurrency
		plan.creditOriginalMinor = decimal.NewFromInt(previous.DocumentGrossMinor.Int64).Mul(decimal.NewFromInt(remaining)).Div(decimal.NewFromInt(whole)).Round(0).IntPart()
		unusedCents = decimal.NewFromInt(previous.GrossEurCents.Int64).Mul(decimal.NewFromInt(plan.creditOriginalMinor)).Div(decimal.NewFromInt(previous.DocumentGrossMinor.Int64)).Round(0).IntPart()
	}
	credit := baseFeeCredit{invoiceID: previous.ID, periodStart: previous.BaseFeePeriodStart, periodEnd: previous.BaseFeePeriodEnd}
	switch previous.Status {
	case "paid":
		credit.cents = unusedCents
		plan.credit = credit
		return plan, nil
	case "pending", "overdue":
	default:
		return plan, nil
	}
	if previous.PendingPayments > 0 {
		return previousBaseFee{}, fmt.Errorf("%w: invoice %s", errPreviousBaseFeePaymentPending, previous.ID)
	}
	basis, err := queries.GetInvoicePaymentFXBasis(ctx, purserdb.GetInvoicePaymentFXBasisParams{InvoiceID: previous.ID, TenantID: tenantID})
	if err != nil {
		return previousBaseFee{}, fmt.Errorf("load invoice %s presentment: %w", previous.ID, err)
	}
	rate, err := invoicePresentmentRate(basis)
	if err != nil {
		return previousBaseFee{}, fmt.Errorf("invoice %s: %w", previous.ID, err)
	}
	reducedCents := min(unusedCents, basis.AmountCents)
	amountCents := basis.AmountCents - reducedCents
	presentmentCents, err := fx.FromEUR(amountCents, rate)
	if err != nil {
		return previousBaseFee{}, fmt.Errorf("present reduced invoice %s: %w", previous.ID, err)
	}
	if plan.taxReady && basis.PresentmentAmountCents.Valid {
		presentmentCents = max(basis.PresentmentAmountCents.Int64-plan.creditOriginalMinor, 0)
	}
	covered := basis.PaidOriginalCents >= presentmentCents
	credit.cents = unusedCents - reducedCents
	if covered && basis.PaidEurCents > amountCents {
		credit.cents += basis.PaidEurCents - amountCents
	}
	plan.credit = credit
	plan.reduction = &baseFeeReduction{
		taxReady: plan.taxReady, creditOriginalMinor: plan.creditOriginalMinor, creditCurrency: plan.creditCurrency,
		invoiceID: previous.ID, reducedCents: reducedCents, amountCents: amountCents, presentmentCents: presentmentCents,
		unusedFrom: periodStart, unusedUntil: previous.BaseFeePeriodEnd, covered: covered,
	}
	return plan, nil
}

// reducePreviousBaseFeeInvoiceTx applies a planned reduction: the invoice's
// EUR and presentment amounts drop to the used share, a negative line states
// the unused share, and an invoice its payments now cover is marked paid.
func reducePreviousBaseFeeInvoiceTx(ctx context.Context, tx *sql.Tx, tenantID string, reduction baseFeeReduction, now time.Time) error {
	queries := purserdb.New(tx)
	details, err := json.Marshal(map[string]any{"unused_base_fee_reduction": map[string]any{
		"period_start": reduction.unusedFrom,
		"period_end":   reduction.unusedUntil,
		"amount":       decimal.New(reduction.reducedCents, -2).StringFixed(2),
	}})
	if err != nil {
		return fmt.Errorf("marshal base-fee reduction: %w", err)
	}
	rows, err := queries.ReduceUnpaidBaseFeeInvoice(ctx, purserdb.ReduceUnpaidBaseFeeInvoiceParams{
		Amount:                 decimal.New(reduction.amountCents, -2).StringFixed(2),
		PresentmentAmountCents: reduction.presentmentCents,
		Details:                details,
		InvoiceID:              reduction.invoiceID,
		TenantID:               tenantID,
	})
	if err != nil {
		return fmt.Errorf("reduce base-fee invoice %s: %w", reduction.invoiceID, err)
	}
	if rows != 1 {
		return fmt.Errorf("reduce base-fee invoice %s: invoice is no longer payable", reduction.invoiceID)
	}
	if reduction.taxReady {
		if err := issueUnusedBaseFeeCredit(ctx, queries, tenantID, reduction.invoiceID, reduction.creditOriginalMinor, reduction.creditCurrency, reduction.unusedFrom); err != nil {
			return err
		}
	}
	if reduction.reducedCents > 0 && !reduction.taxReady {
		reducedEUR := decimal.New(-reduction.reducedCents, -2)
		if err := queries.UpsertInvoiceLineItem(ctx, purserdb.UpsertInvoiceLineItemParams{
			InvoiceID: reduction.invoiceID, TenantID: tenantID, LineKey: unusedBaseFeeReductionLineKey,
			Dimensions: []byte("{}"),
			Description: fmt.Sprintf("Unused base subscription %s to %s after a tier change",
				reduction.unusedFrom.Format(time.DateOnly), reduction.unusedUntil.Format(time.DateOnly)),
			Quantity: "1", IncludedQuantity: "0", BillableQuantity: "1",
			UnitPrice: reducedEUR.String(), Amount: reducedEUR.String(), Currency: billing.LedgerCurrency,
			PricingSource: string(pricing.SourceTier),
		}); err != nil {
			return fmt.Errorf("record base-fee reduction line: %w", err)
		}
	}
	if reduction.covered {
		if _, err := SettleCoveredInvoiceTx(ctx, tx, queries, reduction.invoiceID, now); err != nil {
			return err
		}
	}
	return nil
}

func issueUnusedBaseFeeCredit(ctx context.Context, queries *purserdb.Queries, tenant, invoice string, minor int64, currency string, at time.Time) error {
	if minor <= 0 {
		return nil
	}
	return queries.InsertUnusedBaseFeeCreditNote(ctx, purserdb.InsertUnusedBaseFeeCreditNoteParams{TenantID: tenant, InvoiceID: invoice, AmountMinor: minor, Currency: currency, UnusedFrom: at, ReferenceID: invoice + ":" + at.UTC().Format(time.RFC3339Nano)})
}

// baseFeeInvoiceDetails is the usage_details of a base-fee invoice.
func baseFeeInvoiceDetails(tier *billingpkg.EffectiveTier, baseEUR decimal.Decimal, periodStart, periodEnd time.Time, credit baseFeeCredit) ([]byte, error) {
	details := map[string]any{
		"base_fee_period_start": periodStart,
		"base_fee_period_end":   periodEnd,
		"tier_info": map[string]any{
			"tier_id":    tier.TierID,
			"tier_name":  tier.TierName,
			"base_price": baseEUR.String(),
		},
	}
	if credit.cents > 0 || credit.forfeitedCents > 0 {
		details["unused_base_fee_credit"] = map[string]any{
			"invoice_id":          credit.invoiceID,
			"provider_payment_id": credit.providerPaymentID,
			"period_start":        credit.periodStart,
			"period_end":          credit.periodEnd,
			"amount":              decimal.New(credit.cents, -2).StringFixed(2),
			"forfeited_amount":    decimal.New(credit.forfeitedCents, -2).StringFixed(2),
		}
	}
	usageDetails, err := json.Marshal(details)
	if err != nil {
		return nil, fmt.Errorf("marshal base-fee details: %w", err)
	}
	return usageDetails, nil
}

// invoicePresentmentRate is the rate an invoice was presented at. An invoice
// finalized without presentment fields is an EUR invoice presented one to one.
func invoicePresentmentRate(basis purserdb.GetInvoicePaymentFXBasisRow) (fx.Rate, error) {
	if !basis.PresentmentAmountCents.Valid {
		if basis.Currency != billing.LedgerCurrency {
			return fx.Rate{}, fmt.Errorf("a %s invoice has no presentment rate", basis.Currency)
		}
		return fx.Identity(time.Now()), nil
	}
	currency := strings.ToUpper(strings.TrimSpace(basis.PresentmentCurrency))
	if currency == billing.LedgerCurrency {
		if !basis.PresentmentReferenceDate.Valid {
			return fx.Identity(time.Now()), nil
		}
		return fx.Identity(basis.PresentmentReferenceDate.Time), nil
	}
	units, err := decimal.NewFromString(basis.PresentmentUnitsPerEur)
	if err != nil || !basis.PresentmentReferenceDate.Valid {
		return fx.Rate{}, fmt.Errorf("stored presentment rate %q on %v is incomplete", basis.PresentmentUnitsPerEur, basis.PresentmentReferenceDate)
	}
	return fx.Rate{Currency: currency, ReferenceDate: fx.Date(basis.PresentmentReferenceDate.Time), UnitsPerEUR: units, Source: fx.SourceECB}, nil
}

// InvoicePaymentFX is the FX record of a payment of amountMinor in currency
// against an invoice: the share of the EUR the invoice was issued for, at the
// rate it was presented at, not at the rate of the payment date. The payment
// that completes the presentment amount, with the invoice's earlier confirmed
// payments net of reversals, takes the EUR those payments left, so an invoice's
// payments sum to exactly its EUR amount.
func InvoicePaymentFX(ctx context.Context, db purserdb.DBTX, tenantID, invoiceID, currency string, amountMinor int64) (fx.Record, error) {
	basis, err := purserdb.New(db).GetInvoicePaymentFXBasis(ctx, purserdb.GetInvoicePaymentFXBasisParams{InvoiceID: invoiceID, TenantID: tenantID})
	if err != nil {
		return fx.Record{}, fmt.Errorf("load invoice %s presentment: %w", invoiceID, err)
	}
	rate, err := invoicePresentmentRate(basis)
	if err != nil {
		return fx.Record{}, fmt.Errorf("invoice %s: %w", invoiceID, err)
	}
	currency = strings.ToUpper(strings.TrimSpace(currency))
	if currency != rate.Currency {
		return fx.Record{}, fmt.Errorf("invoice %s is presented in %s, not %s", invoiceID, rate.Currency, currency)
	}
	presentmentMinor := basis.AmountCents
	if basis.PresentmentAmountCents.Valid {
		presentmentMinor = basis.PresentmentAmountCents.Int64
	}
	source := fx.SourceECB
	if rate.Currency == fx.EUR {
		source = fx.SourceIdentity
	}
	issued := fx.Record{
		OriginalMinor: presentmentMinor, OriginalCurrency: rate.Currency, EURMinor: basis.AmountCents,
		UnitsPerEUR: rate.UnitsPerEUR, Source: source, ReferenceDate: rate.ReferenceDate,
	}
	return issued.Part(amountMinor, basis.PaidOriginalCents, basis.PaidEurCents)
}

// consumePreviousBaseFee records the replacement that used an invoice's unused
// share. Without this marker, a later prepaid round trip can credit it again.
func consumePreviousBaseFee(ctx context.Context, queries *purserdb.Queries, tenantID string, previous previousBaseFee, applied baseFeeCredit, replacementID string, at time.Time) error {
	if !previous.found || previous.credit.invoiceID == "" {
		return nil
	}
	details, err := json.Marshal(map[string]any{
		"unused_from": at, "replacement_invoice_id": replacementID,
		"credited":  decimal.New(applied.cents, -2).StringFixed(2),
		"forfeited": decimal.New(applied.forfeitedCents, -2).StringFixed(2),
	})
	if err != nil {
		return fmt.Errorf("marshal consumed base fee: %w", err)
	}
	rows, err := queries.MarkBaseFeeUnusedShareReturned(ctx, purserdb.MarkBaseFeeUnusedShareReturnedParams{TenantID: tenantID, InvoiceID: previous.credit.invoiceID, Details: details})
	if err != nil {
		return fmt.Errorf("record consumed base fee: %w", err)
	}
	if rows != 1 {
		return fmt.Errorf("record consumed base fee: invoice %s not found", previous.credit.invoiceID)
	}
	return nil
}

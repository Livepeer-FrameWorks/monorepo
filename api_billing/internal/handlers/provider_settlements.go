package handlers

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"frameworks/api_billing/internal/database/purserdb"
	billingmollie "frameworks/api_billing/internal/mollie"
	billingstripe "frameworks/api_billing/internal/stripe"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/billing"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/VictorAvelar/mollie-api-go/v4/mollie"
	"github.com/shopspring/decimal"
)

const (
	// providerSettlementRetryInterval is how often pending settlements are
	// re-read from the provider, and how long a pending row waits before it is.
	providerSettlementRetryInterval = 10 * time.Minute
	// mollieSettlementCursorLag keeps the Mollie cursor this far behind now, so
	// a transaction whose local row is written shortly after it is read again.
	// A row written later rewinds the cursor to its payment instead
	// (rewindMollieSettlementCursor).
	mollieSettlementCursorLag = time.Hour
	mollieBalancePageSize     = 250
	// mollieBalanceMaxPages bounds one read. A read that ends there without
	// reaching the cursor still moves it forward, so no pending row makes
	// every later read page back to it.
	mollieBalanceMaxPages = 40
)

// errSettlementCurrency means the provider settled a charge into a balance
// that is not in the EUR ledger currency.
var errSettlementCurrency = errors.New("provider settlement is not in the ledger currency")

// runProviderSettlementRetry fills pending provider settlement rows every
// providerSettlementRetryInterval.
func (jm *JobManager) runProviderSettlementRetry(ctx context.Context) {
	ticker := time.NewTicker(providerSettlementRetryInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-jm.stopCh:
			return
		case <-ticker.C:
			if jm.billing == nil {
				continue
			}
			now := time.Now()
			if err := jm.billing.retryStripeSettlements(ctx, now); err != nil {
				jm.logger.WithError(err).Warn("Stripe settlement retry failed")
			}
			if err := jm.billing.readMollieSettlements(ctx, now); err != nil {
				jm.logger.WithError(err).Warn("Mollie balance settlement read failed")
			}
		}
	}
}

// handleStripeChargeUpdated fills a charge's settlement row once Stripe has
// attached the charge's balance transaction.
func (s *Service) handleStripeChargeUpdated(payload StripeWebhookPayload) error {
	var charge StripeChargeObject
	if err := json.Unmarshal(payload.Data.Object, &charge); err != nil {
		return fmt.Errorf("failed to parse charge: %w", err)
	}
	if charge.PaymentIntent == "" || charge.BalanceTxn == "" || s.stripeClient == nil {
		return nil
	}
	ctx := context.Background()
	balanceTransaction, err := s.stripeClient.GetBalanceTransaction(ctx, charge.BalanceTxn)
	if err != nil {
		return err
	}
	_, err = s.settleStripePayment(ctx, charge.PaymentIntent, balanceTransaction)
	return err
}

// retryStripeSettlements reads the balance transaction of every Stripe
// settlement still pending after providerSettlementRetryInterval.
func (s *Service) retryStripeSettlements(ctx context.Context, now time.Time) error {
	if s.stripeClient == nil {
		return nil
	}
	queries := purserdb.New(s.db)
	pending, err := queries.ListPendingProviderSettlements(ctx, purserdb.ListPendingProviderSettlementsParams{
		Provider: "stripe", UpdatedBefore: now.Add(-providerSettlementRetryInterval),
	})
	if err != nil {
		return fmt.Errorf("list pending Stripe settlements: %w", err)
	}
	for _, row := range pending {
		balanceTransaction, getErr := s.stripeClient.PaymentIntentBalanceTransaction(ctx, row.ProviderPaymentID)
		if getErr == nil && balanceTransaction != nil {
			if _, settleErr := s.settleStripePayment(ctx, row.ProviderPaymentID, balanceTransaction); settleErr == nil {
				continue
			} else {
				getErr = settleErr
			}
		}
		if getErr != nil {
			s.logger.WithError(getErr).WithField("payment_intent_id", row.ProviderPaymentID).Warn("Stripe settlement not available yet")
		}
		if touchErr := queries.TouchPendingProviderSettlement(ctx, purserdb.TouchPendingProviderSettlementParams{
			Provider: "stripe", ProviderPaymentID: row.ProviderPaymentID,
		}); touchErr != nil {
			return fmt.Errorf("touch pending Stripe settlement: %w", touchErr)
		}
	}
	return nil
}

// settleStripePayment records a Stripe balance transaction on the pending
// settlement row of a PaymentIntent. It reports whether a row was settled.
func (s *Service) settleStripePayment(ctx context.Context, paymentIntentID string, bt *billingstripe.BalanceTransaction) (bool, error) {
	if bt == nil {
		return false, nil
	}
	queries := purserdb.New(s.db)
	row, err := queries.GetProviderSettlement(ctx, purserdb.GetProviderSettlementParams{Provider: "stripe", ProviderPaymentID: paymentIntentID})
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("load Stripe settlement %s: %w", paymentIntentID, err)
	}
	if row.Status != "pending" {
		return false, nil
	}
	if !strings.EqualFold(bt.Currency, billing.LedgerCurrency) {
		return false, fmt.Errorf("%w: Stripe balance transaction %s is in %s", errSettlementCurrency, bt.ID, bt.Currency)
	}
	exchangeRate := sql.NullString{}
	if !strings.EqualFold(row.ChargeCurrency, billing.LedgerCurrency) {
		if bt.ExchangeRate == nil {
			return false, fmt.Errorf("stripe balance transaction %s converts %s without an exchange rate", bt.ID, row.ChargeCurrency)
		}
		exchangeRate = sql.NullString{String: bt.ExchangeRate.String(), Valid: true}
	}
	return s.settleProviderPayment(ctx, "stripe", paymentIntentID, bt.ID, bt.Amount, bt.Fee, bt.Net, exchangeRate, bt.Created)
}

func (s *Service) settleProviderPayment(ctx context.Context, provider, providerPaymentID, balanceTransactionID string, settled, fee, net int64, exchangeRate sql.NullString, settledAt time.Time) (bool, error) {
	rows, err := purserdb.New(s.db).SettleProviderSettlement(ctx, purserdb.SettleProviderSettlementParams{
		BalanceTransactionID: sql.NullString{String: balanceTransactionID, Valid: true},
		SettledAmountCents:   settled,
		FeeCents:             fee,
		NetCents:             net,
		SettlementCurrency:   billing.LedgerCurrency,
		ExchangeRate:         exchangeRate,
		SettledAt:            settledAt,
		Provider:             provider,
		ProviderPaymentID:    providerPaymentID,
	})
	if err != nil {
		return false, fmt.Errorf("settle %s payment %s: %w", provider, providerPaymentID, err)
	}
	if rows == 1 {
		s.logger.WithFields(logging.Fields{
			"provider":            provider,
			"provider_payment_id": providerPaymentID,
			"balance_transaction": balanceTransactionID,
			"settled_eur_cents":   settled,
			"fee_eur_cents":       fee,
		}).Info("Recorded provider settlement")
	}
	return rows == 1, nil
}

// readMollieSettlements reads the primary Mollie balance's transactions,
// newest first, back to the stored cursor, and settles the pending rows of the
// payments they belong to. The deprecated payment settlementAmount is never
// read: the balance transaction is the settled amount, fee, and net.
func (s *Service) readMollieSettlements(ctx context.Context, now time.Time) error {
	if s.mollieClient == nil {
		return nil
	}
	queries := purserdb.New(s.db)
	pending, err := queries.ListPendingProviderSettlements(ctx, purserdb.ListPendingProviderSettlementsParams{
		Provider: "mollie", UpdatedBefore: now,
	})
	if err != nil {
		return fmt.Errorf("list pending Mollie settlements: %w", err)
	}
	if len(pending) == 0 {
		return nil
	}
	balanceID, err := s.mollieClient.PrimaryBalanceID(ctx)
	if err != nil {
		return err
	}
	// Rewinds must have a row to update, even during the first scan.
	if ensureErr := queries.EnsureMollieBalanceCursor(ctx, balanceID); ensureErr != nil {
		return fmt.Errorf("initialize Mollie balance cursor: %w", ensureErr)
	}
	cursor, err := queries.GetMollieBalanceCursor(ctx, balanceID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("load Mollie balance cursor: %w", err)
	}

	// The cursor only moves to a transaction older than the lag.
	advanceBefore := now.Add(-mollieSettlementCursorLag)
	var nextCursor *billingmollie.BalanceTransaction
	from := ""
	complete := false
	for page := 0; page < mollieBalanceMaxPages; page++ {
		transactions, listErr := s.mollieClient.ListBalanceTransactions(ctx, balanceID, from, mollieBalancePageSize)
		if listErr != nil {
			return listErr
		}
		reachedCursor := false
		for i := range transactions.Transactions {
			tx := transactions.Transactions[i]
			if cursor.LastTransactionID != "" && tx.ID == cursor.LastTransactionID {
				reachedCursor = true
				break
			}
			if cursor.LastTransactionCreatedAt.Valid && tx.CreatedAt.Before(cursor.LastTransactionCreatedAt.Time) {
				reachedCursor = true
				break
			}
			if nextCursor == nil && tx.CreatedAt.Before(advanceBefore) {
				nextCursor = &tx
			}
			if tx.Type() != "payment" || tx.PaymentID() == "" {
				continue
			}
			if _, settleErr := s.settleMolliePayment(ctx, tx); settleErr != nil {
				return fmt.Errorf("settle Mollie balance transaction %s: %w", tx.ID, settleErr)
			}
		}
		if reachedCursor || transactions.NextFrom == "" {
			complete = true
			break
		}
		from = transactions.NextFrom
	}
	if !complete {
		return errors.New("mollie balance scan exceeded its page limit; cursor preserved")
	}
	if nextCursor == nil {
		return nil
	}
	if err := queries.UpsertMollieBalanceCursor(ctx, purserdb.UpsertMollieBalanceCursorParams{
		BalanceID:                balanceID,
		LastTransactionID:        sql.NullString{String: nextCursor.ID, Valid: true},
		LastTransactionCreatedAt: nextCursor.CreatedAt,
		ExpectedID:               cursor.LastTransactionID,
		ExpectedCreatedAt:        cursor.LastTransactionCreatedAt,
	}); err != nil {
		return fmt.Errorf("store Mollie balance cursor: %w", err)
	}
	return nil
}

// rewindMollieSettlementCursor moves the Mollie balance cursor back to a
// payment's creation time while its settlement row is still pending. A
// payment's balance transaction is never older than the payment, so the next
// read reaches it even when its local row was written after the cursor passed
// it.
func (s *Service) rewindMollieSettlementCursor(ctx context.Context, tenantID string, payment *mollie.Payment) error {
	if payment == nil || payment.CreatedAt == nil {
		return nil
	}
	if _, err := purserdb.New(s.db).RewindMollieBalanceCursorForPendingSettlement(ctx, purserdb.RewindMollieBalanceCursorForPendingSettlementParams{
		PaymentCreatedAt: *payment.CreatedAt, TenantID: tenantID, ProviderPaymentID: payment.ID,
	}); err != nil {
		return fmt.Errorf("rewind Mollie balance cursor for payment %s: %w", payment.ID, err)
	}
	return nil
}

// settleMolliePayment records a Mollie payment balance transaction on the
// payment's pending settlement row: the initial amount is the settled EUR, the
// deductions the fee, and the result the net.
func (s *Service) settleMolliePayment(ctx context.Context, tx billingmollie.BalanceTransaction) (bool, error) {
	paymentID := tx.PaymentID()
	row, err := purserdb.New(s.db).GetProviderSettlement(ctx, purserdb.GetProviderSettlementParams{Provider: "mollie", ProviderPaymentID: paymentID})
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("load Mollie settlement %s: %w", paymentID, err)
	}
	if row.Status != "pending" {
		return false, nil
	}
	if tx.InitialAmount == nil || tx.ResultAmount == nil {
		return false, fmt.Errorf("mollie balance transaction %s has no amounts", tx.ID)
	}
	if !strings.EqualFold(tx.InitialAmount.Currency, billing.LedgerCurrency) || !strings.EqualFold(tx.ResultAmount.Currency, billing.LedgerCurrency) {
		return false, fmt.Errorf("%w: Mollie balance transaction %s is in %s", errSettlementCurrency, tx.ID, tx.InitialAmount.Currency)
	}
	settled, _, err := mollieAmountToCents(tx.InitialAmount.Value, billing.LedgerCurrency)
	if err != nil {
		return false, err
	}
	net, _, err := mollieAmountToCents(tx.ResultAmount.Value, billing.LedgerCurrency)
	if err != nil {
		return false, err
	}
	fee := settled - net
	if tx.Deductions != nil && tx.Deductions.Value != "" {
		deductions, _, deductionErr := mollieAmountToCents(tx.Deductions.Value, billing.LedgerCurrency)
		if deductionErr != nil {
			return false, deductionErr
		}
		if -deductions != fee {
			return false, fmt.Errorf("mollie balance transaction %s deductions %d do not match initial %d minus result %d", tx.ID, deductions, settled, net)
		}
	}
	exchangeRate := sql.NullString{}
	if !strings.EqualFold(row.ChargeCurrency, billing.LedgerCurrency) {
		if row.ChargeAmountCents <= 0 {
			return false, fmt.Errorf("mollie settlement %s has no charge amount", paymentID)
		}
		rate := decimal.NewFromInt(settled).DivRound(decimal.NewFromInt(row.ChargeAmountCents), 10)
		exchangeRate = sql.NullString{String: rate.String(), Valid: true}
	}
	return s.settleProviderPayment(ctx, "mollie", paymentID, tx.ID, settled, fee, net, exchangeRate, tx.CreatedAt)
}

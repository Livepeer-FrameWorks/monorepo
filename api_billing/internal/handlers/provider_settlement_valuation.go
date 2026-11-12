package handlers

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"frameworks/api_billing/internal/database/purserdb"
	"frameworks/api_billing/internal/fx"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/database"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/shopspring/decimal"
)

var errProviderSettlementPersistence = errors.New("provider settlement persistence failed")

var errProviderSettlementEvidenceConflict = errors.New("provider settlement evidence conflicts with its recorded transaction")

type recordedSettlementError struct{ cause error }

func (e *recordedSettlementError) Error() string {
	return "native settlement recorded: " + e.cause.Error()
}
func (e *recordedSettlementError) Unwrap() error { return e.cause }

func nativeSettlementRecorded(err error) bool {
	var recorded *recordedSettlementError
	return errors.As(err, &recorded)
}

// settleProviderPayment commits native evidence before attempting valuation,
// so missing reference rates cannot lose provider facts or pin its scan cursor.
func (s *Service) settleProviderPayment(ctx context.Context, tenant, provider, paymentID, transactionID, currency string, gross, fee, net int64, providerRate sql.NullString, at time.Time) (bool, error) {
	q := purserdb.New(s.db)
	at = at.UTC().Truncate(time.Microsecond)
	if transactionID == "" || at.IsZero() || fee < 0 || net != gross-fee {
		return false, errors.New("provider settlement has invalid transaction identity, time or native amounts")
	}
	_, err := q.RecordNativeProviderSettlement(ctx, purserdb.RecordNativeProviderSettlementParams{
		TenantID: tenant, Provider: provider, ProviderPaymentID: paymentID,
		BalanceTransactionID: transactionID, SettlementCurrency: currency,
		SettledAmountCents: gross, FeeCents: fee, NetCents: net, ExchangeRate: providerRate, SettledAt: at,
	})
	if err != nil {
		return false, fmt.Errorf("record %s native settlement: %w", provider, err)
	}
	row, err := q.GetProviderSettlement(ctx, purserdb.GetProviderSettlementParams{Provider: provider, ProviderPaymentID: paymentID})
	if err != nil {
		return false, fmt.Errorf("read recorded %s settlement: %w", provider, err)
	}
	if row.TenantID != tenant || row.ProviderBalanceTransactionID != transactionID || row.SettlementCurrency != currency ||
		!row.SettledAmountCents.Valid || row.SettledAmountCents.Int64 != gross ||
		!row.FeeCents.Valid || row.FeeCents.Int64 != fee || !row.NetCents.Valid || row.NetCents.Int64 != net ||
		!row.SettledAt.Valid || !row.SettledAt.Time.Equal(at) || !equalProviderRate(row.ExchangeRate, providerRate) {
		return false, errProviderSettlementEvidenceConflict
	}
	return s.valueRecordedProviderSettlement(ctx, provider, paymentID, row)
}

func equalProviderRate(recorded string, rate sql.NullString) bool {
	if !rate.Valid {
		return recorded == ""
	}
	left, err := decimal.NewFromString(recorded)
	if err != nil {
		return false
	}
	right, err := decimal.NewFromString(rate.String)
	return err == nil && left.Equal(right)
}

func (s *Service) valueRecordedProviderSettlement(ctx context.Context, provider, paymentID string, row purserdb.GetProviderSettlementRow) (bool, error) {
	if row.Status == "settled" {
		return false, nil
	}
	q := purserdb.New(s.db)
	rate, err := fx.Lookup(ctx, s.db, row.SettlementCurrency, row.SettledAt.Time)
	if err != nil {
		code, message := "fx_lookup_failed", "EUR reference valuation is temporarily unavailable"
		blocked := errors.Is(err, fx.ErrUnsupportedCurrency)
		if blocked {
			code, message = "unsupported_currency", "native settlement currency has no supported EUR valuation"
		} else if errors.Is(err, fx.ErrNoRate) || errors.Is(err, fx.ErrStaleRate) {
			code, message = "fx_unavailable", "no sufficiently recent EUR reference rate for the provider transaction date"
			err = fmt.Errorf("%w: %w", fx.ErrUnavailable, err)
		}
		persistErr := q.RecordProviderValuationFailure(ctx, purserdb.RecordProviderValuationFailureParams{
			TenantID: row.TenantID, Provider: provider, ProviderPaymentID: paymentID,
			Blocked: blocked, ErrorCode: code, ErrorMessage: message,
		})
		if persistErr != nil {
			return false, &recordedSettlementError{cause: errors.Join(errProviderSettlementPersistence, err, persistErr)}
		}
		return false, &recordedSettlementError{cause: err}
	}
	gross, err := fx.RecordToEUR(row.SettledAmountCents.Int64, rate)
	if err != nil {
		return false, &recordedSettlementError{cause: err}
	}
	fee, err := fx.RecordToEUR(row.FeeCents.Int64, rate)
	if err != nil {
		return false, &recordedSettlementError{cause: err}
	}
	var changed int64
	err = database.WithRetryablePostgresTx(ctx, s.db, nil, func(tx *sql.Tx) error {
		queries := purserdb.New(tx)
		var writeErr error
		changed, writeErr = queries.SettleProviderSettlement(ctx, purserdb.SettleProviderSettlementParams{
			TenantID: row.TenantID, Provider: provider, ProviderPaymentID: paymentID,
			SettledEurCents: gross.EURMinor, FeeEurCents: fee.EURMinor,
			ValuationUnitsPerEur: rate.UnitsPerEUR.String(), ValuationReferenceDate: rate.ReferenceDate,
			ValuationSource: gross.Source,
		})
		if writeErr != nil {
			return writeErr
		}
		return queries.WakeValuedProviderWebhooks(ctx, purserdb.WakeValuedProviderWebhooksParams{TenantID: row.TenantID, Provider: provider, ProviderPaymentID: paymentID})
	})
	if err != nil {
		return false, &recordedSettlementError{cause: fmt.Errorf("value %s native settlement: %w", provider, err)}
	}
	if changed == 1 {
		s.logger.WithFields(logging.Fields{
			"tenant_id": row.TenantID, "provider": provider, "provider_payment_id": paymentID,
			"balance_transaction": row.ProviderBalanceTransactionID, "settlement_currency": row.SettlementCurrency,
			"native_gross_minor": row.SettledAmountCents.Int64, "native_fee_minor": row.FeeCents.Int64, "native_net_minor": row.NetCents.Int64,
			"settled_eur_cents": gross.EURMinor, "fee_eur_cents": fee.EURMinor, "net_eur_cents": gross.EURMinor - fee.EURMinor,
			"valuation_source": gross.Source, "valuation_reference_date": rate.ReferenceDate.Format(time.DateOnly),
		}).Info("Recorded provider settlement")
	}
	return changed == 1, nil
}

func (s *Service) retryRecordedProviderSettlements(ctx context.Context, now time.Time) error {
	q := purserdb.New(s.db)
	rows, err := q.ListRecordedProviderSettlements(ctx, purserdb.ListRecordedProviderSettlementsParams{
		SupportedCurrencies: []string{fx.EUR, fx.USD, fx.GBP}, UpdatedBefore: now.Add(-providerSettlementRetryInterval),
	})
	if err != nil {
		return fmt.Errorf("list recorded provider settlements: %w", err)
	}
	var failures error
	for _, candidate := range rows {
		row, readErr := q.GetProviderSettlement(ctx, purserdb.GetProviderSettlementParams{Provider: candidate.Provider, ProviderPaymentID: candidate.ProviderPaymentID})
		if readErr != nil {
			failures = errors.Join(failures, readErr)
			continue
		}
		if row.TenantID != candidate.TenantID || row.Status == "pending" {
			failures = errors.Join(failures, errors.New("recorded provider settlement ownership or state changed"))
			continue
		}
		if _, err := s.valueRecordedProviderSettlement(ctx, candidate.Provider, candidate.ProviderPaymentID, row); err != nil {
			failures = errors.Join(failures, err)
		}
	}
	return failures
}

func settlementOutcome(err error) string {
	switch {
	case err == nil:
		return "settled"
	case errors.Is(err, errProviderSettlementPersistence):
		return "transient"
	case errors.Is(err, fx.ErrUnsupportedCurrency), errors.Is(err, errProviderSettlementEvidenceConflict):
		return "blocked"
	case errors.Is(err, fx.ErrUnavailable), errors.Is(err, ErrAwaitingProviderSettlement):
		return "waiting"
	default:
		return "transient"
	}
}

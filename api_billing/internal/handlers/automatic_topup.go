package handlers

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"frameworks/api_billing/internal/database/purserdb"
	"frameworks/api_billing/internal/fx"
	billingstripe "frameworks/api_billing/internal/stripe"
	"frameworks/api_billing/internal/subscriptions"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/billing"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/database"
	"github.com/google/uuid"
	"strings"
	"time"
)

const AutomaticTopupConsentVersion = "prepaid_auto_topup_v1"

type AutomaticTopupPolicy struct {
	Enabled                                              bool
	ThresholdEUR, NetAmountMinor, MonthlyGrossLimitMinor int64
	Currency, ConsentVersion, Actor                      string
}
type AutomaticTopupState struct {
	AutomaticTopupPolicy
	PausedReason      string
	MonthlySpentMinor int64
	Available         bool
	UnavailableReason string
}
type automaticTopupContext struct {
	Profile      json.RawMessage `json:"profile"`
	CustomerID   string          `json:"customer_id"`
	Currency     string          `json:"currency"`
	Eligible     bool            `json:"eligible"`
	BalanceCents int64           `json:"balance_cents"`
}

func readAutomaticTopupContext(ctx context.Context, q *purserdb.Queries, tenant string) (automaticTopupContext, error) {
	var out automaticTopupContext
	raw, err := q.GetAutomaticTopupContext(ctx, tenant)
	if err != nil {
		return out, err
	}
	err = json.Unmarshal(raw, &out)
	return out, err
}
func (s *Service) GetAutomaticTopup(ctx context.Context, tenant string) (AutomaticTopupState, error) {
	var out AutomaticTopupState
	q := purserdb.New(s.db)
	source, err := readAutomaticTopupContext(ctx, q, tenant)
	if err != nil {
		return out, err
	}
	out.Currency = source.Currency
	out.Available = s.stripeClient != nil && source.Eligible && source.CustomerID != ""
	if !out.Available {
		out.UnavailableReason = "Automatic top-ups require prepaid PAYG, settled Stripe funding, complete billing details and a saved Stripe card. Monthly plans and explicit Free choices are preserved."
	}
	row, err := q.GetAutomaticTopupSettings(ctx, tenant)
	if errors.Is(err, sql.ErrNoRows) {
		return out, nil
	}
	if err != nil {
		return out, err
	}
	out.AutomaticTopupPolicy = AutomaticTopupPolicy{Enabled: row.Enabled, ThresholdEUR: row.ThresholdEurCents, NetAmountMinor: row.NetAmountMinor, MonthlyGrossLimitMinor: row.MonthlyGrossLimitMinor, Currency: row.Currency, ConsentVersion: row.ConsentVersion, Actor: row.ConsentActor}
	out.PausedReason = row.PausedReason
	spent, err := q.GetAutomaticTopupMonthlySpend(ctx, purserdb.GetAutomaticTopupMonthlySpendParams{TenantID: tenant, Currency: row.Currency})
	if err != nil {
		return out, err
	}
	out.MonthlySpentMinor = spent.AmountMinor
	return out, nil
}
func (s *Service) DisableAutomaticTopup(ctx context.Context, tenant, actor string) error {
	return purserdb.New(s.db).DisableAutomaticTopup(ctx, purserdb.DisableAutomaticTopupParams{TenantID: tenant, Actor: actor})
}
func (s *Service) SetAutomaticTopup(ctx context.Context, tenant string, policy AutomaticTopupPolicy) (AutomaticTopupState, error) {
	if !policy.Enabled {
		if err := s.DisableAutomaticTopup(ctx, tenant, policy.Actor); err != nil {
			return AutomaticTopupState{}, err
		}
		return s.GetAutomaticTopup(ctx, tenant)
	}
	if policy.ConsentVersion != AutomaticTopupConsentVersion || strings.TrimSpace(policy.Actor) == "" {
		return AutomaticTopupState{}, fmt.Errorf("explicit automatic top-up consent is required")
	}
	minimum, err := billing.FiatTopupMinimumCents("stripe", policy.Currency)
	if err != nil {
		return AutomaticTopupState{}, err
	}
	if policy.ThresholdEUR < 0 || policy.ThresholdEUR > 100000 || policy.NetAmountMinor < minimum || policy.NetAmountMinor > billing.MaximumTopupCents || policy.MonthlyGrossLimitMinor < policy.NetAmountMinor || policy.MonthlyGrossLimitMinor > billing.MaximumTopupCents {
		return AutomaticTopupState{}, fmt.Errorf("invalid threshold, top-up amount or monthly spending limit")
	}
	if s.stripeClient == nil {
		return AutomaticTopupState{}, fmt.Errorf("stripe automatic top-ups unavailable")
	}
	q := purserdb.New(s.db)
	source, err := readAutomaticTopupContext(ctx, q, tenant)
	if err != nil {
		return AutomaticTopupState{}, err
	}
	if !source.Eligible || source.Currency != policy.Currency || source.CustomerID == "" {
		return AutomaticTopupState{}, fmt.Errorf("automatic top-up eligibility or billing currency changed")
	}
	method, err := s.stripeClient.ResolveDefaultPaymentMethod(ctx, source.CustomerID, "")
	if err != nil {
		return AutomaticTopupState{}, err
	}
	if method == "" {
		return AutomaticTopupState{}, fmt.Errorf("save a default Stripe payment card in Billing before enabling automatic top-ups")
	}
	net, err := fx.QuoteToEUR(ctx, s.db, policy.Currency, policy.NetAmountMinor, time.Now().UTC())
	if err != nil {
		return AutomaticTopupState{}, err
	}
	_, cash, _, err := QuoteFiatPrepaidAdvance(ctx, s.db, tenant, net)
	if err != nil {
		return AutomaticTopupState{}, err
	}
	if cash.OriginalMinor > policy.MonthlyGrossLimitMinor {
		return AutomaticTopupState{}, fmt.Errorf("monthly cash limit must cover one top-up including tax")
	}
	err = database.WithRetryablePostgresTx(ctx, s.db, nil, func(tx *sql.Tx) error {
		if _, lockErr := subscriptions.LockChange(ctx, tx, tenant, subscriptions.PreservePricing); lockErr != nil {
			return lockErr
		}
		q := purserdb.New(tx)
		current, readErr := readAutomaticTopupContext(ctx, q, tenant)
		if readErr != nil {
			return readErr
		}
		if !current.Eligible || !bytes.Equal(current.Profile, source.Profile) {
			return fmt.Errorf("billing details changed while recording consent; review again")
		}
		held, holdErr := q.HasAutomaticTopupReconciliationHold(ctx, tenant)
		if holdErr != nil {
			return holdErr
		}
		if held {
			return fmt.Errorf("an earlier automatic payment needs operator reconciliation before automation can resume")
		}
		spent, spendErr := q.GetAutomaticTopupMonthlySpend(ctx, purserdb.GetAutomaticTopupMonthlySpendParams{TenantID: tenant, Currency: policy.Currency})
		if spendErr != nil {
			return spendErr
		}
		if spent.DifferentCurrency {
			return fmt.Errorf("automatic top-up currency can change after the current UTC budget month ends")
		}
		return q.SetAutomaticTopupSettings(ctx, purserdb.SetAutomaticTopupSettingsParams{TenantID: tenant, ThresholdEurCents: policy.ThresholdEUR, NetAmountMinor: policy.NetAmountMinor, MonthlyGrossLimitMinor: policy.MonthlyGrossLimitMinor, Currency: policy.Currency, ConsentVersion: policy.ConsentVersion, ConsentActor: policy.Actor, Revision: uuid.NewString(), CustomerID: source.CustomerID, PaymentMethodID: method, Profile: source.Profile})
	})
	if err != nil {
		return AutomaticTopupState{}, err
	}
	return s.GetAutomaticTopup(ctx, tenant)
}

func (jm *JobManager) runAutomaticTopups(ctx context.Context) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		jm.automaticTopupTick(ctx)
		select {
		case <-ctx.Done():
			return
		case <-jm.stopCh:
			return
		case <-ticker.C:
		}
	}
}
func (jm *JobManager) automaticTopupTick(ctx context.Context) {
	if jm.billing == nil || jm.billing.stripeClient == nil {
		return
	}
	tenants, err := purserdb.New(jm.db).ListAutomaticTopupCandidates(ctx)
	if err != nil {
		jm.logger.WithError(err).Warn("Automatic top-up discovery unavailable")
		return
	}
	for _, tenant := range tenants {
		if ctx.Err() != nil {
			return
		}
		if err := jm.billing.reserveAutomaticTopup(ctx, tenant); err != nil {
			jm.logger.WithError(err).WithField("tenant_id", tenant).Warn("Automatic top-up not reserved")
		}
	}
}

func (s *Service) reserveAutomaticTopup(ctx context.Context, tenant string) error {
	q := purserdb.New(s.db)
	settings, err := q.GetAutomaticTopupSettings(ctx, tenant)
	if err != nil {
		return err
	}
	net, err := fx.QuoteToEUR(ctx, s.db, settings.Currency, settings.NetAmountMinor, time.Now().UTC())
	if err != nil {
		return err
	}
	advance, cash, tax, err := QuoteFiatPrepaidAdvance(ctx, s.db, tenant, net)
	if err != nil {
		return q.PauseAutomaticTopup(ctx, purserdb.PauseAutomaticTopupParams{TenantID: tenant, Revision: settings.Revision, Reason: "Billing details or tax evidence need review before another automatic charge."})
	}
	attempt, topup := uuid.NewString(), uuid.NewString()
	return database.WithRetryablePostgresTx(ctx, s.db, nil, func(tx *sql.Tx) error {
		if _, err := subscriptions.LockChange(ctx, tx, tenant, subscriptions.PreservePricing); err != nil {
			return err
		}
		q := purserdb.New(tx)
		current, err := q.LockAutomaticTopupSettings(ctx, tenant)
		if err != nil {
			return err
		}
		if !current.Enabled || current.PausedReason != "" || current.Revision != settings.Revision {
			return nil
		}
		source, err := readAutomaticTopupContext(ctx, q, tenant)
		if err != nil {
			return err
		}
		if source.BalanceCents > current.ThresholdEurCents {
			return nil
		}
		if !source.Eligible || !bytes.Equal(source.Profile, current.ProfileSnapshot) {
			return q.PauseAutomaticTopup(ctx, purserdb.PauseAutomaticTopupParams{TenantID: tenant, Revision: current.Revision, Reason: "Billing arrangement, country, currency or saved payment details changed. Review automatic top-ups again."})
		}
		spent, err := q.GetAutomaticTopupMonthlySpend(ctx, purserdb.GetAutomaticTopupMonthlySpendParams{TenantID: tenant, Currency: current.Currency})
		if err != nil {
			return err
		}
		if spent.DifferentCurrency || cash.OriginalMinor > current.MonthlyGrossLimitMinor-spent.AmountMinor || cash.OriginalMinor > billing.MaximumTopupCents {
			return q.TouchAutomaticTopup(ctx, tenant)
		}
		expires := time.Now().UTC().Add(24 * time.Hour)
		intent, err := q.UpsertPrepaidTopupProviderIntent(ctx, purserdb.UpsertPrepaidTopupProviderIntentParams{TenantID: tenant, Provider: "stripe", TopupID: topup, Currency: cash.OriginalCurrency, AmountCents: cash.OriginalMinor, IdempotencyKey: "automatic-prepaid-topup:" + attempt, ExpiresAt: sql.NullTime{Time: expires, Valid: true}})
		if err != nil {
			return err
		}
		if err = q.InsertPendingCardTopup(ctx, purserdb.InsertPendingCardTopupParams{TopupID: topup, TenantID: tenant, Provider: "stripe", AmountCents: cash.OriginalMinor, Currency: cash.OriginalCurrency, ExpiresAt: expires, IntentID: intent, EurAmountCents: cash.EURMinor, FxUnitsPerEur: cash.UnitsText(), FxSource: cash.Source, FxReferenceDate: cash.ReferenceDate}); err != nil {
			return err
		}
		frozen, err := q.FreezePendingTopupTax(ctx, purserdb.FreezePendingTopupTaxParams{TenantID: tenant, TopupID: topup, NetEurCents: sql.NullInt64{Int64: advance.NetEUR, Valid: true}, VatEurCents: sql.NullInt64{Int64: advance.VATEUR, Valid: true}, GrossEurCents: sql.NullInt64{Int64: advance.GrossEUR, Valid: true}, TaxAssessment: tax})
		if err != nil {
			return err
		}
		if frozen != 1 {
			return fmt.Errorf("automatic top-up tax quote not retained")
		}
		consent, err := json.Marshal(current)
		if err != nil {
			return err
		}
		if err = q.InsertAutomaticTopupAttempt(ctx, purserdb.InsertAutomaticTopupAttemptParams{AttemptID: attempt, TenantID: tenant, Revision: current.Revision, TopupID: topup, GrossAmountMinor: cash.OriginalMinor, Currency: cash.OriginalCurrency, CustomerID: current.StripeCustomerID, PaymentMethodID: current.StripePaymentMethodID, Profile: current.ProfileSnapshot, Consent: consent}); err != nil {
			return err
		}
		payload, marshalErr := json.Marshal(struct{ TenantID, AttemptID string }{tenant, attempt})
		if marshalErr != nil {
			return marshalErr
		}
		if err = q.EnqueueAutomaticTopup(ctx, purserdb.EnqueueAutomaticTopupParams{TenantID: tenant, AttemptID: attempt, Payload: payload}); err != nil {
			return err
		}
		return q.TouchAutomaticTopup(ctx, tenant)
	})
}

func (s *Service) processAutomaticTopup(ctx context.Context, tenant, attempt string) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var row purserdb.GetAutomaticTopupAttemptRow
	skip := false
	err := database.WithRetryablePostgresTx(ctx, s.db, nil, func(tx *sql.Tx) error {
		if _, err := subscriptions.LockChange(ctx, tx, tenant, subscriptions.PreservePricing); err != nil {
			return err
		}
		q := purserdb.New(tx)
		settings, err := q.LockAutomaticTopupSettings(ctx, tenant)
		if err != nil {
			return err
		}
		row, err = q.GetAutomaticTopupAttempt(ctx, purserdb.GetAutomaticTopupAttemptParams{TenantID: tenant, AttemptID: attempt})
		if err != nil {
			return err
		}
		if row.Status == "settled" || row.Status == "failed" || row.Status == "cancelled" || row.Status == "needs_review" {
			skip = true
			return nil
		}
		if row.Status == "reserved" {
			source, err := readAutomaticTopupContext(ctx, q, tenant)
			if err != nil {
				return err
			}
			if !settings.Enabled || settings.Revision != row.SettingsRevision || !source.Eligible || !bytes.Equal(source.Profile, row.ProfileSnapshot) {
				skip = true
				if err := q.CancelAutomaticTopupPendingFunding(ctx, purserdb.CancelAutomaticTopupPendingFundingParams{TenantID: tenant, TopupID: row.TopupID}); err != nil {
					return err
				}
				return q.UpdateAutomaticTopupAttempt(ctx, purserdb.UpdateAutomaticTopupAttemptParams{TenantID: tenant, AttemptID: attempt, Status: "cancelled"})
			}
			return q.UpdateAutomaticTopupAttempt(ctx, purserdb.UpdateAutomaticTopupAttemptParams{TenantID: tenant, AttemptID: attempt, Status: "submitted"})
		}
		return nil
	})
	if err != nil || skip {
		return err
	}
	if s.stripeClient == nil {
		return fmt.Errorf("stripe automatic top-up unavailable")
	}
	result, err := s.stripeClient.ChargeAutomaticTopup(ctx, billingstripe.AutomaticTopupCharge{TenantID: tenant, TopupID: row.TopupID, AttemptID: attempt, CustomerID: row.CustomerID, PaymentMethodID: row.PaymentMethodID, Currency: row.Currency, PaymentIntentID: row.ProviderPaymentID, AmountMinor: row.GrossAmountMinor, CreatedAt: row.CreatedAt})
	if errors.Is(err, billingstripe.ErrAutomaticTopupNeedsReview) {
		return s.pauseAutomaticTopupAttempt(ctx, row, "needs_review", "Automatic payment outcome needs operator reconciliation. No further automatic charges will start.", "")
	}
	if err != nil {
		return err
	}
	if result.Status == "failed" {
		return s.pauseAutomaticTopupAttempt(ctx, row, "failed", "Automatic payment failed or required authentication. Add funds manually, then review automatic top-ups to resume.", result.PaymentIntentID)
	}
	if err = purserdb.New(s.db).UpdateAutomaticTopupAttempt(ctx, purserdb.UpdateAutomaticTopupAttemptParams{TenantID: tenant, AttemptID: attempt, Status: "submitted", ProviderPaymentID: result.PaymentIntentID}); err != nil {
		return err
	}
	if result.Status != "succeeded" {
		return ErrAwaitingProviderSettlement
	}
	if result.AmountReceived != row.GrossAmountMinor {
		return s.pauseAutomaticTopupAttempt(ctx, row, "needs_review", "Automatic payment amount differs from its reservation.", result.PaymentIntentID)
	}
	return s.settleAutomaticTopup(ctx, automaticTopupReceipt{TenantID: tenant, TopupID: row.TopupID, PaymentID: result.PaymentIntentID, AttemptID: row.ID, CustomerID: row.CustomerID, PaymentMethodID: row.PaymentMethodID, AmountMinor: row.GrossAmountMinor, Currency: row.Currency})
}
func (s *Service) pauseAutomaticTopupAttempt(ctx context.Context, row purserdb.GetAutomaticTopupAttemptRow, state, reason, payment string) error {
	return database.WithRetryablePostgresTx(ctx, s.db, nil, func(tx *sql.Tx) error {
		q := purserdb.New(tx)
		if _, err := subscriptions.LockChange(ctx, tx, row.TenantID, subscriptions.PreservePricing); err != nil {
			return err
		}
		if err := q.PauseAutomaticTopup(ctx, purserdb.PauseAutomaticTopupParams{TenantID: row.TenantID, Revision: row.SettingsRevision, Reason: reason}); err != nil {
			return err
		}
		if state == "failed" {
			if err := q.CancelAutomaticTopupPendingFunding(ctx, purserdb.CancelAutomaticTopupPendingFundingParams{TenantID: row.TenantID, TopupID: row.TopupID}); err != nil {
				return err
			}
		}
		return q.UpdateAutomaticTopupAttempt(ctx, purserdb.UpdateAutomaticTopupAttemptParams{TenantID: row.TenantID, AttemptID: row.ID, Status: state, ProviderPaymentID: payment})
	})
}

type automaticTopupReceipt struct {
	TenantID, TopupID, PaymentID, AttemptID, CustomerID, PaymentMethodID, Currency string
	AmountMinor                                                                    int64
}

func (s *Service) settleAutomaticTopup(ctx context.Context, in automaticTopupReceipt) error {
	q := purserdb.New(s.db)
	row, err := q.FindAutomaticTopupForReceipt(ctx, purserdb.FindAutomaticTopupForReceiptParams{TenantID: in.TenantID, TopupID: in.TopupID})
	if err != nil {
		return err
	}
	if row.ID != in.AttemptID || row.CustomerID != in.CustomerID || row.PaymentMethodID != in.PaymentMethodID || row.GrossAmountMinor != in.AmountMinor || !strings.EqualFold(row.Currency, in.Currency) || row.ProviderPaymentID != "" && row.ProviderPaymentID != in.PaymentID {
		return fmt.Errorf("automatic payment differs from its reserved payer, identity or amount")
	}
	if row.Status != "submitted" && row.Status != "settled" && row.Status != "needs_review" {
		return fmt.Errorf("automatic payment captured after its reservation closed; operator reconciliation required")
	}
	if err = s.handlePrepaidCheckoutCompleted(ctx, in.PaymentID, in.PaymentID, in.TenantID, in.TopupID, in.AmountMinor, in.Currency, ProviderStripe, true); err != nil {
		return err
	}
	return q.UpdateAutomaticTopupAttempt(ctx, purserdb.UpdateAutomaticTopupAttemptParams{TenantID: in.TenantID, AttemptID: row.ID, Status: "settled", ProviderPaymentID: in.PaymentID})
}

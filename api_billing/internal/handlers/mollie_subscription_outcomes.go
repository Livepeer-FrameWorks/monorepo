package handlers

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"frameworks/api_billing/internal/billingevents"
	"frameworks/api_billing/internal/database/purserdb"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/database"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/events"
	publicv1 "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/events/public/v1"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	mollieapi "github.com/VictorAvelar/mollie-api-go/v4/mollie"
)

// A native subscription's base-fee tax invoice is separate from its usage
// invoice. Its payment outcome still updates collection health and events.
func (s *Service) applyNativeMollieSubscriptionOutcome(ctx context.Context, tenant string, payment *mollieapi.Payment, status string) error {
	owner, err := purserdb.New(s.db).GetMollieCustomerID(ctx, tenant)
	if err != nil {
		return err
	}
	if owner != payment.CustomerID {
		return fmt.Errorf("native Mollie payment customer owner mismatch")
	}
	if status == "confirmed" {
		if err = s.issueMollieNativeTaxInvoice(ctx, tenant, payment); err != nil {
			return err
		}
	}
	subscription, err := s.mollieClient.GetSubscription(ctx, payment.CustomerID, payment.SubscriptionID)
	if err != nil {
		return fmt.Errorf("read native Mollie subscription outcome: %w", err)
	}
	var amount int64
	if status == "confirmed" || status == "failed" {
		if payment.Amount == nil {
			return fmt.Errorf("native Mollie payment has no amount")
		}
		var currency string
		amount, currency, err = mollieAmountToCents(payment.Amount.Value, payment.Amount.Currency)
		if err != nil {
			return err
		}
		if currency != "EUR" || amount <= 0 {
			return fmt.Errorf("native Mollie subscription outcome requires positive EUR amount")
		}
	}
	return database.WithRetryablePostgresTx(ctx, s.db, nil, func(tx *sql.Tx) error {
		q := purserdb.New(tx)
		if subscription.NextPaymentDate != nil {
			if err := q.UpdateMollieNextPaymentDate(ctx, purserdb.UpdateMollieNextPaymentDateParams{TenantID: tenant, NextPaymentDate: subscription.NextPaymentDate.Format(time.DateOnly), MollieSubscriptionID: sql.NullString{String: payment.SubscriptionID, Valid: true}}); err != nil {
				return err
			}
		}
		if status != "confirmed" && status != "failed" {
			return nil
		}
		// Refund observations have distinct webhook keys; the stable outcome
		// key keeps them from resetting or incrementing dunning a second time.
		rows, err := q.ClaimNativeMollieSubscriptionOutcome(ctx, purserdb.ClaimNativeMollieSubscriptionOutcomeParams{TenantID: tenant, OutcomeID: "subscription_outcome:" + tenant + ":" + payment.ID + ":" + status})
		if err != nil || rows == 0 {
			return err
		}
		if status == "failed" {
			metadata, marshalErr := json.Marshal(map[string]any{"status": "failed", "amount_minor": amount, "currency": "EUR", "subscription_id": payment.SubscriptionID})
			if marshalErr != nil {
				return marshalErr
			}
			if err = q.UpsertProviderPaymentObject(ctx, purserdb.UpsertProviderPaymentObjectParams{Provider: "mollie", ObjectType: "payment", ProviderObjectID: payment.ID, TenantID: sql.NullString{String: tenant, Valid: true}, Metadata: metadata}); err != nil {
				return err
			}
			if err = q.IncrementTenantDunningAttempts(ctx, tenant); err != nil {
				return err
			}
			failed, eventErr := billingevents.New(tenant, payment.ID, &publicv1.PaymentFailed{Amount: billingevents.EUR(amount), Provider: "mollie", ProviderReferenceId: payment.ID}, events.Actor{})
			if eventErr != nil {
				return eventErr
			}
			return emitBillingEventsTx(ctx, tx, eventPaymentFailed, tenant, "payment", payment.ID, &ipcpb.BillingEvent{PaymentId: payment.ID, Amount: float64(amount) / 100, Currency: "EUR", Provider: "mollie", Status: "failed"}, failed)
		}
		invoice, err := q.GetNativeMollieInvoiceID(ctx, purserdb.GetNativeMollieInvoiceIDParams{TenantID: tenant, PaymentID: sql.NullString{String: payment.ID, Valid: true}})
		if err != nil {
			return err
		}
		if err = q.ResetTenantDunningAttempts(ctx, tenant); err != nil {
			return err
		}
		if err = molliePaymentEventWriter(payment.ID, status, providerSettlementEvidence{TenantID: tenant, AmountCents: amount, Currency: "EUR"})(ctx, tx, resolvedInvoicePayment{TenantID: tenant, InvoiceID: invoice}); err != nil {
			return err
		}
		return enqueueInvoicePaidTx(ctx, tx, invoice, &legacyBillingEvent{eventType: eventInvoicePaid, resourceType: "invoice", resourceID: invoice, payload: &ipcpb.BillingEvent{InvoiceId: invoice, Amount: float64(amount) / 100, Currency: "EUR", Provider: "mollie", Status: "paid"}})
	})
}

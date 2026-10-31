package handlers

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"frameworks/api_billing/internal/database/purserdb"
	"frameworks/api_billing/internal/subscriptions"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/billing"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/database"
	"github.com/google/uuid"
)

func enqueueRefundSettlements(ctx context.Context, q *purserdb.Queries, tenantID string, plans []billing.RefundSettlement) error {
	for _, plan := range plans {
		plan.TenantID = tenantID
		plan.Currency = strings.ToUpper(plan.Currency)
		if plan.ReferenceID == "" || plan.PaymentID == "" || plan.AmountCents <= 0 || len(plan.Currency) != 3 {
			return errors.New("refund settlement has incomplete purchase evidence")
		}
		body, err := json.Marshal(plan)
		if err != nil {
			return err
		}
		key := "refund-settlement:" + plan.Provider + ":" + plan.ReferenceKind + ":" + plan.ReferenceID + ":" + plan.PaymentID
		if err = q.EnqueueRefundSettlement(ctx, purserdb.EnqueueRefundSettlementParams{EventKey: key, TenantID: tenantID, Provider: plan.Provider, PaymentID: plan.PaymentID, ReferenceID: plan.ReferenceID, ReferenceKind: plan.ReferenceKind, InvoiceID: plan.InvoiceID, Currency: plan.Currency, AmountCents: plan.AmountCents, Payload: body}); err != nil {
			return err
		}
		if plan.Provider == "stripe" && plan.InvoiceID != "" && plan.ReferenceKind == "checkout" {
			if err = q.ApplyCompensatedStripeInvoiceCredit(ctx, purserdb.ApplyCompensatedStripeInvoiceCreditParams{TenantID: tenantID, InvoiceID: plan.InvoiceID, Currency: plan.Currency}); err != nil {
				return err
			}
		}
	}
	return nil
}

// settlePurchaseRefund leaves pending bank returns in the durable inbox. Each
// confirmed receipt is stored under the purchase lock; operator credit was
// voided when the initial-purchase refund liability was recorded.
func (s *Service) settlePurchaseRefund(ctx context.Context, plan billing.RefundSettlement) error {
	if _, err := uuid.Parse(plan.TenantID); err != nil {
		return err
	}
	if _, err := uuid.Parse(plan.ReferenceID); err != nil {
		return err
	}
	if plan.PaymentID == "" || plan.AmountCents <= 0 || len(plan.Currency) != 3 {
		return errors.New("invalid purchase refund settlement")
	}
	// Refund inspection can retry a failed bank return. Validate its durable
	// purchase ownership before that I/O, then recheck before storing receipts.
	if err := database.WithRetryablePostgresTx(ctx, s.db, nil, func(tx *sql.Tx) error {
		if _, lockErr := subscriptions.LockChange(ctx, tx, plan.TenantID, subscriptions.PreservePricing); lockErr != nil {
			return lockErr
		}
		_, checkErr := validateRefundPurchase(ctx, purserdb.New(tx), plan)
		return checkErr
	}); err != nil {
		return err
	}

	var receipts []billing.SettledRefund
	var inspection error
	switch plan.Provider {
	case "stripe":
		if s.stripeClient == nil {
			return errors.New("stripe refund settlement is not configured")
		}
		receipts, inspection = s.stripeClient.InspectRefundSettlement(ctx, plan)
	case "mollie":
		if s.mollieClient == nil {
			return errors.New("mollie refund settlement is not configured")
		}
		receipts, inspection = s.mollieClient.InspectRefundSettlement(ctx, plan)
	default:
		return errors.New("unsupported purchase refund provider")
	}
	err := database.WithRetryablePostgresTx(ctx, s.db, nil, func(tx *sql.Tx) error {
		if _, err := subscriptions.LockChange(ctx, tx, plan.TenantID, subscriptions.PreservePricing); err != nil {
			return err
		}
		q := purserdb.New(tx)
		intent, validationErr := validateRefundPurchase(ctx, q, plan)
		if validationErr != nil {
			return validationErr
		}

		for _, receipt := range receipts {
			if (receipt.ObjectType != "refund" && receipt.ObjectType != "dispute" && receipt.ObjectType != "chargeback") || receipt.ID == "" || receipt.AmountCents <= 0 || !strings.EqualFold(receipt.Currency, plan.Currency) {
				return errors.New("invalid settled refund evidence")
			}
			metadata, err := json.Marshal(map[string]any{"reference_kind": plan.ReferenceKind, "reference_id": plan.ReferenceID, "invoice_id": plan.InvoiceID, "payment_id": receipt.PaymentID, "charge_id": receipt.ChargeID, "amount_cents": receipt.AmountCents, "currency": strings.ToUpper(receipt.Currency), "status": "succeeded"})
			if err != nil {
				return err
			}
			referenceType := sql.NullString{}
			if intent.Valid {
				referenceType = sql.NullString{String: "intent", Valid: true}
			}
			if err = q.RecordSettledCompensationRefund(ctx, purserdb.RecordSettledCompensationRefundParams{Provider: plan.Provider, ObjectType: receipt.ObjectType, RefundID: receipt.ID, TenantID: plan.TenantID, ReferenceType: referenceType, ReferenceID: intent, IntentID: intent, Metadata: metadata}); err != nil {
				return err
			}
		}
		if plan.Provider == "stripe" && plan.InvoiceID != "" {
			return q.ApplyCompensatedStripeInvoiceCredit(ctx, purserdb.ApplyCompensatedStripeInvoiceCreditParams{TenantID: plan.TenantID, InvoiceID: plan.InvoiceID, Currency: strings.ToUpper(plan.Currency)})
		}
		return nil
	})
	if err != nil {
		return err
	}
	if errors.Is(inspection, billing.ErrRefundSettling) {
		return fmt.Errorf("%w: %w", ErrAwaitingProviderSettlement, inspection)
	}
	return inspection
}

func validateRefundPurchase(ctx context.Context, q *purserdb.Queries, plan billing.RefundSettlement) (sql.NullString, error) {
	intent := sql.NullString{}
	switch plan.ReferenceKind {
	case "checkout":
		row, err := q.GetProviderCheckoutReservation(ctx, purserdb.GetProviderCheckoutReservationParams{TenantID: plan.TenantID, IntentID: plan.ReferenceID})
		if err != nil {
			return sql.NullString{}, err
		}
		if row.Provider != plan.Provider || !row.RecoveryStartedAt.Valid {
			return sql.NullString{}, errors.New("refund has no matching purchase compensation")
		}
		intent = sql.NullString{String: plan.ReferenceID, Valid: true}
	case "tier_upgrade":
		row, err := q.GetTierUpgrade(ctx, purserdb.GetTierUpgradeParams{TenantID: plan.TenantID, ID: plan.ReferenceID})
		if err != nil {
			return sql.NullString{}, err
		}
		if row.Provider != plan.Provider || row.State != "cancelled" {
			return sql.NullString{}, errors.New("refund does not belong to a cancelled upgrade")
		}
	default:
		return sql.NullString{}, errors.New("invalid refund purchase kind")
	}
	return intent, nil
}

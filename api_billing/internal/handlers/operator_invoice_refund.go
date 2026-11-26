package handlers

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"frameworks/api_billing/internal/database/purserdb"
	"frameworks/api_billing/internal/subscriptions"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/billing"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/database"
	"strings"
	"time"
)

type InvoiceRefundOutcome struct {
	RequestID, InvoiceID, Provider, Currency, Status, Fingerprint, Detail, ProviderRefundID string
	AmountMinor                                                                             int64
}
type invoiceRefundSource struct {
	Invoice struct {
		ID string `json:"id"`
	} `json:"invoice"`
	Payment struct {
		ID            string `json:"id"`
		TransactionID string `json:"tx_id"`
		AmountMinor   int64  `json:"original_amount_cents"`
		Currency      string `json:"original_currency"`
	} `json:"payment"`
	ProviderCustomerID string `json:"provider_customer_id"`
}

func invoiceRefundFingerprint(source []byte, tenant, invoice, request, reason, actor string) (string, error) {
	body, err := json.Marshal(struct {
		Source                                  json.RawMessage
		Tenant, Invoice, Request, Reason, Actor string
	}{source, tenant, invoice, request, reason, actor})
	if err != nil {
		return "", err
	}
	hash := sha256.Sum256(body)
	return hex.EncodeToString(hash[:]), nil
}
func invoiceRefundResult(row purserdb.GetOperatorInvoiceRefundRow) InvoiceRefundOutcome {
	return InvoiceRefundOutcome{RequestID: row.ID, InvoiceID: row.InvoiceID, Provider: row.Provider, Currency: row.Currency, Status: row.Status, Fingerprint: row.ReviewFingerprint, AmountMinor: row.AmountMinor, ProviderRefundID: row.ProviderRefundID, Detail: "Full original payment refund. The wallet receives no credit and the subscription is unchanged. " + row.LastError}
}

// ReviewInvoiceRefund queues a reviewed full refund; provider calls happen in the durable inbox worker.
func (s *Service) ReviewInvoiceRefund(ctx context.Context, tenant, invoice, request, reason, actor string, confirmed bool, expected string) (InvoiceRefundOutcome, error) {
	var out InvoiceRefundOutcome
	err := database.WithRetryablePostgresTx(ctx, s.db, nil, func(tx *sql.Tx) error {
		if _, err := subscriptions.LockChange(ctx, tx, tenant, subscriptions.PreservePricing); err != nil {
			return err
		}
		q := purserdb.New(tx)
		existing, err := q.GetOperatorInvoiceRefund(ctx, purserdb.GetOperatorInvoiceRefundParams{TenantID: tenant, RequestID: request})
		if err == nil {
			if existing.InvoiceID != invoice || existing.Reason != reason || existing.ActorID != actor {
				return fmt.Errorf("refund reference already belongs to a different request")
			}
			out = invoiceRefundResult(existing)
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		previous, lookupErr := q.GetOperatorInvoiceRefundReference(ctx, purserdb.GetOperatorInvoiceRefundReferenceParams{TenantID: tenant, InvoiceID: invoice})
		if lookupErr == nil {
			return fmt.Errorf("invoice already has refund request %s; inspect that same reference", previous)
		}
		if !errors.Is(lookupErr, sql.ErrNoRows) {
			return lookupErr
		}
		source, err := q.GetOperatorInvoiceRefundSource(ctx, purserdb.GetOperatorInvoiceRefundSourceParams{TenantID: tenant, InvoiceID: invoice})
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("full refund requires one confirmed card payment, complete original tax and FX evidence, and no earlier reversal, credit note or subsequent operator credit; reconcile the invoice manually")
		}
		if err != nil {
			return err
		}
		var receipt invoiceRefundSource
		if err = json.Unmarshal(source, &receipt); err != nil {
			return err
		}
		provider := ""
		switch {
		case strings.HasPrefix(receipt.Payment.TransactionID, "pi_"):
			provider = "stripe"
		case strings.HasPrefix(receipt.Payment.TransactionID, "tr_"):
			provider = "mollie"
		default:
			return fmt.Errorf("invoice payment is not supported for an automatic refund")
		}
		if provider == "stripe" && s.stripeClient == nil || provider == "mollie" && s.mollieClient == nil {
			return fmt.Errorf("original payment provider is unavailable")
		}
		fingerprint, fingerprintErr := invoiceRefundFingerprint(source, tenant, invoice, request, reason, actor)
		if fingerprintErr != nil {
			return fingerprintErr
		}
		out = InvoiceRefundOutcome{RequestID: request, InvoiceID: invoice, Provider: provider, AmountMinor: receipt.Payment.AmountMinor, Currency: receipt.Payment.Currency, Status: "preview", Fingerprint: fingerprint, Detail: "Full original payment refund. No wallet credit. The subscription remains active until changed separately. Prior related goodwill must be reconciled before approval."}
		if !confirmed {
			return nil
		}
		if subtle.ConstantTimeCompare([]byte(expected), []byte(fingerprint)) != 1 {
			return fmt.Errorf("invoice or payment changed after review; preview the refund again")
		}
		if err = q.InsertOperatorInvoiceRefund(ctx, purserdb.InsertOperatorInvoiceRefundParams{RequestID: request, TenantID: tenant, InvoiceID: invoice, PaymentID: receipt.Payment.ID, Provider: provider, ProviderPaymentID: receipt.Payment.TransactionID, AmountMinor: receipt.Payment.AmountMinor, Currency: receipt.Payment.Currency, Reason: reason, ActorID: actor, SourceSnapshot: source, Fingerprint: fingerprint}); err != nil {
			return err
		}
		rows, err := q.HoldOperatorRefundInvoice(ctx, purserdb.HoldOperatorRefundInvoiceParams{TenantID: tenant, InvoiceID: invoice})
		if err != nil {
			return err
		}
		if rows != 1 {
			return fmt.Errorf("invoice changed while approving refund")
		}
		payload, marshalErr := json.Marshal(struct{ TenantID, RequestID string }{tenant, request})
		if marshalErr != nil {
			return marshalErr
		}
		if err = q.EnqueueOperatorInvoiceRefund(ctx, purserdb.EnqueueOperatorInvoiceRefundParams{RequestID: request, TenantID: tenant, Payload: payload}); err != nil {
			return err
		}
		out.Status = "queued"
		return nil
	})
	return out, err
}

func (s *Service) processOperatorInvoiceRefund(ctx context.Context, tenant, request string) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	row, err := purserdb.New(s.db).GetOperatorInvoiceRefund(ctx, purserdb.GetOperatorInvoiceRefundParams{TenantID: tenant, RequestID: request})
	if err != nil {
		return err
	}
	if row.Status == "succeeded" || row.Status == "needs_review" {
		return nil
	}
	var original invoiceRefundSource
	if err = json.Unmarshal(row.SourceSnapshot, &original); err != nil {
		return err
	}
	in := billing.CompletedInvoiceRefund{TenantID: tenant, InvoiceID: row.InvoiceID, RequestID: request, PaymentID: row.PaymentID, ProviderPaymentID: row.ProviderPaymentID, ProviderCustomerID: original.ProviderCustomerID, AmountMinor: row.AmountMinor, Currency: row.Currency, RefundID: row.ProviderRefundID}
	var receipt billing.CompletedInvoiceRefundReceipt
	switch row.Provider {
	case "stripe":
		if s.stripeClient == nil {
			return fmt.Errorf("stripe unavailable")
		}
		receipt, err = s.stripeClient.RefundCompletedInvoice(ctx, in)
	case "mollie":
		if s.mollieClient == nil {
			return fmt.Errorf("mollie unavailable")
		}
		receipt, err = s.mollieClient.RefundCompletedInvoice(ctx, in)
	default:
		return fmt.Errorf("invalid refund provider")
	}
	if err != nil {
		if recordErr := purserdb.New(s.db).RecordOperatorInvoiceRefundError(ctx, purserdb.RecordOperatorInvoiceRefundErrorParams{TenantID: tenant, RequestID: request, Message: "The provider could not reconcile this return. Collection remains held; inspect the original payment and queued work before retrying."}); recordErr != nil {
			return errors.Join(err, recordErr)
		}
		return err
	}
	if receipt.Status == "succeeded" {
		if _, err = s.applyProviderReversal(ctx, providerReversalInput{provider: row.Provider, reversalType: "refund", providerReversalID: receipt.ID, providerChargeID: receipt.ChargeID, providerPaymentID: row.ProviderPaymentID, amountCents: receipt.AmountMinor, currency: receipt.Currency, reason: row.Reason}); err != nil {
			return err
		}
	}
	err = database.WithRetryablePostgresTx(ctx, s.db, nil, func(tx *sql.Tx) error {
		if _, lockErr := subscriptions.LockChange(ctx, tx, tenant, subscriptions.PreservePricing); lockErr != nil {
			return lockErr
		}
		q := purserdb.New(tx)
		if updateErr := q.UpdateOperatorInvoiceRefund(ctx, purserdb.UpdateOperatorInvoiceRefundParams{TenantID: tenant, RequestID: request, Status: receipt.Status, ProviderRefundID: receipt.ID, LastError: refundReceiptError(receipt.Status)}); updateErr != nil {
			return updateErr
		}
		if receipt.Status != "succeeded" {
			return nil
		}
		if noteErr := q.IssueOperatorRefundCreditNote(ctx, purserdb.IssueOperatorRefundCreditNoteParams{TenantID: tenant, RequestID: request}); noteErr != nil {
			return noteErr
		}
		rows, finalizeErr := q.FinalizeOperatorRefundInvoice(ctx, purserdb.FinalizeOperatorRefundInvoiceParams{TenantID: tenant, InvoiceID: row.InvoiceID})
		if finalizeErr != nil {
			return finalizeErr
		}
		if rows != 1 {
			return fmt.Errorf("refund invoice finalization did not match its tenant")
		}
		return nil
	})
	if err != nil {
		return err
	}
	if receipt.Status == "submitted" {
		return ErrAwaitingProviderSettlement
	}
	return nil
}

func refundReceiptError(state string) string {
	if state == "needs_review" {
		return "The bank return failed or was cancelled. Collection remains held and no replacement refund is started automatically."
	}
	return ""
}
func (s *Service) GetInvoiceRefund(ctx context.Context, tenant, request string) (InvoiceRefundOutcome, error) {
	row, err := purserdb.New(s.db).GetOperatorInvoiceRefund(ctx, purserdb.GetOperatorInvoiceRefundParams{TenantID: tenant, RequestID: request})
	if err != nil {
		return InvoiceRefundOutcome{}, err
	}
	return invoiceRefundResult(row), nil
}

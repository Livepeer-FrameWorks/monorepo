package handlers

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"frameworks/api_billing/internal/billingevents"
	"frameworks/api_billing/internal/database/purserdb"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/events"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	publicv1 "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/events/public/v1"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const (
	eventPaymentSucceeded      = "payment_succeeded"
	eventPaymentFailed         = "payment_failed"
	eventSubscriptionUpdated   = "subscription_updated"
	eventSubscriptionCanceled  = "subscription_canceled"
	eventInvoicePaid           = "invoice_paid"
	eventInvoicePaymentFailed  = "invoice_payment_failed"
	eventTopupCredited         = "topup_credited"
	eventX402SettlementPending = "x402_settlement_pending"
	eventX402SettlementFailed  = "x402_settlement_failed"
	eventX402SettlementConfirm = "x402_settlement_confirmed"
	eventX402RPCError          = "x402_rpc_error"
	eventX402LateRecovery      = "x402_late_recovery"
	eventX402AccountingAnomaly = "x402_accounting_anomaly"
	eventX402ReorgDetected     = "x402_reorg_detected"
	eventCryptoDepositReorg    = "crypto_deposit_reorg_detected"
)

// emitBillingEventTx writes a billing event row into
// purser.billing_event_outbox through exec. Callers pass the transaction that
// performs the billing mutation the event reports, so the row commits or rolls
// back with that mutation. The marshal or insert error is returned for the
// caller to abort its transaction. The drain worker (runBillingOutboxWorker in
// api_billing/internal/grpc) dispatches committed rows to Decklog.
func emitBillingEventTx(ctx context.Context, exec purserdb.DBTX, eventType, tenantID, resourceType, resourceID string, payload *ipcpb.BillingEvent) error {
	return emitBillingEventsTx(ctx, exec, eventType, tenantID, resourceType, resourceID, payload, nil)
}

// emitBillingEventsTx is emitBillingEventTx for a fact that also has a domain
// event: domain is written to purser.domain_event_outbox through the same exec
// and the legacy row takes its ID. A nil domain writes the legacy row only.
func emitBillingEventsTx(ctx context.Context, exec purserdb.DBTX, eventType, tenantID, resourceType, resourceID string, payload *ipcpb.BillingEvent, domain *events.Event) error {
	params, err := billingEventOutboxParams(eventType, tenantID, resourceType, resourceID, payload)
	if err != nil {
		return err
	}
	if params.ID, err = billingevents.LegacyRowID(domain); err != nil {
		return fmt.Errorf("%s billing event id: %w", eventType, err)
	}
	if err := billingevents.Enqueue(ctx, exec, domain); err != nil {
		return err
	}
	if err := purserdb.New(exec).EnqueueBillingEventOutboxNoReturn(ctx, params); err != nil {
		return fmt.Errorf("enqueue %s billing event: %w", eventType, err)
	}
	return nil
}

// legacyBillingEvent is a billing_event_outbox row a domain event helper
// writes alongside its domain event, under the same event ID.
type legacyBillingEvent struct {
	eventType    string
	resourceType string
	resourceID   string
	payload      *ipcpb.BillingEvent
}

// topupCreditedEvent builds billing.topup_credited for a top-up credited in
// the caller's transaction.
func topupCreditedEvent(tenantID, topupID string, amountCents int64, currency string) (*events.Event, error) {
	return billingevents.New(tenantID, topupID, &publicv1.TopupCredited{
		TopupId: topupID, Amount: &publicv1.Money{AmountMinor: amountCents, Currency: currency},
	}, events.Actor{})
}

// enqueueInvoiceCreatedTx writes billing.invoice_created for an invoice this
// transaction issued with amountCents EUR due. An invoice issued with nothing
// to collect is issued paid, so billing.invoice_paid follows it. Drafts and
// invoices held for manual review are not issued and emit nothing; their
// finalization emits once, because it only matches a draft or held row.
func enqueueInvoiceCreatedTx(ctx context.Context, tx *sql.Tx, tenantID, invoiceID, status string, amountCents int64, periodStart, periodEnd, dueAt time.Time) error {
	if status != "pending" && status != "paid" {
		return nil
	}
	if err := billingevents.NewAndEnqueue(ctx, tx, tenantID, invoiceID, &publicv1.InvoiceCreated{
		InvoiceId:   invoiceID,
		AmountDue:   billingevents.EUR(amountCents),
		PeriodStart: timestamppb.New(periodStart),
		PeriodEnd:   timestamppb.New(periodEnd),
		DueAt:       timestamppb.New(dueAt),
	}, events.Actor{}); err != nil {
		return err
	}
	if status != "paid" {
		return nil
	}
	return billingevents.NewAndEnqueue(ctx, tx, tenantID, invoiceID, &publicv1.InvoicePaid{
		InvoiceId: invoiceID, AmountPaid: billingevents.EUR(amountCents),
	}, events.Actor{})
}

// emitBillingTelemetryEvent writes a billing event row in its own statement and
// logs a failed insert instead of returning it. It is reserved for x402
// accounting-anomaly and RPC-error events: they report a condition the
// reconciler observed rather than a billing state transition, so there is no
// mutation for the row to share a transaction with, and a failed insert must
// not stop the reconciler pass. Outbox rows are tenant-keyed, so an
// observation without a tenant is dropped.
func emitBillingTelemetryEvent(ctx context.Context, db *sql.DB, logger logging.Logger, eventType, tenantID, resourceType, resourceID string, payload *ipcpb.BillingEvent) {
	if db == nil || tenantID == "" {
		return
	}
	if err := emitBillingEventTx(ctx, db, eventType, tenantID, resourceType, resourceID, payload); err != nil && logger != nil {
		logger.WithError(err).WithField("event_type", eventType).
			Warn("Failed to enqueue billing telemetry event outbox row")
	}
}

func billingEventOutboxParams(eventType, tenantID, resourceType, resourceID string, payload *ipcpb.BillingEvent) (purserdb.EnqueueBillingEventOutboxNoReturnParams, error) {
	if tenantID == "" {
		return purserdb.EnqueueBillingEventOutboxNoReturnParams{}, errors.New("billing event " + eventType + " has no tenant")
	}
	if payload == nil {
		payload = &ipcpb.BillingEvent{}
	}
	if payload.TenantId == "" {
		payload.TenantId = tenantID
	}
	billingJSON, err := protojson.Marshal(payload)
	if err != nil {
		return purserdb.EnqueueBillingEventOutboxNoReturnParams{}, fmt.Errorf("marshal %s billing event: %w", eventType, err)
	}
	return purserdb.EnqueueBillingEventOutboxNoReturnParams{
		EventType: eventType, TenantID: tenantID, UserID: "",
		ResourceType: resourceType, ResourceID: resourceID, BillingEvent: billingJSON,
	}, nil
}

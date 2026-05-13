package handlers

import (
	"context"

	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto"
	"google.golang.org/protobuf/encoding/protojson"
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
)

// emitBillingEvent enqueues a billing event into purser.billing_event_outbox.
// The Purser drain worker (runBillingOutboxWorker in api_billing/internal/grpc)
// dispatches pending rows to Decklog with exponential backoff, so events
// survive Decklog outages instead of getting lost. Best-effort durability:
// this helper opens its own short tx for the INSERT, so it's NOT
// transactionally atomic with the caller's billing state mutation. For
// strict atomicity, callers should switch to the tx-aware variant on
// PurserServer.EnqueueBillingEventTx when they hold their own tx.
func emitBillingEvent(eventType, tenantID, resourceType, resourceID string, payload *pb.BillingEvent) {
	if db == nil || tenantID == "" {
		return
	}
	if payload == nil {
		payload = &pb.BillingEvent{}
	}
	if payload.TenantId == "" {
		payload.TenantId = tenantID
	}
	billingJSON, err := protojson.Marshal(payload)
	if err != nil {
		if logger != nil {
			logger.WithError(err).WithField("event_type", eventType).
				Warn("Failed to marshal billing event payload for outbox")
		}
		return
	}
	if _, err := db.ExecContext(context.Background(), `
		INSERT INTO purser.billing_event_outbox
			(event_type, tenant_id, user_id, resource_type, resource_id, billing_event)
		VALUES ($1, $2::uuid, $3, $4, $5, $6::jsonb)
	`, eventType, tenantID, "", resourceType, resourceID, billingJSON); err != nil && logger != nil {
		logger.WithError(err).WithField("event_type", eventType).
			Warn("Failed to enqueue billing event outbox row")
	}
}

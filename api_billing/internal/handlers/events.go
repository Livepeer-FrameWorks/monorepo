package handlers

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"frameworks/api_billing/internal/database/purserdb"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	"github.com/google/uuid"
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
	eventCryptoDepositReorg    = "crypto_deposit_reorg_detected"
)

// emitBillingEventTx writes a billing event row into
// purser.billing_event_outbox through exec. Callers pass the transaction that
// performs the billing mutation the event reports, so the row commits or rolls
// back with that mutation. The marshal or insert error is returned for the
// caller to abort its transaction. The drain worker (runBillingOutboxWorker in
// api_billing/internal/grpc) dispatches committed rows to Decklog.
func emitBillingEventTx(ctx context.Context, exec purserdb.DBTX, eventType, tenantID, resourceType, resourceID string, payload *ipcpb.BillingEvent) error {
	params, err := billingEventOutboxParams(eventType, tenantID, resourceType, resourceID, payload)
	if err != nil {
		return err
	}
	if err := purserdb.New(exec).EnqueueBillingEventOutboxNoReturn(ctx, params); err != nil {
		return fmt.Errorf("enqueue %s billing event: %w", eventType, err)
	}
	return nil
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
		ID: uuid.Must(uuid.NewV7()), EventType: eventType, TenantID: tenantID, UserID: "",
		ResourceType: resourceType, ResourceID: resourceID, BillingEvent: billingJSON,
	}, nil
}

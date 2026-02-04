package handlers

import (
	pb "frameworks/pkg/proto"
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
)

func emitBillingEvent(eventType, tenantID, resourceType, resourceID string, payload *pb.BillingEvent) {
	if decklogClient == nil || tenantID == "" {
		return
	}
	if payload == nil {
		payload = &pb.BillingEvent{}
	}
	payload.TenantId = tenantID

	event := &pb.ServiceEvent{
		EventType:    eventType,
		Timestamp:    timestamppb.Now(),
		Source:       "purser",
		TenantId:     tenantID,
		ResourceType: resourceType,
		ResourceId:   resourceID,
		Payload:      &pb.ServiceEvent_BillingEvent{BillingEvent: payload},
	}
	if err := decklogClient.SendServiceEvent(event); err != nil {
		logger.WithError(err).WithField("event_type", eventType).Warn("Failed to emit billing event")
	}
}

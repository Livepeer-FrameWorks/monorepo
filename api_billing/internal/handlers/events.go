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
	eventX402SettlementFailed  = "x402_settlement_failed"
	eventX402SettlementConfirm = "x402_settlement_confirmed"
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

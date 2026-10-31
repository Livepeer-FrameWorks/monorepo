package billing

import "time"

// SubscriptionEndRequest identifies the established subscription whose future collection must end.
// It never authorizes checkout compensation or a refund of delivered service.
type SubscriptionEndRequest struct {
	// AllowRecovery keeps a failed subscription whose payment and renewal authority recovered.
	AllowRecovery bool
	// InitialPaymentID ties an initial Mollie payment to this exact native subscription.
	InitialPaymentID string
	TenantID         string
	CustomerID       string
	SubscriptionID   string
	TierID           string
	PeriodEnd        time.Time
}

// SubscriptionEndResult separates payment recovery, settlement and a terminated paid term.
type SubscriptionEndResult struct {
	// Recovered retains a subscription whose current payment recovered before cancellation.
	Recovered bool
	// Pending keeps the fence while an authorized payment still settles.
	Pending bool
	// PaidUntil retains access bought by a payment that settled during cancellation.
	PaidUntil time.Time
}

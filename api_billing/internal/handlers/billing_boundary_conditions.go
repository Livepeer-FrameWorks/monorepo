package handlers

import (
	"errors"
	"fmt"

	"frameworks/api_billing/internal/pricing"
)

// BillingBoundaryCondition identifies billing data that must be repaired or
// completed before a pricing boundary can commit. Database failures retain their
// own error type so callers do not mistake an outage for an operator action.
type BillingBoundaryCondition struct {
	// Reason describes the billing data that must be repaired.
	Reason string
}

func (e *BillingBoundaryCondition) Error() string { return e.Reason }

func billingBoundaryConditionf(format string, args ...any) error {
	return &BillingBoundaryCondition{Reason: fmt.Sprintf(format, args...)}
}

// IsBillingBoundaryCondition distinguishes actionable billing state from infrastructure errors.
func IsBillingBoundaryCondition(err error) bool {
	var condition *BillingBoundaryCondition
	return errors.As(err, &condition) ||
		errors.Is(err, pricing.ErrThirdPartyPricingMissing) ||
		errors.Is(err, pricing.ErrCustomPricingMissingForCluster) ||
		errors.Is(err, pricing.ErrAmbiguousClusterOwnership)
}

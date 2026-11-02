package grpc

import (
	"errors"

	"frameworks/api_billing/internal/subscriptions"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// billingErrorDomain is the ErrorInfo domain the gateway reads tenant-facing
// billing refusals from.
const billingErrorDomain = "billing.frameworks.network"

// Tenant-facing refusal reasons. The gateway presents each as the GraphQL
// error code, and the dashboard and MCP tools branch on them.
const (
	RefusalPendingCheckout      = "PENDING_CHECKOUT"
	RefusalPendingFreeFallback  = "PENDING_FREE_FALLBACK"
	RefusalPendingUpgrade       = "PENDING_UPGRADE"
	RefusalOutstandingGrant     = "OUTSTANDING_GRANT"
	RefusalAccountClosed        = "ACCOUNT_CLOSED"
	RefusalDowngradeHeld        = "DOWNGRADE_HELD"
	RefusalPaidTermEnding       = "PAID_TERM_ENDING"
	RefusalProviderSubscription = "PROVIDER_SUBSCRIPTION_LIVE"
	RefusalTierNotSelfService   = "TIER_NOT_SELF_SERVICE"
	RefusalUnusedFirstPayment   = "UNUSED_FIRST_PAYMENT"
	RefusalFirstPaymentRequired = "FIRST_PAYMENT_REQUIRED"
	RefusalInvoicesOutstanding  = "INVOICES_OUTSTANDING"
	RefusalCollectionSetup      = "COLLECTION_SETUP_REQUIRED"
)

// tenantRefusal is the code and message a tenant sees for a pricing refusal.
// Operator guidance (recovery commands, grant mechanics) stays in the
// sentinel errors, which admin paths and logs keep using.
type tenantRefusal struct {
	reason  string
	message string
}

func classifyPricingRefusal(err error) (tenantRefusal, bool) {
	switch {
	case errors.Is(err, subscriptions.ErrTenantBillingClosed):
		return tenantRefusal{RefusalAccountClosed, "This account is closed. Billing changes are no longer possible."}, true
	case errors.Is(err, subscriptions.ErrOutstandingGrant):
		return tenantRefusal{RefusalOutstandingGrant, "Your plan is arranged by FrameWorks. Contact us to change it before the arrangement ends."}, true
	case errors.Is(err, subscriptions.ErrPendingFreeFallback):
		return tenantRefusal{RefusalPendingFreeFallback, "Your move to Free is being applied. Choose a new plan once it completes."}, true
	case errors.Is(err, subscriptions.ErrPendingTierUpgrade):
		return tenantRefusal{RefusalPendingUpgrade, "Your plan upgrade is still being confirmed. Retry the same plan to see its result."}, true
	case errors.Is(err, subscriptions.ErrPendingCheckout):
		return tenantRefusal{RefusalPendingCheckout, "A checkout is still open. Finish or close it, then try again."}, true
	case errors.Is(err, subscriptions.ErrPaidCancellationTerm):
		return tenantRefusal{RefusalPaidTermEnding, "Your cancelled plan runs to the end of its paid period. Choose a new plan after it moves to Free."}, true
	case errors.Is(err, subscriptions.ErrSelfServiceSubscription), errors.Is(err, subscriptions.ErrSelfServiceTierChange):
		return tenantRefusal{RefusalProviderSubscription, "Your subscription is still active. Change your plan from the plan list instead of starting another checkout."}, true
	case errors.Is(err, subscriptions.ErrSelfServiceTier):
		return tenantRefusal{RefusalTierNotSelfService, "This plan cannot be bought online. Contact us to arrange it."}, true
	case errors.Is(err, subscriptions.ErrUnusedMollieFunding):
		return tenantRefusal{RefusalUnusedFirstPayment, subscriptions.ErrUnusedMollieFunding.Error()}, true
	case errors.Is(err, subscriptions.ErrMollieFundingMismatch):
		return tenantRefusal{RefusalUnusedFirstPayment, subscriptions.ErrMollieFundingMismatch.Error()}, true
	case errors.Is(err, subscriptions.ErrMollieFundingRequired):
		return tenantRefusal{RefusalFirstPaymentRequired, subscriptions.ErrMollieFundingRequired.Error()}, true
	}
	return tenantRefusal{}, false
}

// pricingRefusalStatus presents a pricing refusal to the tenant as
// FailedPrecondition with a typed reason. The cause stays reachable through
// errors.Is. ok is false for an error that is not a pricing refusal.
func pricingRefusalStatus(err error) (error, bool) {
	refusal, ok := classifyPricingRefusal(err)
	if !ok {
		return nil, false
	}
	return &checkoutPolicyError{cause: err, status: refusalStatus(refusal.reason, refusal.message)}, true
}

// tenantPricingRefusal presents a refusal on a tenant-facing RPC, falling
// back to the error's own text for a refusal without a typed reason.
func tenantPricingRefusal(err error) error {
	if refusal, ok := pricingRefusalStatus(err); ok {
		return refusal
	}
	return status.Error(codes.FailedPrecondition, err.Error())
}

// billingRefusal is a typed FailedPrecondition with no sentinel cause.
func billingRefusal(reason, message string) error {
	return refusalStatus(reason, message).Err()
}

func refusalStatus(reason, message string) *status.Status {
	st := status.New(codes.FailedPrecondition, message)
	withDetails, err := st.WithDetails(&errdetails.ErrorInfo{
		Reason:   reason,
		Domain:   billingErrorDomain,
		Metadata: map[string]string{"public_message": message},
	})
	if err != nil {
		return st
	}
	return withDetails
}

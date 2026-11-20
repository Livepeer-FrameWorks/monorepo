package grpcutil

import (
	"context"

	"google.golang.org/grpc/metadata"
)

const billingRefreshHeader = "x-frameworks-refresh-billing"

// WithFreshBillingAdmission asks the billing authority behind ValidateTenant
// to bypass its admission cache. It grants no authorization or entitlement.
func WithFreshBillingAdmission(ctx context.Context) context.Context {
	return metadata.AppendToOutgoingContext(ctx, billingRefreshHeader, "true")
}

// FreshBillingAdmissionRequested reads refresh intent on an authorized RPC.
func FreshBillingAdmissionRequested(ctx context.Context) bool {
	values := metadata.ValueFromIncomingContext(ctx, billingRefreshHeader)
	return len(values) > 0 && values[len(values)-1] == "true"
}

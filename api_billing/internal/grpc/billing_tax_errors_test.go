package grpc

import (
	"strings"
	"testing"

	billingtax "frameworks/api_billing/internal/tax"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestTaxSetupFailureGivesCustomerAnActionAndRetainsMachineReason(t *testing.T) {
	internal := billingtax.Review("destination_registration_required", "confirm OSS or destination registration")
	err := billingTaxRequirementError(internal)
	st := status.Convert(err)
	if st.Code() != codes.FailedPrecondition || !strings.Contains(st.Message(), "Contact support") || strings.Contains(st.Message(), "OSS") {
		t.Fatalf("customer-facing error=%v", err)
	}
	found := false
	for _, detail := range st.Details() {
		if info, ok := detail.(*errdetails.ErrorInfo); ok && info.Reason == "DESTINATION_REGISTRATION_REQUIRED" {
			found = true
			if info.GetMetadata()["public_message"] != st.Message() {
				t.Fatalf("gateway cannot present tax guidance without public_message: %+v", info)
			}
		}
	}
	if !found {
		t.Fatal("machine-readable tax review reason missing")
	}
	payment := billingTaxPaymentFailure(internal, "fallback")
	if payment.Error != st.Message() || payment.ErrorCode != "DESTINATION_REGISTRATION_REQUIRED" {
		t.Fatalf("payment error differs from RPC error: %+v", payment)
	}
}

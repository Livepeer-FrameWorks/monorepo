package grpc

import (
	"errors"
	"strings"

	billingtax "frameworks/api_billing/internal/tax"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func billingTaxRequirementError(err error) error {
	var review *billingtax.ReviewError
	if !errors.As(err, &review) {
		return status.Errorf(codes.Unavailable, "determine tax-document requirement: %v", err)
	}
	st := status.New(codes.FailedPrecondition, billingTaxPublicMessage(review))
	detailed, detailErr := st.WithDetails(&errdetails.ErrorInfo{Reason: strings.ToUpper(review.Code), Domain: "billing.frameworks.network"})
	if detailErr != nil {
		return st.Err()
	}
	return detailed.Err()
}

func billingTaxPaymentFailure(err error, fallback string) *purserpb.PaymentRequirements {
	response := &purserpb.PaymentRequirements{X402Version: 2, Error: fallback, TopupUrl: "/account/billing"}
	var review *billingtax.ReviewError
	if errors.As(err, &review) {
		response.Error, response.ErrorCode = billingTaxPublicMessage(review), strings.ToUpper(review.Code)
	}
	return response
}

func billingTaxPublicMessage(review *billingtax.ReviewError) string {
	switch review.Code {
	case "consumer_vat_regime_required", "destination_registration_required", "foreign_tax_policy_required", "consumer_threshold_unproven":
		return "Payments from this billing country are awaiting tax setup. Contact support before paying."
	case "consumer_location_evidence_required", "consumer_location_conflict":
		return "We could not confirm your billing country. Check your billing details or contact support before paying."
	case "business_evidence_required":
		return "We need additional business details for this billing country. Contact support before paying."
	default:
		return review.Message
	}
}

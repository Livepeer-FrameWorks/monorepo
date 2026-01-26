package handlers

import (
	"context"
	"net/http"
	"strings"

	"frameworks/api_balancing/internal/triggers"
	"frameworks/pkg/logging"
	pkgx402 "frameworks/pkg/x402"
)

type x402Decision struct {
	Status int
	Body   map[string]any
}

func getBillingStatus(ctx context.Context, internalName, tenantID string) *triggers.BillingStatus {
	if triggerProcessor != nil {
		return triggerProcessor.GetBillingStatus(ctx, internalName, tenantID)
	}
	if quartermasterClient != nil && tenantID != "" {
		resp, err := quartermasterClient.ValidateTenant(ctx, tenantID, "")
		if err == nil && resp != nil && resp.Valid {
			return &triggers.BillingStatus{
				TenantID:          tenantID,
				BillingModel:      resp.BillingModel,
				IsSuspended:       resp.IsSuspended,
				IsBalanceNegative: resp.IsBalanceNegative,
				FromCache:         false,
			}
		}
	}
	return nil
}

func settleX402PaymentForPlayback(ctx context.Context, tenantID, resourceID, paymentHeader, clientIP string, logger logging.Logger) (bool, *x402Decision) {
	if tenantID == "" || paymentHeader == "" || purserClient == nil {
		return false, nil
	}

	result, err := pkgx402.SettleX402Payment(ctx, pkgx402.SettlementOptions{
		PaymentHeader: paymentHeader,
		Resource:      "viewer://" + resourceID,
		AuthTenantID:  "",
		ClientIP:      clientIP,
		Purser:        purserClient,
		Commodore:     nil,
		Logger:        logger,
		Resolution: &pkgx402.ResourceResolution{
			Resource: "viewer://" + resourceID,
			Kind:     pkgx402.ResourceKindViewer,
			TenantID: tenantID,
			Resolved: true,
		},
	})

	if err != nil {
		return false, mapSettlementErrorToHTTPDecision(ctx, tenantID, resourceID, err)
	}

	if result == nil || result.Settle == nil || !result.Settle.Success {
		return false, &x402Decision{
			Status: http.StatusPaymentRequired,
			Body:   buildPaymentFailedResponse("payment settlement failed"),
		}
	}

	return true, nil
}

func mapSettlementErrorToHTTPDecision(ctx context.Context, tenantID, resourceID string, err *pkgx402.SettlementError) *x402Decision {
	switch err.Code {
	case pkgx402.ErrInvalidPayment:
		return &x402Decision{
			Status: http.StatusPaymentRequired,
			Body:   buildPaymentFailedResponse(err.Message),
		}
	case pkgx402.ErrBillingDetailsRequired:
		return &x402Decision{
			Status: http.StatusPaymentRequired,
			Body:   buildBillingDetailsRequiredResponse(err.Message),
		}
	case pkgx402.ErrAuthOnly:
		return &x402Decision{
			Status: http.StatusPaymentRequired,
			Body:   buildInsufficientBalanceResponse(ctx, tenantID, resourceID, "payment required - balance exhausted"),
		}
	case pkgx402.ErrVerificationFailed:
		return &x402Decision{
			Status: http.StatusPaymentRequired,
			Body:   buildPaymentFailedResponse(err.Message),
		}
	case pkgx402.ErrSettlementFailed:
		return &x402Decision{
			Status: http.StatusPaymentRequired,
			Body:   buildPaymentFailedResponse(err.Message),
		}
	default:
		return &x402Decision{
			Status: http.StatusPaymentRequired,
			Body:   buildPaymentFailedResponse(err.Message),
		}
	}
}

func buildInsufficientBalanceResponse(ctx context.Context, tenantID, resourcePath, message string) map[string]any {
	resp := map[string]any{
		"error":     "insufficient_balance",
		"message":   message,
		"code":      "INSUFFICIENT_BALANCE",
		"topup_url": "/account/billing",
	}

	if purserClient == nil {
		return resp
	}

	requirements, err := purserClient.GetPaymentRequirements(ctx, tenantID, resourcePath)
	if err != nil {
		return resp
	}

	if requirements != nil && requirements.Error == "" {
		resp["payment"] = map[string]any{
			"x402_version": requirements.X402Version,
			"accepts":      requirements.Accepts,
		}
	}

	return resp
}

func buildPaymentFailedResponse(message string) map[string]any {
	msg := strings.TrimSpace(message)
	if msg == "" {
		msg = "payment verification or settlement failed"
	}
	return map[string]any{
		"error":   "payment_failed",
		"message": msg,
		"code":    "PAYMENT_FAILED",
	}
}

func buildBillingDetailsRequiredResponse(message string) map[string]any {
	msg := strings.TrimSpace(message)
	if msg == "" {
		msg = "billing details required for this payment"
	}
	return map[string]any{
		"error":   "billing_details_required",
		"message": msg,
		"code":    "BILLING_DETAILS_REQUIRED",
	}
}

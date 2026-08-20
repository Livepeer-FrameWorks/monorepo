package middleware

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"frameworks/api_gateway/internal/clients"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	x402 "github.com/Livepeer-FrameWorks/monorepo/pkg/x402"

	"github.com/gin-gonic/gin"
)

var resolveViewerRegex = regexp.MustCompile(`(?i)resolveviewerendpoint\s*\(\s*contentid\s*:\s*\"([^\"]+)\"`)

type graphqlRequestEnvelope struct {
	Query         string                 `json:"query"`
	OperationName string                 `json:"operationName"`
	Variables     map[string]interface{} `json:"variables"`
}

// ViewerX402Middleware enforces x402 for resolveViewerEndpoint before GraphQL executes.
// This keeps the HTTP 402 response consistent with other platform flows.
func ViewerX402Middleware(serviceClients *clients.ServiceClients, logger logging.Logger) func(c *gin.Context) {
	return func(c *gin.Context) {
		if c.Request == nil || c.Request.Body == nil {
			c.Next()
			return
		}
		if c.Request.Method != http.MethodPost {
			c.Next()
			return
		}

		body, err := io.ReadAll(c.Request.Body)
		if err != nil {
			c.Next()
			return
		}
		c.Request.Body = io.NopCloser(bytes.NewReader(body))

		if !strings.Contains(strings.ToLower(string(body)), "resolveviewerendpoint") {
			c.Next()
			return
		}

		var payload graphqlRequestEnvelope
		if unmarshalErr := json.Unmarshal(body, &payload); unmarshalErr != nil {
			c.Next()
			return
		}

		contentID := resolveContentID(payload)
		if contentID == "" {
			c.Next()
			return
		}

		if serviceClients == nil || serviceClients.Commodore == nil || serviceClients.Quartermaster == nil {
			c.Next()
			return
		}

		resourcePath := "viewer://" + contentID
		resolution, resErr := x402.ResolveResource(c.Request.Context(), resourcePath, serviceClients.Commodore)
		if resErr != nil || resolution == nil || resolution.TenantID == "" {
			c.Next()
			return
		}
		tenantID := resolution.TenantID

		ctx, cancel := context.WithTimeout(c.Request.Context(), 2*time.Second)
		defer cancel()

		status, err := serviceClients.Quartermaster.ValidateTenant(ctx, tenantID, "")
		if err != nil || status == nil || !status.Valid {
			c.Next()
			return
		}

		billingModel := status.BillingModel
		isSuspended := status.IsSuspended
		isBalanceNegative := status.IsBalanceNegative
		x402Processed := false
		if paymentHeader := GetX402PaymentHeader(c.Request); paymentHeader != "" && serviceClients.Purser != nil {
			settleResult, settleErr := x402.SettleX402Payment(c.Request.Context(), x402.SettlementOptions{
				PaymentHeader: paymentHeader,
				Resource:      resourcePath,
				AuthTenantID:  "",
				ClientIP:      ClientIPFromRequest(c.Request),
				Purser:        serviceClients.Purser,
				Commodore:     serviceClients.Commodore,
				Logger:        logger,
				Resolution:    resolution,
			})
			if settleErr != nil {
				c.AbortWithStatusJSON(http.StatusPaymentRequired, viewerX402ErrorResponse(c.Request.Context(), serviceClients, tenantID, resourcePath, settleErr))
				return
			}
			if settleResult != nil {
				x402Processed = true
				if settleResult.X402Version == 2 {
					if paymentResponse, encodeErr := x402.EncodePaymentResponseHeader(settleResult, settleResult.Network); encodeErr == nil {
						c.Header(x402.PaymentResponseHeader, paymentResponse)
					}
				}
			}
		}

		if x402Processed {
			freshStatus, freshErr := serviceClients.Purser.GetTenantBillingStatus(c.Request.Context(), tenantID)
			if freshErr != nil || freshStatus == nil {
				c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{
					"error":   "billing_status_unavailable",
					"message": "payment was processed but the updated owner balance could not be verified; retry safely",
					"code":    "BILLING_STATUS_UNAVAILABLE",
				})
				return
			}
			billingModel = freshStatus.GetBillingModel()
			isSuspended = freshStatus.GetIsSuspended()
			isBalanceNegative = freshStatus.GetIsBalanceNegative()
		}

		if isSuspended || (billingModel == "prepaid" && isBalanceNegative) {
			message := "payment required - stream owner needs to top up balance"
			if isSuspended {
				message = "payment required - owner account suspended"
			}
			response, paymentRequired := buildViewer402ResponseWithHeader(c.Request.Context(), serviceClients, tenantID, resourcePath, message)
			if paymentRequired != "" {
				c.Header(x402.PaymentRequiredHeader, paymentRequired)
			}
			c.AbortWithStatusJSON(http.StatusPaymentRequired, response)
			return
		}

		c.Next()
	}
}

func resolveContentID(payload graphqlRequestEnvelope) string {
	if payload.Variables != nil {
		if v, ok := payload.Variables["contentId"]; ok {
			if s, ok := v.(string); ok {
				return s
			}
		}
		if v, ok := payload.Variables["contentID"]; ok {
			if s, ok := v.(string); ok {
				return s
			}
		}
		if v, ok := payload.Variables["content_id"]; ok {
			if s, ok := v.(string); ok {
				return s
			}
		}
	}
	if payload.Query != "" {
		if matches := resolveViewerRegex.FindStringSubmatch(payload.Query); len(matches) > 1 {
			return matches[1]
		}
	}
	return ""
}

func buildViewer402Response(ctx context.Context, serviceClients *clients.ServiceClients, tenantID, resourcePath, message string) map[string]any {
	response, _ := buildViewer402ResponseWithHeader(ctx, serviceClients, tenantID, resourcePath, message)
	return response
}

func buildViewer402ResponseWithHeader(ctx context.Context, serviceClients *clients.ServiceClients, tenantID, resourcePath, message string) (map[string]any, string) {
	response := map[string]any{
		"error":     "insufficient_balance",
		"message":   message,
		"code":      "INSUFFICIENT_BALANCE",
		"operation": "resolveViewerEndpoint",
		"topup_url": "/account/billing",
	}

	if serviceClients == nil || serviceClients.Purser == nil {
		return response, ""
	}

	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()

	requirements, err := serviceClients.Purser.GetPaymentRequirements(ctx, tenantID, resourcePath)
	if err != nil || requirements == nil {
		return response, ""
	}

	response["x402Version"] = requirements.X402Version
	accepts := make([]map[string]any, 0, len(requirements.Accepts))
	for _, req := range requirements.Accepts {
		accepted := map[string]any{
			"scheme":            req.Scheme,
			"network":           req.Network,
			"payTo":             req.PayTo,
			"asset":             req.Asset,
			"maxTimeoutSeconds": req.MaxTimeoutSeconds,
		}
		if requirements.X402Version == 2 {
			accepted["amount"] = req.Amount
			if len(req.ExtraJson) > 0 {
				accepted["extra"] = json.RawMessage(req.ExtraJson)
			}
		} else {
			accepted["maxAmountRequired"] = req.MaxAmountRequired
			accepted["resource"] = req.Resource
			accepted["description"] = req.Description
		}
		accepts = append(accepts, accepted)
	}
	response["accepts"] = accepts
	if requirements.X402Version != 2 {
		return response, ""
	}
	response["resource"] = map[string]any{
		"url":         requirements.ResourceUrl,
		"description": requirements.ResourceDescription,
		"mimeType":    requirements.ResourceMimeType,
	}
	header, err := x402.EncodePaymentRequiredHeader(requirements)
	if err != nil {
		return response, ""
	}
	return response, header
}

func viewerX402ErrorResponse(ctx context.Context, serviceClients *clients.ServiceClients, tenantID, resourcePath string, err *x402.SettlementError) map[string]any {
	if err == nil {
		return map[string]any{
			"error":     "payment_failed",
			"message":   "payment failed",
			"code":      "X402_PAYMENT_FAILED",
			"topup_url": "/account/billing",
		}
	}
	switch err.Code {
	case x402.ErrBillingDetailsRequired:
		return map[string]any{
			"error":           "billing_details_required",
			"message":         err.Message,
			"code":            "BILLING_DETAILS_REQUIRED",
			"topup_url":       "/account/billing",
			"required_fields": []string{"email", "street", "city", "postal_code", "country"},
		}
	case x402.ErrAuthOnly:
		return buildViewer402Response(ctx, serviceClients, tenantID, resourcePath, "payment required - balance exhausted")
	default:
		message := err.Message
		if message == "" {
			message = "payment failed"
		}
		return map[string]any{
			"error":     "payment_failed",
			"message":   message,
			"code":      "X402_PAYMENT_FAILED",
			"topup_url": "/account/billing",
		}
	}
}

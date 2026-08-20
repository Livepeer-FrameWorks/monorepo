package x402

import (
	"encoding/base64"
	"encoding/json"
	"fmt"

	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
	x402sdk "github.com/x402-foundation/x402/go/v2"
)

const (
	PaymentRequiredHeader  = "PAYMENT-REQUIRED"
	PaymentSignatureHeader = "PAYMENT-SIGNATURE"
	PaymentResponseHeader  = "PAYMENT-RESPONSE"
)

// EncodePaymentRequired serializes the official x402 v2 PaymentRequired
// document. The returned bytes are suitable both for a response body and for
// base64 encoding in PAYMENT-REQUIRED.
func EncodePaymentRequired(requirements *purserpb.PaymentRequirements) ([]byte, error) {
	if requirements == nil {
		return nil, fmt.Errorf("payment requirements are required")
	}
	if len(requirements.GetCanonicalJson()) > 0 {
		return append([]byte(nil), requirements.GetCanonicalJson()...), nil
	}
	accepts := make([]x402sdk.PaymentRequirements, 0, len(requirements.GetAccepts()))
	for _, accepted := range requirements.GetAccepts() {
		if accepted == nil {
			continue
		}
		extra := map[string]interface{}{}
		if len(accepted.GetExtraJson()) > 0 {
			if err := json.Unmarshal(accepted.GetExtraJson(), &extra); err != nil {
				return nil, fmt.Errorf("decode accepted.extra: %w", err)
			}
		}
		amount := accepted.GetAmount()
		if amount == "" {
			amount = accepted.GetMaxAmountRequired()
		}
		accepts = append(accepts, x402sdk.PaymentRequirements{
			Scheme:            accepted.GetScheme(),
			Network:           accepted.GetNetwork(),
			Asset:             accepted.GetAsset(),
			Amount:            amount,
			PayTo:             accepted.GetPayTo(),
			MaxTimeoutSeconds: int(accepted.GetMaxTimeoutSeconds()),
			Extra:             extra,
		})
	}
	document := x402sdk.PaymentRequired{
		X402Version: 2,
		Error:       requirements.GetError(),
		Accepts:     accepts,
	}
	if requirements.GetResourceUrl() != "" {
		document.Resource = &x402sdk.ResourceInfo{
			URL:         requirements.GetResourceUrl(),
			Description: requirements.GetResourceDescription(),
			MimeType:    requirements.GetResourceMimeType(),
		}
	}
	return json.Marshal(document)
}

func EncodePaymentRequiredHeader(requirements *purserpb.PaymentRequirements) (string, error) {
	document, err := EncodePaymentRequired(requirements)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(document), nil
}

// EncodePaymentResponseHeader emits the standard v2 settlement receipt after
// Purser has confirmed and credited the payment.
func EncodePaymentResponseHeader(result *SettlementResult, network string) (string, error) {
	if result == nil || result.Settle == nil || !result.Settle.GetSuccess() {
		return "", fmt.Errorf("successful settlement result is required")
	}
	response := x402sdk.SettleResponse{
		Success:     true,
		Transaction: result.Settle.GetTxHash(),
		Network:     x402sdk.Network(network),
		Payer:       result.PayerAddress,
	}
	document, err := json.Marshal(response)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(document), nil
}

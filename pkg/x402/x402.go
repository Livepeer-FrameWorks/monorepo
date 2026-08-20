//nolint:govet,errcheck // Protocol envelope decoding uses local scopes and optional extension type assertions.
package x402

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"

	x402pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/x402"
	x402types "github.com/x402-foundation/x402/go/v2/types"
	"google.golang.org/grpc/metadata"
)

type paymentHeaderAuthorizationWire struct {
	From        string `json:"from"`
	To          string `json:"to"`
	Value       string `json:"value"`
	ValidAfter  string `json:"validAfter"`
	ValidBefore string `json:"validBefore"`
	Nonce       string `json:"nonce"`
}

type paymentHeaderExactPayloadWire struct {
	Signature     string                         `json:"signature"`
	Authorization paymentHeaderAuthorizationWire `json:"authorization"`
}

type paymentHeaderWire struct {
	X402Version int                           `json:"x402Version"`
	Scheme      string                        `json:"scheme"`
	Network     string                        `json:"network"`
	Payload     paymentHeaderExactPayloadWire `json:"payload"`
}

func (w paymentHeaderWire) toProto() *x402pb.X402PaymentPayload {
	return &x402pb.X402PaymentPayload{
		X402Version: int32(w.X402Version),
		Scheme:      w.Scheme,
		Network:     w.Network,
		Payload: &x402pb.X402ExactPayload{
			Signature: w.Payload.Signature,
			Authorization: &x402pb.X402Authorization{
				From:        w.Payload.Authorization.From,
				To:          w.Payload.Authorization.To,
				Value:       w.Payload.Authorization.Value,
				ValidAfter:  w.Payload.Authorization.ValidAfter,
				ValidBefore: w.Payload.Authorization.ValidBefore,
				Nonce:       w.Payload.Authorization.Nonce,
			},
		},
	}
}

// GetPaymentHeaderFromRequest returns the x402 payment header from an HTTP request.
// Accepts both X-PAYMENT and PAYMENT-SIGNATURE.
func GetPaymentHeaderFromRequest(r *http.Request) string {
	if r == nil {
		return ""
	}
	return GetPaymentHeaderFromHeaders(r.Header)
}

// GetPaymentHeaderFromHeaders returns the x402 payment header from HTTP headers.
// Accepts both X-PAYMENT and PAYMENT-SIGNATURE.
func GetPaymentHeaderFromHeaders(headers http.Header) string {
	if headers == nil {
		return ""
	}
	// PAYMENT-SIGNATURE is the x402 v2 header. X-PAYMENT is accepted only as a
	// v1 compatibility fallback and must not override a standards-compliant
	// client when an intermediary forwards both.
	if value := strings.TrimSpace(headers.Get("PAYMENT-SIGNATURE")); value != "" {
		return value
	}
	if value := strings.TrimSpace(headers.Get("X-PAYMENT")); value != "" {
		return value
	}
	return ""
}

// GetPaymentHeaderFromContext returns the x402 payment header from gRPC metadata.
// Accepts both x-payment and payment-signature keys.
func GetPaymentHeaderFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok || md == nil {
		return ""
	}
	if values := md.Get("payment-signature"); len(values) > 0 {
		if value := strings.TrimSpace(values[0]); value != "" {
			return value
		}
	}
	if values := md.Get("x-payment"); len(values) > 0 {
		if value := strings.TrimSpace(values[0]); value != "" {
			return value
		}
	}
	return ""
}

// ParsePaymentHeader decodes and parses an x402 payment header value.
func ParsePaymentHeader(header string) (*x402pb.X402PaymentPayload, error) {
	payloadBytes, err := base64Decode(header)
	if err != nil {
		return nil, err
	}

	var envelope struct {
		X402Version int `json:"x402Version"`
	}
	if err := json.Unmarshal(payloadBytes, &envelope); err != nil {
		return nil, err
	}
	if envelope.X402Version == 2 {
		return parseV2PaymentPayload(payloadBytes)
	}

	var payload paymentHeaderWire
	if err := json.Unmarshal(payloadBytes, &payload); err != nil {
		return nil, err
	}
	parsed := payload.toProto()
	parsed.CanonicalPayloadJson = append([]byte(nil), payloadBytes...)
	return parsed, nil

}

func parseV2PaymentPayload(payloadBytes []byte) (*x402pb.X402PaymentPayload, error) {
	payload, err := x402types.ToPaymentPayload(payloadBytes)
	if err != nil {
		return nil, err
	}
	payloadJSON, err := json.Marshal(payload.Payload)
	if err != nil {
		return nil, err
	}
	var exact paymentHeaderExactPayloadWire
	if err := json.Unmarshal(payloadJSON, &exact); err != nil {
		return nil, err
	}
	extraJSON, err := json.Marshal(payload.Accepted.Extra)
	if err != nil {
		return nil, err
	}
	quoteID := quoteIDFromExtra(payload.Accepted.Extra)
	return &x402pb.X402PaymentPayload{
		X402Version: int32(payload.X402Version),
		Scheme:      payload.Accepted.Scheme,
		Network:     payload.Accepted.Network,
		Payload: &x402pb.X402ExactPayload{
			Signature: exact.Signature,
			Authorization: &x402pb.X402Authorization{
				From:        exact.Authorization.From,
				To:          exact.Authorization.To,
				Value:       exact.Authorization.Value,
				ValidAfter:  exact.Authorization.ValidAfter,
				ValidBefore: exact.Authorization.ValidBefore,
				Nonce:       exact.Authorization.Nonce,
			},
		},
		CanonicalPayloadJson: append([]byte(nil), payloadBytes...),
		Accepted: &x402pb.X402AcceptedRequirements{
			Scheme:            payload.Accepted.Scheme,
			Network:           payload.Accepted.Network,
			Asset:             payload.Accepted.Asset,
			Amount:            payload.Accepted.Amount,
			PayTo:             payload.Accepted.PayTo,
			MaxTimeoutSeconds: int32(payload.Accepted.MaxTimeoutSeconds),
			ExtraJson:         extraJSON,
		},
		QuoteId: quoteID,
	}, nil
}

func quoteIDFromExtra(extra map[string]interface{}) string {
	if quoteID, ok := extra["quoteId"].(string); ok {
		return strings.TrimSpace(quoteID)
	}
	frameworks, ok := extra["frameworks"].(map[string]interface{})
	if !ok {
		return ""
	}
	quoteID, _ := frameworks["quoteId"].(string)
	return strings.TrimSpace(quoteID)
}

func base64Decode(s string) ([]byte, error) {
	if decoded, err := base64.StdEncoding.DecodeString(s); err == nil {
		return decoded, nil
	}
	if decoded, err := base64.URLEncoding.DecodeString(s); err == nil {
		return decoded, nil
	}
	if decoded, err := base64.RawURLEncoding.DecodeString(s); err == nil {
		return decoded, nil
	}
	return base64.RawStdEncoding.DecodeString(s)
}

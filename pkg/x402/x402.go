package x402

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"

	pb "frameworks/pkg/proto"

	"google.golang.org/grpc/metadata"
)

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
	if value := strings.TrimSpace(headers.Get("X-PAYMENT")); value != "" {
		return value
	}
	if value := strings.TrimSpace(headers.Get("PAYMENT-SIGNATURE")); value != "" {
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
	if values := md.Get("x-payment"); len(values) > 0 {
		if value := strings.TrimSpace(values[0]); value != "" {
			return value
		}
	}
	if values := md.Get("payment-signature"); len(values) > 0 {
		if value := strings.TrimSpace(values[0]); value != "" {
			return value
		}
	}
	return ""
}

// ParsePaymentHeader decodes and parses an x402 payment header value.
func ParsePaymentHeader(header string) (*pb.X402PaymentPayload, error) {
	payloadBytes, err := base64Decode(header)
	if err != nil {
		return nil, err
	}

	var payload struct {
		X402Version int    `json:"x402Version"`
		Scheme      string `json:"scheme"`
		Network     string `json:"network"`
		Payload     struct {
			Signature     string `json:"signature"`
			Authorization struct {
				From        string `json:"from"`
				To          string `json:"to"`
				Value       string `json:"value"`
				ValidAfter  string `json:"validAfter"`
				ValidBefore string `json:"validBefore"`
				Nonce       string `json:"nonce"`
			} `json:"authorization"`
		} `json:"payload"`
	}

	if err := json.Unmarshal(payloadBytes, &payload); err != nil {
		return nil, err
	}

	return &pb.X402PaymentPayload{
		X402Version: int32(payload.X402Version),
		Scheme:      payload.Scheme,
		Network:     payload.Network,
		Payload: &pb.X402ExactPayload{
			Signature: payload.Payload.Signature,
			Authorization: &pb.X402Authorization{
				From:        payload.Payload.Authorization.From,
				To:          payload.Payload.Authorization.To,
				Value:       payload.Payload.Authorization.Value,
				ValidAfter:  payload.Payload.Authorization.ValidAfter,
				ValidBefore: payload.Payload.Authorization.ValidBefore,
				Nonce:       payload.Payload.Authorization.Nonce,
			},
		},
	}, nil
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

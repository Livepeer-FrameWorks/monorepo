package x402

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"testing"

	"google.golang.org/grpc/metadata"
)

func TestGetPaymentHeaderFromRequest(t *testing.T) {
	t.Run("nil request", func(t *testing.T) {
		if got := GetPaymentHeaderFromRequest(nil); got != "" {
			t.Errorf("expected empty string, got %q", got)
		}
	})

	t.Run("X-PAYMENT header", func(t *testing.T) {
		req, _ := http.NewRequestWithContext(context.Background(), "GET", "/", nil)
		req.Header.Set("X-PAYMENT", "test-payment")
		if got := GetPaymentHeaderFromRequest(req); got != "test-payment" {
			t.Errorf("got %q, want %q", got, "test-payment")
		}
	})

	t.Run("PAYMENT-SIGNATURE header", func(t *testing.T) {
		req, _ := http.NewRequestWithContext(context.Background(), "GET", "/", nil)
		req.Header.Set("PAYMENT-SIGNATURE", "test-sig")
		if got := GetPaymentHeaderFromRequest(req); got != "test-sig" {
			t.Errorf("got %q, want %q", got, "test-sig")
		}
	})

	t.Run("X-PAYMENT takes precedence", func(t *testing.T) {
		req, _ := http.NewRequestWithContext(context.Background(), "GET", "/", nil)
		req.Header.Set("X-PAYMENT", "x-payment-value")
		req.Header.Set("PAYMENT-SIGNATURE", "sig-value")
		if got := GetPaymentHeaderFromRequest(req); got != "x-payment-value" {
			t.Errorf("got %q, want %q", got, "x-payment-value")
		}
	})

	t.Run("whitespace is trimmed", func(t *testing.T) {
		req, _ := http.NewRequestWithContext(context.Background(), "GET", "/", nil)
		req.Header.Set("X-PAYMENT", "  trimmed  ")
		if got := GetPaymentHeaderFromRequest(req); got != "trimmed" {
			t.Errorf("got %q, want %q", got, "trimmed")
		}
	})
}

func TestGetPaymentHeaderFromHeaders(t *testing.T) {
	t.Run("nil headers", func(t *testing.T) {
		if got := GetPaymentHeaderFromHeaders(nil); got != "" {
			t.Errorf("expected empty string, got %q", got)
		}
	})

	t.Run("empty headers", func(t *testing.T) {
		if got := GetPaymentHeaderFromHeaders(http.Header{}); got != "" {
			t.Errorf("expected empty string, got %q", got)
		}
	})
}

func TestGetPaymentHeaderFromContext(t *testing.T) {
	t.Run("nil context", func(t *testing.T) {
		//nolint:staticcheck // Testing nil context handling
		if got := GetPaymentHeaderFromContext(nil); got != "" {
			t.Errorf("expected empty string, got %q", got)
		}
	})

	t.Run("context without metadata", func(t *testing.T) {
		ctx := context.Background()
		if got := GetPaymentHeaderFromContext(ctx); got != "" {
			t.Errorf("expected empty string, got %q", got)
		}
	})

	t.Run("x-payment in metadata", func(t *testing.T) {
		md := metadata.Pairs("x-payment", "grpc-payment")
		ctx := metadata.NewIncomingContext(context.Background(), md)
		if got := GetPaymentHeaderFromContext(ctx); got != "grpc-payment" {
			t.Errorf("got %q, want %q", got, "grpc-payment")
		}
	})

	t.Run("payment-signature in metadata", func(t *testing.T) {
		md := metadata.Pairs("payment-signature", "grpc-sig")
		ctx := metadata.NewIncomingContext(context.Background(), md)
		if got := GetPaymentHeaderFromContext(ctx); got != "grpc-sig" {
			t.Errorf("got %q, want %q", got, "grpc-sig")
		}
	})

	t.Run("x-payment takes precedence", func(t *testing.T) {
		md := metadata.Pairs("x-payment", "x-val", "payment-signature", "sig-val")
		ctx := metadata.NewIncomingContext(context.Background(), md)
		if got := GetPaymentHeaderFromContext(ctx); got != "x-val" {
			t.Errorf("got %q, want %q", got, "x-val")
		}
	})
}

func TestParsePaymentHeader(t *testing.T) {
	validPayload := map[string]interface{}{
		"x402Version": 1,
		"scheme":      "exact",
		"network":     "base-mainnet",
		"payload": map[string]interface{}{
			"signature": "0xabc123",
			"authorization": map[string]interface{}{
				"from":        "0xFromAddress",
				"to":          "0xToAddress",
				"value":       "1000000",
				"validAfter":  "0",
				"validBefore": "9999999999",
				"nonce":       "12345",
			},
		},
	}
	payloadJSON, _ := json.Marshal(validPayload)

	t.Run("standard base64", func(t *testing.T) {
		encoded := base64.StdEncoding.EncodeToString(payloadJSON)
		result, err := ParsePaymentHeader(encoded)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result.X402Version != 1 {
			t.Errorf("X402Version = %d, want 1", result.X402Version)
		}
		if result.Scheme != "exact" {
			t.Errorf("Scheme = %q, want %q", result.Scheme, "exact")
		}
		if result.Network != "base-mainnet" {
			t.Errorf("Network = %q, want %q", result.Network, "base-mainnet")
		}
		if result.Payload == nil {
			t.Fatal("Payload should not be nil")
		}
		if result.Payload.Signature != "0xabc123" {
			t.Errorf("Signature = %q, want %q", result.Payload.Signature, "0xabc123")
		}
		if result.Payload.Authorization == nil {
			t.Fatal("Authorization should not be nil")
		}
		if result.Payload.Authorization.From != "0xFromAddress" {
			t.Errorf("From = %q, want %q", result.Payload.Authorization.From, "0xFromAddress")
		}
	})

	t.Run("url-safe base64", func(t *testing.T) {
		encoded := base64.URLEncoding.EncodeToString(payloadJSON)
		result, err := ParsePaymentHeader(encoded)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result.X402Version != 1 {
			t.Errorf("X402Version = %d, want 1", result.X402Version)
		}
	})

	t.Run("raw url-safe base64", func(t *testing.T) {
		encoded := base64.RawURLEncoding.EncodeToString(payloadJSON)
		result, err := ParsePaymentHeader(encoded)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result.X402Version != 1 {
			t.Errorf("X402Version = %d, want 1", result.X402Version)
		}
	})

	t.Run("invalid base64", func(t *testing.T) {
		_, err := ParsePaymentHeader("!!!not-base64!!!")
		if err == nil {
			t.Error("expected error for invalid base64")
		}
	})

	t.Run("invalid json", func(t *testing.T) {
		encoded := base64.StdEncoding.EncodeToString([]byte("not json"))
		_, err := ParsePaymentHeader(encoded)
		if err == nil {
			t.Error("expected error for invalid JSON")
		}
	})
}

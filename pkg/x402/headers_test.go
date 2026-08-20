package x402

import (
	"encoding/base64"
	"encoding/json"
	"testing"

	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
	x402sdk "github.com/x402-foundation/x402/go/v2"
)

func TestEncodePaymentRequiredHeaderUsesV2Shape(t *testing.T) {
	requirements := &purserpb.PaymentRequirements{
		X402Version:         2,
		ResourceUrl:         "https://api.example.com/graphql",
		ResourceDescription: "FrameWorks prepaid API credit",
		ResourceMimeType:    "application/json",
		Accepts: []*purserpb.PaymentRequirement{{
			Scheme:            "exact",
			Network:           "eip155:8453",
			Asset:             "0xAsset",
			Amount:            "5000000",
			PayTo:             "0xPayTo",
			MaxTimeoutSeconds: 60,
			ExtraJson:         []byte(`{"frameworks":{"quoteId":"quote-1"}}`),
		}},
	}
	header, err := EncodePaymentRequiredHeader(requirements)
	if err != nil {
		t.Fatalf("EncodePaymentRequiredHeader() error = %v", err)
	}
	document, err := base64.StdEncoding.DecodeString(header)
	if err != nil {
		t.Fatalf("decode header: %v", err)
	}
	var decoded x402sdk.PaymentRequired
	if err := json.Unmarshal(document, &decoded); err != nil {
		t.Fatalf("decode document: %v", err)
	}
	if decoded.X402Version != 2 {
		t.Fatalf("x402Version = %v, want 2", decoded.X402Version)
	}
	if len(decoded.Accepts) != 1 {
		t.Fatalf("accepts = %+v", decoded.Accepts)
	}
	accepted := decoded.Accepts[0]
	if accepted.Amount != "5000000" || accepted.Network != "eip155:8453" {
		t.Fatalf("unexpected accepted requirement: %+v", accepted)
	}
	var raw map[string]any
	if err := json.Unmarshal(document, &raw); err != nil {
		t.Fatal(err)
	}
	if _, exists := raw["accepts"].([]any)[0].(map[string]any)["maxAmountRequired"]; exists {
		t.Fatal("v1 maxAmountRequired leaked into v2 document")
	}
}

func TestEncodePaymentResponseHeaderUsesOfficialV2Shape(t *testing.T) {
	header, err := EncodePaymentResponseHeader(&SettlementResult{
		PayerAddress: "0x1111111111111111111111111111111111111111",
		Settle: &purserpb.SettleX402PaymentResponse{
			Success: true,
			TxHash:  "0xabc",
		},
	}, "eip155:8453")
	if err != nil {
		t.Fatal(err)
	}
	document, err := base64.StdEncoding.DecodeString(header)
	if err != nil {
		t.Fatal(err)
	}
	var decoded x402sdk.SettleResponse
	if err := json.Unmarshal(document, &decoded); err != nil {
		t.Fatal(err)
	}
	if !decoded.Success || decoded.Transaction != "0xabc" || decoded.Network != "eip155:8453" || decoded.Payer != "0x1111111111111111111111111111111111111111" {
		t.Fatalf("unexpected PAYMENT-RESPONSE: %+v", decoded)
	}
}

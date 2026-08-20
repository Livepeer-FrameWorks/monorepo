package x402

import (
	"encoding/base64"
	"encoding/json"
	"testing"

	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
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
	var decoded map[string]interface{}
	if err := json.Unmarshal(document, &decoded); err != nil {
		t.Fatalf("decode document: %v", err)
	}
	if decoded["x402Version"] != float64(2) {
		t.Fatalf("x402Version = %v, want 2", decoded["x402Version"])
	}
	accepted := decoded["accepts"].([]interface{})[0].(map[string]interface{})
	if accepted["amount"] != "5000000" || accepted["network"] != "eip155:8453" {
		t.Fatalf("unexpected accepted requirement: %+v", accepted)
	}
	if _, exists := accepted["maxAmountRequired"]; exists {
		t.Fatal("v1 maxAmountRequired leaked into v2 document")
	}
}

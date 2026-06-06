package handlers

import (
	"encoding/json"
	"testing"
)

func samplePayload() *X402PaymentPayload {
	return &X402PaymentPayload{
		X402Version: 1,
		Scheme:      "exact",
		Network:     "base",
		Payload: &X402ExactPayload{
			Signature: "0xsig",
			Authorization: &X402Authorization{
				From:        "0xfrom",
				To:          "0xto",
				Value:       "1000",
				ValidAfter:  "0",
				ValidBefore: "9999999999",
				Nonce:       "0xnonce",
			},
		},
	}
}

func TestSameX402Payload(t *testing.T) {
	// Intent: this is the replay/idempotency guard — a reused nonce is only
	// accepted as "the same settlement" when every signed field matches. Any
	// divergence (or an unparsable stored blob, or missing nested structs) must
	// return false so a different authorization can never settle on a used nonce.
	mustJSON := func(p *X402PaymentPayload) string {
		b, err := json.Marshal(p)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		return string(b)
	}

	t.Run("identical payloads match", func(t *testing.T) {
		stored := mustJSON(samplePayload())
		if !sameX402Payload(stored, samplePayload()) {
			t.Fatal("identical payloads must be considered the same")
		}
	})

	t.Run("any signed-field divergence does not match", func(t *testing.T) {
		stored := mustJSON(samplePayload())
		mutations := map[string]func(*X402PaymentPayload){
			"version":     func(p *X402PaymentPayload) { p.X402Version = 2 },
			"scheme":      func(p *X402PaymentPayload) { p.Scheme = "other" },
			"network":     func(p *X402PaymentPayload) { p.Network = "mainnet" },
			"signature":   func(p *X402PaymentPayload) { p.Payload.Signature = "0xother" },
			"from":        func(p *X402PaymentPayload) { p.Payload.Authorization.From = "0xevil" },
			"to":          func(p *X402PaymentPayload) { p.Payload.Authorization.To = "0xevil" },
			"value":       func(p *X402PaymentPayload) { p.Payload.Authorization.Value = "2000" },
			"validAfter":  func(p *X402PaymentPayload) { p.Payload.Authorization.ValidAfter = "1" },
			"validBefore": func(p *X402PaymentPayload) { p.Payload.Authorization.ValidBefore = "1" },
			"nonce":       func(p *X402PaymentPayload) { p.Payload.Authorization.Nonce = "0xdifferent" },
		}
		for field, mutate := range mutations {
			t.Run(field, func(t *testing.T) {
				cur := samplePayload()
				mutate(cur)
				if sameX402Payload(stored, cur) {
					t.Fatalf("divergent %s must not match", field)
				}
			})
		}
	})

	t.Run("unparsable stored blob does not match", func(t *testing.T) {
		if sameX402Payload("{not-json", samplePayload()) {
			t.Fatal("unparsable stored payload must not match")
		}
	})

	t.Run("missing nested structs do not match", func(t *testing.T) {
		stored := mustJSON(samplePayload())
		if sameX402Payload(stored, &X402PaymentPayload{}) {
			t.Fatal("current payload without authorization must not match")
		}
		nilAuthStored := mustJSON(&X402PaymentPayload{X402Version: 1})
		if sameX402Payload(nilAuthStored, samplePayload()) {
			t.Fatal("stored payload without authorization must not match")
		}
	})
}

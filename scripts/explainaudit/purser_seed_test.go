package main

import (
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
)

func TestProviderInboxSeedPreservesKindOwnedFormats(t *testing.T) {
	for row := 0; row < 18; row++ {
		provider, ok := providerInboxSeedValue("provider", row)
		if !ok {
			t.Fatal("missing provider fixture")
		}
		payload, ok := providerInboxSeedValue("raw_payload", row)
		if !ok || !strings.HasPrefix(payload, `\x`) {
			t.Fatal("missing bytea payload")
		}
		raw, err := hex.DecodeString(payload[2:])
		if err != nil {
			t.Fatal(err)
		}
		var body map[string]any
		if provider == "mollie" {
			if json.Valid(raw) || !strings.HasPrefix(string(raw), "id=tr_fixture_") {
				t.Fatalf("Mollie callback must exercise form data, got %s", raw)
			}
		} else {
			if err := json.Unmarshal(raw, &body); err != nil {
				t.Fatalf("%s payload: %v", provider, err)
			}
			if provider != "stripe" && body["tenant_id"] == nil {
				t.Fatalf("%s internal record has no tenant UUID", provider)
			}
		}
		headers, ok := providerInboxSeedValue("headers", row)
		var h map[string]string
		if !ok || json.Unmarshal([]byte(headers), &h) != nil {
			t.Fatal("invalid fixture headers")
		}
		if (provider == "stripe" || provider == "mollie") && h["tenant_id"] != "" {
			t.Fatal("native callback headers exceeded the ingress whitelist")
		}
		if provider != "stripe" && provider != "mollie" && h["tenant_id"] != body["tenant_id"] {
			t.Fatalf("%s tenant header does not match its internal payload", provider)
		}
	}
	if _, ok := providerInboxSeedValue("attempts", 0); ok {
		t.Fatal("payload shaping must not change failure/backlog cardinality")
	}
}

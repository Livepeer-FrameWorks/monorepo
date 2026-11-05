package mollie

import (
	"context"
	"encoding/json"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/billing"
	"net/http"
	"strings"
	"testing"
)

func TestMollieTaxTermsCollectGrossAndFreezeRenewalMetadata(t *testing.T) {
	terms, err := NewTaxTerms(strings.Repeat("a", 64), 10000, 2100)
	if err != nil {
		t.Fatal(err)
	}
	if terms.GrossMinor != 12100 {
		t.Fatal("VAT not added on top")
	}
	patches, posts := 0, 0
	metadata := map[string]any{"tenant_id": "tenant", "tier_id": "old"}
	value := "100.00"
	client := newMollieContractClient(t, func(r *http.Request) (*http.Response, error) {
		if r.Method == http.MethodGet {
			body, _ := json.Marshal(map[string]any{"id": "subscription", "status": "active", "amount": map[string]string{"currency": "EUR", "value": value}, "metadata": metadata})
			return mollieJSONResponse(200, string(body)), nil
		}
		body := decodeMollieRequest(t, r)
		amount := body["amount"].(map[string]any)
		meta := body["metadata"].(map[string]any)
		if meta["frameworks_tax_profile"] != terms.ProfileSHA || meta["frameworks_vat_bps"] != "2100" {
			t.Fatal("collection lost frozen proof")
		}
		if r.Method == http.MethodPatch {
			patches++
			metadata = meta
			value = amount["value"].(string)
		} else {
			posts++
			if amount["value"] != "121.00" {
				t.Fatal("new collection is not gross")
			}
		}
		result, _ := json.Marshal(map[string]any{"id": "subscription", "status": "active", "amount": amount, "metadata": meta})
		return mollieJSONResponse(200, string(result)), nil
	})
	if _, err = client.CreateSubscription(context.Background(), SubscriptionParams{CustomerID: "customer", TenantID: "tenant", Amount: Amount("121.00", "EUR"), TaxTerms: terms, IdempotencyKey: "new-sub"}); err != nil {
		t.Fatal(err)
	}
	if err = client.SyncSubscriptionTax(context.Background(), "tenant", "customer", "subscription", *terms); err != nil {
		t.Fatal(err)
	}
	if err = client.SyncSubscriptionTax(context.Background(), "tenant", "customer", "subscription", *terms); err != nil {
		t.Fatal(err)
	}
	if patches != 1 || posts != 1 {
		t.Fatal("provider retry created another update or charge")
	}
	if _, err = client.CreateSubscription(context.Background(), SubscriptionParams{Amount: Amount("100.00", "EUR"), TaxTerms: terms, IdempotencyKey: "bad"}); err == nil {
		t.Fatal("net amount accepted as gross")
	}
	if err = client.SyncSubscriptionTax(context.Background(), "foreign", "customer", "subscription", *terms); err == nil {
		t.Fatal("foreign tenant changed renewal")
	}
	// A tier change retains the VAT rate and updates its net basis atomically.
	if _, err = client.SyncSubscriptionTier(context.Background(), billing.ProviderTierRequest{TenantID: "tenant", CustomerID: "customer", SubscriptionID: "subscription", CurrentTierID: "old", TierID: "new", BaseCents: 5000, Downgrade: true}); err != nil {
		t.Fatal(err)
	}
	if value != "60.50" || metadata["frameworks_net_minor"] != "5000" {
		t.Fatal("tier change dropped VAT or retained old net basis")
	}
}

func TestMollieTaxSyncNormalizesLegacyDowngradeRestoreAmount(t *testing.T) {
	metadata := map[string]any{"tenant_id": "tenant", "tier_id": "lower", restoreTierKey: "original", restoreAmountKey: "100.00"}
	value := "50.00"
	client := newMollieContractClient(t, func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodGet {
			request := decodeMollieRequest(t, r)
			metadata = request["metadata"].(map[string]any)
			value = request["amount"].(map[string]any)["value"].(string)
		}
		raw, err := json.Marshal(map[string]any{"id": "subscription", "status": "active", "amount": map[string]string{"currency": "EUR", "value": value}, "metadata": metadata})
		if err != nil {
			t.Fatal(err)
		}
		return mollieJSONResponse(200, string(raw)), nil
	})
	terms, err := NewTaxTerms(strings.Repeat("a", 64), 5000, 2100)
	if err != nil {
		t.Fatal(err)
	}
	if err = client.SyncSubscriptionTax(context.Background(), "tenant", "customer", "subscription", *terms); err != nil {
		t.Fatal(err)
	}
	if value != "60.50" || metadata[restoreAmountKey] != "121.00" || metadata["billing_restore_net_minor"] != "10000" {
		t.Fatalf("legacy downgrade lost original net tax basis: current=%s restore=%v net=%v", value, metadata[restoreAmountKey], metadata["billing_restore_net_minor"])
	}
	if _, err = client.SyncSubscriptionTier(context.Background(), billing.ProviderTierRequest{TenantID: "tenant", CustomerID: "customer", SubscriptionID: "subscription", TierID: "original", CurrentTierID: "lower", BaseCents: 10000}); err != nil {
		t.Fatal(err)
	}
	if value != "121.00" || metadata["frameworks_net_minor"] != "10000" {
		t.Fatal("cancelled legacy downgrade restored an untaxed amount")
	}
}

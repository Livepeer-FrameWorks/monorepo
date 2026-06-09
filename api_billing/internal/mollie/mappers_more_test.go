package mollie

import (
	"testing"
	"time"

	mollielib "github.com/VictorAvelar/mollie-api-go/v4/mollie"
)

func TestExtractMandateInfo(t *testing.T) {
	c := &Client{}
	created := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	mandate := &mollielib.Mandate{
		ID:     "mdt_1",
		Status: "valid",
		Method: "directdebit",
		Details: mollielib.MandateDetails{
			ConsumerName:    "Acme BV",
			ConsumerAccount: "NL55INGB0000000000",
			// ConsumerBic intentionally empty -> must be omitted from details.
		},
		CreatedAt: &created,
	}

	info := c.ExtractMandateInfo(mandate, "cst_9")
	if info.MollieMandateID != "mdt_1" || info.MollieCustomerID != "cst_9" {
		t.Fatalf("identity fields wrong: %+v", info)
	}
	if info.Status != "valid" || info.Method != "directdebit" {
		t.Fatalf("status/method not stringified: %+v", info)
	}
	if !info.CreatedAt.Equal(created) {
		t.Fatalf("created_at not carried: %v", info.CreatedAt)
	}
	if info.Details["consumer_name"] != "Acme BV" || info.Details["consumer_account"] != "NL55INGB0000000000" {
		t.Fatalf("details not populated: %+v", info.Details)
	}
	// Empty source fields are omitted, not stored as "".
	if _, ok := info.Details["consumer_bic"]; ok {
		t.Fatalf("empty consumer_bic should be omitted: %+v", info.Details)
	}
}

func TestExtractMandateInfoNilCreatedAt(t *testing.T) {
	c := &Client{}
	info := c.ExtractMandateInfo(&mollielib.Mandate{ID: "mdt_2"}, "cst_1")
	// Nil CreatedAt yields the zero time, not a panic.
	if !info.CreatedAt.IsZero() {
		t.Fatalf("nil createdAt should be zero time, got %v", info.CreatedAt)
	}
}

func TestExtractSubscriptionInfo(t *testing.T) {
	c := &Client{}
	next := &mollielib.ShortDate{Time: time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)}
	sub := &mollielib.Subscription{
		ID:              "sub_1",
		Status:          "active",
		Interval:        "1 month",
		Amount:          &mollielib.Amount{Value: "10.00", Currency: "EUR"},
		NextPaymentDate: next,
		Metadata:        map[string]any{"tenant_id": "tenant-1", "tier_id": "tier-pro"},
	}

	info := c.ExtractSubscriptionInfo(sub, "cst_2")
	if info.MollieSubscriptionID != "sub_1" || info.MollieCustomerID != "cst_2" {
		t.Fatalf("identity fields wrong: %+v", info)
	}
	if info.Status != "active" || info.Interval != "1 month" {
		t.Fatalf("status/interval wrong: %+v", info)
	}
	if info.Amount != "10.00" || info.Currency != "EUR" {
		t.Fatalf("amount/currency not unpacked: %+v", info)
	}
	if info.NextPaymentDate == "" {
		t.Fatalf("next payment date should be set: %+v", info)
	}
	// tenant_id/tier_id are lifted out of the metadata map.
	if info.TenantID != "tenant-1" || info.TierID != "tier-pro" {
		t.Fatalf("metadata not extracted: %+v", info)
	}
}

func TestExtractSubscriptionInfoNilAmountAndMetadata(t *testing.T) {
	c := &Client{}
	info := c.ExtractSubscriptionInfo(&mollielib.Subscription{ID: "sub_3"}, "cst_3")
	// Nil amount/metadata must leave the fields empty, not panic.
	if info.Amount != "" || info.Currency != "" || info.TenantID != "" {
		t.Fatalf("nil amount/metadata should leave fields empty: %+v", info)
	}
}

func TestAmountHelper(t *testing.T) {
	a := Amount("5.00", "USD")
	if a == nil || a.Value != "5.00" || a.Currency != "USD" {
		t.Fatalf("Amount helper wrong: %+v", a)
	}
}

func TestMollieURLRequiresInitializedClient(t *testing.T) {
	c := &Client{}
	if _, err := c.mollieURL("/v2/payments"); err == nil {
		t.Fatal("uninitialized client should error")
	}
}

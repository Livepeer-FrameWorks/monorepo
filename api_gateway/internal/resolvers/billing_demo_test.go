package resolvers

import (
	"testing"

	"frameworks/api_gateway/internal/demo"
)

func TestDemoTenantUsageUsesInvoicePreviewLineItems(t *testing.T) {
	preview := demo.GenerateInvoicePreview()
	usage, err := demoTenantUsageFromInvoicePreview(preview)
	if err != nil {
		t.Fatalf("demoTenantUsageFromInvoicePreview: %v", err)
	}

	if usage.UsageAmount != "0.64" {
		t.Fatalf("UsageAmount = %q, want 0.64", usage.UsageAmount)
	}
	if usage.TotalCost != preview.MeteredAmount {
		t.Fatalf("TotalCost = %v, want %v", usage.TotalCost, preview.MeteredAmount)
	}
	for _, line := range usage.LineItems {
		if line.LineKey == "base_subscription" {
			t.Fatal("demo tenant usage should expose metered line items only")
		}
	}
	if len(usage.LineItems) != len(usage.Usage) || len(usage.LineItems) != len(usage.Costs) {
		t.Fatalf("line/usage/cost counts = %d/%d/%d", len(usage.LineItems), len(usage.Usage), len(usage.Costs))
	}
}

func TestDemoInvoicePaymentAmountRequiresKnownInvoice(t *testing.T) {
	amount, currency, ok := demoInvoicePaymentAmount("inv_demo_current_001")
	if !ok {
		t.Fatal("expected known demo invoice")
	}
	if amount == 0 || currency != "EUR" {
		t.Fatalf("demo invoice payment amount = %v %s", amount, currency)
	}

	if _, _, ok := demoInvoicePaymentAmount("missing_invoice"); ok {
		t.Fatal("unknown demo invoice should not synthesize an amount")
	}
}

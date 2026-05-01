package handlers

import (
	"strings"
	"testing"
	"time"
)

func TestIsConfigured_AllSet(t *testing.T) {
	es := &EmailService{
		smtpHost:     "smtp.example.com",
		smtpUser:     "user",
		smtpPassword: "pass",
		fromEmail:    "noreply@example.com",
	}
	if !es.IsConfigured() {
		t.Fatal("expected configured when all fields set")
	}
}

func TestIsConfigured_MissingFields(t *testing.T) {
	tests := []struct {
		name string
		es   *EmailService
	}{
		{"missing host", &EmailService{smtpUser: "u", smtpPassword: "p", fromEmail: "f@f.com"}},
		{"missing user", &EmailService{smtpHost: "h", smtpPassword: "p", fromEmail: "f@f.com"}},
		{"missing password", &EmailService{smtpHost: "h", smtpUser: "u", fromEmail: "f@f.com"}},
		{"missing from", &EmailService{smtpHost: "h", smtpUser: "u", smtpPassword: "p"}},
		{"all empty", &EmailService{}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.es.IsConfigured() {
				t.Fatal("expected not configured")
			}
		})
	}
}

func TestRenderTemplate_InvoiceCreated(t *testing.T) {
	es := &EmailService{}
	data := EmailData{
		TenantName: "Acme Corp",
		InvoiceID:  "INV-001",
		Amount:     49.99,
		Currency:   "USD",
		DueDate:    time.Date(2026, 3, 15, 0, 0, 0, 0, time.UTC),
		LoginURL:   "https://app.example.com/login",
	}

	body, err := es.renderTemplate("invoice_created", data)
	if err != nil {
		t.Fatal(err)
	}

	expects := []string{
		"Acme Corp",
		"INV-001",
		"49.99",
		"USD",
		"March 15, 2026",
		"https://app.example.com/login",
	}
	for _, want := range expects {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q", want)
		}
	}
}

func TestRenderTemplate_UnknownTemplate(t *testing.T) {
	es := &EmailService{}
	_, err := es.renderTemplate("nonexistent", EmailData{})
	if err == nil {
		t.Fatal("expected error for unknown template")
	}
	if !strings.Contains(err.Error(), "nonexistent") {
		t.Fatalf("error should mention template name: %v", err)
	}
}

func TestInvoiceCreatedRendersClusterGroupings(t *testing.T) {
	// Verify the email template contains all three cluster kinds with the
	// right badges, plus a $0.00 self-hosted line that's rendered as
	// "Included" rather than dropped. This is the customer-facing
	// presentation invariant.
	es := &EmailService{}
	lines := []EmailInvoiceLineItem{
		{
			Description: "Base subscription", Quantity: "1", UnitPrice: "49", Total: "49", Currency: "EUR",
			PricingSource: "tier", PricingLabel: "Subscription tier",
		},
		{
			Description: "Delivered minutes", ClusterID: "central-primary", ClusterName: "Platform EU",
			ClusterKind: "platform_official",
			Quantity:    "60000", UnitPrice: "0.00055", Total: "33.00", Currency: "EUR",
			PricingSource: "tier", PricingLabel: "Subscription tier",
		},
		{
			Description: "Delivered minutes", ClusterID: "self-edge-1", ClusterName: "My self-hosted",
			ClusterKind: "tenant_private",
			Quantity:    "12000", UnitPrice: "0", Total: "0", Currency: "EUR",
			PricingSource: "self_hosted", PricingLabel: "Self-hosted (no charge)", IsZeroPrice: true,
		},
		{
			Description: "Delivered minutes", ClusterID: "operator-eu-1", ClusterName: "Operator NL Edge",
			ClusterKind: "third_party_marketplace",
			Quantity:    "30000", UnitPrice: "0.0005", Total: "15.00", Currency: "EUR",
			PricingSource: "cluster_metered", PricingLabel: "Marketplace metered",
		},
	}

	data := EmailData{
		TenantName:     "Acme",
		InvoiceID:      "INV-3KIND",
		Amount:         97.90,
		Currency:       "EUR",
		DueDate:        time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC),
		LoginURL:       "https://app.example.com/login",
		LineItems:      lines,
		LineItemGroups: groupEmailLineItems(lines),
	}

	body, err := es.renderTemplate("invoice_created", data)
	if err != nil {
		t.Fatalf("render: %v", err)
	}

	wants := []string{
		"Platform EU",             // platform-official cluster name
		"My self-hosted",          // tenant_private cluster name
		"Operator NL Edge",        // marketplace cluster name
		"Self-hosted",             // self-hosted badge
		"Marketplace",             // marketplace badge
		"Subscription",            // tenant-scoped group label
		"Self-hosted (no charge)", // pricing label visible
		"Marketplace metered",     // marketplace pricing label
		"Included",                // zero-price line shows "Included" not the $0.00 column
	}
	for _, want := range wants {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q", want)
		}
	}
}

func TestGroupEmailLineItems_OrdersByKindThenPlatformLast(t *testing.T) {
	lines := []EmailInvoiceLineItem{
		{Description: "marketplace usage", ClusterID: "m1", ClusterKind: "third_party_marketplace"},
		{Description: "self-hosted usage", ClusterID: "s1", ClusterKind: "tenant_private"},
		{Description: "platform usage", ClusterID: "p1", ClusterKind: "platform_official"},
		{Description: "base subscription"},
	}
	groups := groupEmailLineItems(lines)
	if len(groups) != 4 {
		t.Fatalf("expected 4 groups, got %d", len(groups))
	}
	if groups[0].ClusterKind != "platform_official" {
		t.Errorf("group 0 = %s, want platform_official", groups[0].ClusterKind)
	}
	if groups[1].ClusterKind != "tenant_private" {
		t.Errorf("group 1 = %s, want tenant_private", groups[1].ClusterKind)
	}
	if groups[2].ClusterKind != "third_party_marketplace" {
		t.Errorf("group 2 = %s, want third_party_marketplace", groups[2].ClusterKind)
	}
	if !groups[3].PlatformScoped {
		t.Errorf("last group must be PlatformScoped (base_subscription)")
	}
}

func TestRenderTemplate_PaymentSuccess(t *testing.T) {
	es := &EmailService{}
	now := time.Date(2026, 3, 10, 14, 30, 0, 0, time.UTC)
	data := EmailData{
		TenantName:    "Test Tenant",
		InvoiceID:     "INV-002",
		Amount:        100.00,
		Currency:      "EUR",
		PaidAt:        &now,
		PaymentMethod: "Credit Card",
		LoginURL:      "https://app.example.com/login",
	}

	body, err := es.renderTemplate("payment_success", data)
	if err != nil {
		t.Fatal(err)
	}

	expects := []string{
		"Test Tenant",
		"INV-002",
		"Credit Card",
		"Payment Confirmed",
	}
	for _, want := range expects {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q", want)
		}
	}
}

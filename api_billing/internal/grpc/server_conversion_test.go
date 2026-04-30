package grpc

import (
	"strings"
	"testing"

	pb "frameworks/pkg/proto"
)

func TestScanBillingFeatures(t *testing.T) {
	got := scanBillingFeatures(nil)
	if got != nil {
		t.Fatalf("expected nil for empty input, got %+v", got)
	}

	got = scanBillingFeatures([]byte(`{"recording":true,"analytics":true,"custom_branding":true,"api_access":true,"support_level":"premium","sla":true,"processing_customizable":true}`))
	if got == nil || !got.Recording || !got.Analytics || !got.CustomBranding || !got.ApiAccess || got.SupportLevel != "premium" || !got.Sla || !got.ProcessingCustomizable {
		t.Fatalf("billing features parse failed: %+v", got)
	}
}

func TestScanBillingAddress(t *testing.T) {
	addr := scanBillingAddress(nil)
	if addr != nil {
		t.Fatalf("expected nil for empty input, got %+v", addr)
	}
	addr = scanBillingAddress([]byte(`{"street":"1 Main","city":"NYC","state":"NY","postal_code":"10001","country":"US"}`))
	if addr == nil {
		t.Fatal("expected parsed billing address")
	}
	if addr.Street != "1 Main" || addr.City != "NYC" || addr.PostalCode != "10001" {
		t.Fatalf("billing address mismatch: %+v", addr)
	}
}

func TestInvoicePaymentReturnURLs(t *testing.T) {
	t.Setenv("WEBAPP_PUBLIC_URL", "https://app.example.com")

	success, cancel, err := invoicePaymentReturnURLs("")
	if err != nil {
		t.Fatalf("invoicePaymentReturnURLs: %v", err)
	}
	if success != "https://app.example.com/account/billing?payment=success" {
		t.Fatalf("success URL = %q", success)
	}
	if cancel != "https://app.example.com/account/billing?payment=cancelled" {
		t.Fatalf("cancel URL = %q", cancel)
	}

	success, cancel, err = invoicePaymentReturnURLs("https://app.example.com/account/billing?tab=invoices")
	if err != nil {
		t.Fatalf("invoicePaymentReturnURLs custom: %v", err)
	}
	if !strings.Contains(success, "payment=success") || !strings.Contains(success, "tab=invoices") {
		t.Fatalf("custom success URL = %q", success)
	}
	if !strings.Contains(cancel, "payment=cancelled") || !strings.Contains(cancel, "tab=invoices") {
		t.Fatalf("custom cancel URL = %q", cancel)
	}

	success, _, err = invoicePaymentReturnURLs("/account/billing?tab=invoices")
	if err != nil {
		t.Fatalf("invoicePaymentReturnURLs relative: %v", err)
	}
	if success != "https://app.example.com/account/billing?payment=success&tab=invoices" {
		t.Fatalf("relative success URL = %q", success)
	}

	if _, _, err = invoicePaymentReturnURLs("https://evil.example/account/billing"); err == nil {
		t.Fatal("expected cross-origin return_url to fail")
	}
}

func TestMarshalBillingFeaturesRoundTrip(t *testing.T) {
	bf := &pb.BillingFeatures{
		Recording:      true,
		Analytics:      true,
		CustomBranding: true,
		ApiAccess:      true,
		SupportLevel:   "premium",
		Sla:            true,
	}
	bfJSON, err := marshalBillingFeatures(bf)
	if err != nil {
		t.Fatalf("marshalBillingFeatures: %v", err)
	}
	if got := scanBillingFeatures(bfJSON); got == nil || !got.Recording || !got.Analytics || !got.CustomBranding || !got.ApiAccess || got.SupportLevel != "premium" || !got.Sla {
		t.Fatalf("billing features roundtrip failed: %+v", got)
	}
}

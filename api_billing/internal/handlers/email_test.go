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

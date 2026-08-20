package handlers

import (
	"context"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestCryptoDocumentRequirementIsPaymentSpecific(t *testing.T) {
	tests := []struct {
		name            string
		amountCents     int64
		email           any
		legalName       any
		company         any
		address         any
		vatNumber       any
		wantKind        string
		wantVATClaim    bool
		wantProfileGate bool
	}{
		{
			name:        "small anonymous payment uses simplified document",
			amountCents: 500,
			wantKind:    "simplified",
		},
		{
			name:            "payment over one hundred euros needs full profile",
			amountCents:     10_001,
			wantKind:        "full",
			wantProfileGate: true,
		},
		{
			name:         "VAT claim selects full document with complete profile",
			amountCents:  500,
			email:        "billing@example.com",
			legalName:    "Example Customer",
			company:      "Example BV",
			address:      []byte(`{"street":"Main 1","city":"Amsterdam","postal_code":"1000AA","country":"NL"}`),
			vatNumber:    "NL123456789B01",
			wantKind:     "full",
			wantVATClaim: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			mock.ExpectQuery("SELECT billing_email, billing_name, billing_company, billing_address, tax_id").
				WithArgs("tenant-1").
				WillReturnRows(sqlmock.NewRows([]string{"billing_email", "billing_name", "billing_company", "billing_address", "tax_id"}).
					AddRow(test.email, test.legalName, test.company, test.address, test.vatNumber))

			requirement, err := (&X402Handler{db: db}).GetCryptoDocumentRequirement(context.Background(), "tenant-1", test.amountCents)
			if err != nil {
				t.Fatalf("GetCryptoDocumentRequirement() error = %v", err)
			}
			if requirement.DocumentKind != test.wantKind || requirement.HasVATClaim != test.wantVATClaim || requirement.RequiresCompleteProfile != test.wantProfileGate {
				t.Fatalf("requirement = %+v", requirement)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestAnonymousCryptoTopupDefaultsToSupplierCountryVAT(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	at := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	mock.ExpectQuery("SELECT rate_bps, source, source_checked_on::text, effective_from::text").
		WithArgs("NL", at).
		WillReturnRows(sqlmock.NewRows([]string{"rate_bps", "source", "source_checked_on", "effective_from"}).
			AddRow(2100, "Belastingdienst standard rate", "2026-01-01", "2026-01-01"))

	decision, err := (&X402Handler{db: db, supplierCountry: "NL"}).getVATDecisionForProfile(
		context.Background(), "tenant-1", CryptoBillingProfile{}, "203.0.113.1", at,
	)
	if err != nil {
		t.Fatal(err)
	}
	if decision.Country != "NL" || decision.RateBPS != 2100 || decision.ReverseCharge {
		t.Fatalf("anonymous VAT decision = %+v", decision)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestCryptoEvidenceStatusUsesSchemaVocabulary(t *testing.T) {
	tests := []struct {
		name           string
		billingCountry string
		ipCountry      string
		wantStatus     string
		wantConflict   bool
	}{
		{name: "matching sources", billingCountry: "nl", ipCountry: "NL", wantStatus: "complete"},
		{name: "conflicting sources", billingCountry: "NL", ipCountry: "DE", wantStatus: "conflict", wantConflict: true},
		{name: "billing only", billingCountry: "NL", wantStatus: "single_source"},
		{name: "IP only", ipCountry: "NL", wantStatus: "single_source"},
		{name: "no source", wantStatus: "missing"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			status, conflict := cryptoEvidenceStatus(test.billingCountry, test.ipCountry)
			if status != test.wantStatus || conflict != test.wantConflict {
				t.Fatalf("cryptoEvidenceStatus() = (%q, %t), want (%q, %t)", status, conflict, test.wantStatus, test.wantConflict)
			}
		})
	}
}

func TestLoadCryptoTaxSnapshotUsesLockedQuoteRate(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	observedAt := time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC)
	mock.ExpectQuery("SELECT quote.tax_document_kind, quote.tax_profile_snapshot").
		WithArgs("tenant-1", "0xtx").
		WillReturnRows(sqlmock.NewRows([]string{"tax_document_kind", "tax_profile_snapshot", "eur_per_usd_rate", "created_at"}).
			AddRow("simplified", []byte(`{}`), "0.9123456789", observedAt))

	snapshot, err := (&X402Handler{db: db}).loadCryptoTaxSnapshot(context.Background(), "tenant-1", "x402_payment", "0xtx")
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.DocumentKind != "simplified" || snapshot.EURPerUSDRate != 0.9123456789 || !snapshot.FXRateObservedAt.Equal(observedAt) {
		t.Fatalf("snapshot = %+v", snapshot)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

package handlers

import (
	"context"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
)

func TestDutchSupplierNonEUConsumerGetsZeroDutchVATBeforePayment(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.ExpectQuery("SELECT billing_email, billing_name, billing_company").WithArgs("tenant-1").WillReturnRows(sqlmock.NewRows([]string{"billing_email", "billing_name", "billing_company", "billing_address", "tax_id", "customer_type"}).AddRow("person@example.test", "Private customer", nil, []byte(`{"street":"Main 1","city":"London","postal_code":"SW1A 1AA","country":"GB"}`), nil, "consumer"))
	h := &X402Handler{db: db, supplierCountry: "NL", countryFromIP: func(string) string { return "GB" }}
	ctx := context.WithValue(context.Background(), ctxkeys.KeyClientIP, "203.0.113.1")
	requirement, err := h.GetCryptoDocumentRequirement(ctx, "tenant-1", 500, "203.0.113.1")
	if err != nil || requirement.RequiresCompleteProfile || requirement.Profile.TaxAssessment == nil {
		t.Fatalf("selected non-EU policy failed before payment: %+v %v", requirement, err)
	}
	decision := requirement.Profile.TaxAssessment.Decision
	if decision.RateBPS != 0 || decision.Treatment != "non_eu_service" || decision.Wording != "Outside scope of Dutch VAT" {
		t.Fatalf("wrong Dutch VAT treatment: %+v", decision)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestCryptoPreflightUsesExplicitRequestIP(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.ExpectQuery("SELECT billing_email, billing_name, billing_company").WithArgs("tenant-1").WillReturnRows(sqlmock.NewRows([]string{"billing_email", "billing_name", "billing_company", "billing_address", "tax_id", "customer_type"}).AddRow(nil, nil, nil, []byte(`{"country":"DE"}`), nil, "consumer"))
	mock.ExpectQuery("SELECT rate_bps, source, source_checked_on::text AS source_checked_on").WillReturnRows(sqlmock.NewRows([]string{"rate_bps", "source", "source_checked_on", "effective_from"}).AddRow(2100, "Dutch VAT", "2026-01-01", "2026-01-01"))
	h := &X402Handler{db: db, supplierCountry: "NL", countryFromIP: func(ip string) string {
		if ip == "203.0.113.1" {
			return "DE"
		}
		return ""
	}}
	// A fresh server-side context contains no gateway's Go context values.
	r, err := h.GetCryptoDocumentRequirement(context.Background(), "tenant-1", 500, "203.0.113.1")
	if err != nil || r.Profile.TaxAssessment == nil || r.Profile.TaxAssessment.IPCountry != "DE" {
		t.Fatalf("request IP was lost before tax preflight: %+v %v", r, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

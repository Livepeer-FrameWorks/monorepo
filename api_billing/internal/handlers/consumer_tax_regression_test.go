package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	billingtax "frameworks/api_billing/internal/tax"
	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
)

func TestNonEUConsumerCannotBecomeTaxFreeFromCountryAlone(t *testing.T) {
	db, _, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	h := &X402Handler{db: db, supplierCountry: "NL"}
	decision, err := h.getVATDecisionForProfile(context.Background(), "tenant-1", CryptoBillingProfile{
		Name:    "Private customer",
		Address: json.RawMessage(`{"street":"Main 1","city":"London","postal_code":"SW1A 1AA","country":"GB"}`),
	}, "203.0.113.1", time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC))
	if err == nil {
		t.Fatalf("unconfigured non-EU consumer tax must require review, got rate=%d country=%s", decision.RateBPS, decision.Country)
	}
}

func TestUnsupportedConsumerTaxIsHeldBeforePaymentQuote(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.ExpectQuery("SELECT billing_email, billing_name, billing_company").WithArgs("tenant-1").WillReturnRows(sqlmock.NewRows([]string{"billing_email", "billing_name", "billing_company", "billing_address", "tax_id", "customer_type"}).AddRow("person@example.test", "Private customer", nil, []byte(`{"street":"Main 1","city":"London","postal_code":"SW1A 1AA","country":"GB"}`), nil, "consumer"))
	h := &X402Handler{db: db, supplierCountry: "NL", countryFromIP: func(string) string { return "GB" }}
	ctx := context.WithValue(context.Background(), ctxkeys.KeyClientIP, "203.0.113.1")
	_, err = h.GetCryptoDocumentRequirement(ctx, "tenant-1", 500)
	if err == nil {
		t.Fatal("unsupported consumer tax must be held before payment instructions are produced")
	}
	var review *billingtax.ReviewError
	if !errors.As(err, &review) || review.Code != "foreign_tax_policy_required" {
		t.Fatalf("expected jurisdiction policy review after matching location evidence, got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

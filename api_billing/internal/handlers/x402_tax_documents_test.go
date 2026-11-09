package handlers

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestExistingCryptoDocumentDoesNotRecheckCurrentTaxSetup(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.ExpectQuery("-- name: GetExistingCryptoTaxDocumentNumber").WithArgs("tenant-1", "x402_payment", "0xtx").WillReturnRows(sqlmock.NewRows([]string{"invoice_number"}).AddRow("SI-0000000001"))
	got, err := (&X402Handler{db: db}).generateCryptoTopupInvoice(context.Background(), "tenant-1", 500, "x402_payment", "0xtx", "", "base")
	if err != nil || got != "SI-0000000001" {
		t.Fatalf("issued document must remain available independently of current tax setup: %q %v", got, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestLegacyForeignQuoteStillProducesItsTaxDocument(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	at := time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC)
	mock.ExpectQuery("-- name: GetExistingCryptoTaxDocumentNumber").WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery("SELECT quote.tax_document_kind, quote.tax_profile_snapshot").WithArgs("tenant-1", "0xtx").WillReturnRows(sqlmock.NewRows([]string{
		"tax_document_kind", "tax_profile_snapshot", "created_at", "original_amount_cents", "original_currency", "eur_amount_cents", "fx_units_per_eur", "fx_source", "fx_reference_date",
	}).AddRow("simplified", []byte(`{"address":{"country":"US"}}`), at, int64(1096), "USD", int64(1000), "1.0960000000", "ecb", at))
	mock.ExpectBegin()
	mock.ExpectExec("-- name: AcquireCryptoTaxDocumentLock").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery("-- name: GetExistingCryptoTaxDocumentNumber").WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery("-- name: NextSimplifiedInvoiceNumber").WillReturnRows(sqlmock.NewRows([]string{"nextval"}).AddRow(1))
	mock.ExpectExec("-- name: InsertSimplifiedCryptoTopupInvoice").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	h := &X402Handler{db: db, supplierName: "Seller", supplierAddress: "Amsterdam", supplierVAT: "NL123456789B01", supplierRegistration: "12345678", supplierCountry: "NL"}
	got, err := h.generateCryptoTopupInvoice(context.Background(), "tenant-1", 1000, "x402_payment", "0xtx", "", "base")
	if err != nil || got == "" {
		t.Fatalf("already-paid legacy foreign quote lost its document: invoice=%q err=%v", got, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestCryptoQuoteFreezesTaxAssessmentBeforePayment(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.ExpectQuery("SELECT billing_email, billing_name, billing_company").WithArgs("tenant-1").WillReturnRows(sqlmock.NewRows([]string{"billing_email", "billing_name", "billing_company", "billing_address", "tax_id", "customer_type"}).AddRow(nil, nil, nil, []byte(`{"country":"NL"}`), nil, "consumer"))
	mock.ExpectQuery("SELECT rate_bps, source, source_checked_on::text AS source_checked_on").WillReturnRows(sqlmock.NewRows([]string{"rate_bps", "source", "source_checked_on", "effective_from"}).AddRow(2100, "Dutch VAT", "2026-01-01", "2026-01-01"))
	r, err := (&X402Handler{db: db, supplierCountry: "NL"}).GetCryptoDocumentRequirement(context.Background(), "tenant-1", 500, "")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(r.Profile)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if decodeErr := json.Unmarshal(raw, &fields); decodeErr != nil {
		t.Fatal(decodeErr)
	}
	if len(fields["tax_assessment"]) == 0 {
		t.Fatalf("payable quote has no frozen tax assessment: %s", raw)
	}
	var captured CryptoBillingProfile
	if decodeErr := json.Unmarshal(raw, &captured); decodeErr != nil {
		t.Fatal(decodeErr)
	}
	d, err := captured.TaxAssessment.ValidatedDecision()
	if err != nil || d.RateBPS != 2100 || d.Source != "Dutch VAT" {
		t.Fatalf("settlement changed an accepted tax assessment: %+v %v", d, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestAcceptedCryptoTreatmentUsesTheConfirmationDateRate(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	at := time.Date(2027, 1, 1, 0, 1, 0, 0, time.UTC)
	mock.ExpectQuery("SELECT rate_bps, source, source_checked_on::text AS source_checked_on").WithArgs("DE", at).WillReturnRows(sqlmock.NewRows([]string{"rate_bps", "source", "source_checked_on", "effective_from"}).AddRow(2000, "Effective tax-point rate", "2026-12-01", "2027-01-01"))
	profile := CryptoBillingProfile{TaxAssessment: &CryptoTaxAssessment{PolicyVersion: frozenCryptoTaxPolicy, AssessedAt: at.Add(-5 * time.Minute), SupplierCountry: "NL", Decision: vatDecision{Treatment: "eu_b2c_destination", Country: "DE", RateBPS: 1900, Source: "Accepted quote rate"}}}
	d, err := (&X402Handler{db: db}).cryptoSettlementVATDecision(context.Background(), "tenant-1", profile, at, at)
	if err != nil || d.Treatment != "eu_b2c_destination" || d.Country != "DE" || d.RateBPS != 2000 || d.EffectiveFrom != "2027-01-01" {
		t.Fatalf("statutory rate must follow confirmation without changing the accepted treatment: %+v %v", d, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestCryptoDocumentRequirementIsPaymentSpecific(t *testing.T) {
	tests := []struct {
		name            string
		amountCents     int64
		email           any
		legalName       any
		company         any
		address         any
		vatNumber       any
		customerType    string
		wantKind        string
		wantVATClaim    bool
		wantProfileGate bool
	}{
		{
			name:            "small payment still needs tax profile before instructions",
			amountCents:     500,
			wantKind:        "simplified",
			wantProfileGate: true,
		},
		{
			name:         "consumer simplified invoice needs no company or VAT number",
			amountCents:  500,
			customerType: "consumer",
			address:      []byte(`{"country":"NL"}`),
			wantKind:     "simplified",
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
			address := test.address
			if address == nil {
				address = []byte(`{}`)
			}
			mock.ExpectQuery("SELECT billing_email, billing_name, billing_company").
				WithArgs("tenant-1").
				WillReturnRows(sqlmock.NewRows([]string{"billing_email", "billing_name", "billing_company", "billing_address", "tax_id", "customer_type"}).
					AddRow(test.email, test.legalName, test.company, address, test.vatNumber, test.customerType))

			requirement, err := (&X402Handler{db: db}).GetCryptoDocumentRequirement(context.Background(), "tenant-1", test.amountCents, "")
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

func TestLegacyAnonymousCryptoTopupDefaultsToSupplierCountryVAT(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	at := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	mock.ExpectQuery("SELECT rate_bps, source, source_checked_on::text AS source_checked_on").
		WithArgs("NL", at).
		WillReturnRows(sqlmock.NewRows([]string{"rate_bps", "source", "source_checked_on", "effective_from"}).
			AddRow(2100, "Belastingdienst standard rate", "2026-01-01", "2026-01-01"))

	decision, err := (&X402Handler{db: db, supplierCountry: "NL"}).legacyCryptoVATDecision(
		context.Background(), "tenant-1", CryptoBillingProfile{}, at, at,
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
		WillReturnRows(sqlmock.NewRows([]string{
			"tax_document_kind", "tax_profile_snapshot", "created_at",
			"original_amount_cents", "original_currency", "eur_amount_cents",
			"fx_units_per_eur", "fx_source", "fx_reference_date",
		}).AddRow("simplified", []byte(`{}`), observedAt, int64(1096), "USD", int64(1000), "1.0960000000", "ecb", observedAt))

	snapshot, err := (&X402Handler{db: db}).loadCryptoTaxSnapshot(context.Background(), "tenant-1", "x402_payment", "0xtx")
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.DocumentKind != "simplified" || snapshot.FX.OriginalMinor != 1096 || snapshot.FX.OriginalCurrency != "USD" ||
		snapshot.FX.EURMinor != 1000 || snapshot.FX.UnitsPerEUR.String() != "1.096" || !snapshot.FXRateObservedAt.Equal(observedAt) {
		t.Fatalf("snapshot = %+v", snapshot)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestLegacyDomesticBusinessDoesNotDependOnVIES(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.ExpectQuery("-- name: GetEffectiveVATRate").WillReturnRows(sqlmock.NewRows([]string{"rate_bps", "source", "source_checked_on", "effective_from"}).AddRow(2100, "Dutch VAT", "2026-01-01", "2026-01-01"))
	profile := CryptoBillingProfile{CustomerType: "business", VATNumber: "NL123456789B01", Address: json.RawMessage(`{"country":"NL"}`)}
	d, err := (&X402Handler{db: db, supplierCountry: "NL"}).legacyCryptoVATDecision(context.Background(), "tenant", profile, time.Now().Add(-time.Hour), time.Now())
	if err != nil || d.RateBPS != 2100 || d.ReverseCharge {
		t.Fatalf("domestic business was gated on foreign VAT validation: %+v %v", d, err)
	}
	if err = mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

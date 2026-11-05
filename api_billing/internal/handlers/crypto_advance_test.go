package handlers

import (
	"context"
	"encoding/json"
	"math/big"
	"testing"
	"time"

	"frameworks/api_billing/internal/fx"
	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/billing"
	"github.com/shopspring/decimal"
)

func TestCryptoAdvanceQuoteChargesVATOnTop(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.ExpectQuery("-- name: GetCryptoDocumentBillingProfile").WithArgs("tenant").WillReturnRows(sqlmock.NewRows([]string{"billing_email", "billing_name", "billing_company", "billing_address", "tax_id", "customer_type"}).AddRow("buyer@example.test", "Buyer", nil, []byte(`{"street":"Main1","city":"Leiden","postal_code":"1234","country":"NL"}`), nil, "consumer"))
	mock.ExpectQuery("-- name: GetEffectiveVATRate").WillReturnRows(sqlmock.NewRows([]string{"rate_bps", "source", "source_checked_on", "effective_from"}).AddRow(2100, "Dutch rate", "2026-01-01", "2026-01-01"))
	cash, err := fx.RecordToEUR(10000, fx.Rate{Currency: "USD", UnitsPerEUR: decimal.RequireFromString("1.25"), Source: "ecb", ReferenceDate: time.Now().UTC()})
	if err != nil {
		t.Fatal(err)
	}
	quote := &DepositQuote{FX: cash, ExpectedAmountBaseUnits: big.NewInt(100000000)}
	h := &X402Handler{db: db, supplierCountry: "NL", supplierName: "DDVTech B.V.", supplierVAT: "NL123456789B01", supplierAddress: "Leiden, NL", supplierRegistration: "12345678"}
	req, err := h.ApplyCryptoAdvanceQuote(context.Background(), "tenant", quote, "")
	if err != nil {
		t.Fatal(err)
	}
	if req.RequiresCompleteProfile || quote.FX.OriginalMinor != 12100 || quote.FX.EURMinor != 9680 || quote.ExpectedAmountBaseUnits.String() != "121000000" || quote.TaxProfile.TaxAssessment.Advance.NetEUR != 8000 {
		t.Fatalf("quote=%+v money=%+v requirement=%+v", quote, quote.TaxProfile.TaxAssessment.Advance, req)
	}
	if err = mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestCryptoAdvanceGrossAmountControlsSimplifiedCeiling(t *testing.T) {
	req := CryptoDocumentRequirement{DocumentKind: "simplified", Profile: CryptoBillingProfile{CustomerType: "consumer", Address: json.RawMessage(`{"country":"NL"}`), TaxAssessment: &CryptoTaxAssessment{}}}
	got := bindCryptoAdvance(req, AdvanceMoney{NetEUR: 9500, VATEUR: 1995, GrossEUR: 11495})
	if !got.NeedsFullDocument || !got.RequiresCompleteProfile || got.DocumentKind != "full" {
		t.Fatalf("gross payment above EUR100 accepted anonymously: %+v", got)
	}
}

func TestCryptoAdvanceOverpaymentSeparatesAdditionalVAT(t *testing.T) {
	rate := fx.Rate{Currency: "USD", UnitsPerEUR: decimal.RequireFromString("1.25"), Source: "ecb", ReferenceDate: time.Now().UTC()}
	money, _, err := quoteAdvanceMoney(10000, 2100, rate)
	if err != nil {
		t.Fatal(err)
	}
	profile := CryptoBillingProfile{TaxAssessment: &CryptoTaxAssessment{PolicyVersion: billing.CurrentCryptoTopupTaxPolicy, Advance: &money}}
	extra, err := fx.RecordToEUR(24200, rate)
	if err != nil {
		t.Fatal(err)
	}
	if err = resizeCryptoAdvance(&profile, extra); err != nil {
		t.Fatal(err)
	}
	if profile.TaxAssessment.Advance.NetEUR != 16000 || profile.TaxAssessment.Advance.VATEUR != 3360 || money.NetEUR != 8000 {
		t.Fatalf("overpayment=%+v original=%+v", profile.TaxAssessment.Advance, money)
	}
}

func TestCryptoAdvanceRejectsFutureAndMismatchedMoney(t *testing.T) {
	rate := fx.Rate{Currency: "EUR", UnitsPerEUR: decimal.NewFromInt(1), Source: "identity", ReferenceDate: time.Now().UTC()}
	money, cash, err := quoteAdvanceMoney(100, 2100, rate)
	if err != nil {
		t.Fatal(err)
	}
	profile := CryptoBillingProfile{TaxAssessment: &CryptoTaxAssessment{Advance: &money, PolicyVersion: "future-policy"}}
	if _, err = cryptoAdvanceAssessment(profile, "simplified", cash, time.Now()); err == nil {
		t.Fatal("unrecognized policy was treated as legacy")
	}
	profile.TaxAssessment.PolicyVersion = billing.CurrentCryptoTopupTaxPolicy
	cash.EURMinor++
	if _, err = cryptoAdvanceAssessment(profile, "simplified", cash, time.Now()); err == nil {
		t.Fatal("cash different from accepted frozen terms was accepted")
	}
}

func TestCryptoAdvanceRefusesUnconfiguredSupplierBeforePaymentInstructions(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.ExpectQuery("-- name: GetCryptoDocumentBillingProfile").WillReturnRows(sqlmock.NewRows([]string{"billing_email", "billing_name", "billing_company", "billing_address", "tax_id", "customer_type"}).AddRow(nil, nil, nil, []byte(`{"country":"NL"}`), nil, "consumer"))
	mock.ExpectQuery("-- name: GetEffectiveVATRate").WillReturnRows(sqlmock.NewRows([]string{"rate_bps", "source", "source_checked_on", "effective_from"}).AddRow(2100, "Dutch VAT", "2026-01-01", "2026-01-01"))
	cash, err := fx.RecordToEUR(100, fx.Identity(time.Now().UTC()))
	if err != nil {
		t.Fatal(err)
	}
	quote := &DepositQuote{FX: cash, ExpectedAmountBaseUnits: big.NewInt(1000000)}
	_, err = (&X402Handler{db: db, supplierCountry: "NL"}).ApplyCryptoAdvanceQuote(context.Background(), "tenant", quote, "")
	if err == nil {
		t.Fatal("payment instructions exposed without a complete frozen supplier")
	}
	if quote.ExpectedAmountBaseUnits.Int64() != 1000000 || quote.FX.OriginalMinor != 100 {
		t.Fatal("refused quote mutated payment instructions")
	}
	if err = mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestCryptoAdvanceRetainsReviewForGraceAndExpiredPolicy(t *testing.T) {
	now := time.Now().UTC()
	expired := now.Add(-time.Minute)
	money, cash, err := quoteAdvanceMoney(800, 0, fx.Identity(now))
	if err != nil {
		t.Fatal(err)
	}
	profile := CryptoBillingProfile{CustomerType: "business", Name: "Accepted Buyer", Email: "buyer@example.test", Address: json.RawMessage(`{"street":"Main1","city":"Berlin","postal_code":"10115","country":"DE"}`), TaxAssessment: &CryptoTaxAssessment{Advance: &money, PolicyVersion: billing.CurrentCryptoTopupTaxPolicy, AssessedAt: now.Add(-2 * time.Minute), SupplierCountry: "NL", Supplier: DocumentSupplier{Name: "Accepted Supplier", Address: "Leiden, NL", VATNumber: "NL123456789B01", RegistrationNumber: "12345678"}, VATValidationMode: "outage_grace", Decision: vatDecision{Country: "DE", RateBPS: 0, VIESValidated: true, ReverseCharge: true, Source: "Retained VIES", PolicyValidUntil: &expired}}}
	a, err := cryptoAdvanceAssessment(profile, "full", cash, now)
	if err != nil || !a.ReviewRequired || len(a.ReviewReasons) != 2 || a.Net != 800 || a.VAT != 0 || a.Gross != 800 || a.Customer.Name != "Accepted Buyer" || a.Supplier.Name != "Accepted Supplier" {
		t.Fatalf("review changed or lost accepted advance facts: %+v %v", a, err)
	}
}

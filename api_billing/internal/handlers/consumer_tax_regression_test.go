package handlers

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"frameworks/api_billing/internal/appconfig/appconfigtest"
	billingtax "frameworks/api_billing/internal/tax"
	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/countries"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
)

func TestReviewedNonEUPolicyEnablesOnlyBoundBusinessProfile(t *testing.T) {
	appconfigtest.Set(t, "SUPPLIER_COUNTRY", "NL")
	profile := CryptoBillingProfile{CustomerType: "business", Email: "buyer@example.test", Name: "US Business", Company: "US Business Inc", Address: json.RawMessage(`{"street":"Main 1","city":"Seattle","postal_code":"98101","country":"US"}`)}
	fingerprint, err := CryptoBusinessProfileFingerprint(profile)
	if err != nil {
		t.Fatal(err)
	}
	tenant := "92000000-0000-0000-0000-000000000001"
	policy := `{"reference":"tax-review-2026","supplier_country":"NL","valid_from":"2026-01-01T00:00:00Z","valid_until":"2027-01-01T00:00:00Z","non_eu_businesses":{"US":{"rate_bps":0,"source":"Reviewed US business policy","evidence_reference":"US-tax-memo","tax_label":"VAT","wording":"Outside scope of Dutch VAT"}}}`
	appconfigtest.Set(t, "CRYPTO_TAX_POLICY_JSON", policy)
	db, mock, mockErr := sqlmock.New()
	if mockErr != nil {
		t.Fatal(mockErr)
	}
	defer db.Close()
	at := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	mock.ExpectQuery("-- name: GetBusinessEstablishmentReviewSnapshot").WithArgs(at, tenant, fingerprint, "US").WillReturnRows(sqlmock.NewRows([]string{"review_snapshot"}).AddRow([]byte(`{"reference":"KYB-US-1","valid_until":"2026-12-01T00:00:00Z"}`)))
	h := &X402Handler{db: db, supplierCountry: "NL"}
	d, err := h.getVATDecisionForProfile(context.Background(), tenant, profile, "", time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC))
	if err != nil || d.RateBPS != 0 || d.Source != "Reviewed US business policy" {
		t.Fatalf("reviewed operator policy is not usable: %+v %v", d, err)
	}
	profile.Company = "Different business"
	changedHash, err := CryptoBusinessProfileFingerprint(profile)
	if err != nil {
		t.Fatal(err)
	}
	mock.ExpectQuery("-- name: GetBusinessEstablishmentReviewSnapshot").WithArgs(at, tenant, changedHash, "US").WillReturnError(sql.ErrNoRows)
	_, err = h.getVATDecisionForProfile(context.Background(), tenant, profile, "", time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC))
	var review *billingtax.ReviewError
	if !errors.As(err, &review) || review.Code != "business_evidence_required" {
		t.Fatalf("changing customer identity must invalidate the establishment proof: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

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
	appconfigtest.Set(t, "EU_CONSUMER_VAT_MODE", "destination")
	appconfigtest.Set(t, "EU_CONSUMER_DESTINATION_REGISTERED", "true")
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.ExpectQuery("SELECT billing_email, billing_name, billing_company").WithArgs("tenant-1").WillReturnRows(sqlmock.NewRows([]string{"billing_email", "billing_name", "billing_company", "billing_address", "tax_id", "customer_type"}).AddRow(nil, nil, nil, []byte(`{"country":"DE"}`), nil, "consumer"))
	mock.ExpectQuery("SELECT rate_bps, source, source_checked_on::text AS source_checked_on").WillReturnRows(sqlmock.NewRows([]string{"rate_bps", "source", "source_checked_on", "effective_from"}).AddRow(1900, "German VAT", "2026-01-01", "2026-01-01"))
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

func TestCryptoQuoteDoesNotOutliveEitherReview(t *testing.T) {
	now := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	policyUntil := now.Add(time.Hour)
	proofUntil := now.Add(5 * time.Minute)
	proof, err := json.Marshal(map[string]any{"valid_until": proofUntil, "reference": "KYB-proof"})
	if err != nil {
		t.Fatal(err)
	}
	assessment := &CryptoTaxAssessment{Decision: vatDecision{PolicyValidUntil: &policyUntil, BusinessEstablishment: proof}}
	deadline, err := assessment.QuoteExpiry(now.Add(24*time.Hour), now)
	if err != nil || !deadline.Equal(proofUntil) {
		t.Fatalf("payment instructions outlive business evidence: %v %v", deadline, err)
	}
	if _, err := assessment.QuoteExpiry(now.Add(24*time.Hour), proofUntil); err == nil {
		t.Fatal("expired review authorized new payment instructions")
	}
}

func TestDevelopmentRatesFollowEUMembership(t *testing.T) {
	for country := range countries.ValidCodes {
		_, hasRate := developmentVATRates[country]
		if hasRate != billingtax.IsEU(country) {
			t.Errorf("development rate catalog disagrees with EU membership for %s", country)
		}
	}
}

func TestLegacyVATBusinessHasTheSameEvidenceIdentityAsExplicitBusiness(t *testing.T) {
	profile := CryptoBillingProfile{CustomerType: "unspecified", Email: "buyer@example.test", Name: "US Buyer", VATNumber: "US-REG-123", Address: json.RawMessage(`{"street":"Main 1","city":"Seattle","postal_code":"98101","country":"US"}`)}
	legacy, err := CryptoBusinessProfileFingerprint(profile)
	if err != nil {
		t.Fatalf("legacy VAT business cannot be reviewed by an operator: %v", err)
	}
	profile.CustomerType = "business"
	explicit, err := CryptoBusinessProfileFingerprint(profile)
	if err != nil || legacy != explicit {
		t.Fatalf("classification confirmation changed legal identity: %s %s %v", legacy, explicit, err)
	}
}

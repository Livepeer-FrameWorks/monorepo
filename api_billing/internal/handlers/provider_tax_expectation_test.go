package handlers

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"
	"time"

	billingstripe "frameworks/api_billing/internal/stripe"
	"github.com/DATA-DOG/go-sqlmock"
)

func TestStripePartyMismatchRetainsExpectedTaxProof(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	point := time.Now().UTC().Truncate(time.Second)
	supplier := DocumentSupplier{Name: "Supplier", Address: "Leiden NL", VATNumber: "NL853319145B01", RegistrationNumber: "59104694"}
	digest := sha256.Sum256([]byte("DE123456789"))
	validation, _ := json.Marshal(map[string]any{"tenant_id": "tenant", "country_code": "DE", "vat_number_hash": hex.EncodeToString(digest[:]), "valid": true, "checked_at": point.Add(-time.Hour), "expires_at": point.Add(time.Hour)})
	proof := TaxAssessment{CustomerType: "business", PolicyVersion: invoiceTaxPolicyVersion, AssessedAt: point.Add(-time.Hour), SupplierCountry: "NL", Supplier: supplier, VATValidation: validation, Decision: vatDecision{Country: "DE", VIESValidated: true, ReverseCharge: true, Source: "Validated EU VAT", Wording: "btw verlegd"}}
	encoded, _ := json.Marshal(proof)
	profile := billingstripe.TaxProfile{Name: "Old Buyer", Email: "old@example.test", Street: "Old1", City: "Berlin", PostalCode: "10115", Country: "DE", VATNumber: "DE123456789", ReverseCharge: true, Wording: "btw verlegd", SupplierCountry: "NL", SupplierIdentity: billingstripe.SupplierTaxIdentity(supplier.Name, supplier.Address, supplier.VATNumber, supplier.RegistrationNumber), Evidence: encoded}
	profile.ProfileSHA = profile.Fingerprint()
	retained, _ := json.Marshal(profile)
	expected := profile
	expected.Name = "New Buyer"
	expected.Country = "NL"
	expected.VATNumber = ""
	expected.ReverseCharge = false
	expected.RateBPS = 2100
	expected.Wording = ""
	proof.Decision = vatDecision{Country: "NL", RateBPS: 2100, Source: "Dutch rate"}
	expected.Evidence, _ = json.Marshal(proof)
	expected.ProfileSHA = expected.Fingerprint()
	expectedJSON, _ := json.Marshal(expected)
	raw, _ := json.Marshal(map[string]any{"id": "in_partial", "customer": "cus", "number": "DDV-PARTIAL", "currency": "eur", "status": "paid", "created": point.Unix(), "total": 10000, "total_excluding_tax": 10000, "total_taxes": []any{map[string]int{"amount": 0}}, "customer_name": "New Buyer", "customer_email": "new@example.test", "customer_address": map[string]string{"line1": "New1", "city": "Leiden", "postal_code": "2332ED", "country": "NL"}, "footer": profile.SupplierIdentity, "metadata": map[string]string{"frameworks_tax_profile": profile.ProfileSHA}})
	doc, err := billingstripe.ParseProviderTaxDocument("provider_invoice", raw)
	if err != nil {
		t.Fatal(err)
	}
	mock.ExpectQuery("-- name: GetProviderTaxProfile").WithArgs("tenant", profile.ProfileSHA).WillReturnRows(sqlmock.NewRows([]string{"profile"}).AddRow(retained))
	mock.ExpectQuery("-- name: GetProviderTaxExpectationAtPoint").WithArgs("tenant", sqlmock.AnyArg()).WillReturnRows(sqlmock.NewRows([]string{"assessment"}).AddRow(expectedJSON))
	assessment, reasons, err := (&Service{db: db}).assessImportedStripeInvoice(t.Context(), "tenant", doc)
	if err != nil || len(reasons) == 0 || !bytes.Contains(assessment, []byte("expected_tax_assessment")) {
		t.Fatalf("partial customer synchronization discarded expected21 proof: reasons%v assessment%s err%v", reasons, assessment, err)
	}
	if err = mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

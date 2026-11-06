//go:build schema_verify

package handlers

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"testing"
	"time"

	"frameworks/api_billing/internal/appconfig/appconfigtest"
	"frameworks/api_billing/internal/database/purserdb"
	billingstripe "frameworks/api_billing/internal/stripe"
)

func historicalStripeReverseCharge(t *testing.T, db *sql.DB, tenant, invoice string, point time.Time, validation ...json.RawMessage) (json.RawMessage, billingstripe.TaxProfile) {
	t.Helper()
	if _, err := db.Exec(`UPDATE purser.tenant_subscriptions SET stripe_customer_id=$2,billing_customer_type='consumer',billing_name='Different current buyer',billing_email='current@example.test',billing_address='{"street":"Current1","city":"Leiden","postal_code":"2332ED","country":"NL"}' WHERE tenant_id=$1::uuid`, tenant, "cus_"+tenant); err != nil {
		t.Fatal(err)
	}
	supplier := ConfiguredDocumentSupplier()
	proof := CryptoTaxAssessment{CustomerType: "business", PolicyVersion: frozenCryptoTaxPolicy, AssessedAt: point.Add(-time.Hour), SupplierCountry: "NL", Supplier: supplier, Decision: vatDecision{RateBPS: 0, Country: "DE", VIESValidated: true, ReverseCharge: true, Source: "Validated EU business reverse charge", Treatment: "reverse_charge", Wording: "btw verlegd"}}
	if len(validation) > 0 {
		proof.VATValidation, proof.VATValidationMode = validation[0], "fresh_cache"
	}
	evidence, err := json.Marshal(proof)
	if err != nil {
		t.Fatal(err)
	}
	profile := billingstripe.TaxProfile{Name: "Historical Buyer", Email: "original@example.test", Street: "Original1", City: "Berlin", PostalCode: "10115", Country: "DE", VATNumber: "DE123456789", ReverseCharge: true, Wording: "btw verlegd", SupplierCountry: "NL", SupplierIdentity: billingstripe.SupplierTaxIdentity(supplier.Name, supplier.Address, supplier.VATNumber, supplier.RegistrationNumber), Evidence: evidence}
	profile.ProfileSHA = profile.Fingerprint()
	frozen, err := json.Marshal(profile)
	if err != nil {
		t.Fatal(err)
	}
	if err = purserdb.New(db).StoreProviderTaxProfile(t.Context(), purserdb.StoreProviderTaxProfileParams{TenantID: tenant, ProfileSha256: profile.ProfileSHA, Profile: frozen}); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(map[string]any{"id": invoice, "customer": "cus_" + tenant, "number": "DDV-" + invoice, "status": "paid", "currency": "eur", "total": 10000, "total_excluding_tax": 10000, "total_taxes": []any{map[string]any{"amount": 0}}, "created": point.Unix(), "invoice_pdf": "https://invoice.stripe.com/" + invoice + ".pdf", "account_name": supplier.Name, "account_country": "NL", "footer": profile.SupplierIdentity, "account_tax_ids": []any{map[string]string{"type": "eu_vat", "value": supplier.VATNumber}}, "customer_name": profile.Name, "customer_email": profile.Email, "customer_tax_ids": []any{map[string]string{"type": "eu_vat", "value": profile.VATNumber}}, "customer_address": map[string]string{"line1": profile.Street, "city": profile.City, "postal_code": profile.PostalCode, "country": profile.Country}, "metadata": map[string]string{"frameworks_tax_profile": profile.ProfileSHA}})
	if err != nil {
		t.Fatal(err)
	}
	return raw, profile
}

func TestStripeReverseChargeUsesTaxPointEvidence_RealPG(t *testing.T) {
	for _, tc := range []struct {
		name, status, reason string
		checked, expires     time.Duration
		frozen               bool
	}{{"timely", "ready", "", -2 * time.Hour, 24 * time.Hour, false}, {"late", "legacy_unknown", "vat_evidence_missing_at_tax_point", 24 * time.Hour, 48 * time.Hour, false}, {"expired", "legacy_unknown", "vat_evidence_requires_document_review", -48 * time.Hour, -time.Hour, false}, {"frozen_timely_current_row_late", "ready", "", 24 * time.Hour, 48 * time.Hour, true}} {
		t.Run(tc.name, func(t *testing.T) {
			db, service, _ := newStripeReassessmentService(t)
			tenant := seedPrepaidLedgerTenant(t, db)
			point := time.Now().UTC().Add(-48 * time.Hour).Truncate(time.Second)
			hash := sha256.Sum256([]byte("DE123456789"))
			var frozen []json.RawMessage
			if tc.frozen {
				snapshot, err := json.Marshal(map[string]any{"tenant_id": tenant, "country_code": "DE", "vat_number_hash": hex.EncodeToString(hash[:]), "valid": true, "checked_at": point.Add(-2 * time.Hour), "expires_at": point.Add(24 * time.Hour)})
				if err != nil {
					t.Fatal(err)
				}
				frozen = append(frozen, snapshot)
			}
			raw, _ := historicalStripeReverseCharge(t, db, tenant, "in_taxpoint_"+tc.name, point, frozen...)
			if _, err := db.Exec(`INSERT INTO purser.vat_validation_evidence(tenant_id,country_code,vat_number_hash,vat_number_masked,valid,checked_at,request_date,expires_at,raw_response) VALUES($1::uuid,'DE',$2,'DE*****6789',true,$3,$4,$5,'{}')`, tenant, hex.EncodeToString(hash[:]), point.Add(tc.checked), point.Format(time.DateOnly), point.Add(tc.expires)); err != nil {
				t.Fatal(err)
			}
			appconfigtest.Set(t, "SUPPLIER_NAME", "")
			if err := service.importStripeTaxDocument(t.Context(), "provider_invoice", raw); err != nil {
				t.Fatal(err)
			}
			var status string
			var proof []byte
			if err := db.QueryRow(`SELECT tax_status,tax_assessment FROM purser.provider_billing_documents WHERE tenant_id=$1::uuid`, tenant).Scan(&status, &proof); err != nil {
				t.Fatal(err)
			}
			if status != tc.status || tc.reason != "" && !bytes.Contains(proof, []byte(tc.reason)) {
				t.Fatalf("historical VIES proof used the wrong point: status%s want%s assessment%s", status, tc.status, proof)
			}
		})
	}
}

func TestStripeGraceOriginalKeepsDocumentReviewAfterVIESNotice_RealPG(t *testing.T) {
	db, service, _ := newStripeReassessmentService(t)
	tenant := seedPrepaidLedgerTenant(t, db)
	point := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	raw, profile := historicalStripeReverseCharge(t, db, tenant, "in_retained_grace", point)
	var proof CryptoTaxAssessment
	if err := json.Unmarshal(profile.Evidence, &proof); err != nil {
		t.Fatal(err)
	}
	proof.VATValidationMode = "outage_grace"
	encodedProof, err := json.Marshal(proof)
	if err != nil {
		t.Fatal(err)
	}
	profile.Evidence = encodedProof
	assessment, err := json.Marshal(profile)
	if err != nil {
		t.Fatal(err)
	}
	doc, err := billingstripe.ParseProviderTaxDocument("provider_invoice", raw)
	if err != nil {
		t.Fatal(err)
	}
	pdf := []byte("%PDF-1.4\nRetained grace original\n%%EOF")
	digest := sha256.Sum256(pdf)
	q := purserdb.New(db)
	stored, err := q.StoreProviderBillingDocument(t.Context(), purserdb.StoreProviderBillingDocumentParams{TenantID: tenant, ProviderAccountID: "acct_reassessment", DocumentKind: doc.Kind, ProviderDocumentID: doc.ID, OriginalNumber: doc.Number, Status: doc.Status, Currency: doc.Currency, NetMinor: sql.NullInt64{Int64: 10000, Valid: true}, VatMinor: sql.NullInt64{Int64: 0, Valid: true}, GrossMinor: 10000, TaxStatus: "ready", TaxGroups: doc.TaxGroups, SupplierSnapshot: doc.Supplier, CustomerSnapshot: doc.Customer, RawFacts: raw, TaxAssessment: assessment, OriginalPdfUrl: sql.NullString{String: doc.PDFURL, Valid: true}, OriginalPdf: pdf, PdfSha256: sql.NullString{String: hex.EncodeToString(digest[:]), Valid: true}, IssuedAt: point})
	if err != nil {
		t.Fatal(err)
	}
	captured := stripeCapturedTaxFacts(t, db, tenant, stored.ID)
	if err = q.RecordCryptoAccountingAnomaly(t.Context(), purserdb.RecordCryptoAccountingAnomalyParams{TenantID: tenant, Kind: "vies_revalidation", Network: "vies", ReferenceType: "vat_number", ReferenceID: tenant, Detail: "VIES notice", EvidenceJson: json.RawMessage(`{}`)}); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`UPDATE purser.crypto_accounting_anomalies SET status='resolved',resolved_at=NOW() WHERE tenant_id=$1::uuid AND kind='vies_revalidation'`, tenant); err != nil {
		t.Fatal(err)
	}
	if err = service.importStripeTaxDocument(t.Context(), "provider_invoice", raw); err != nil {
		t.Fatal(err)
	}
	var status string
	var occurrences int
	if err = db.QueryRow(`SELECT status,occurrences FROM purser.crypto_accounting_anomalies WHERE tenant_id=$1::uuid AND kind='provider_tax_evidence_review' AND reference_id=$2`, tenant, stored.ID).Scan(&status, &occurrences); err != nil {
		t.Fatal(err)
	}
	if status != "open" || occurrences != 1 {
		t.Fatalf("original grace review disappeared with tenant VIES notice: %s occurrences%d", status, occurrences)
	}
	if _, err = db.Exec(`UPDATE purser.crypto_accounting_anomalies SET status='resolved',resolved_at=NOW(),resolution_note='Original claim reviewed' WHERE tenant_id=$1::uuid AND kind='provider_tax_evidence_review' AND reference_id=$2`, tenant, stored.ID); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err = service.importStripeTaxDocument(t.Context(), "provider_invoice", raw); err != nil {
			t.Fatal(err)
		}
	}
	if err = db.QueryRow(`SELECT status,occurrences FROM purser.crypto_accounting_anomalies WHERE tenant_id=$1::uuid AND kind='provider_tax_evidence_review' AND reference_id=$2`, tenant, stored.ID).Scan(&status, &occurrences); err != nil {
		t.Fatal(err)
	}
	if status != "resolved" || occurrences != 1 || !bytes.Equal(captured, stripeCapturedTaxFacts(t, db, tenant, stored.ID)) {
		t.Fatalf("unchanged replay reopened review or rewrote original: status%s occurrences%d", status, occurrences)
	}
}

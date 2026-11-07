//go:build schema_verify

package handlers

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"

	"frameworks/api_billing/internal/database/purserdb"
	billingstripe "frameworks/api_billing/internal/stripe"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/billing"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	mollieapi "github.com/VictorAvelar/mollie-api-go/v4/mollie"
)

func legacyProviderPolicyProfile(t *testing.T) billingstripe.TaxProfile {
	t.Helper()
	supplier := ConfiguredDocumentSupplier()
	proof := CryptoTaxAssessment{CustomerType: "business", PolicyVersion: frozenCryptoTaxPolicy, AssessedAt: time.Now().UTC().Add(-time.Hour), SupplierCountry: "NL", Supplier: supplier, Decision: vatDecision{Country: "US", RateBPS: 0, Source: "Accepted non-EU service policy", Treatment: "non_eu_service", PolicyReference: "nl-service-policy-v1", Wording: "Outside scope of Dutch VAT"}}
	evidence, err := json.Marshal(proof)
	if err != nil {
		t.Fatal(err)
	}
	return billingstripe.TaxProfile{Name: "Original Buyer", Email: "buyer@example.test", Street: "Main1", City: "Austin", PostalCode: "78701", Country: "US", RateBPS: 0, Wording: proof.Decision.Wording, SupplierCountry: "NL", SupplierIdentity: billingstripe.SupplierTaxIdentity(supplier.Name, supplier.Address, supplier.VATNumber, supplier.RegistrationNumber), PolicyReference: proof.Decision.PolicyReference, Evidence: evidence}
}

func TestProviderPolicyFieldMigrationDoesNotResyncLegacyStripe_RealPG(t *testing.T) {
	db, service, fixture := newStripeReassessmentService(t)
	tenant := seedPrepaidLedgerTenant(t, db)
	current := legacyProviderPolicyProfile(t)
	legacy := current
	legacy.PolicyReference = ""
	legacy.ProfileSHA = legacy.Fingerprint()
	stored, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	q := purserdb.New(db)
	if err = q.StoreProviderTaxProfile(t.Context(), purserdb.StoreProviderTaxProfileParams{TenantID: tenant, ProfileSha256: legacy.ProfileSHA, Profile: stored}); err != nil {
		t.Fatal(err)
	}
	retained, err := retainProviderTaxProfile(t.Context(), db, tenant, &current)
	if err != nil {
		t.Fatal(err)
	}
	if retained.Fingerprint() != legacy.ProfileSHA {
		t.Error("adding top-level policy fields changed an already accepted identical policy identity")
	}
	fixture.route("GET", "/v1/subscriptions/sub", func(recordedProviderRequest) (int, any) {
		return 200, map[string]any{"id": "sub", "status": "active", "customer": "customer", "metadata": map[string]string{"tenant_id": tenant, "frameworks_tax_profile": legacy.ProfileSHA}, "automatic_tax": map[string]bool{"enabled": false}, "default_tax_rates": []any{map[string]string{"id": "rate0"}}, "items": map[string]any{"object": "list", "has_more": false, "data": []any{map[string]any{"id": "item", "tax_rates": []any{}}}}}
	})
	fixture.route("GET", "/v1/customers/customer", func(recordedProviderRequest) (int, any) {
		return 200, map[string]any{"id": "customer", "metadata": map[string]string{"tenant_id": tenant}, "name": legacy.Name, "email": legacy.Email, "address": map[string]string{"line1": legacy.Street, "city": legacy.City, "postal_code": legacy.PostalCode, "country": legacy.Country}, "tax_exempt": "none", "invoice_settings": map[string]string{"footer": legacy.SupplierIdentity + "\n" + legacy.Wording}, "tax_ids": map[string]any{"object": "list", "data": []any{}}}
	})
	fixture.route("GET", "/v1/tax_rates", func(recordedProviderRequest) (int, any) {
		return 200, map[string]any{"object": "list", "has_more": false, "data": []any{map[string]any{"id": "rate0", "active": true, "country": "NL", "percentage": 0, "inclusive": false, "display_name": "VAT", "metadata": map[string]string{"frameworks_policy": billing.CurrentInvoiceTaxPolicy}}}}
	})
	if err = service.stripeClient.SyncSubscriptionTax(t.Context(), tenant, "customer", "sub", *retained); err != nil {
		t.Errorf("pure policy-field migration attempted provider resynchronization: %v", err)
	}
	if len(fixture.matching("POST", "/v1/subscriptions/sub")) != 0 || len(fixture.matching("POST", "/v1/customers/customer")) != 0 {
		t.Error("unchanged legacy legal/rate/policy terms caused provider writes")
	}
	renewed := legacyProviderPolicyProfile(t)
	var renewedProof CryptoTaxAssessment
	if err = json.Unmarshal(renewed.Evidence, &renewedProof); err != nil {
		t.Fatal(err)
	}
	from, until := time.Now().UTC().Add(-time.Hour), time.Now().UTC().Add(24*time.Hour)
	renewedProof.Decision.PolicyValidFrom, renewedProof.Decision.PolicyValidUntil = &from, &until
	renewed.PolicyValidFrom, renewed.PolicyValidUntil = &from, &until
	renewed.Evidence, _ = json.Marshal(renewedProof)
	renewal, err := retainProviderTaxProfile(t.Context(), db, tenant, &renewed)
	if err != nil || renewal.Fingerprint() == legacy.ProfileSHA || renewal.PolicyValidUntil == nil {
		t.Fatalf("genuine policy window renewal reused old identity: %+v %v", renewal, err)
	}
	after, err := q.GetProviderTaxProfile(t.Context(), purserdb.GetProviderTaxProfileParams{TenantID: tenant, ProfileSha256: legacy.ProfileSHA})
	if err != nil || !bytes.Equal(stored, after) {
		// JSONB normalizes whitespace/order, so compare against its original
		// semantic object instead of the application's encoding.
		var beforeObject, afterObject any
		if json.Unmarshal(stored, &beforeObject) != nil || json.Unmarshal(after, &afterObject) != nil {
			t.Fatal("accepted legacy profile was lost", err)
		}
		beforeJSON, _ := json.Marshal(beforeObject)
		afterJSON, _ := json.Marshal(afterObject)
		if !bytes.Equal(beforeJSON, afterJSON) {
			t.Fatal("compatibility promotion rewrote immutable accepted proof")
		}
	}
}

func TestNativeMollieLegacyPolicyShapeDoesNotCreateFalseReview_RealPG(t *testing.T) {
	db, _, _ := newStripeReassessmentService(t)
	tenant := seedPrepaidLedgerTenant(t, db)
	if _, err := db.Exec(`UPDATE purser.tenant_subscriptions SET billing_name='Original Buyer',billing_email='buyer@example.test',billing_customer_type='business',billing_address='{"street":"Main1","city":"Austin","postal_code":"78701","country":"US"}' WHERE tenant_id=$1::uuid`, tenant); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO purser.mollie_customers(tenant_id,mollie_customer_id) VALUES($1::uuid,'customer')`, tenant); err != nil {
		t.Fatal(err)
	}
	current, err := AssessStripeTaxProfile(t.Context(), db, tenant, "")
	if err != nil {
		t.Fatal(err)
	}
	legacy := *current
	legacy.PolicyReference, legacy.PolicyValidFrom, legacy.PolicyValidUntil = "", nil, nil
	legacy.ProfileSHA = legacy.Fingerprint()
	frozen, _ := json.Marshal(legacy)
	q := purserdb.New(db)
	if err = q.StoreProviderTaxProfile(t.Context(), purserdb.StoreProviderTaxProfileParams{TenantID: tenant, ProfileSha256: legacy.ProfileSHA, Profile: frozen}); err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC()
	payment := &mollieapi.Payment{ID: "inflight-legacy-policy", CustomerID: "customer", SubscriptionID: "subscription", Status: "paid", PaidAt: &at, Amount: &mollieapi.Amount{Currency: "EUR", Value: "100.00"}, Metadata: map[string]any{"tenant_id": tenant, "frameworks_tax_profile": legacy.ProfileSHA, "frameworks_net_minor": "10000", "frameworks_vat_bps": "0"}}
	if err = (&Service{db: db, logger: logging.NewLogger()}).issueMollieNativeTaxInvoice(t.Context(), tenant, payment); err != nil {
		t.Fatal(err)
	}
	var reviewed bool
	var notices int
	if err = db.QueryRow(`SELECT COALESCE((tax_decision->>'review_required')::boolean,false),(SELECT count(*) FROM purser.crypto_accounting_anomalies WHERE tenant_id=$1::uuid AND kind='provider_tax_document_review') FROM purser.billing_invoices WHERE tenant_id=$1::uuid AND source_reference_id=$2`, tenant, payment.ID).Scan(&reviewed, &notices); err != nil {
		t.Fatal(err)
	}
	if reviewed || notices != 0 {
		t.Fatalf("identical immutable legacy policy became false Mollie review: reviewed%v notices%d", reviewed, notices)
	}
}

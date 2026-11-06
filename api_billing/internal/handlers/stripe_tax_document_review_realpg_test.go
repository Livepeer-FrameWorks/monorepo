//go:build schema_verify

package handlers

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"frameworks/api_billing/internal/appconfig/appconfigtest"
	"frameworks/api_billing/internal/database/purserdb"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
)

func TestStripeRenewalRequiresMatchingTaxSetup_RealPG(t *testing.T) {
	db := startPurserUsageRealPG(t)
	tenant := seedPrepaidLedgerTenant(t, db)
	for key, value := range map[string]string{"SUPPLIER_NAME": "DDVTech B.V.", "SUPPLIER_ADDRESS": "Leiden NL", "SUPPLIER_VAT_NUMBER": "NL853319145B01", "SUPPLIER_REGISTRATION_NUMBER": "59104694", "SUPPLIER_COUNTRY": "NL"} {
		appconfigtest.Set(t, key, value)
	}
	if _, err := db.ExecContext(t.Context(), `UPDATE purser.tenant_subscriptions SET stripe_customer_id='cus_tax_review',billing_customer_type='consumer',billing_name='Original Buyer',billing_email='buyer@example.test',billing_address='{"street":"Main 1","city":"Leiden","postal_code":"2332ED","country":"NL"}' WHERE tenant_id=$1::uuid`, tenant); err != nil {
		t.Fatal(err)
	}
	fixture, client := installStripeFixture(t)
	fixture.route("GET", "/v1/account", func(recordedProviderRequest) (int, any) {
		return http.StatusOK, map[string]any{"id": "acct_tax_review", "object": "account"}
	})
	previous := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = previous })
	http.DefaultTransport = providerPDFTestTransport{next: previous, handler: fixtureRoundTripper{handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("%PDF-1.4\nOriginal " + r.URL.Path + "\n%%EOF"))
	})}}
	service := &Service{db: db, stripeClient: client, logger: logging.NewLogger()}
	profile, err := AssessStripeTaxProfile(t.Context(), db, tenant, "")
	if err != nil {
		t.Fatal(err)
	}
	var initial CryptoTaxAssessment
	if err = json.Unmarshal(profile.Evidence, &initial); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, sha string
		net, vat  int64
		ready     bool
		mixed     bool
	}{
		{"unsynced", "", 10000, 0, false, false},
		{"missing_profile", strings.Repeat("b", 64), 10000, 0, false, false},
		{"wrong_tax", profile.ProfileSHA, 10000, 0, false, false},
		{"matching", profile.ProfileSHA, 10000, 2100, true, false},
		{"mixed_original_credit", profile.ProfileSHA, 5000, 2100, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			invoice := map[string]any{
				"id": "in_" + tc.name, "customer": "cus_tax_review", "number": "DDV-" + tc.name, "status": "paid", "currency": "eur",
				"total": tc.net + tc.vat, "total_excluding_tax": tc.net, "total_taxes": []any{map[string]any{"amount": tc.vat}},
				"created": time.Now().Unix(), "invoice_pdf": "https://invoice.stripe.com/in_" + tc.name + ".pdf",
				"account_name": "DDVTech B.V.", "account_country": "NL", "footer": profile.SupplierIdentity,
				"customer_name": "Original Buyer", "customer_email": "buyer@example.test", "customer_address": map[string]string{"line1": "Main 1", "city": "Leiden", "postal_code": "2332ED", "country": "NL"},
				"parent": map[string]any{"subscription_details": map[string]any{"metadata": map[string]any{"frameworks_tax_profile": tc.sha}}},
			}
			if tc.mixed {
				invoice["lines"] = map[string]any{"has_more": false, "data": []any{
					map[string]any{"amount": 10000, "taxes": []any{map[string]any{"amount": 2100}}},
					map[string]any{"amount": -5000, "taxes": []any{map[string]any{"amount": 0}}, "metadata": map[string]string{
						"frameworks_credit_invoice": "in_original_untaxed", "frameworks_credit_line": "il_original_untaxed",
						"frameworks_credit_original_net": "10000", "frameworks_credit_original_vat": "0",
						"frameworks_credit_rates": `[{"id":"txr_original_zero","rate_bps":0}]`,
					}},
				}}
			}
			raw, err := json.Marshal(invoice)
			if err != nil {
				t.Fatal(err)
			}
			if err = service.importStripeTaxDocument(t.Context(), "provider_invoice", raw); err != nil {
				t.Fatal(err)
			}
			q := purserdb.New(db)
			found, err := q.FindProviderBillingDocument(t.Context(), purserdb.FindProviderBillingDocumentParams{TenantID: tenant, ProviderDocumentID: "in_" + tc.name, DocumentKind: "provider_invoice"})
			if err != nil {
				t.Fatal(err)
			}
			doc, err := q.GetProviderBillingDocument(t.Context(), purserdb.GetProviderBillingDocumentParams{TenantID: tenant, DocumentID: found.ID, DocumentKind: "provider_invoice"})
			if err != nil {
				t.Fatal(err)
			}
			if (doc.TaxStatus == "ready") != tc.ready {
				t.Fatalf("renewal readiness ignored its synchronized tax setup: status=%s want_ready=%v", doc.TaxStatus, tc.ready)
			}
			if doc.GrossMinor != tc.net+tc.vat || doc.NetMinor.Int64 != tc.net || doc.VatMinor.Int64 != tc.vat || len(doc.OriginalPdf) == 0 {
				t.Fatal("review changed the original provider amounts or omitted its PDF")
			}
			var retained struct {
				ReviewRequired bool                `json:"review_required"`
				Evidence       CryptoTaxAssessment `json:"evidence"`
			}
			if err = json.Unmarshal(doc.TaxAssessment, &retained); err != nil {
				t.Fatal(err)
			}
			if tc.ready {
				if retained.ReviewRequired || !retained.Evidence.AssessedAt.Equal(initial.AssessedAt) || retained.Evidence.AssessedAt.After(doc.IssuedAt.Add(time.Second-time.Nanosecond)) {
					t.Fatal("invoice lost retained acceptance proof or postdated its tax point")
				}
			} else {
				var anomalies int
				if err = db.QueryRowContext(t.Context(), `SELECT count(*) FROM purser.crypto_accounting_anomalies WHERE tenant_id=$1::uuid AND kind='provider_tax_document_review' AND reference_id=$2 AND status='open'`, tenant, found.ID).Scan(&anomalies); err != nil || anomalies != 1 || !retained.ReviewRequired {
					t.Fatalf("unverified invoice has no durable visible review: anomalies=%d err=%v", anomalies, err)
				}
				invoice["total"], invoice["metadata"] = 99999, map[string]string{"frameworks_tax_profile": profile.ProfileSHA}
				replay, _ := json.Marshal(invoice)
				if err = service.importStripeTaxDocument(t.Context(), "provider_invoice", replay); err != nil {
					t.Fatal(err)
				}
				original, err := q.GetProviderBillingDocument(t.Context(), purserdb.GetProviderBillingDocumentParams{TenantID: tenant, DocumentID: found.ID, DocumentKind: "provider_invoice"})
				if err != nil || original.GrossMinor != tc.net+tc.vat || original.TaxStatus == "ready" {
					t.Fatal("retry silently rewrote an already issued provider invoice")
				}
			}
		})
	}
}

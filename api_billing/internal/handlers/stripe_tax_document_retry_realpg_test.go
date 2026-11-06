//go:build schema_verify

package handlers

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"frameworks/api_billing/internal/appconfig/appconfigtest"
	"frameworks/api_billing/internal/database/purserdb"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
)

func TestStripeTransientAssessmentRetriesWithoutFreezingReview_RealPG(t *testing.T) {
	db := startPurserUsageRealPG(t)
	tenant := seedPrepaidLedgerTenant(t, db)
	for key, value := range map[string]string{"SUPPLIER_NAME": "DDVTech B.V.", "SUPPLIER_ADDRESS": "Leiden NL", "SUPPLIER_VAT_NUMBER": "NL853319145B01", "SUPPLIER_REGISTRATION_NUMBER": "59104694", "SUPPLIER_COUNTRY": "NL"} {
		appconfigtest.Set(t, key, value)
	}
	if _, err := db.ExecContext(t.Context(), `UPDATE purser.tenant_subscriptions SET stripe_customer_id='cus_retry',billing_customer_type='consumer',billing_name='Buyer',billing_email='buyer@example.test',billing_address='{"street":"Main1","city":"Leiden","postal_code":"2332ED","country":"NL"}' WHERE tenant_id=$1::uuid`, tenant); err != nil {
		t.Fatal(err)
	}
	fixture, client := installStripeFixture(t)
	fixture.route("GET", "/v1/account", func(recordedProviderRequest) (int, any) {
		return 200, map[string]any{"id": "acct_retry", "object": "account"}
	})
	previous := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = previous })
	http.DefaultTransport = providerPDFTestTransport{next: previous, handler: fixtureRoundTripper{handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("%PDF-1.4\nOriginal retry invoice\n%%EOF"))
	})}}
	profile, err := AssessStripeTaxProfile(t.Context(), db, tenant, "")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(map[string]any{"id": "in_retry", "customer": "cus_retry", "number": "DDV-RETRY", "status": "paid", "currency": "eur", "total": 12100, "total_excluding_tax": 10000, "total_taxes": []any{map[string]any{"amount": 2100}}, "created": time.Now().Unix(), "invoice_pdf": "https://invoice.stripe.com/in_retry.pdf", "metadata": map[string]string{"frameworks_tax_profile": profile.ProfileSHA}})
	if err != nil {
		t.Fatal(err)
	}
	service := &Service{db: db, stripeClient: client, logger: logging.NewLogger()}
	if _, err = db.Exec(`CREATE FUNCTION purser.fail_stripe_profile_assessment() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'temporary assessment storage failure' USING ERRCODE='08006'; END $$; CREATE TRIGGER fail_stripe_profile_assessment BEFORE INSERT ON purser.provider_tax_profiles FOR EACH ROW EXECUTE FUNCTION purser.fail_stripe_profile_assessment()`); err != nil {
		t.Fatal(err)
	}
	if err = service.importStripeTaxDocument(t.Context(), "provider_invoice", raw); err == nil {
		t.Error("temporary database error was accepted as a permanent invoice assessment")
	}
	var count int
	if err = db.QueryRowContext(t.Context(), `SELECT count(*) FROM purser.provider_billing_documents WHERE tenant_id=$1::uuid AND provider_document_id='in_retry'`, tenant).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Error("temporary assessment failure froze an immutable unknown verdict")
	}
	if _, err = db.Exec(`DROP TRIGGER fail_stripe_profile_assessment ON purser.provider_tax_profiles; DROP FUNCTION purser.fail_stripe_profile_assessment()`); err != nil {
		t.Fatal(err)
	}
	if err = service.importStripeTaxDocument(t.Context(), "provider_invoice", raw); err != nil {
		t.Fatal(err)
	}
	q := purserdb.New(db)
	found, err := q.FindProviderBillingDocument(t.Context(), purserdb.FindProviderBillingDocumentParams{TenantID: tenant, ProviderDocumentID: "in_retry", DocumentKind: "provider_invoice"})
	if err != nil {
		t.Fatal(err)
	}
	doc, err := q.GetProviderBillingDocument(t.Context(), purserdb.GetProviderBillingDocumentParams{TenantID: tenant, DocumentID: found.ID, DocumentKind: "provider_invoice"})
	if err != nil || doc.TaxStatus != "ready" || doc.GrossMinor != 12100 || doc.NetMinor.Int64 != 10000 || doc.VatMinor.Int64 != 2100 || len(doc.OriginalPdf) == 0 {
		t.Fatalf("successful retry could not complete the original tax document: status=%s err=%v", doc.TaxStatus, err)
	}
}

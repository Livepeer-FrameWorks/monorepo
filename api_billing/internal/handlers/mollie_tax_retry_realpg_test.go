//go:build schema_verify

package handlers

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"frameworks/api_billing/internal/appconfig/appconfigtest"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	mollieapi "github.com/VictorAvelar/mollie-api-go/v4/mollie"
)

func TestNativeMollieTransientAssessmentRetriesBeforeIssuance_RealPG(t *testing.T) {
	db, service, _ := newStripeReassessmentService(t)
	tenant := seedPrepaidLedgerTenant(t, db)
	if _, err := db.Exec(`UPDATE purser.tenant_subscriptions SET billing_name='Original Buyer',billing_email='buyer@example.test',billing_customer_type='business',billing_address='{"street":"Main1","city":"Berlin","postal_code":"1000","country":"DE"}',tax_id='DE123456789' WHERE tenant_id=$1::uuid`, tenant); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO purser.mollie_customers(tenant_id,mollie_customer_id) VALUES($1::uuid,'customer')`, tenant); err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256([]byte("DE123456789"))
	if _, err := db.Exec(`INSERT INTO purser.vat_validation_evidence(tenant_id,country_code,vat_number_hash,vat_number_masked,valid,request_date,expires_at,raw_response) VALUES($1::uuid,'DE',$2,'DE*****6789',true,CURRENT_DATE,NOW()+INTERVAL '1 day','{}')`, tenant, hex.EncodeToString(hash[:])); err != nil {
		t.Fatal(err)
	}
	terms, err := AssessMollieTaxTerms(t.Context(), db, tenant, 10000)
	if err != nil {
		t.Fatal(err)
	}
	meta := map[string]any{"tenant_id": tenant}
	terms.Metadata(meta)
	at := time.Now().UTC()
	payment := &mollieapi.Payment{ID: "paid-retained-proof", CustomerID: "customer", SubscriptionID: "subscription", Status: "paid", PaidAt: &at, Amount: &mollieapi.Amount{Currency: "EUR", Value: "100.00"}, Metadata: meta}
	// A real read failure remains retryable and cannot freeze a partial invoice.
	if _, err = db.Exec(`ALTER TABLE purser.provider_tax_profiles RENAME TO temporarily_unavailable_tax_profiles`); err != nil {
		t.Fatal(err)
	}
	issueErr := service.issueMollieNativeTaxInvoice(t.Context(), tenant, payment)
	if _, err = db.Exec(`ALTER TABLE purser.temporarily_unavailable_tax_profiles RENAME TO provider_tax_profiles`); err != nil {
		t.Fatal(err)
	}
	if issueErr == nil {
		t.Fatal("database read failure became a permanent invoice")
	}
	var count int
	if err = db.QueryRow(`SELECT COUNT(*) FROM purser.billing_invoices WHERE tenant_id=$1::uuid`, tenant).Scan(&count); err != nil || count != 0 {
		t.Fatalf("failed read froze documents%d %v", count, err)
	}
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()
	appconfigtest.Set(t, "VIES_ENDPOINT", server.URL)
	if _, err = db.Exec(`DELETE FROM purser.vat_validation_evidence WHERE tenant_id=$1::uuid`, tenant); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err = service.issueMollieNativeTaxInvoice(t.Context(), tenant, payment); err != nil {
			t.Fatal(err)
		}
	}
	var reviewed bool
	var gross int64
	if err = db.QueryRow(`SELECT COALESCE((tax_decision->>'review_required')::boolean,false),gross_eur_cents FROM purser.billing_invoices WHERE tenant_id=$1::uuid`, tenant).Scan(&reviewed, &gross); err != nil {
		t.Fatal(err)
	}
	if requests.Load() != 0 || reviewed || gross != 10000 {
		t.Fatalf("retry replaced valid accepted evidence with today's VIES: requests%d review%v gross%d", requests.Load(), reviewed, gross)
	}
}

func TestNativeMollieOutageGraceRetainsVisibleReview_RealPG(t *testing.T) {
	db := startPurserUsageRealPG(t)
	for key, value := range map[string]string{"SUPPLIER_NAME": "DDVTech B.V.", "SUPPLIER_ADDRESS": "Leiden NL", "SUPPLIER_VAT_NUMBER": "NL853319145B01", "SUPPLIER_REGISTRATION_NUMBER": "59104694", "SUPPLIER_COUNTRY": "NL"} {
		appconfigtest.Set(t, key, value)
	}
	tenant := seedPrepaidLedgerTenant(t, db)
	if _, err := db.Exec(`UPDATE purser.tenant_subscriptions SET billing_name='Buyer',billing_email='buyer@example.test',billing_customer_type='business',billing_address='{"street":"Main1","city":"Berlin","postal_code":"10115","country":"DE"}',tax_id='DE123456789' WHERE tenant_id=$1::uuid`, tenant); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO purser.mollie_customers(tenant_id,mollie_customer_id) VALUES($1::uuid,'customer')`, tenant); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte("DE123456789"))
	if _, err := db.Exec(`INSERT INTO purser.vat_validation_evidence(tenant_id,country_code,vat_number_hash,vat_number_masked,valid,request_date,expires_at,raw_response) VALUES($1,'DE',$2,'DE*****6789',true,CURRENT_DATE,NOW()+INTERVAL '1 day','{}')`, tenant, hex.EncodeToString(digest[:])); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE purser.vat_validation_evidence SET expires_at=NOW()-INTERVAL '1 minute' WHERE tenant_id=$1::uuid`, tenant); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusServiceUnavailable) }))
	defer server.Close()
	appconfigtest.Set(t, "VIES_ENDPOINT", server.URL)
	terms, err := AssessMollieTaxTerms(t.Context(), db, tenant, 10000)
	if err != nil {
		t.Fatal(err)
	}
	meta := map[string]any{"tenant_id": tenant}
	terms.Metadata(meta)
	at := time.Now().UTC()
	payment := &mollieapi.Payment{ID: "paid-grace", CustomerID: "customer", SubscriptionID: "subscription", Status: "paid", PaidAt: &at, Amount: &mollieapi.Amount{Currency: "EUR", Value: "100.00"}, Metadata: meta}
	if err = (&Service{db: db, logger: logging.NewLogger()}).issueMollieNativeTaxInvoice(t.Context(), tenant, payment); err != nil {
		t.Fatal(err)
	}
	var reviewed bool
	var mode string
	var notices int
	if err = db.QueryRow(`SELECT COALESCE((tax_decision->>'review_required')::boolean,false),tax_decision->>'vat_validation_mode',(SELECT count(*) FROM purser.crypto_accounting_anomalies WHERE tenant_id=$1::uuid AND kind='vies_revalidation' AND status='open') FROM purser.billing_invoices WHERE tenant_id=$1::uuid`, tenant).Scan(&reviewed, &mode, &notices); err != nil {
		t.Fatal(err)
	}
	if !reviewed || mode != "outage_grace" || notices != 1 {
		t.Fatalf("native Mollie grace review invisible: review%v mode%s notices%d", reviewed, mode, notices)
	}
	if _, err = db.Exec(`UPDATE purser.crypto_accounting_anomalies SET status='resolved' WHERE tenant_id=$1::uuid AND kind='vies_revalidation'`, tenant); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow(`SELECT count(*) FROM purser.crypto_accounting_anomalies a JOIN purser.billing_invoices i ON i.tenant_id=a.tenant_id AND i.id::text=a.reference_id WHERE a.tenant_id=$1::uuid AND a.kind='invoice_tax_evidence_review' AND a.reference_type='invoice' AND a.status='open'`, tenant).Scan(&notices); err != nil {
		t.Fatal(err)
	}
	if notices != 1 {
		t.Fatalf("native Mollie invoice lost document-specific grace review after VIES notice closed: %d", notices)
	}
}

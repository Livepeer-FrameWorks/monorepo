//go:build schema_verify

package handlers

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"frameworks/api_billing/internal/appconfig/appconfigtest"
	"frameworks/api_billing/internal/pricing"
	"frameworks/api_billing/internal/rating"
	billingtax "frameworks/api_billing/internal/tax"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/database"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

func TestPostpaidVIESOutageRetriesWithoutPermanentReview_RealPG(t *testing.T) {
	db := startPurserUsageRealPG(t)
	for key, value := range map[string]string{"SUPPLIER_NAME": "DDVTech B.V.", "SUPPLIER_ADDRESS": "Leiden NL", "SUPPLIER_VAT_NUMBER": "NL853319145B01", "SUPPLIER_REGISTRATION_NUMBER": "59104694", "SUPPLIER_COUNTRY": "NL"} {
		appconfigtest.Set(t, key, value)
	}
	tenant := seedPrepaidLedgerTenant(t, db)
	if _, err := db.Exec(`UPDATE purser.tenant_subscriptions SET billing_model='postpaid',billing_name='Original Buyer',billing_email='buyer@example.test',billing_customer_type='business',billing_address='{"street":"Main1","city":"Berlin","postal_code":"10115","country":"DE"}',tax_id='DE123456789' WHERE tenant_id=$1::uuid`, tenant); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusServiceUnavailable) }))
	defer server.Close()
	appconfigtest.Set(t, "VIES_ENDPOINT", server.URL)
	start := time.Now().UTC().Add(-time.Hour)
	amount := decimal.NewFromInt(100)
	run := &postpaidInvoiceRun{tenantID: tenant, currency: "EUR", presentmentCurrency: "EUR", periodStart: start, periodEnd: start.Add(time.Hour), invoiceID: uuid.NewString(), billingEmail: "buyer@example.test", usageDetails: map[string]any{}, phase: usagePhase{start: start, end: start.Add(time.Hour)}, rating: &clusterRatingResult{TotalAmount: amount, BaseAmount: amount, BaseLine: pricedLine{PricingSource: pricing.SourceTier, LineItem: rating.LineItem{LineKey: "base_subscription", Description: "Support subscription", Currency: "EUR", Amount: amount, UnitPrice: amount, Quantity: decimal.NewFromInt(1), BillableQuantity: decimal.NewFromInt(1)}}}}
	write := func() error {
		return database.WithRetryablePostgresTx(t.Context(), db, nil, func(tx *sql.Tx) error {
			_, err := writePostpaidInvoiceTx(t.Context(), tx, run)
			return err
		})
	}
	if err := write(); !billingtax.IsTransient(err) {
		t.Errorf("VIES outage did not remain retryable: %v", err)
	}
	var invoices, reviews, events int
	if err := db.QueryRow(`SELECT (SELECT count(*) FROM purser.billing_invoices WHERE tenant_id=$1::uuid),(SELECT count(*) FROM purser.crypto_accounting_anomalies WHERE tenant_id=$1::uuid AND kind='invoice_tax_review'),(SELECT count(*) FROM purser.domain_event_outbox WHERE tenant_id=$1::uuid)`, tenant).Scan(&invoices, &reviews, &events); err != nil {
		t.Fatal(err)
	}
	if invoices != 0 || reviews != 0 || events != 0 {
		t.Errorf("transient outage became durable manual review: invoices%d reviews%d events%d", invoices, reviews, events)
	}
	digest := sha256.Sum256([]byte("DE123456789"))
	if _, err := db.Exec(`INSERT INTO purser.vat_validation_evidence(tenant_id,country_code,vat_number_hash,vat_number_masked,valid,request_date,expires_at,raw_response) VALUES($1,'DE',$2,'DE*****6789',true,CURRENT_DATE,NOW()+INTERVAL '1 day','{}')`, tenant, hex.EncodeToString(digest[:])); err != nil {
		t.Fatal(err)
	}
	if err := write(); err != nil {
		t.Fatal(err)
	}
	var status string
	var net, vat, gross int64
	if err := db.QueryRow(`SELECT tax_status,net_eur_cents,vat_eur_cents,gross_eur_cents FROM purser.billing_invoices WHERE tenant_id=$1::uuid`, tenant).Scan(&status, &net, &vat, &gross); err != nil {
		t.Fatal(err)
	}
	if status != "ready" || net != 10000 || vat != 0 || gross != 10000 {
		t.Fatalf("retry lost original invoice tax/money: status%s net%d VAT%d gross%d", status, net, vat, gross)
	}
}

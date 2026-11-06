//go:build schema_verify

package handlers

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"testing"
	"time"

	"frameworks/api_billing/internal/appconfig/appconfigtest"
	"frameworks/api_billing/internal/database/purserdb"
	billingstripe "frameworks/api_billing/internal/stripe"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	mollieapi "github.com/VictorAvelar/mollie-api-go/v4/mollie"
)

func TestNativeMollieConfirmedPaymentRetainsAcceptedSupplier_RealPG(t *testing.T) {
	db := startPurserUsageRealPG(t)
	fixture, client := installMollieFixture(t)
	for key, value := range map[string]string{"SUPPLIER_NAME": "DDVTech B.V.", "SUPPLIER_ADDRESS": "Leiden NL", "SUPPLIER_VAT_NUMBER": "NL853319145B01", "SUPPLIER_REGISTRATION_NUMBER": "59104694", "SUPPLIER_COUNTRY": "NL"} {
		appconfigtest.Set(t, key, value)
	}
	tenant := seedPrepaidLedgerTenant(t, db)
	if _, err := db.Exec(`UPDATE purser.tenant_subscriptions SET billing_model='postpaid',mollie_subscription_id='subscription',dunning_attempts=2,billing_name='Buyer',billing_email='buyer@example.test',billing_customer_type='consumer',billing_address='{"street":"Main1","city":"Leiden","postal_code":"2332ED","country":"NL"}' WHERE tenant_id=$1::uuid`, tenant); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO purser.mollie_customers(tenant_id,mollie_customer_id) VALUES($1::uuid,'customer')`, tenant); err != nil {
		t.Fatal(err)
	}
	terms, err := AssessMollieTaxTerms(t.Context(), db, tenant, 10000)
	if err != nil {
		t.Fatal(err)
	}
	meta := map[string]any{"tenant_id": tenant}
	terms.Metadata(meta)
	at := time.Now().UTC().Truncate(time.Second)
	next := at.AddDate(0, 1, 0).Format(time.DateOnly)
	fixture.route("GET", "/v2/customers/customer/subscriptions/subscription", func(recordedProviderRequest) (int, any) {
		return 200, map[string]any{"id": "subscription", "status": "active", "nextPaymentDate": next}
	})
	payment := &mollieapi.Payment{ID: "confirmed-with-frozen-supplier", CustomerID: "customer", SubscriptionID: "subscription", Status: "paid", PaidAt: &at, Amount: &mollieapi.Amount{Currency: "EUR", Value: "121.00"}, Metadata: meta}
	appconfigtest.Set(t, "SUPPLIER_NAME", "")
	svc := &Service{db: db, mollieClient: client, logger: logging.NewLogger()}
	for repeat := 0; repeat < 2; repeat++ {
		if err = svc.applyNativeMollieSubscriptionOutcome(t.Context(), tenant, payment, "confirmed"); err != nil {
			t.Fatalf("already-paid accepted terms blocked payment lifecycle when current supplier configuration disappeared: %v", err)
		}
	}
	var invoices, payments, events, reviews, dunning int
	var date string
	if err = db.QueryRow(`SELECT (SELECT count(*) FROM purser.billing_invoices WHERE tenant_id=$1::uuid),(SELECT count(*) FROM purser.billing_payments WHERE tenant_id=$1::uuid AND status='confirmed'),(SELECT count(*) FROM purser.billing_event_outbox WHERE tenant_id=$1::uuid AND event_type IN ('payment_succeeded','invoice_paid')),(SELECT count(*) FROM purser.crypto_accounting_anomalies WHERE tenant_id=$1::uuid AND kind='provider_tax_document_review' AND status='open'),dunning_attempts,mollie_next_payment_date::text FROM purser.tenant_subscriptions WHERE tenant_id=$1::uuid`, tenant).Scan(&invoices, &payments, &events, &reviews, &dunning, &date); err != nil {
		t.Fatal(err)
	}
	if invoices != 1 || payments != 1 || events != 2 || reviews != 1 || dunning != 0 || date != next {
		t.Fatalf("confirmed outcome lost/duplicated lifecycle: invoices%d payments%d events%d reviews%d dunning%d next%s", invoices, payments, events, reviews, dunning, date)
	}
	var proof invoiceTaxAssessment
	var encoded []byte
	var net, vat, gross int64
	if err = db.QueryRow(`SELECT tax_decision,net_eur_cents,vat_eur_cents,gross_eur_cents FROM purser.billing_invoices WHERE tenant_id=$1::uuid`, tenant).Scan(&encoded, &net, &vat, &gross); err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(encoded, &proof); err != nil {
		t.Fatal(err)
	}
	if !proof.ReviewRequired || proof.Supplier.Name != "DDVTech B.V." || proof.Supplier.Address != "Leiden NL" || proof.Supplier.VATNumber != "NL853319145B01" || proof.Supplier.RegistrationNumber != "59104694" || proof.Decision.RateBPS != 2100 || net != 10000 || vat != 2100 || gross != 12100 {
		t.Fatalf("accepted supplier or tax facts were lost/invented: %+v net%d VAT%d gross%d", proof, net, vat, gross)
	}
	accepted, err := purserdb.New(db).GetProviderTaxProfile(t.Context(), purserdb.GetProviderTaxProfileParams{TenantID: tenant, ProfileSha256: terms.ProfileSHA})
	if err != nil {
		t.Fatal(err)
	}
	var profile billingstripe.TaxProfile
	if err = json.Unmarshal(accepted, &profile); err != nil {
		t.Fatal(err)
	}
	var partial CryptoTaxAssessment
	if err = json.Unmarshal(profile.Evidence, &partial); err != nil {
		t.Fatal(err)
	}
	partial.Supplier.RegistrationNumber = ""
	profile.Evidence, err = json.Marshal(partial)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte("missing-original-supplier"))
	missingSHA := hex.EncodeToString(digest[:])
	encoded, err = json.Marshal(profile)
	if err != nil {
		t.Fatal(err)
	}
	if err = purserdb.New(db).StoreProviderTaxProfile(t.Context(), purserdb.StoreProviderTaxProfileParams{TenantID: tenant, ProfileSha256: missingSHA, Profile: encoded}); err != nil {
		t.Fatal(err)
	}
	payment.ID = "confirmed-with-incomplete-supplier"
	meta["frameworks_tax_profile"] = missingSHA
	if err = svc.issueMollieNativeTaxInvoice(t.Context(), tenant, payment); err == nil {
		t.Fatal("incomplete accepted supplier identity was invented at issuance")
	}
	if _, err = purserdb.New(db).GetNativeMollieInvoiceID(t.Context(), purserdb.GetNativeMollieInvoiceIDParams{TenantID: tenant, PaymentID: sql.NullString{String: payment.ID, Valid: true}}); err != sql.ErrNoRows {
		t.Fatalf("incomplete accepted proof produced a tax invoice: %v", err)
	}
}

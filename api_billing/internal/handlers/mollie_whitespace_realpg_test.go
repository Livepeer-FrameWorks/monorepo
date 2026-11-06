//go:build schema_verify

package handlers

import (
	"encoding/json"
	"testing"
	"time"

	"frameworks/api_billing/internal/appconfig/appconfigtest"
	"frameworks/api_billing/internal/database/purserdb"
	billingstripe "frameworks/api_billing/internal/stripe"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	mollieapi "github.com/VictorAvelar/mollie-api-go/v4/mollie"
)

func TestNativeMollieLegacyWhitespaceDoesNotRequireReview_RealPG(t *testing.T) {
	db := startPurserUsageRealPG(t)
	for key, value := range map[string]string{"SUPPLIER_NAME": "DDVTech B.V.", "SUPPLIER_ADDRESS": "Leiden NL", "SUPPLIER_VAT_NUMBER": "NL853319145B01", "SUPPLIER_REGISTRATION_NUMBER": "59104694", "SUPPLIER_COUNTRY": "NL"} {
		appconfigtest.Set(t, key, value)
	}
	tenant := seedPrepaidLedgerTenant(t, db)
	if _, err := db.Exec(`UPDATE purser.tenant_subscriptions SET billing_name=' Buyer ',billing_company=' Company ',billing_email=' buyer@example.test ',billing_customer_type='business',billing_address='{"street":" Main1 ","city":" Leiden ","postal_code":" 2332ED ","country":" nl "}',tax_id=' nl 853319145b01 ' WHERE tenant_id=$1::uuid`, tenant); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO purser.mollie_customers(tenant_id,mollie_customer_id) VALUES($1::uuid,'customer')`, tenant); err != nil {
		t.Fatal(err)
	}
	terms, err := AssessMollieTaxTerms(t.Context(), db, tenant, 10000)
	if err != nil {
		t.Fatal(err)
	}
	q := purserdb.New(db)
	raw, err := q.GetProviderTaxProfile(t.Context(), purserdb.GetProviderTaxProfileParams{TenantID: tenant, ProfileSha256: terms.ProfileSHA})
	if err != nil {
		t.Fatal(err)
	}
	var legacy billingstripe.TaxProfile
	if err = json.Unmarshal(raw, &legacy); err != nil {
		t.Fatal(err)
	}
	legacy.Name, legacy.Company, legacy.Email = " Buyer ", " Company ", " buyer@example.test "
	legacy.Street, legacy.City, legacy.PostalCode, legacy.Country, legacy.VATNumber = " Main1 ", " Leiden ", " 2332ED ", " nl ", " nl 853319145b01 "
	legacy.ProfileSHA = legacy.Fingerprint()
	terms.ProfileSHA = legacy.ProfileSHA
	raw, err = json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if err = q.StoreProviderTaxProfile(t.Context(), purserdb.StoreProviderTaxProfileParams{TenantID: tenant, ProfileSha256: legacy.ProfileSHA, Profile: raw}); err != nil {
		t.Fatal(err)
	}
	meta := map[string]any{"tenant_id": tenant}
	terms.Metadata(meta)
	at := time.Now().UTC()
	payment := &mollieapi.Payment{ID: "paid-whitespace", CustomerID: "customer", SubscriptionID: "subscription", PaidAt: &at, Status: "paid", Amount: &mollieapi.Amount{Currency: "EUR", Value: "121.00"}, Metadata: meta}
	if err = (&Service{db: db, logger: logging.NewLogger()}).issueMollieNativeTaxInvoice(t.Context(), tenant, payment); err != nil {
		t.Fatal(err)
	}
	var review bool
	var name, country, vat string
	var gross int64
	var anomalies int
	if err = db.QueryRow(`SELECT COALESCE((tax_decision->>'review_required')::boolean,false),customer_snapshot->>'name',tax_decision->'customer'->>'vat_number',gross_eur_cents,(customer_snapshot->>'address')::jsonb->>'country',(SELECT count(*) FROM purser.crypto_accounting_anomalies WHERE tenant_id=$1::uuid AND kind='provider_tax_document_review') FROM purser.billing_invoices WHERE tenant_id=$1::uuid`, tenant).Scan(&review, &name, &vat, &gross, &country, &anomalies); err != nil {
		t.Fatal(err)
	}
	if review || anomalies != 0 || name != "Buyer" || country != "NL" || vat != "NL853319145B01" || gross != 12100 {
		t.Fatalf("cosmetic whitespace changed paid tax facts: review%v anomalies%d buyer%s country%s VAT%s gross%d", review, anomalies, name, country, vat, gross)
	}
}

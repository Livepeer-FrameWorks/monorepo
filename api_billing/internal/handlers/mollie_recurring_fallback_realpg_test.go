//go:build schema_verify

package handlers

import (
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"frameworks/api_billing/internal/appconfig/appconfigtest"
	"frameworks/api_billing/internal/database/purserdb"
	billingstripe "frameworks/api_billing/internal/stripe"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	mollieapi "github.com/VictorAvelar/mollie-api-go/v4/mollie"
)

func TestNativeMollieIncompleteRecurringProfileCollectsDutchVAT_RealPG(t *testing.T) {
	db := startPurserUsageRealPG(t)
	fixture, client := installMollieFixture(t)
	for key, value := range map[string]string{"SUPPLIER_NAME": "DDVTech B.V.", "SUPPLIER_ADDRESS": "Leiden NL", "SUPPLIER_VAT_NUMBER": "NL853319145B01", "SUPPLIER_REGISTRATION_NUMBER": "59104694", "SUPPLIER_COUNTRY": "NL"} {
		appconfigtest.Set(t, key, value)
	}
	tenant := seedPrepaidLedgerTenant(t, db)
	if _, err := db.Exec(`UPDATE purser.tenant_subscriptions SET mollie_subscription_id='subscription',billing_name=NULL,billing_email='buyer@example.test',billing_customer_type='business',billing_address='{"country":"US"}' WHERE tenant_id=$1::uuid`, tenant); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO purser.mollie_customers(tenant_id,mollie_customer_id) VALUES($1::uuid,'customer')`, tenant); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	state := map[string]any{"id": "subscription", "status": "active", "amount": map[string]string{"currency": "EUR", "value": "100.00"}, "metadata": map[string]any{"tenant_id": tenant, "frameworks_net_minor": "10000"}, "nextPaymentDate": time.Now().UTC().AddDate(0, 1, 0).Format(time.DateOnly)}
	copyState := func() map[string]any {
		raw, _ := json.Marshal(state)
		var copied map[string]any
		_ = json.Unmarshal(raw, &copied)
		return copied
	}
	path := "/v2/customers/customer/subscriptions/subscription"
	svc := &Service{db: db, mollieClient: client, logger: logging.NewLogger()}
	jobs := &JobManager{db: db, billing: svc, logger: logging.NewLogger()}
	fixture.route("GET", path, func(recordedProviderRequest) (int, any) {
		return 503, map[string]any{"status": 503, "title": "temporarily unavailable"}
	})
	if err := jobs.reconcileMollieTaxTenant(t.Context(), tenant); err == nil {
		t.Fatal("provider read failure disappeared")
	}
	var expectedBeforeProviderRead int
	if err := db.QueryRow(`SELECT COUNT(*) FROM purser.provider_tax_assessments WHERE tenant_id=$1::uuid AND (assessment->>'rate_bps')::int=2100`, tenant).Scan(&expectedBeforeProviderRead); err != nil || expectedBeforeProviderRead != 1 {
		t.Fatalf("provider GET failure discarded accepted domestic terms: count%d err%v", expectedBeforeProviderRead, err)
	}
	fixture.route("GET", path, func(recordedProviderRequest) (int, any) {
		mu.Lock()
		defer mu.Unlock()
		return 200, copyState()
	})
	fixture.route("PATCH", path, func(request recordedProviderRequest) (int, any) {
		var reviewsBeforeCollection int
		if err := db.QueryRow(`SELECT count(*) FROM purser.crypto_accounting_anomalies WHERE tenant_id=$1::uuid AND kind='provider_tax_review' AND network='mollie' AND reference_type='mollie_subscription' AND reference_id='subscription' AND status='open'`, tenant).Scan(&reviewsBeforeCollection); err != nil || reviewsBeforeCollection != 1 {
			t.Errorf("Mollie fallback synchronized collection before durable subscription review: count%d err%v", reviewsBeforeCollection, err)
		}
		var body map[string]any
		if err := json.Unmarshal(request.body, &body); err != nil {
			return 400, map[string]string{"error": "invalid request"}
		}
		mu.Lock()
		defer mu.Unlock()
		state["amount"], state["metadata"] = body["amount"], body["metadata"]
		return 200, copyState()
	})
	if err := jobs.reconcileMollieTaxTenant(t.Context(), tenant); err != nil {
		t.Fatalf("incomplete existing recurring profile blocked Dutch VAT collection: %v", err)
	}
	mu.Lock()
	providerState := copyState()
	mu.Unlock()
	amount := providerState["amount"].(map[string]any)
	metadata := providerState["metadata"].(map[string]any)
	sha, _ := metadata["frameworks_tax_profile"].(string)
	if amount["value"] != "121.00" || metadata["frameworks_vat_bps"] != "2100" || metadata["frameworks_net_minor"] != "10000" || len(sha) != 64 {
		t.Fatalf("recurring fallback lost net or failed to charge Dutch VAT: amount%v metadata%v", amount, metadata)
	}
	frozen, err := purserdb.New(db).GetProviderTaxProfile(t.Context(), purserdb.GetProviderTaxProfileParams{TenantID: tenant, ProfileSha256: sha})
	if err != nil {
		t.Fatal(err)
	}
	var profile billingstripe.TaxProfile
	if err = json.Unmarshal(frozen, &profile); err != nil {
		t.Fatal(err)
	}
	if profile.Name != "" || profile.Street != "" || profile.Country != "US" || profile.ReverseCharge || profile.RateBPS != 2100 {
		t.Fatalf("fallback invented buyer facts or zero-tax entitlement: %+v", profile)
	}
	for renewal := 1; renewal <= 2; renewal++ {
		at := time.Now().UTC()
		payment := &mollieapi.Payment{ID: fmt.Sprintf("fallback-renewal-%d", renewal), CustomerID: "customer", SubscriptionID: "subscription", Status: "paid", PaidAt: &at, Amount: &mollieapi.Amount{Currency: "EUR", Value: "121.00"}, Metadata: metadata}
		for repeat := 0; repeat < 2; repeat++ {
			if err = svc.applyNativeMollieSubscriptionOutcome(t.Context(), tenant, payment, "confirmed"); err != nil {
				t.Fatal(err)
			}
		}
		var invoices, reviewed, notices, payments, emails int
		if err = db.QueryRow(`SELECT (SELECT count(*) FROM purser.billing_invoices WHERE tenant_id=$1::uuid AND net_eur_cents=10000 AND vat_eur_cents=2100 AND gross_eur_cents=12100),(SELECT count(*) FROM purser.billing_invoices WHERE tenant_id=$1::uuid AND COALESCE((tax_decision->>'review_required')::boolean,false)),(SELECT count(*) FROM purser.crypto_accounting_anomalies WHERE tenant_id=$1::uuid AND kind='provider_tax_document_review' AND status='open'),(SELECT count(*) FROM purser.billing_payments WHERE tenant_id=$1::uuid AND status='confirmed'),(SELECT count(*) FROM purser.invoice_email_outbox WHERE tenant_id=$1::uuid)`, tenant).Scan(&invoices, &reviewed, &notices, &payments, &emails); err != nil {
			t.Fatal(err)
		}
		if invoices != renewal || reviewed != renewal || notices != 1 || payments != renewal {
			t.Fatalf("new fallback renewal lost/reused review or money: invoices%d reviewed%d openNotices%d payments%d", invoices, reviewed, notices, payments)
		}
		if emails != renewal {
			t.Errorf("fallback renewal omitted the requested invoice email: emails%d want%d", emails, renewal)
		}
		if _, err = db.Exec(`UPDATE purser.crypto_accounting_anomalies SET status='resolved',resolution_note='Original renewal reviewed' WHERE tenant_id=$1::uuid AND kind='provider_tax_document_review' AND status='open'`, tenant); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = db.Exec(`UPDATE purser.tenant_subscriptions SET billing_name='Verified Buyer',billing_customer_type='business',tax_id=NULL,billing_address='{"street":"Main1","city":"Leiden","postal_code":"2332ED","country":"NL"}' WHERE tenant_id=$1::uuid`, tenant); err != nil {
		t.Fatal(err)
	}
	if err = jobs.reconcileMollieTaxTenant(t.Context(), tenant); err != nil {
		t.Fatal(err)
	}
	var openSubscriptionReviews int
	if err = db.QueryRow(`SELECT count(*) FROM purser.crypto_accounting_anomalies WHERE tenant_id=$1::uuid AND kind='provider_tax_review' AND network='mollie' AND reference_type='mollie_subscription' AND reference_id='subscription' AND status='open'`, tenant).Scan(&openSubscriptionReviews); err != nil || openSubscriptionReviews != 0 {
		t.Fatalf("complete synchronized Mollie profile retained an obsolete subscription review: count %d err %v", openSubscriptionReviews, err)
	}
}

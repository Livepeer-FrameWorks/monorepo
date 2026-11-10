//go:build schema_verify

package handlers

import (
	"context"
	"database/sql"
	"strconv"
	"testing"
	"time"

	"frameworks/api_billing/internal/appconfig/appconfigtest"
	"frameworks/api_billing/internal/pricing"
	"frameworks/api_billing/internal/rating"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/database"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// An existing relationship is one whose tenant_subscriptions row has no
// tax_assessed_since: a Canadian Stripe customer on a postpaid subscription
// without a complete saved billing profile.
func TestExistingRelationshipKeepsUntaxedTreatmentUntilProfileCompleted_RealPG(t *testing.T) {
	db := startPurserUsageRealPG(t)
	for key, value := range map[string]string{"SUPPLIER_NAME": "DDVTech B.V.", "SUPPLIER_ADDRESS": "Leiden NL", "SUPPLIER_VAT_NUMBER": "NL853319145B01", "SUPPLIER_REGISTRATION_NUMBER": "59104694", "SUPPLIER_COUNTRY": "NL"} {
		appconfigtest.Set(t, key, value)
	}
	ctx := context.Background()
	stripeFixture, stripeClient := installStripeFixture(t)
	tenant := seedPrepaidLedgerTenant(t, db)
	customer, subscription := "cus_"+tenant, "sub_"+tenant
	if _, err := db.Exec(`UPDATE purser.tenant_subscriptions SET billing_model='postpaid',payment_method='stripe',stripe_customer_id=$2,stripe_subscription_id=$3,billing_name=NULL,billing_email='buyer@example.ca',billing_customer_type='unspecified',billing_address='{"city":"Toronto","country":"CA"}',tax_assessed_since=NULL WHERE tenant_id=$1::uuid`, tenant, customer, subscription); err != nil {
		t.Fatal(err)
	}
	stripeCustomer := func(recordedProviderRequest) (int, any) {
		return 200, map[string]any{"id": customer, "object": "customer", "metadata": map[string]string{"tenant_id": tenant}, "name": "Buyer", "email": "buyer@example.ca", "address": map[string]string{"line1": "1 King St W", "city": "Toronto", "postal_code": "M5H 1A1", "country": "CA"}, "tax_exempt": "none", "tax_ids": map[string]any{"object": "list", "data": []any{}, "has_more": false}}
	}
	stripeSubscription := func(recordedProviderRequest) (int, any) {
		return 200, map[string]any{"id": subscription, "object": "subscription", "customer": customer, "metadata": map[string]string{"tenant_id": tenant}, "automatic_tax": map[string]any{"enabled": false}, "items": map[string]any{"object": "list", "data": []any{}, "has_more": false}}
	}
	stripeFixture.route("GET", "/v1/customers/"+customer, stripeCustomer)
	stripeFixture.route("POST", "/v1/customers/"+customer, stripeCustomer)
	stripeFixture.route("GET", "/v1/subscriptions/"+subscription, stripeSubscription)
	stripeFixture.route("POST", "/v1/subscriptions/"+subscription, stripeSubscription)
	stripeFixture.route("GET", "/v1/tax_rates", func(recordedProviderRequest) (int, any) {
		return 200, map[string]any{"object": "list", "data": []any{}, "has_more": false, "url": "/v1/tax_rates"}
	})
	stripeFixture.route("POST", "/v1/tax_rates", func(request recordedProviderRequest) (int, any) {
		percentage, _ := strconv.ParseFloat(request.form.Get("percentage"), 64)
		return 200, map[string]any{"id": "txr_" + strconv.FormatFloat(percentage, 'f', -1, 64), "object": "tax_rate", "country": request.form.Get("country"), "percentage": percentage, "inclusive": false, "display_name": request.form.Get("display_name")}
	})
	jobs := &JobManager{db: db, billing: &Service{db: db, stripeClient: stripeClient, logger: logging.NewLogger()}, logger: logging.NewLogger()}
	count := func(t *testing.T, query string, args ...any) int {
		t.Helper()
		var n int
		if err := db.QueryRow(query, args...).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	writeInvoice := func(t *testing.T, start time.Time) postpaidInvoiceResult {
		t.Helper()
		amount := decimal.NewFromInt(100)
		run := &postpaidInvoiceRun{tenantID: tenant, currency: "EUR", presentmentCurrency: "EUR", periodStart: start, periodEnd: start.Add(time.Hour), invoiceID: uuid.NewString(), billingEmail: "buyer@example.ca", usageDetails: map[string]any{}, phase: usagePhase{start: start, end: start.Add(time.Hour)}, rating: &clusterRatingResult{TotalAmount: amount, BaseAmount: amount, BaseLine: pricedLine{PricingSource: pricing.SourceTier, LineItem: rating.LineItem{LineKey: "base_subscription", Description: "Supporter subscription", Currency: "EUR", Amount: amount, UnitPrice: amount, Quantity: decimal.NewFromInt(1), BillableQuantity: decimal.NewFromInt(1)}}}}
		var result postpaidInvoiceResult
		if err := database.WithRetryablePostgresTx(ctx, db, nil, func(tx *sql.Tx) error {
			var err error
			result, err = writePostpaidInvoiceTx(ctx, tx, run)
			return err
		}); err != nil {
			t.Fatalf("write postpaid invoice: %v", err)
		}
		return result
	}
	first := time.Now().UTC().Add(-3 * time.Hour).Truncate(time.Microsecond)

	t.Run("stripe_customer_and_subscription_untouched", func(t *testing.T) {
		if err := jobs.reconcileStripeTaxTenant(ctx, tenant); err != nil {
			t.Fatalf("existing untaxed relationship failed tax synchronization: %v", err)
		}
		profiles := count(t, `SELECT count(*) FROM purser.provider_tax_profiles WHERE tenant_id=$1::uuid`, tenant)
		expectations := count(t, `SELECT count(*) FROM purser.provider_tax_assessments WHERE tenant_id=$1::uuid`, tenant)
		if requests := len(stripeFixture.requests); requests != 0 || profiles != 0 || expectations != 0 {
			t.Fatalf("existing untaxed relationship was synchronized to assessed tax: stripe requests=%d stored provider profiles=%d expectations=%d", requests, profiles, expectations)
		}
	})

	t.Run("postpaid_invoice_collectible_without_VAT", func(t *testing.T) {
		untaxed := writeInvoice(t, first)
		var status, taxStatus string
		var fullAmount, vatRecorded bool
		if err := db.QueryRow(`SELECT status,tax_status,amount=100,vat_eur_cents IS NOT NULL FROM purser.billing_invoices WHERE tenant_id=$1::uuid AND id=$2::uuid`, tenant, untaxed.invoiceID).Scan(&status, &taxStatus, &fullAmount, &vatRecorded); err != nil {
			t.Fatal(err)
		}
		reviews := count(t, `SELECT count(*) FROM purser.crypto_accounting_anomalies WHERE tenant_id=$1::uuid AND kind='invoice_tax_review'`, tenant)
		vatLines := count(t, `SELECT count(*) FROM purser.invoice_line_items WHERE tenant_id=$1::uuid AND invoice_id=$2::uuid AND vat_eur_cents IS NOT NULL`, tenant, untaxed.invoiceID)
		if status != "pending" || taxStatus != "legacy_unknown" || !fullAmount || vatRecorded || vatLines != 0 || reviews != 0 {
			t.Fatalf("existing untaxed relationship's invoice changed treatment: status=%s tax_status=%s amount_is_net=%v vat_recorded=%v vat_lines=%d tax_reviews=%d", status, taxStatus, fullAmount, vatRecorded, vatLines, reviews)
		}
	})

	saved := BillingProfile{Name: "Buyer", Email: "buyer@example.com", Address: []byte(`{"street":"1 Congress Ave","city":"Austin","postal_code":"78701","country":"US"}`)}
	if _, err := db.Exec(`UPDATE purser.tenant_subscriptions SET billing_name=$2,billing_email=$3,billing_customer_type='consumer',billing_address=$4::jsonb WHERE tenant_id=$1::uuid`, tenant, saved.Name, saved.Email, string(saved.Address)); err != nil {
		t.Fatal(err)
	}
	if err := StartAssessedTaxHandlingForSavedProfile(ctx, db, tenant, saved); err != nil {
		t.Fatal(err)
	}
	t.Run("complete_US_profile_gets_non_eu_service_at_0", func(t *testing.T) {
		before := len(stripeFixture.matching("POST", "/v1/subscriptions/"+subscription))
		if err := jobs.reconcileStripeTaxTenant(ctx, tenant); err != nil {
			t.Fatalf("completed profile did not synchronize assessed tax: %v", err)
		}
		var rates []recordedProviderRequest
		for _, request := range stripeFixture.matching("POST", "/v1/tax_rates") {
			if percentage, _ := strconv.ParseFloat(request.form.Get("percentage"), 64); percentage == 0 {
				rates = append(rates, request)
			}
		}
		updates := stripeFixture.matching("POST", "/v1/subscriptions/"+subscription)[before:]
		if len(rates) != 1 || len(updates) != 1 || updates[0].form.Get("default_tax_rates[0]") != "txr_0" {
			t.Fatalf("completed US profile was not synchronized at 0%%: zero rates=%+v subscription updates=%+v", rates, updates)
		}
		if zero := count(t, `SELECT count(*) FROM purser.provider_tax_profiles WHERE tenant_id=$1::uuid AND (profile->>'rate_bps')::int=0 AND NOT COALESCE((profile->>'incomplete_profile')::boolean,false)`, tenant); zero != 1 {
			t.Fatalf("completed US profile stored %d zero-rate provider profiles, want 1", zero)
		}
		assessed := writeInvoice(t, first.Add(time.Hour))
		var status, taxStatus string
		var vat int64
		if err := db.QueryRow(`SELECT status,tax_status,vat_eur_cents FROM purser.billing_invoices WHERE tenant_id=$1::uuid AND id=$2::uuid`, tenant, assessed.invoiceID).Scan(&status, &taxStatus, &vat); err != nil {
			t.Fatal(err)
		}
		nonEU := count(t, `SELECT count(*) FROM purser.invoice_line_items WHERE tenant_id=$1::uuid AND invoice_id=$2::uuid AND tax_treatment='non_eu_service' AND vat_rate_bps=0`, tenant, assessed.invoiceID)
		if status != "pending" || taxStatus != "ready" || vat != 0 || nonEU != 1 {
			t.Fatalf("completed US profile did not receive non_eu_service at 0%%: status=%s tax_status=%s vat=%d non_eu_lines=%d", status, taxStatus, vat, nonEU)
		}
	})
}

func TestExistingMollieRelationshipKeepsItsCollectionAmount_RealPG(t *testing.T) {
	db := startPurserUsageRealPG(t)
	for key, value := range map[string]string{"SUPPLIER_NAME": "DDVTech B.V.", "SUPPLIER_ADDRESS": "Leiden NL", "SUPPLIER_VAT_NUMBER": "NL853319145B01", "SUPPLIER_REGISTRATION_NUMBER": "59104694", "SUPPLIER_COUNTRY": "NL"} {
		appconfigtest.Set(t, key, value)
	}
	fixture, client := installMollieFixture(t)
	tenant := seedPrepaidLedgerTenant(t, db)
	if _, err := db.Exec(`UPDATE purser.tenant_subscriptions SET mollie_subscription_id='subscription',billing_name=NULL,billing_email='buyer@example.test',billing_address='{"country":"US"}',tax_assessed_since=NULL WHERE tenant_id=$1::uuid`, tenant); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO purser.mollie_customers(tenant_id,mollie_customer_id) VALUES($1::uuid,'customer')`, tenant); err != nil {
		t.Fatal(err)
	}
	jobs := &JobManager{db: db, billing: &Service{db: db, mollieClient: client, logger: logging.NewLogger()}, logger: logging.NewLogger()}
	if err := jobs.reconcileMollieTaxTenant(t.Context(), tenant); err != nil {
		t.Fatalf("existing untaxed Mollie relationship failed tax synchronization: %v", err)
	}
	var expectations int
	if err := db.QueryRow(`SELECT count(*) FROM purser.provider_tax_assessments WHERE tenant_id=$1::uuid`, tenant).Scan(&expectations); err != nil {
		t.Fatal(err)
	}
	if requests := len(fixture.requests); requests != 0 || expectations != 0 {
		t.Fatalf("existing untaxed Mollie relationship was synchronized to assessed tax: mollie requests=%d expectations=%d", requests, expectations)
	}
}

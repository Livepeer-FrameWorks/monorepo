//go:build schema_verify

package handlers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"frameworks/api_billing/internal/appconfig/appconfigtest"
	"frameworks/api_billing/internal/database/purserdb"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/billing"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	mollieapi "github.com/VictorAvelar/mollie-api-go/v4/mollie"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

func TestNativeMolliePaymentsFreezeOneExclusiveTaxInvoice_RealPG(t *testing.T) {
	db := startPurserUsageRealPG(t)
	ctx := context.Background()
	for key, value := range map[string]string{"SUPPLIER_NAME": "DDVTech B.V.", "SUPPLIER_ADDRESS": "Amphoraweg5 Leiden NL", "SUPPLIER_VAT_NUMBER": "NL853319145B01", "SUPPLIER_REGISTRATION_NUMBER": "59104694", "SUPPLIER_COUNTRY": "NL"} {
		appconfigtest.Set(t, key, value)
	}
	for _, tc := range []struct {
		country, kind, vat string
		wantVAT            int64
	}{{"NL", "consumer", "", 2100}, {"NL", "business", "", 2100}, {"DE", "business", "DE123456789", 0}, {"CA", "business", "", 0}, {"DE", "consumer", "", 2100}} {
		t.Run(tc.country+tc.kind, func(t *testing.T) {
			tenant := seedPrepaidLedgerTenant(t, db)
			customer := "customer-" + tenant
			address := fmt.Sprintf(`{"street":"Main1","city":"City","postal_code":"1000","country":%q}`, tc.country)
			if _, err := db.Exec(`UPDATE purser.tenant_subscriptions SET billing_model='postpaid',billing_customer_type=$2,billing_name='Original Buyer',billing_email='buyer@example.test',billing_address=$3::jsonb,tax_id=NULLIF($4,'') WHERE tenant_id=$1::uuid`, tenant, tc.kind, address, tc.vat); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`INSERT INTO purser.mollie_customers(tenant_id,mollie_customer_id) VALUES($1::uuid,$2)`, tenant, customer); err != nil {
				t.Fatal(err)
			}
			if tc.vat != "" {
				digest := sha256.Sum256([]byte(tc.vat))
				if _, err := db.Exec(`INSERT INTO purser.vat_validation_evidence(tenant_id,country_code,vat_number_hash,vat_number_masked,valid,request_date,expires_at,raw_response) VALUES($1,$2,$3,'DE*****6789',true,CURRENT_DATE,NOW()+INTERVAL '1 day','{}')`, tenant, tc.country, hex.EncodeToString(digest[:])); err != nil {
					t.Fatal(err)
				}
			}
			terms, err := AssessMollieTaxTerms(ctx, db, tenant, 10000)
			if err != nil {
				t.Fatal(err)
			}
			if terms.GrossMinor != 10000+tc.wantVAT {
				t.Fatal("Mollie price did not add correct VAT", terms)
			}
			// Payment metadata is fixed before collection; changing the current buyer cannot alter its tax invoice.
			if _, err = db.Exec(`UPDATE purser.tenant_subscriptions SET billing_name='Changed Buyer',billing_address='{"street":"Other","city":"Other","postal_code":"2222","country":"CA"}'::jsonb WHERE tenant_id=$1::uuid`, tenant); err != nil {
				t.Fatal(err)
			}
			at := time.Now().UTC().Truncate(time.Microsecond)
			meta := map[string]any{"tenant_id": tenant}
			terms.Metadata(meta)
			payment := &mollieapi.Payment{ID: "payment-" + tenant, CustomerID: customer, SubscriptionID: "subscription-" + tenant, Amount: &mollieapi.Amount{Currency: "EUR", Value: decimal.New(terms.GrossMinor, -2).StringFixed(2)}, Metadata: meta, PaidAt: &at, Status: "paid"}
			svc := &Service{db: db, logger: logging.NewLogger()}
			if err = svc.issueMollieNativeTaxInvoice(ctx, tenant, payment); err != nil {
				t.Fatal(err)
			}
			if err = svc.issueMollieNativeTaxInvoice(ctx, tenant, payment); err != nil {
				t.Fatal(err)
			}
			var count, payments int
			var net, vat, gross int64
			var buyer, status string
			var frozen []byte
			if err = db.QueryRow(`SELECT count(*),min(net_eur_cents),min(vat_eur_cents),min(gross_eur_cents),min(customer_snapshot->>'name'),min(tax_status) FROM purser.billing_invoices WHERE tenant_id=$1::uuid`, tenant).Scan(&count, &net, &vat, &gross, &buyer, &status); err != nil {
				t.Fatal(err)
			}
			if count != 1 || net != 10000 || vat != tc.wantVAT || gross != terms.GrossMinor || buyer != "Original Buyer" || status != "ready" {
				t.Fatalf("wrong or duplicate native invoice: count%d net%d vat%d gross%d buyer%s status%s", count, net, vat, gross, buyer, status)
			}
			if err = db.QueryRow(`SELECT count(*) FROM purser.billing_payments WHERE tenant_id=$1::uuid AND status='confirmed' AND original_amount_cents=$2 AND eur_amount_cents=$2 AND original_currency='EUR'`, tenant, terms.GrossMinor).Scan(&payments); err != nil || payments != 1 {
				t.Fatal("missing or duplicate confirmed payment", err, payments)
			}
			if err = db.QueryRow(`SELECT tax_decision FROM purser.billing_invoices WHERE tenant_id=$1::uuid`, tenant).Scan(&frozen); err != nil || !json.Valid(frozen) {
				t.Fatal("tax proof missing", err)
			}
			payment.ID = "bad-" + tenant
			payment.Amount.Value = "1.00"
			if err = svc.issueMollieNativeTaxInvoice(ctx, tenant, payment); err != nil {
				t.Fatalf("confirmed cash mismatch stranded instead of retaining review: %v", err)
			}
			var mismatchStatus, mismatchCode string
			var actual, expected int64
			var split bool
			if err = db.QueryRow(`SELECT tax_status,tax_decision->>'code',presentment_amount_cents,(tax_decision->>'expected_gross_minor')::bigint,net_eur_cents IS NOT NULL OR vat_eur_cents IS NOT NULL OR gross_eur_cents IS NOT NULL FROM purser.billing_invoices WHERE tenant_id=$1::uuid AND source_reference_id=$2`, tenant, payment.ID).Scan(&mismatchStatus, &mismatchCode, &actual, &expected, &split); err != nil {
				t.Fatal(err)
			}
			if mismatchStatus != "review" || mismatchCode != "accepted_tax_gross_mismatch" || actual != 100 || expected != terms.GrossMinor || split {
				t.Fatalf("cash discrepancy disappeared or became valid tax: %s %s actual%d expected%d split%v", mismatchStatus, mismatchCode, actual, expected, split)
			}
			payment.ID = "legacy-" + tenant
			payment.Metadata = map[string]any{}
			if err = svc.issueMollieNativeTaxInvoice(ctx, tenant, payment); err != nil {
				t.Fatal(err)
			}
			if err = db.QueryRow(`SELECT count(*) FROM purser.billing_invoices WHERE tenant_id=$1::uuid`, tenant).Scan(&count); err != nil || count != 2 {
				t.Fatal("historical payment re-invoiced", err)
			}
		})
	}
}

func TestTaxedMollieRecurringWebhookKeepsPaymentLifecycle_RealPG(t *testing.T) {
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
	mailLog := logging.NewLogger()
	emailAttempts := &failedNativePaymentEmailHook{}
	mailLog.AddHook(emailAttempts)
	svc := &Service{db: db, mollieClient: client, logger: logging.NewLogger(), emailService: &EmailService{logger: mailLog}}
	for _, tc := range []struct {
		id, status string
		dunning    int
	}{{"failed-payment", "failed", 3}, {"paid-payment", "paid", 0}} {
		fixture.route("GET", "/v2/payments/"+tc.id, func(recordedProviderRequest) (int, any) {
			payment := map[string]any{"id": tc.id, "status": tc.status, "subscriptionId": "subscription", "customerId": "customer", "amount": map[string]string{"currency": "EUR", "value": "121.00"}, "metadata": meta, "createdAt": at.Format(time.RFC3339)}
			if tc.status == "paid" {
				payment["paidAt"] = at.Format(time.RFC3339)
			}
			return 200, payment
		})
		for repeat := 0; repeat < 2; repeat++ {
			if _, err = svc.handleMolliePaymentWebhook(t.Context(), tc.id, []byte("id="+tc.id)); err != nil {
				t.Fatal(err)
			}
		}
		payment, readErr := client.GetPayment(t.Context(), tc.id)
		if readErr != nil {
			t.Fatal(readErr)
		}
		mapped, _ := mapMolliePaymentStatus(tc.status)
		if err = svc.applyNativeMollieSubscriptionOutcome(t.Context(), tenant, payment, mapped); err != nil {
			t.Fatal(err)
		}
		var dunning int
		var nextDate string
		if err = db.QueryRow(`SELECT dunning_attempts,COALESCE(mollie_next_payment_date::text,'') FROM purser.tenant_subscriptions WHERE tenant_id=$1::uuid`, tenant).Scan(&dunning, &nextDate); err != nil {
			t.Fatal(err)
		}
		if dunning != tc.dunning || nextDate != next {
			t.Fatalf("native tax branch dropped payment lifecycle: %s dunning=%d want=%d next=%s want=%s", tc.status, dunning, tc.dunning, nextDate, next)
		}
	}
	var invoices, failed, paid, events int
	if err = db.QueryRow(`SELECT count(*) FROM purser.billing_invoices WHERE tenant_id=$1::uuid`, tenant).Scan(&invoices); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow(`SELECT count(*) FROM purser.billing_payments WHERE tenant_id=$1::uuid AND status='confirmed'`, tenant).Scan(&paid); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow(`SELECT count(*) FROM purser.provider_payment_objects WHERE tenant_id=$1::uuid AND provider='mollie' AND object_type='payment' AND provider_object_id='failed-payment' AND metadata->>'status'='failed' AND (metadata->>'amount_minor')::bigint=12100`, tenant).Scan(&failed); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow(`SELECT count(*) FROM purser.billing_event_outbox WHERE tenant_id=$1::uuid AND event_type IN ('payment_failed','payment_succeeded','invoice_paid')`, tenant).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if invoices != 1 || failed != 1 || paid != 1 || events != 3 {
		t.Fatalf("native payment outcomes missing or duplicated: invoices=%d failed=%d paid=%d events=%d", invoices, failed, paid, events)
	}
	deadline := time.Now().Add(time.Second)
	for emailAttempts.count.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := emailAttempts.count.Load(); got != 1 {
		t.Fatalf("native failed payment notification attempts=%d, want exactly one across webhook/outcome replays", got)
	}
}

func TestNativeMollieUpgradeCreditsOriginalSourceTax_RealPG(t *testing.T) {
	db := startPurserUsageRealPG(t)
	for key, value := range map[string]string{"SUPPLIER_NAME": "DDVTech B.V.", "SUPPLIER_ADDRESS": "Leiden NL", "SUPPLIER_VAT_NUMBER": "NL853319145B01", "SUPPLIER_REGISTRATION_NUMBER": "59104694", "SUPPLIER_COUNTRY": "NL"} {
		appconfigtest.Set(t, key, value)
	}
	for _, tc := range []struct {
		name                            string
		sources                         int
		target, wantCredit, priorCredit int64
	}{{"original NL tax after move to zero", 1, 20000, 6050, 0}, {"multiple exact source payments", 2, 20000, 12100, 0}, {"credit capped at replacement", 1, 4000, 4000, 0}, {"cumulative original source cap", 1, 20000, 2100, 10000}} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := t.Context()
			tenant := seedPrepaidLedgerTenant(t, db)
			customer := "customer-" + tenant
			if _, err := db.Exec(`UPDATE purser.tenant_subscriptions SET billing_model='postpaid',presentment_currency='EUR',billing_customer_type='business',billing_name='Buyer',billing_email='buyer@example.test',billing_address='{"street":"Main1","city":"Leiden","postal_code":"2332ED","country":"NL"}' WHERE tenant_id=$1::uuid`, tenant); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`INSERT INTO purser.mollie_customers(tenant_id,mollie_customer_id) VALUES($1::uuid,$2)`, tenant, customer); err != nil {
				t.Fatal(err)
			}
			terms, err := AssessMollieTaxTerms(ctx, db, tenant, 10000)
			if err != nil {
				t.Fatal(err)
			}
			at := time.Now().UTC().Truncate(time.Microsecond)
			svc := &Service{db: db, logger: logging.NewLogger()}
			ids := []string{}
			for n := 0; n < tc.sources; n++ {
				ids = append(ids, fmt.Sprintf("native-%s-%d", tenant, n))
			}
			receipt := billing.TierUpgradeReceipt{Applied: true, PeriodStart: at, PeriodEnd: at.AddDate(0, 1, 0), PaidPeriodStart: at.Add(-15 * 24 * time.Hour), PaidPeriodEnd: at.Add(15 * 24 * time.Hour), UnusedPaidCents: int64(tc.sources) * 5000, PaidReceiptID: strings.Join(ids, ","), TaxDocumentPaymentIDs: ids}
			missingTx, err := db.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			missingErr := createUpgradeInvoiceTx(ctx, missingTx, purserdb.GetTierUpgradeRow{ID: uuid.NewString(), TenantID: tenant, Provider: "mollie", BaseCents: tc.target}, receipt)
			if err = missingTx.Rollback(); err != nil {
				t.Fatal(err)
			}
			if missingErr == nil || !strings.Contains(missingErr.Error(), "awaits original tax documents") {
				t.Fatal("tax-marked payment without imported original did not hold", missingErr)
			}
			for n := 0; n < tc.sources; n++ {
				id := ids[n]
				meta := map[string]any{"tenant_id": tenant}
				terms.Metadata(meta)
				payment := &mollieapi.Payment{ID: id, CustomerID: customer, SubscriptionID: "sub-" + tenant, Amount: &mollieapi.Amount{Currency: "EUR", Value: "121.00"}, Metadata: meta, PaidAt: &at, Status: "paid"}
				if err = svc.issueMollieNativeTaxInvoice(ctx, tenant, payment); err != nil {
					t.Fatal(err)
				}
			}
			if tc.priorCredit > 0 {
				_, err = db.Exec(`INSERT INTO purser.credit_notes(credit_note_number,tenant_id,source_document_type,source_document_id,reversal_reference_type,reversal_reference_id,amount_cents,currency,reason) SELECT 'CN-'||lpad(nextval('purser.credit_note_number_seq')::text,10,'0'),tenant_id,'invoice',id,'refund','prior-refund',$3,'EUR','Prior partial refund' FROM purser.billing_invoices WHERE tenant_id=$1::uuid AND source_reference_id=$2`, tenant, ids[0], tc.priorCredit)
				if err != nil {
					t.Fatal(err)
				}
			}
			if _, err = db.Exec(`UPDATE purser.tenant_subscriptions SET billing_address='{"street":"Other1","city":"Toronto","postal_code":"M5V1A1","country":"CA"}' WHERE tenant_id=$1::uuid`, tenant); err != nil {
				t.Fatal(err)
			}
			tx, err := db.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			if err = createUpgradeInvoiceTx(ctx, tx, purserdb.GetTierUpgradeRow{ID: uuid.NewString(), TenantID: tenant, Provider: "mollie", BaseCents: tc.target, OldBaseCents: int64(tc.sources) * 10000}, receipt); err != nil {
				_ = tx.Rollback()
				t.Fatal(err)
			}
			if err = tx.Commit(); err != nil {
				t.Fatal(err)
			}
			var net, vat, gross, credit, due int64
			if err = db.QueryRow(`SELECT net_eur_cents,vat_eur_cents,gross_eur_cents,round(prepaid_credit_applied*100)::bigint,round(amount*100)::bigint FROM purser.billing_invoices WHERE tenant_id=$1::uuid AND base_fee_period_start=$2`, tenant, at).Scan(&net, &vat, &gross, &credit, &due); err != nil {
				t.Fatal(err)
			}
			if net != tc.target || vat != 0 || gross != tc.target || credit != tc.wantCredit || due != tc.target-tc.wantCredit {
				t.Fatalf("original VAT lost or replacement tax reduced: net%d vat%d gross%d credit%d due%d", net, vat, gross, credit, due)
			}
			var credits, creditNet, creditVAT int64
			if err = db.QueryRow(`SELECT COALESCE(sum(amount_cents),0),COALESCE(sum(net_eur_cents),0),COALESCE(sum(vat_eur_cents),0) FROM purser.credit_notes WHERE tenant_id=$1::uuid AND reversal_reference_type='unused_base_fee'`, tenant).Scan(&credits, &creditNet, &creditVAT); err != nil {
				t.Fatal(err)
			}
			if credits != tc.wantCredit || creditNet+creditVAT != credits || creditVAT <= 0 {
				t.Fatalf("original source CN missing: gross%d net%d vat%d", credits, creditNet, creditVAT)
			}
			var originals int
			if err = db.QueryRow(`SELECT count(*) FROM purser.billing_invoices WHERE tenant_id=$1::uuid AND source_reference_type='mollie_native_payment' AND net_eur_cents=10000 AND vat_eur_cents=2100 AND gross_eur_cents=12100 AND base_fee_period_start IS NULL`, tenant).Scan(&originals); err != nil || originals != tc.sources {
				t.Fatal("original invoice rewritten or invented service period", err, originals)
			}
			queries := purserdb.New(db)
			replay := receipt
			replay.PaidReceiptID += "," + ids[0]
			plan, _, planErr := planNativeMollieUpgradeCredit(ctx, queries, tenant, replay)
			if planErr != nil || !plan.found || plan.credit.cents != 0 {
				t.Fatal("consumed original or duplicate payment ID credited again", planErr, plan.credit.cents)
			}
			replay.PaidReceiptID = ids[0] + ",missing-source"
			if _, _, planErr = planNativeMollieUpgradeCredit(ctx, queries, tenant, replay); planErr == nil {
				t.Fatal("partially mapped source payment set silently accepted")
			}
			plan, _, planErr = planNativeMollieUpgradeCredit(ctx, queries, uuid.NewString(), receipt)
			if planErr == nil || plan.found || plan.credit.cents != 0 {
				t.Fatal("another tenant obtained original invoice credit", planErr)
			}
		})
	}
}

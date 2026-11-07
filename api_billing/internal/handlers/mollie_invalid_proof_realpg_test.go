//go:build schema_verify

package handlers

import (
	"crypto/sha256"
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

func TestNativeMollieInvalidAcceptedProofKeepsConfirmedOutcome_RealPG(t *testing.T) {
	db := startPurserUsageRealPG(t)
	fixture, client := installMollieFixture(t)
	for key, value := range map[string]string{"SUPPLIER_NAME": "DDVTech B.V.", "SUPPLIER_ADDRESS": "Leiden NL", "SUPPLIER_VAT_NUMBER": "NL853319145B01", "SUPPLIER_REGISTRATION_NUMBER": "59104694", "SUPPLIER_COUNTRY": "NL"} {
		appconfigtest.Set(t, key, value)
	}
	now := time.Now().UTC().Truncate(time.Second)
	next := now.AddDate(0, 1, 0).Format(time.DateOnly)
	fixture.route("GET", "/v2/customers/customer/subscriptions/subscription", func(recordedProviderRequest) (int, any) {
		return 200, map[string]any{"id": "subscription", "status": "active", "nextPaymentDate": next}
	})
	for _, mode := range []string{"invalid_decision", "incomplete_supplier", "mismatched_rate", "gross_mismatch", "overpayment", "missing_profile", "missing_net", "malformed_net", "negative_net"} {
		t.Run(mode, func(t *testing.T) {
			tenant := seedPrepaidLedgerTenant(t, db)
			customer := "customer-" + tenant
			fixture.route("GET", "/v2/customers/"+customer+"/subscriptions/subscription", func(recordedProviderRequest) (int, any) {
				return 200, map[string]any{"id": "subscription", "status": "active", "nextPaymentDate": next}
			})
			if _, err := db.Exec(`UPDATE purser.tenant_subscriptions SET mollie_subscription_id='subscription',dunning_attempts=2,billing_name='Original Buyer',billing_email='buyer@example.test',billing_customer_type='consumer',billing_address='{"street":"Main1","city":"Leiden","postal_code":"2332ED","country":"NL"}' WHERE tenant_id=$1::uuid`, tenant); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`INSERT INTO purser.mollie_customers(tenant_id,mollie_customer_id) VALUES($1::uuid,$2)`, tenant, customer); err != nil {
				t.Fatal(err)
			}
			terms, err := AssessMollieTaxTerms(t.Context(), db, tenant, 10000)
			if err != nil {
				t.Fatal(err)
			}
			frozen, err := purserdb.New(db).GetProviderTaxProfile(t.Context(), purserdb.GetProviderTaxProfileParams{TenantID: tenant, ProfileSha256: terms.ProfileSHA})
			if err != nil {
				t.Fatal(err)
			}
			var profile billingstripe.TaxProfile
			if err = json.Unmarshal(frozen, &profile); err != nil {
				t.Fatal(err)
			}
			var proof CryptoTaxAssessment
			if err = json.Unmarshal(profile.Evidence, &proof); err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "invalid_decision":
				proof.PolicyVersion = "unrecognized-retained-policy"
			case "incomplete_supplier":
				proof.Supplier.Address = ""
			case "mismatched_rate":
				profile.RateBPS = 0
			}
			profile.Evidence, _ = json.Marshal(proof)
			digest := sha256.Sum256([]byte(tenant + mode))
			sha := hex.EncodeToString(digest[:])
			frozen, _ = json.Marshal(profile)
			if mode != "missing_profile" {
				if err = purserdb.New(db).StoreProviderTaxProfile(t.Context(), purserdb.StoreProviderTaxProfileParams{TenantID: tenant, ProfileSha256: sha, Profile: frozen}); err != nil {
					t.Fatal(err)
				}
			}
			metadata := map[string]any{"tenant_id": tenant}
			terms.Metadata(metadata)
			metadata["frameworks_tax_profile"] = sha
			switch mode {
			case "missing_net":
				delete(metadata, "frameworks_net_minor")
			case "malformed_net":
				metadata["frameworks_net_minor"] = "not-cents"
			case "negative_net":
				metadata["frameworks_net_minor"] = "-100"
			}
			payment := &mollieapi.Payment{ID: "confirmed-" + tenant, CustomerID: customer, SubscriptionID: "subscription", Status: "paid", PaidAt: &now, Amount: &mollieapi.Amount{Currency: "EUR", Value: "121.00"}, Metadata: metadata}
			wantGross, wantDunning := int64(12100), 0
			if mode == "gross_mismatch" {
				payment.Amount.Value = "1.00"
				wantGross, wantDunning = 100, 2
			} else if mode == "overpayment" {
				payment.Amount.Value = "200.00"
				wantGross = 20000
			}
			svc := &Service{db: db, mollieClient: client, logger: logging.NewLogger()}
			for repeat := 0; repeat < 2; repeat++ {
				if err = svc.applyNativeMollieSubscriptionOutcome(t.Context(), tenant, payment, "confirmed"); err != nil {
					t.Fatalf("confirmed cash stranded behind permanent accepted-proof failure: %v", err)
				}
			}
			var invoices, payments, events, reviews, emails, dunning int
			var date, status, supplierAddress string
			var hasTaxSplit, reviewed bool
			var gross int64
			if err = db.QueryRow(`SELECT (SELECT count(*) FROM purser.billing_invoices WHERE tenant_id=$1::uuid),(SELECT count(*) FROM purser.billing_payments WHERE tenant_id=$1::uuid AND status='confirmed'),(SELECT count(*) FROM purser.billing_event_outbox WHERE tenant_id=$1::uuid AND event_type IN ('payment_succeeded','invoice_paid')),(SELECT count(*) FROM purser.crypto_accounting_anomalies WHERE tenant_id=$1::uuid AND kind='provider_tax_document_review' AND status='open'),(SELECT count(*) FROM purser.invoice_email_outbox WHERE tenant_id=$1::uuid),dunning_attempts,mollie_next_payment_date::text FROM purser.tenant_subscriptions WHERE tenant_id=$1::uuid`, tenant).Scan(&invoices, &payments, &events, &reviews, &emails, &dunning, &date); err != nil {
				t.Fatal(err)
			}
			if invoices != 1 || payments != 1 || events != 2 || reviews != 1 || emails != 0 || dunning != wantDunning || date != next {
				t.Fatalf("review lost/duplicated outcome invoices%d payments%d events%d reviews%d emails%d dunning%d next%s", invoices, payments, events, reviews, emails, dunning, date)
			}
			if err = db.QueryRow(`SELECT tax_status,net_eur_cents IS NOT NULL OR vat_eur_cents IS NOT NULL OR gross_eur_cents IS NOT NULL,COALESCE((tax_decision->>'review_required')::boolean,false),presentment_amount_cents,COALESCE(supplier_snapshot->>'address','') FROM purser.billing_invoices WHERE tenant_id=$1::uuid`, tenant).Scan(&status, &hasTaxSplit, &reviewed, &gross, &supplierAddress); err != nil {
				t.Fatal(err)
			}
			if status != "review" || hasTaxSplit || !reviewed || gross != wantGross || (mode == "incomplete_supplier" && supplierAddress != "") {
				t.Fatalf("unknown accepted tax facts invented: status%s split%v reviewed%v cash%d supplierAddress%q", status, hasTaxSplit, reviewed, gross, supplierAddress)
			}
			if mode == "missing_profile" && supplierAddress != "" {
				t.Fatal("missing accepted profile was replaced by current supplier")
			}
			if mode == "missing_net" || mode == "malformed_net" || mode == "negative_net" {
				var inventedNet bool
				if err = db.QueryRow(`SELECT tax_decision->'accepted_net_minor'<>'null'::jsonb FROM purser.billing_invoices WHERE tenant_id=$1::uuid`, tenant).Scan(&inventedNet); err != nil || inventedNet {
					t.Fatalf("unknown accepted net was fabricated: invented%v err%v", inventedNet, err)
				}
			}
			documents, err := purserdb.New(db).ListBillingDocuments(t.Context(), tenant)
			if err != nil {
				t.Fatal(err)
			}
			visible := 0
			for _, document := range documents {
				if document.Kind == "invoice" {
					visible++
					if document.Status != "paid" || document.AmountCents != wantGross || document.NetEurCents.Valid || document.VatEurCents.Valid {
						t.Fatalf("reviewed receipt became collectible or displayed invented tax: %+v", document)
					}
				}
			}
			if visible != 1 {
				t.Fatalf("reviewed payment document disappeared from billing: %d", visible)
			}
			rows, err := purserdb.New(db).FinancialAuditPage(t.Context(), purserdb.FinancialAuditPageParams{TenantID: tenant, Source: "invoices", FromAt: now.Add(-time.Hour), ThroughAt: now.Add(time.Hour)})
			if err != nil || len(rows) != 1 {
				t.Fatalf("reviewed original missing from finance export: rows%d err%v", len(rows), err)
			}
			var exported struct {
				Status string `json:"tax_status"`
				Tax    struct {
					ReviewRequired bool `json:"review_required"`
				} `json:"tax_decision"`
			}
			if err = json.Unmarshal([]byte(rows[0].Payload), &exported); err != nil || exported.Status != "review" || !exported.Tax.ReviewRequired {
				t.Fatalf("finance export lost original tax review: %+v %v", exported, err)
			}
		})
	}
}

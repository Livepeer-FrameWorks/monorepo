//go:build schema_verify

package handlers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"frameworks/api_billing/internal/appconfig/appconfigtest"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	mollieapi "github.com/VictorAvelar/mollie-api-go/v4/mollie"
	"github.com/sirupsen/logrus"
)

type failedNativePaymentEmailHook struct{ count atomic.Int64 }

func (h *failedNativePaymentEmailHook) Levels() []logrus.Level { return logrus.AllLevels }
func (h *failedNativePaymentEmailHook) Fire(e *logrus.Entry) error {
	if strings.Contains(e.Message, "skipping payment failed email") {
		h.count.Add(1)
	}
	return nil
}

func TestNativeMolliePendingOutcomeDoesNotReadSubscription_RealPG(t *testing.T) {
	db := startPurserUsageRealPG(t)
	fixture, client := installMollieFixture(t)
	tenant := seedPrepaidLedgerTenant(t, db)
	if _, err := db.Exec(`INSERT INTO purser.mollie_customers(tenant_id,mollie_customer_id) VALUES($1::uuid,'customer')`, tenant); err != nil {
		t.Fatal(err)
	}
	payment := &mollieapi.Payment{ID: "pending-payment", CustomerID: "customer", SubscriptionID: "subscription"}
	if err := (&Service{db: db, mollieClient: client, logger: logging.NewLogger()}).applyNativeMollieSubscriptionOutcome(t.Context(), tenant, payment, "pending"); err != nil {
		t.Fatalf("pending payment depends on irrelevant provider subscription read: %v", err)
	}
	if count := len(fixture.matching("GET", "/v2/customers/customer/subscriptions/subscription")); count != 0 {
		t.Fatalf("pending outcome made %d subscription requests", count)
	}
}

func TestNativeMollieIssuanceDoesNotAssessInsideLockedTransaction_RealPG(t *testing.T) {
	for _, changed := range []bool{false, true} {
		t.Run(fmt.Sprint("profile_changed_", changed), func(t *testing.T) {
			db := startPurserUsageRealPG(t)
			for key, value := range map[string]string{"SUPPLIER_NAME": "DDVTech B.V.", "SUPPLIER_ADDRESS": "Leiden NL", "SUPPLIER_VAT_NUMBER": "NL853319145B01", "SUPPLIER_REGISTRATION_NUMBER": "59104694", "SUPPLIER_COUNTRY": "NL"} {
				appconfigtest.Set(t, key, value)
			}
			tenant := seedPrepaidLedgerTenant(t, db)
			country, vat := "NL", ""
			if changed {
				country, vat = "DE", "DE123456789"
			}
			if _, err := db.Exec(`UPDATE purser.tenant_subscriptions SET billing_model='postpaid',billing_name='Original Buyer',billing_email='buyer@example.test',billing_customer_type='business',billing_address=jsonb_build_object('street','Main1','city','City','postal_code','1000','country',$2::text),tax_id=NULLIF($3,'') WHERE tenant_id=$1::uuid`, tenant, country, vat); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`INSERT INTO purser.mollie_customers(tenant_id,mollie_customer_id) VALUES($1::uuid,'customer')`, tenant); err != nil {
				t.Fatal(err)
			}
			if changed {
				digest := sha256.Sum256([]byte(vat))
				if _, err := db.Exec(`INSERT INTO purser.vat_validation_evidence(tenant_id,country_code,vat_number_hash,vat_number_masked,valid,request_date,expires_at,raw_response) VALUES($1,'DE',$2,'DE*****6789',true,CURRENT_DATE,NOW()+INTERVAL '1 day','{}')`, tenant, hex.EncodeToString(digest[:])); err != nil {
					t.Fatal(err)
				}
			}
			terms, err := AssessMollieTaxTerms(t.Context(), db, tenant, 10000)
			if err != nil {
				t.Fatal(err)
			}
			updated := make(chan error, 1)
			if changed {
				if _, err = db.Exec(`UPDATE purser.vat_validation_evidence SET expires_at=NOW()-INTERVAL '1 minute' WHERE tenant_id=$1::uuid`, tenant); err != nil {
					t.Fatal(err)
				}
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					ctx, cancel := context.WithTimeout(r.Context(), time.Second)
					defer cancel()
					_, updateErr := db.ExecContext(ctx, `UPDATE purser.tenant_subscriptions SET billing_name='Changed During Assessment' WHERE tenant_id=$1::uuid`, tenant)
					select {
					case updated <- updateErr:
					default:
					}
					_, _ = fmt.Fprintf(w, `<soap:Envelope xmlns:soap="http://schemas.xmlsoap.org/soap/envelope/"><soap:Body><checkVatResponse xmlns="urn:ec.europa.eu:taxud:vies:services:checkVat:types"><countryCode>DE</countryCode><vatNumber>123456789</vatNumber><requestDate>%s</requestDate><valid>true</valid><name>Buyer</name><address>Berlin</address></checkVatResponse></soap:Body></soap:Envelope>`, time.Now().UTC().Format(time.DateOnly))
				}))
				defer server.Close()
				appconfigtest.Set(t, "VIES_ENDPOINT", server.URL)
			} else {
				db.SetMaxOpenConns(1)
			}
			at := time.Now().UTC()
			meta := map[string]any{"tenant_id": tenant}
			terms.Metadata(meta)
			payment := &mollieapi.Payment{ID: "paid-native", CustomerID: "customer", SubscriptionID: "subscription", Amount: &mollieapi.Amount{Currency: "EUR", Value: fmt.Sprintf("%d.%02d", terms.GrossMinor/100, terms.GrossMinor%100)}, Metadata: meta, PaidAt: &at, Status: "paid"}
			ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
			defer cancel()
			err = (&Service{db: db, logger: logging.NewLogger()}).issueMollieNativeTaxInvoice(ctx, tenant, payment)
			if err != nil {
				t.Fatalf("assessment retained locked issuance connection: %v", err)
			}
			if changed {
				select {
				case <-updated:
					t.Fatal("paid issuance called current VIES instead of retained proof")
				default:
				}
			}

			var reviewed bool
			var buyer string
			var gross int64
			if err = db.QueryRow(`SELECT COALESCE((tax_decision->>'review_required')::boolean,false),customer_snapshot->>'name',gross_eur_cents FROM purser.billing_invoices WHERE tenant_id=$1::uuid`, tenant).Scan(&reviewed, &buyer, &gross); err != nil {
				t.Fatal(err)
			}
			if reviewed || gross != terms.GrossMinor || buyer != "Original Buyer" {
				t.Fatalf("locked binding lost paid terms or changed-profile review: review%v gross%d buyer%s", reviewed, gross, buyer)
			}
		})
	}
}

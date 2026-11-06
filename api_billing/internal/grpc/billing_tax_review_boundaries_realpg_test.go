//go:build schema_verify

package grpc

import (
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
	"github.com/google/uuid"
)

func TestPublicBillingBoundariesRetainTaxReview_RealPG(t *testing.T) {
	configureRPCTaxSupplier(t)
	db := startOperatorGrantDatabase(t)
	paid, payg := uuid.NewString(), uuid.NewString()
	if _, err := db.Exec(`INSERT INTO purser.billing_tiers(id,tier_name,display_name,tier_level,base_price,currency,metering_enabled,is_default_prepaid) VALUES($1,'tax-review-paid','Paid',2,100,'EUR',false,false),($2,'payg','PAYG',0,0,'EUR',false,true)`, paid, payg); err != nil {
		t.Fatal(err)
	}
	server := &PurserServer{db: db, logger: logging.NewLogger(), tierReconciler: &assignTierReconciler{}, commodoreClient: &recordingCommodoreCache{}}
	operator := operatorAssignCtx(uuid.NewString())
	for _, grant := range []bool{false, true} {
		name := "assign"
		if grant {
			name = "grant"
		}
		t.Run(name, func(t *testing.T) {
			tenant := uuid.NewString()
			start := time.Now().UTC().Add(-24 * time.Hour).Truncate(time.Microsecond)
			end := start.AddDate(0, 1, 0)
			if _, err := db.Exec(`INSERT INTO purser.tenant_subscriptions(tenant_id,tier_id,billing_model,status,presentment_currency,billing_period_start,billing_period_end,next_billing_date,billing_customer_type,billing_email,billing_address) VALUES($1,$2,'postpaid','active','EUR',$3,$4,$4,'consumer','buyer@example.test','{"street":"Main1","city":"Leiden","postal_code":"2332ED","country":"NL"}')`, tenant, paid, start, end); err != nil {
				t.Fatal(err)
			}
			apply := func() error {
				if grant {
					zero := "0"
					_, err := server.AdminSetBillingGrant(operator, &purserpb.AdminSetBillingGrantRequest{TenantId: tenant, BasePrice: &zero, Collection: "invoice", Reason: "Grant after closing original paid period"})
					return err
				}
				_, err := server.AdminAssignTier(operator, &purserpb.AdminAssignTierRequest{TenantId: tenant, TierName: "payg", BillingModel: "prepaid", Reason: "Switch after closing original paid period"})
				return err
			}
			for attempt := 0; attempt < 2; attempt++ {
				if err := apply(); err == nil {
					t.Fatal("missing billing name crossed public pricing boundary")
				}
			}
			var actualTier, model string
			var actualStart time.Time
			var anomalies, documents, grants int
			if err := db.QueryRow(`SELECT tier_id::text,billing_model,billing_period_start,(SELECT count(*) FROM purser.crypto_accounting_anomalies WHERE tenant_id=$1::uuid AND kind='invoice_tax_review' AND reference_type='billing_phase' AND status='open'),(SELECT count(*) FROM purser.billing_invoices WHERE tenant_id=$1::uuid),(SELECT count(*) FROM purser.subscription_operator_grants g WHERE g.subscription_id=s.id) FROM purser.tenant_subscriptions s WHERE tenant_id=$1::uuid`, tenant).Scan(&actualTier, &model, &actualStart, &anomalies, &documents, &grants); err != nil {
				t.Fatal(err)
			}
			if actualTier != paid || model != "postpaid" || !actualStart.Equal(start) || anomalies != 1 || documents != 0 || grants != 0 {
				t.Fatalf("public tax refusal lost rollback/evidence: tier%s model%s start%s reviews%d invoices%d grants%d", actualTier, model, actualStart, anomalies, documents, grants)
			}
			if _, err := db.Exec(`UPDATE purser.tenant_subscriptions SET billing_name='Corrected Buyer' WHERE tenant_id=$1::uuid`, tenant); err != nil {
				t.Fatal(err)
			}
			if err := apply(); err != nil {
				t.Fatalf("corrected public boundary remains blocked: %v", err)
			}
			if err := db.QueryRow(`SELECT count(*) FROM purser.crypto_accounting_anomalies WHERE tenant_id=$1::uuid AND kind='invoice_tax_review' AND reference_type='billing_phase' AND status='open'`, tenant).Scan(&anomalies); err != nil {
				t.Fatal(err)
			}
			if anomalies != 0 {
				t.Fatalf("corrected public boundary left%d unresolved phase reviews", anomalies)
			}
		})
	}
}

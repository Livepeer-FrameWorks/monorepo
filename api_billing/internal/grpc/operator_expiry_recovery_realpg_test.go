//go:build schema_verify

package grpc

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"frameworks/api_billing/internal/database/purserdb"
	"frameworks/api_billing/internal/subscriptions"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/google/uuid"
)

func TestOperatorGrantExpiryFailuresDoNotStarveLaterTenants_RealPG(t *testing.T) {
	db := startOperatorGrantDatabase(t)
	ctx := context.Background()
	tier, healthy := uuid.NewString(), uuid.NewString()
	if _, writeErr := db.ExecContext(ctx, `INSERT INTO purser.billing_tiers(id,tier_name,display_name,base_price,currency,metering_enabled,is_active) VALUES($1,'retired','Retired',0,'EUR',false,false)`, tier); writeErr != nil {
		t.Fatal(writeErr)
	}
	for i := 0; i < 101; i++ {
		tenant := uuid.NewString()
		if i == 100 {
			tenant = healthy
		}
		if _, writeErr := db.ExecContext(ctx, `INSERT INTO purser.tenant_subscriptions(tenant_id,tier_id,status,billing_model,presentment_currency) VALUES($1,$2,'active','postpaid','EUR')`, tenant, tier); writeErr != nil {
			t.Fatal(writeErr)
		}
		if _, writeErr := db.ExecContext(ctx, `INSERT INTO purser.subscription_operator_grants(subscription_id,waive_usage,collection,reason,granted_at,expires_at) SELECT id,true,'provider','expiry recovery',NOW()-INTERVAL '2 hours',NOW()-INTERVAL '1 hour'+$2*INTERVAL '1 second' FROM purser.tenant_subscriptions WHERE tenant_id=$1`, tenant, i); writeErr != nil {
			t.Fatal(writeErr)
		}
	}
	if _, writeErr := db.ExecContext(ctx, `CREATE FUNCTION purser.fail_selected_expiries() RETURNS TRIGGER LANGUAGE plpgsql AS $$ BEGIN IF (SELECT tenant_id FROM purser.tenant_subscriptions WHERE id=OLD.subscription_id) <> '`+healthy+`'::uuid THEN RAISE EXCEPTION 'injected expiry failure'; END IF; RETURN OLD; END; $$; CREATE TRIGGER fail_selected_expiries BEFORE DELETE ON purser.subscription_operator_grants FOR EACH ROW EXECUTE FUNCTION purser.fail_selected_expiries()`); writeErr != nil {
		t.Fatal(writeErr)
	}
	server := &PurserServer{db: db, logger: logging.NewLogger(), tierReconciler: &assignTierReconciler{}, commodoreClient: &recordingCommodoreCache{}}
	server.expireOperatorGrantsOnce(ctx)
	server.expireOperatorGrantsOnce(ctx)
	var remaining, failed, history int
	if readErr := db.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM purser.subscription_operator_grants g JOIN purser.tenant_subscriptions s ON s.id=g.subscription_id WHERE s.tenant_id=$1),(SELECT COUNT(*) FROM purser.subscription_operator_grants WHERE expiry_error IS NOT NULL),(SELECT COUNT(*) FROM purser.subscription_operator_grant_history)`, healthy).Scan(&remaining, &failed, &history); readErr != nil {
		t.Fatal(readErr)
	}
	if remaining != 0 || failed != 100 || history != 1 {
		t.Fatalf("healthy remaining=%d failed=%d history=%d, want 0/100/1", remaining, failed, history)
	}
	if count, err := purserdb.New(db).CountRepeatedOperatorGrantExpiryFailures(ctx); err != nil || count != 0 {
		t.Fatalf("one failed attempt counted as repeated: %d,%v", count, err)
	}
	var duplicateTenant string
	var duplicateGrant time.Time
	if err := db.QueryRow(`SELECT s.tenant_id::text,g.granted_at FROM purser.subscription_operator_grants g JOIN purser.tenant_subscriptions s ON s.id=g.subscription_id WHERE g.expiry_error IS NOT NULL LIMIT 1`).Scan(&duplicateTenant, &duplicateGrant); err != nil {
		t.Fatal(err)
	}
	if err := purserdb.New(db).RecordOperatorGrantExpiryFailure(ctx, purserdb.RecordOperatorGrantExpiryFailureParams{TenantID: duplicateTenant, GrantedAt: duplicateGrant, Failure: "same attempt reported by another replica"}); err != nil {
		t.Fatal(err)
	}
	if count, err := purserdb.New(db).CountRepeatedOperatorGrantExpiryFailures(ctx); err != nil || count != 0 {
		t.Fatalf("duplicate failure report bypassed the retry window: %d,%v", count, err)
	}

	if _, err := db.ExecContext(ctx, `UPDATE purser.subscription_operator_grants SET expiry_attempted_at=NOW()-INTERVAL '6 minutes'`); err != nil {
		t.Fatal(err)
	}
	server.expireOperatorGrantsOnce(ctx)
	if count, err := purserdb.New(db).CountRepeatedOperatorGrantExpiryFailures(ctx); err != nil || count != 100 {
		t.Fatalf("second failed attempt not counted: %d,%v", count, err)
	}
	if _, clearErr := db.ExecContext(ctx, `DROP TRIGGER fail_selected_expiries ON purser.subscription_operator_grants`); clearErr != nil {
		t.Fatal(clearErr)
	}
	if _, writeErr := db.ExecContext(ctx, `UPDATE purser.tenant_subscriptions SET status='cancelled' WHERE tenant_id<>$1`, healthy); writeErr != nil {
		t.Fatal(writeErr)
	}
	if _, resetErr := db.ExecContext(ctx, `UPDATE purser.subscription_operator_grants SET expiry_attempted_at=NOW()-INTERVAL '6 minutes'`); resetErr != nil {
		t.Fatal(resetErr)
	}
	server.expireOperatorGrantsOnce(ctx)
	count, err := purserdb.New(db).CountFailedOperatorGrantExpiries(ctx)
	if err != nil || count != 0 {
		t.Fatalf("cancelled expiry failures=%d err=%v, want zero", count, err)
	}
}

func TestOperatorGrantExpiryFailuresDoNotStarveLaterTenants_RealYugabyte(t *testing.T) {
	TestOperatorGrantExpiryFailuresDoNotStarveLaterTenants_RealPG(t)
}

func testCancelledGrantCanEnd(t *testing.T, db *sql.DB) {
	t.Helper()
	tier := uuid.NewString()
	if _, seedErr := db.Exec(`INSERT INTO purser.billing_tiers(id,tier_name,display_name,base_price) VALUES($1::text::uuid,$1::text,'Cancelled fixture',40)`, tier); seedErr != nil {
		t.Fatal(seedErr)
	}
	for _, expired := range []bool{false, true} {
		tenant := uuid.NewString()
		if _, seedErr := db.Exec(`INSERT INTO purser.tenant_subscriptions(tenant_id,tier_id,status,billing_model) VALUES($1,$2,'cancelled','postpaid')`, tenant, tier); seedErr != nil {
			t.Fatal(seedErr)
		}
		if _, seedErr := db.Exec(`INSERT INTO purser.subscription_operator_grants(subscription_id,waive_usage,reason,granted_at,expires_at) SELECT id,true,'cancelled recovery',NOW()-INTERVAL '2 hours',NOW()-INTERVAL '1 hour' FROM purser.tenant_subscriptions WHERE tenant_id=$1`, tenant); seedErr != nil {
			t.Fatal(seedErr)
		}
		server := &PurserServer{db: db, logger: logging.NewLogger(), commodoreClient: &recordingCommodoreCache{}}
		var deadline time.Time
		if err := db.QueryRow(`SELECT grant_row.expires_at FROM purser.subscription_operator_grants grant_row JOIN purser.tenant_subscriptions subscription ON subscription.id=grant_row.subscription_id WHERE subscription.tenant_id=$1`, tenant).Scan(&deadline); err != nil {
			t.Fatal(err)
		}
		if endErr := server.endOperatorGrant(t.Context(), tenant, "end cancelled grant", expired); endErr != nil {
			t.Fatalf("cancelled grant cannot end: %v", endErr)
		}
		var grants, documents int
		var ended time.Time
		var gotTier, status string
		if readErr := db.QueryRow(`SELECT (SELECT count(*) FROM purser.subscription_operator_grants WHERE subscription_id=s.id),(SELECT count(*) FROM purser.billing_invoices WHERE tenant_id=s.tenant_id),s.tier_id::text,s.status,h.ended_at FROM purser.tenant_subscriptions s JOIN purser.subscription_operator_grant_history h ON h.subscription_id=s.id WHERE s.tenant_id=$1`, tenant).Scan(&grants, &documents, &gotTier, &status, &ended); readErr != nil {
			t.Fatal(readErr)
		}
		if grants != 0 || documents != 0 || gotTier != tier || status != "cancelled" || !ended.Equal(deadline) {
			t.Fatalf("cancelled cleanup grants=%d invoices=%d tier=%s status=%s ended=%s", grants, documents, gotTier, status, ended)
		}
		checkout, checkoutErr := subscriptions.TransitionProvider(t.Context(), db, subscriptions.ProviderChange{TenantID: tenant, Operation: subscriptions.ProviderCheckout})
		if checkoutErr != nil || checkout.Rows != 1 {
			t.Fatalf("cancelled grant cleanup did not unblock checkout: %v,%v", checkout, checkoutErr)
		}
	}
}

func testGrantRefusesPendingProviderCheckout(t *testing.T, db *sql.DB) {
	t.Helper()
	for _, purpose := range []string{"tenant_subscription_checkout", "mollie_first_payment", "mollie_subscription_create"} {
		t.Run("grant blocked by "+purpose, func(t *testing.T) {
			tenant, tier := uuid.NewString(), uuid.NewString()
			if _, err := db.Exec(`INSERT INTO purser.billing_tiers(id,tier_name,display_name,base_price,tier_level,metering_enabled) VALUES($1::text::uuid,$1::text,'Checkout tier',40,2,false)`, tier); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`INSERT INTO purser.tenant_subscriptions(tenant_id,tier_id,status,billing_model,presentment_currency) VALUES($1,$2,'active','postpaid','EUR')`, tenant, tier); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`INSERT INTO purser.payment_provider_intents(tenant_id,provider,purpose,local_reference_type,local_reference_id,status,currency,idempotency_key,expires_at) VALUES($1::text::uuid,$2,$3,'billing_tiers',$4,'provider_open','EUR',$1::text,NOW()+INTERVAL '24 hours')`, tenant, map[bool]string{true: "stripe", false: "mollie"}[purpose == "tenant_subscription_checkout"], purpose, tier); err != nil {
				t.Fatal(err)
			}
			server := &PurserServer{db: db, logger: logging.NewLogger(), commodoreClient: &recordingCommodoreCache{}, tierReconciler: &assignTierReconciler{}}
			zero := "0"
			if _, err := server.AdminSetBillingGrant(operatorAssignCtx(uuid.NewString()), &purserpb.AdminSetBillingGrantRequest{TenantId: tenant, BasePrice: &zero, WaiveUsage: true, Reason: "pending checkout"}); status.Code(err) != codes.FailedPrecondition {
				t.Fatalf("grant accepted while checkout can complete: %v", err)
			}
			var count int
			if err := db.QueryRow(`SELECT count(*) FROM purser.subscription_operator_grants WHERE subscription_id=(SELECT id FROM purser.tenant_subscriptions WHERE tenant_id=$1)`, tenant).Scan(&count); err != nil {
				t.Fatal(err)
			}
			if count != 0 {
				t.Fatal("refused checkout grant was persisted")
			}
			if _, err := db.Exec(`UPDATE purser.payment_provider_intents SET status='expired' WHERE tenant_id=$1`, tenant); err != nil {
				t.Fatal(err)
			}
			if _, err := server.AdminSetBillingGrant(operatorAssignCtx(uuid.NewString()), &purserpb.AdminSetBillingGrantRequest{TenantId: tenant, BasePrice: &zero, WaiveUsage: true, Reason: "checkout has settled"}); err != nil {
				t.Fatal(err)
			}
			reserved := false
			if _, err := subscriptions.ReserveCheckout(t.Context(), db, tenant, func(*sql.Tx) (string, error) { reserved = true; return "unexpected", nil }); !errors.Is(err, subscriptions.ErrOutstandingGrant) || reserved {
				t.Fatalf("checkout opened under a grant: reserved=%v err=%v", reserved, err)
			}
		})
	}
}

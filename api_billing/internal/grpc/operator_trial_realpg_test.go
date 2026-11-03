//go:build schema_verify

package grpc

import (
	"context"
	"database/sql"
	"errors"

	"strings"
	"testing"
	"time"

	billingpkg "frameworks/api_billing/internal/billing"
	"frameworks/api_billing/internal/database/purserdb"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/database"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/testutil/dockerpg"
	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestOperatorTrialLifecycle_RealPG(t *testing.T) {
	t.Run("actionable billing boundary", testGrantBoundaryDataCondition)
	t.Run("expired grant without an assignment", TestLegacyExpiredGrantKeepsAdmission_RealPG)
	t.Run("metering hold needs an explicit waiver", TestMeteringHoldNeedsAnExplicitWaiver_RealPG)
	t.Run("requested upgrade cannot waive metering", TestRecoveryOfARequestedUpgradeCannotWaiveMetering_RealPG)
	t.Run("expiry notices", testOperatorGrantNotices)
	db := startOperatorGrantDatabase(t)
	ctx := context.Background()
	tenantID, paygID, enterpriseID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	for _, tier := range []struct {
		id, name string
		level    int
		prepaid  bool
	}{
		{paygID, "payg", 0, true}, {enterpriseID, "enterprise", 4, false},
	} {
		if _, writeErr := db.ExecContext(ctx, `INSERT INTO purser.billing_tiers
   (id,tier_name,display_name,tier_level,base_price,currency,metering_enabled,is_active,is_default_prepaid)
   VALUES ($1,$2,$2,$3,0,'EUR',false,true,$4)`, tier.id, tier.name, tier.level, tier.prepaid); writeErr != nil {
			t.Fatal(writeErr)
		}
	}
	if _, writeErr := db.ExecContext(ctx, `INSERT INTO purser.tenant_subscriptions
  (tenant_id,tier_id,billing_model,status,billing_period_start,billing_period_end,presentment_currency)
  VALUES ($1,$2,'prepaid','active',NOW()-INTERVAL '1 hour',NOW()+INTERVAL '30 days','EUR')`, tenantID, paygID); writeErr != nil {
		t.Fatal(writeErr)
	}
	t.Run("new postpaid subscription periods", func(t *testing.T) { assertDefaultSubscriptionPeriods(t, db, enterpriseID, "postpaid") })
	server := &PurserServer{db: db, logger: logging.NewLogger(), tierReconciler: &assignTierReconciler{}, commodoreClient: &recordingCommodoreCache{}}
	op := operatorAssignCtx(uuid.NewString())
	zero := "0"
	response, err := server.AdminSetBillingGrant(op, &purserpb.AdminSetBillingGrantRequest{
		TenantId: tenantID, TierName: "enterprise", BasePrice: &zero, WaiveUsage: true, Reason: "trial",
		ExpiresAt: timestamppb.New(time.Now().Add(time.Hour)),
	})
	if err != nil {
		t.Fatal(err)
	}
	if response.GetGrant().GetFallbackTierName() != "payg" {
		t.Fatalf("trial fallback = %q, want payg", response.GetGrant().GetFallbackTierName())
	}
	unchanged, err := server.AdminAssignTier(op, &purserpb.AdminAssignTierRequest{TenantId: tenantID, TierName: "enterprise", Reason: "repeat existing assignment"})
	if err != nil {
		t.Fatal(err)
	}
	if unchanged.GetChanged() {
		t.Fatal("identical set-tier changed a granted subscription")
	}
	var priorHistory int
	if err = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM purser.subscription_operator_grant_history WHERE tenant_id=$1`, tenantID).Scan(&priorHistory); err != nil {
		t.Fatal(err)
	}
	if priorHistory != 0 {
		t.Fatalf("identical set-tier archived %d grants", priorHistory)
	}
	var noOpInvoices int
	if err = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM purser.billing_invoices WHERE tenant_id=$1 AND document_kind='invoice'`, tenantID).Scan(&noOpInvoices); err != nil {
		t.Fatal(err)
	}
	if noOpInvoices != 0 {
		t.Fatalf("identical set-tier finalized %d invoices", noOpInvoices)
	}

	renewed, err := server.AdminSetBillingGrant(op, &purserpb.AdminSetBillingGrantRequest{
		TenantId: tenantID, BasePrice: &zero, WaiveUsage: true, Reason: "extended trial",
		ExpiresAt: timestamppb.New(time.Now().Add(2 * time.Hour)),
	})
	if err != nil {
		t.Fatal(err)
	}
	if renewed.GetGrant().GetFallbackTierName() != "payg" {
		t.Fatalf("renewal lost fallback: %+v", renewed)
	}
	// Extending or retrying an arrangement must not collect another partial invoice.
	assertNoRenewalInvoice := func() {
		t.Helper()
		if readErr := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM purser.billing_invoices WHERE tenant_id=$1 AND document_kind='invoice'`, tenantID).Scan(&noOpInvoices); readErr != nil {
			t.Fatal(readErr)
		}
		if noOpInvoices != 0 {
			t.Fatalf("grant renewal/retry finalized %d invoices, want 0", noOpInvoices)
		}
	}
	assertNoRenewalInvoice()
	if _, err = server.AdminSetBillingGrant(op, &purserpb.AdminSetBillingGrantRequest{
		TenantId: tenantID, BasePrice: &zero, WaiveUsage: true, Reason: "extended trial",
		ExpiresAt: renewed.GetGrant().GetExpiresAt(),
	}); err != nil {
		t.Fatal(err)
	}
	assertNoRenewalInvoice()
	listed, err := server.AdminListBillingGrants(op, &purserpb.AdminListBillingGrantsRequest{ExpiringWithinSeconds: 7 * 24 * 60 * 60})
	if err != nil || len(listed.GetGrants()) != 1 || listed.GetGrants()[0].GetTenantId() != tenantID ||
		listed.GetGrants()[0].GetGrant().GetFallbackTierName() != "payg" || !listed.GetGrants()[0].GetGrant().GetActive() {
		t.Fatalf("current expiring grants=%+v, err=%v", listed, err)
	}
	if _, err = server.AdminRevokeBillingGrant(op, &purserpb.AdminRevokeBillingGrantRequest{TenantId: tenantID, Reason: "trial finished"}); err != nil {
		t.Fatal(err)
	}
	tier, err := server.GetTenantAdmissionStatus(ctx, &purserpb.GetTenantAdmissionStatusRequest{TenantId: tenantID})
	if err != nil {
		t.Fatal(err)
	}
	if tier.GetBillingModel() != "prepaid" || tier.GetTierName() != "payg" || !tier.GetIsBalanceNegative() {
		t.Fatalf("revoked trial admission = %+v", tier)
	}
	var history int
	if err = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM purser.subscription_operator_grant_history WHERE tenant_id=$1`, tenantID).Scan(&history); err != nil {
		t.Fatal(err)
	}
	if history != 2 {
		t.Fatalf("grant history rows=%d, want 2", history)
	}
	for _, tc := range []struct {
		name    string
		request *purserpb.AdminListBillingGrantsRequest
		count   int
		more    bool
	}{
		{"active excludes ended", &purserpb.AdminListBillingGrantsRequest{}, 0, false},
		{"history first page", &purserpb.AdminListBillingGrantsRequest{IncludeExpired: true, Limit: 1}, 1, true},
		{"history second page", &purserpb.AdminListBillingGrantsRequest{IncludeExpired: true, Limit: 1, Offset: 1}, 1, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			page, listErr := server.AdminListBillingGrants(op, tc.request)
			if listErr != nil || len(page.GetGrants()) != tc.count || page.GetHasMore() != tc.more {
				t.Fatalf("grant page=%+v, err=%v", page, listErr)
			}
			for _, entry := range page.GetGrants() {
				if entry.GetGrant().GetEndedAt() == nil || entry.GetGrant().GetActive() || entry.GetTierName() != "enterprise" {
					t.Fatalf("ended grant=%+v", entry)
				}
			}
		})
	}
}

func TestOperatorGrantTierAtomic_RealPG(t *testing.T) {
	db := startOperatorGrantDatabase(t)
	testRetiredFallbackRecovery(t, db)
	testCancelledGrantCanEnd(t, db)
	testGrantRefusesPendingProviderCheckout(t, db)
	ctx := context.Background()
	tenantID, paygID, enterpriseID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	for _, tier := range []struct {
		id, name string
		level    int
		prepaid  bool
	}{{paygID, "payg", 0, true}, {enterpriseID, "enterprise", 4, false}} {
		if _, writeErr := db.ExecContext(ctx, `INSERT INTO purser.billing_tiers(id,tier_name,display_name,tier_level,base_price,currency,metering_enabled,is_default_prepaid) VALUES($1,$2,$2,$3,0,'EUR',false,$4)`, tier.id, tier.name, tier.level, tier.prepaid); writeErr != nil {
			t.Fatal(writeErr)
		}
	}
	testFallbackTierLockExcludesSharedUpgrade(t, db, "payg", paygID)
	if _, writeErr := db.ExecContext(ctx, `INSERT INTO purser.tenant_subscriptions(tenant_id,tier_id,billing_model,status,presentment_currency) VALUES($1,$2,'prepaid','active','EUR')`, tenantID, paygID); writeErr != nil {
		t.Fatal(writeErr)
	}
	if _, writeErr := db.ExecContext(ctx, `CREATE FUNCTION purser.reject_test_grant() RETURNS TRIGGER LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected grant write failure'; END; $$; CREATE TRIGGER reject_test_grant BEFORE INSERT ON purser.subscription_operator_grants FOR EACH ROW EXECUTE FUNCTION purser.reject_test_grant();`); writeErr != nil {
		t.Fatal(writeErr)
	}
	server := &PurserServer{db: db, logger: logging.NewLogger(), tierReconciler: &assignTierReconciler{}, commodoreClient: &recordingCommodoreCache{}}
	zero := "0"
	if _, adminSetBillingGrantErr := server.AdminSetBillingGrant(operatorAssignCtx(uuid.NewString()), &purserpb.AdminSetBillingGrantRequest{TenantId: tenantID, TierName: "enterprise", BasePrice: &zero, WaiveUsage: true, Reason: "atomic trial"}); adminSetBillingGrantErr == nil {
		t.Fatal("injected grant upsert failure was accepted")
	}
	var tierID string
	if readErr := db.QueryRowContext(ctx, `SELECT tier_id::text FROM purser.tenant_subscriptions WHERE tenant_id=$1`, tenantID).Scan(&tierID); readErr != nil {
		t.Fatal(readErr)
	}
	if tierID != paygID {
		t.Fatalf("failed grant changed tier to %s, want original %s", tierID, paygID)
	}
}

func TestOperatorGrantExpiryReplicas_RealPG(t *testing.T) {
	db := startOperatorGrantDatabase(t)
	ctx := context.Background()
	tenantID, paygID, enterpriseID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	if _, writeErr := db.ExecContext(ctx, `INSERT INTO purser.billing_tiers(id,tier_name,display_name,tier_level,base_price,currency,metering_enabled,is_default_prepaid) VALUES($1,'payg','PAYG',0,36,'EUR',true,true),($2,'enterprise','Enterprise',4,250,'EUR',true,false)`, paygID, enterpriseID); writeErr != nil {
		t.Fatal(writeErr)
	}
	if _, writeErr := db.ExecContext(ctx, `INSERT INTO purser.tenant_subscriptions(tenant_id,tier_id,billing_model,status,billing_period_start,billing_period_end,presentment_currency) VALUES($1,$2,'postpaid','active','2026-07-01T00:00:00Z','2026-07-31T00:00:00Z','EUR')`, tenantID, enterpriseID); writeErr != nil {
		t.Fatal(writeErr)
	}
	if _, writeErr := db.ExecContext(ctx, `INSERT INTO purser.subscription_operator_grants(subscription_id,base_price,waive_usage,collection,reason,granted_at,expires_at,assigned_tier_id,fallback_tier_id) SELECT id,12,true,'provider','late expiry',billing_period_start,'2026-07-16T00:00:00Z',tier_id,$2 FROM purser.tenant_subscriptions WHERE tenant_id=$1`, tenantID, paygID); writeErr != nil {
		t.Fatal(writeErr)
	}
	cache := &retryingGrantCache{}
	expire := NewBootstrapGrantExpiry(db, logging.NewLogger(), nil, nil, cache)
	results := make(chan error, 2)
	for range 2 {
		go func() { results <- expire(ctx, tenantID) }()
	}
	for range 2 {
		if actionErr := <-results; actionErr != nil {
			t.Fatal(actionErr)
		}
	}
	if cache.calls == 0 {
		t.Fatal("bootstrap expiry did not invalidate tenant cache")
	}
	var tierID string
	var grants, invoices int
	if readErr := db.QueryRowContext(ctx, `SELECT tier_id::text FROM purser.tenant_subscriptions WHERE tenant_id=$1`, tenantID).Scan(&tierID); readErr != nil {
		t.Fatal(readErr)
	}
	if readErr := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM purser.subscription_operator_grant_history WHERE tenant_id=$1`, tenantID).Scan(&grants); readErr != nil {
		t.Fatal(readErr)
	}
	if readErr := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM purser.billing_invoices WHERE tenant_id=$1 AND document_kind='invoice' AND amount=6`, tenantID).Scan(&invoices); readErr != nil {
		t.Fatal(readErr)
	}
	if tierID != paygID || grants != 1 || invoices != 1 {
		t.Fatalf("expiry tier=%s history=%d grant-priced invoices=%d, want PAYG/1/1", tierID, grants, invoices)
	}
}

func TestOperatorGrantMidPeriodPricing_RealPG(t *testing.T) {
	db := startOperatorGrantDatabase(t)
	ctx := context.Background()
	tenantID, tierID := uuid.NewString(), uuid.NewString()
	if _, writeErr := db.ExecContext(ctx, `INSERT INTO purser.billing_tiers(id,tier_name,display_name,tier_level,base_price,currency,metering_enabled) VALUES($1,'paid-contract','Paid contract',4,30,'EUR',false)`, tierID); writeErr != nil {
		t.Fatal(writeErr)
	}
	if _, writeErr := db.ExecContext(ctx, `INSERT INTO purser.tenant_subscriptions(tenant_id,tier_id,billing_model,status,billing_period_start,billing_period_end,presentment_currency) VALUES($1,$2,'postpaid','active',NOW()-INTERVAL '15 days',NOW()+INTERVAL '15 days','EUR')`, tenantID, tierID); writeErr != nil {
		t.Fatal(writeErr)
	}
	server := &PurserServer{db: db, logger: logging.NewLogger(), tierReconciler: &assignTierReconciler{}, commodoreClient: &recordingCommodoreCache{}}
	zero := "0"
	op := operatorAssignCtx(uuid.NewString())
	response, err := server.AdminSetBillingGrant(op, &purserpb.AdminSetBillingGrantRequest{TenantId: tenantID, BasePrice: &zero, WaiveUsage: true, Reason: "mid-period waiver"})
	if err != nil {
		t.Fatal(err)
	}
	var beforeAmount string
	if err = db.QueryRowContext(ctx, `SELECT amount::text FROM purser.billing_invoices WHERE tenant_id=$1 AND document_kind='invoice'`, tenantID).Scan(&beforeAmount); err != nil {
		t.Fatal(err)
	}
	if beforeAmount != "15.00" {
		t.Fatalf("pre-grant charges=%s, want EUR 15.00", beforeAmount)
	}
	if _, err = server.AdminRevokeBillingGrant(op, &purserpb.AdminRevokeBillingGrantRequest{TenantId: tenantID, Reason: "waiver ended"}); err != nil {
		t.Fatal(err)
	}
	historical, err := billingpkg.LoadSubscriptionEffectiveTierAt(ctx, db, tenantID, response.GetGrant().GetGrantedAt().AsTime().Add(time.Microsecond))
	if err != nil {
		t.Fatal(err)
	}
	if !historical.BasePrice.IsZero() || !historical.UsageWaived {
		t.Fatalf("deleted grant lost its historical pricing: %+v", historical)
	}
	var freePhases int
	if err = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM purser.billing_invoices WHERE tenant_id=$1 AND document_kind='invoice' AND amount=0`, tenantID).Scan(&freePhases); err != nil {
		t.Fatal(err)
	}
	if freePhases != 1 {
		t.Fatalf("waived phases=%d, want 1", freePhases)
	}
}

func startOperatorGrantDatabase(t *testing.T) *sql.DB {
	t.Helper()
	if strings.Contains(t.Name(), "_RealYugabyte") {
		db, ok := dockerpg.OpenSharedYugabyteBaseline(t, "operator_grants", "purser")
		if !ok {
			t.Skip("requires the shared Yugabyte fixture")
		}
		return db
	}
	return startPurserTransitionRealPG(t)
}

func TestOperatorTrialLifecycle_RealYugabyte(t *testing.T)  { TestOperatorTrialLifecycle_RealPG(t) }
func TestOperatorGrantTierAtomic_RealYugabyte(t *testing.T) { TestOperatorGrantTierAtomic_RealPG(t) }
func TestOperatorGrantExpiryReplicas_RealYugabyte(t *testing.T) {
	TestOperatorGrantExpiryReplicas_RealPG(t)
}

func TestOperatorGrantMidPeriodPricing_RealYugabyte(t *testing.T) {
	TestOperatorGrantMidPeriodPricing_RealPG(t)
}

func TestOperatorGrantProtectsTierChanges_RealPG(t *testing.T) {
	db := startOperatorGrantDatabase(t)
	ctx := context.Background()
	pro, enterprise, payg := uuid.NewString(), uuid.NewString(), uuid.NewString()
	if _, err := db.Exec(`INSERT INTO purser.billing_tiers(id,tier_name,display_name,tier_level,base_price,metering_enabled,is_default_prepaid) VALUES($1,'protected-pro','Pro',1,100,false,false),($2,'protected-enterprise','Enterprise',4,250,false,false),($3,'protected-payg','PAYG',0,0,false,true)`, pro, enterprise, payg); err != nil {
		t.Fatal(err)
	}
	for _, change := range []struct{ name, current, target string }{{"upgrade", pro, enterprise}, {"downgrade", enterprise, pro}} {
		t.Run(change.name, func(t *testing.T) {
			tenant := uuid.NewString()
			if _, err := db.Exec(`INSERT INTO purser.tenant_subscriptions(tenant_id,tier_id,status,billing_model,presentment_currency,billing_name,billing_email,billing_address,billing_period_start,billing_period_end) VALUES($1,$2,'active','postpaid','EUR','Trial Customer','billing@example.test','{"street":"Street 1","city":"Amsterdam","postal_code":"1000AA","country":"NL"}',NOW()-INTERVAL '1 hour',NOW()+INTERVAL '1 month')`, tenant, change.current); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`INSERT INTO purser.subscription_operator_grants(subscription_id,base_price,waive_usage,collection,reason,assigned_tier_id,fallback_tier_id,granted_at,expires_at) SELECT id,0,true,'provider','trial',tier_id,$2,NOW()-INTERVAL '2 hours',NOW()+INTERVAL '1 hour' FROM purser.tenant_subscriptions WHERE tenant_id=$1`, tenant, payg); err != nil {
				t.Fatal(err)
			}
			server := &PurserServer{db: db, logger: logging.NewLogger(), tierReconciler: &assignTierReconciler{}, commodoreClient: &recordingCommodoreCache{}}
			if _, err := server.ChangeBillingTier(ctx, &purserpb.ChangeBillingTierRequest{TenantId: tenant, TierId: change.target}); status.Code(err) != codes.FailedPrecondition {
				t.Fatalf("self-service changed a granted tier: %v", err)
			}
			current, err := purserdb.New(db).GetSubscriptionTierForGrant(ctx, tenant)
			if err != nil || current.TierID != change.current {
				t.Fatalf("refusal changed tier: %+v,%v", current, err)
			}
			if _, err = db.Exec(`UPDATE purser.subscription_operator_grants SET expires_at=NOW()-INTERVAL '1 minute' WHERE subscription_id=(SELECT id FROM purser.tenant_subscriptions WHERE tenant_id=$1)`, tenant); err != nil {
				t.Fatal(err)
			}
			if err = server.endOperatorGrant(ctx, tenant, "trial deadline", true); err != nil {
				t.Fatal(err)
			}
			current, err = purserdb.New(db).GetSubscriptionTierForGrant(ctx, tenant)
			if err != nil || current.TierID != payg || current.BillingModel != "prepaid" {
				t.Fatalf("refused change lost expiry fallback: %+v,%v", current, err)
			}
		})
	}
}

func TestOperatorGrantProtectsTierChanges_RealYugabyte(t *testing.T) {
	TestOperatorGrantProtectsTierChanges_RealPG(t)
}

// testRetiredFallbackRecovery exercises recovery of pre-existing invalid fallbacks,
// and proves new grants serialize with attempts to retire their fallback tier.
func testRetiredFallbackRecovery(t *testing.T, db *sql.DB) {
	t.Helper()
	ctx := context.Background()
	active, retired, target := uuid.NewString(), uuid.NewString(), uuid.NewString()
	for _, tier := range []struct {
		id     string
		active bool
	}{{active, true}, {retired, false}, {target, true}} {
		if _, writeErr := db.ExecContext(ctx, `INSERT INTO purser.billing_tiers(id,tier_name,display_name,tier_level,base_price,currency,metering_enabled,is_active) VALUES($1::text::uuid,$1::text,'Recovery',4,24,'EUR',false,$2)`, tier.id, tier.active); writeErr != nil {
			t.Fatal(writeErr)
		}
	}
	cache := &retryingGrantCache{err: errors.New("Commodore unavailable")}
	server := &PurserServer{db: db, logger: logging.NewLogger(), commodoreClient: cache}
	for _, recovery := range []string{"set-tier", "revoke"} {
		tenant := uuid.NewString()
		if _, writeErr := db.ExecContext(ctx, `INSERT INTO purser.tenant_subscriptions(tenant_id,tier_id,billing_model,status,billing_period_start,billing_period_end,presentment_currency) VALUES($1,$2,'postpaid','active',NOW()-INTERVAL '2 days',NOW()+INTERVAL '28 days','EUR')`, tenant, active); writeErr != nil {
			t.Fatal(writeErr)
		}
		if _, writeErr := db.ExecContext(ctx, `INSERT INTO purser.subscription_operator_grants(subscription_id,base_price,waive_usage,collection,reason,granted_at,expires_at,assigned_tier_id,fallback_tier_id) SELECT id,0,true,'provider','stuck',billing_period_start,NOW()-INTERVAL '1 hour',tier_id,$2 FROM purser.tenant_subscriptions WHERE tenant_id=$1`, tenant, retired); writeErr != nil {
			t.Fatal(writeErr)
		}
		op := operatorAssignCtx(uuid.NewString())
		var err error
		if _, setErr := server.AdminSetBillingGrant(op, &purserpb.AdminSetBillingGrantRequest{TenantId: tenant, TierName: target, WaiveUsage: true, Reason: "replace expired arrangement", ExpiresAt: timestamppb.New(time.Now().Add(time.Hour))}); status.Code(setErr) != codes.FailedPrecondition {
			t.Fatalf("expired replacement=%v", setErr)
		}
		var stillExpired, untouchedDocuments int
		if readErr := db.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM purser.subscription_operator_grants g JOIN purser.tenant_subscriptions s ON s.id=g.subscription_id WHERE s.tenant_id=$1 AND g.expires_at<NOW()),(SELECT count(*) FROM purser.billing_invoices WHERE tenant_id=$1)`, tenant).Scan(&stillExpired, &untouchedDocuments); readErr != nil {
			t.Fatal(readErr)
		}
		if stillExpired != 1 || untouchedDocuments != 0 {
			t.Fatalf("rejected replacement changed grant=%d documents=%d", stillExpired, untouchedDocuments)
		}

		if recovery == "set-tier" {
			_, err = server.AdminAssignTier(op, &purserpb.AdminAssignTierRequest{TenantId: tenant, TierName: target, Reason: "recover retired fallback"})
		} else {
			_, err = server.AdminRevokeBillingGrant(op, &purserpb.AdminRevokeBillingGrantRequest{TenantId: tenant, TierName: target, Reason: "recover retired fallback"})
		}
		if err != nil {
			t.Fatalf("%s failed recovery after committed cache failure: %v", recovery, err)
		}
		var gotTier string
		var grants, documents, refreshes int
		if readErr := db.QueryRowContext(ctx, `SELECT tier_id::text,(SELECT COUNT(*) FROM purser.subscription_operator_grants g WHERE g.subscription_id=ts.id),(SELECT COUNT(*) FROM purser.billing_invoices WHERE tenant_id=ts.tenant_id),(SELECT COUNT(*) FROM purser.media_authority_refresh_outbox WHERE tenant_id=ts.tenant_id) FROM purser.tenant_subscriptions ts WHERE tenant_id=$1`, tenant).Scan(&gotTier, &grants, &documents, &refreshes); readErr != nil {
			t.Fatal(readErr)
		}
		if gotTier != target || grants != 0 || documents != 1 || refreshes == 0 {
			t.Fatalf("%s tier=%s grants=%d documents=%d refreshes=%d", recovery, gotTier, grants, documents, refreshes)
		}
	}
	if cache.calls != 2 {
		t.Fatalf("cache attempts=%d, want one per committed recovery", cache.calls)
	}
	tenant := uuid.NewString()
	if _, writeErr := db.ExecContext(ctx, `INSERT INTO purser.tenant_subscriptions(tenant_id,tier_id,billing_model,status) VALUES($1,$2,'postpaid','active')`, tenant, active); writeErr != nil {
		t.Fatal(writeErr)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, getTierByNameForOperatorAssignmentErr := purserdb.New(tx).GetTierByNameForOperatorAssignment(ctx, target); getTierByNameForOperatorAssignmentErr != nil {
		t.Fatal(getTierByNameForOperatorAssignmentErr)
	}
	retiredResult := make(chan error, 1)
	go func() {
		retireErr := database.RetryPostgres(ctx, database.DefaultRetryAttempts, 25*time.Millisecond, func() error {
			_, writeErr := db.ExecContext(ctx, `UPDATE purser.billing_tiers SET is_active=false WHERE id=$1 AND is_active`, target)
			return writeErr
		})
		retiredResult <- retireErr
	}()
	select {
	case err := <-retiredResult:
		t.Fatalf("tier retirement bypassed grant writer's row lock: %v", err)
	case <-time.After(75 * time.Millisecond):
	}
	var catalogBefore time.Time
	if readErr := db.QueryRowContext(ctx, `SELECT updated_at FROM purser.billing_tiers WHERE id=$1`, target).Scan(&catalogBefore); readErr != nil {
		t.Fatal(readErr)
	}

	if _, writeErr := tx.ExecContext(ctx, `INSERT INTO purser.subscription_operator_grants(subscription_id,waive_usage,collection,reason,assigned_tier_id,fallback_tier_id) SELECT id,true,'provider','retirement race',tier_id,$2 FROM purser.tenant_subscriptions WHERE tenant_id=$1`, tenant, target); writeErr != nil {
		t.Fatal(writeErr)
	}
	if commitErr := tx.Commit(); commitErr != nil {
		t.Fatal(commitErr)
	}
	select {
	case err := <-retiredResult:
		var pgErr interface{ SQLState() string }
		if !errors.As(err, &pgErr) || pgErr.SQLState() != "23514" {
			t.Fatalf("retirement error=%v, want outstanding fallback constraint", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("tier retirement remained blocked after commit")
	}
	var catalogAfter time.Time
	var bindingRevision int64
	if readErr := db.QueryRowContext(ctx, `SELECT updated_at,operator_grant_fallback_revision FROM purser.billing_tiers WHERE id=$1`, target).Scan(&catalogAfter, &bindingRevision); readErr != nil {
		t.Fatal(readErr)
	}
	if !catalogAfter.Equal(catalogBefore) || bindingRevision == 0 {
		t.Fatalf("fallback fence changed catalog: before=%v after=%v revision=%d", catalogBefore, catalogAfter, bindingRevision)
	}

	// Recoverable cancellation keeps the arrangement and its assignable fallback.
	if _, cancelErr := db.ExecContext(ctx, `UPDATE purser.tenant_subscriptions SET status='cancelled' WHERE tenant_id=$1`, tenant); cancelErr != nil {
		t.Fatal(cancelErr)
	}
	if _, retireErr := db.ExecContext(ctx, `UPDATE purser.billing_tiers SET is_active=false WHERE id=$1`, target); database.SQLState(retireErr) != "23514" {
		t.Fatalf("retired a retained cancellation fallback: %v", retireErr)
	}
	if _, restoreErr := db.ExecContext(ctx, `UPDATE purser.tenant_subscriptions SET status='active' WHERE tenant_id=$1`, tenant); restoreErr != nil {
		t.Fatal(restoreErr)
	}
	var liveGrants, endedGrants int
	if readErr := db.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM purser.subscription_operator_grants g JOIN purser.tenant_subscriptions s ON s.id=g.subscription_id WHERE s.tenant_id=$1),(SELECT count(*) FROM purser.subscription_operator_grant_history WHERE tenant_id=$1)`, tenant).Scan(&liveGrants, &endedGrants); readErr != nil {
		t.Fatal(readErr)
	}
	if liveGrants != 1 || endedGrants != 0 {
		t.Fatalf("reactivation grants=%d history=%d", liveGrants, endedGrants)
	}
}

// Fallback readers must take their eventual write lock before another binder can
// hold a compatible share lock. Target-only reads keep their shared concurrency.
func testFallbackTierLockExcludesSharedUpgrade(t *testing.T, db *sql.DB, name, id string) {
	t.Helper()
	for _, fallback := range []bool{false, true} {
		lockErr := database.WithRetryablePostgresTx(t.Context(), db, nil, func(tx *sql.Tx) error {
			if fallback {
				if _, lookupErr := purserdb.New(tx).GetFallbackTierForOperatorGrant(t.Context(), name); lookupErr != nil {
					return lookupErr
				}
			} else if _, lookupErr := purserdb.New(tx).GetTierByNameForOperatorAssignment(t.Context(), name); lookupErr != nil {
				return lookupErr
			}
			var observed string
			readErr := db.QueryRowContext(t.Context(), `SELECT id::text FROM purser.billing_tiers WHERE id=$1 FOR SHARE SKIP LOCKED`, id).Scan(&observed)
			if fallback {
				if !errors.Is(readErr, sql.ErrNoRows) {
					return errors.New("fallback selection allowed another shared lock before its fence write")
				}
			} else if readErr != nil {
				return readErr
			}
			return nil
		})
		if lockErr != nil {
			t.Fatal(lockErr)
		}
	}
}

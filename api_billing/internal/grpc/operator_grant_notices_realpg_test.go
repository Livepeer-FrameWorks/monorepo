//go:build schema_verify

package grpc

import (
	"testing"
	"time"

	"frameworks/api_billing/internal/handlers"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// A grant ending within seven days is announced once for that end, an
// extension past the lead time is not announced early, and the end itself is
// announced naming the plan the account returns to.
func testOperatorGrantNotices(t *testing.T) {
	db := startOperatorGrantDatabase(t)
	ctx := t.Context()
	tenantID, fallbackID, grantedID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	fallbackName, grantedName := "notice-fallback-"+fallbackID[:8], "notice-granted-"+grantedID[:8]
	if _, err := db.ExecContext(ctx, `INSERT INTO purser.billing_tiers
		(id,tier_name,display_name,tier_level,base_price,currency,metering_enabled,is_active,is_default_prepaid)
		VALUES ($1,$2,'Notice Fallback',0,0,'EUR',false,true,true),($3,$4,'Notice Granted',4,0,'EUR',false,true,false)`,
		fallbackID, fallbackName, grantedID, grantedName); err != nil {
		t.Fatal(err)
	}
	email := "grant-notice-" + tenantID[:8] + "@example.com"
	if _, err := db.ExecContext(ctx, `INSERT INTO purser.tenant_subscriptions
		(tenant_id,tier_id,billing_model,status,billing_email,billing_name,billing_period_start,billing_period_end,presentment_currency)
		VALUES ($1,$2,'prepaid','active',$3,'Acme',NOW()-INTERVAL '1 hour',NOW()+INTERVAL '30 days','EUR')`, tenantID, fallbackID, email); err != nil {
		t.Fatal(err)
	}
	var sent []handlers.OperatorGrantNotice
	server := &PurserServer{db: db, logger: logging.NewLogger(), tierReconciler: &assignTierReconciler{}, commodoreClient: &recordingCommodoreCache{},
		operatorGrantNoticeSender: func(n handlers.OperatorGrantNotice) error {
			if n.Recipient == email {
				sent = append(sent, n)
			}
			return nil
		}}
	op := operatorAssignCtx(uuid.NewString())
	zero := "0"
	setGrant := func(expires time.Time) {
		t.Helper()
		if _, err := server.AdminSetBillingGrant(op, &purserpb.AdminSetBillingGrantRequest{
			TenantId: tenantID, TierName: grantedName, BasePrice: &zero, WaiveUsage: true, Reason: "trial",
			ExpiresAt: timestamppb.New(expires),
		}); err != nil {
			t.Fatal(err)
		}
	}

	setGrant(time.Now().Add(30 * 24 * time.Hour))
	server.sweepOperatorGrantNoticesOnce(ctx)
	if len(sent) != 0 {
		t.Fatalf("a grant ending in 30 days was announced: %+v", sent)
	}

	ends := time.Now().Add(3 * 24 * time.Hour).UTC().Truncate(time.Second)
	setGrant(ends)
	server.sweepOperatorGrantNoticesOnce(ctx)
	server.sweepOperatorGrantNoticesOnce(ctx)
	if len(sent) != 1 || sent[0].Kind != handlers.OperatorGrantNoticeEnding || !sent[0].EndsAt.Equal(ends) ||
		sent[0].PlanName != "Notice Fallback" || sent[0].TenantName != "Acme" {
		t.Fatalf("ending notices = %+v, want one for %s naming Notice Fallback", sent, ends)
	}

	// Moving the end past the lead time leaves nothing to announce yet.
	setGrant(time.Now().Add(20 * 24 * time.Hour))
	server.sweepOperatorGrantNoticesOnce(ctx)
	if len(sent) != 1 {
		t.Fatalf("an extended grant was announced early: %+v", sent)
	}

	if _, err := server.AdminRevokeBillingGrant(op, &purserpb.AdminRevokeBillingGrantRequest{TenantId: tenantID, Reason: "trial finished"}); err != nil {
		t.Fatal(err)
	}
	server.sweepOperatorGrantNoticesOnce(ctx)
	server.sweepOperatorGrantNoticesOnce(ctx)
	if len(sent) != 2 || sent[1].Kind != handlers.OperatorGrantNoticeEnded || sent[1].PlanName != "Notice Fallback" {
		t.Fatalf("notices after the end = %+v, want one ended notice naming Notice Fallback", sent)
	}
	var pending int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM purser.operator_grant_notices WHERE tenant_id=$1 AND notified_at IS NULL`, tenantID).Scan(&pending); err != nil {
		t.Fatal(err)
	}
	if pending != 0 {
		t.Fatalf("%d notices left unsent", pending)
	}
}

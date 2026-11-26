//go:build schema_verify

package grpc

import (
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"testing"
	"time"
)

func TestPricedAssignmentReview_RealPG(t *testing.T) {
	db := startOperatorGrantDatabase(t)
	tenant, current, target := uuid.NewString(), uuid.NewString(), uuid.NewString()
	ctx := operatorAssignCtx(uuid.NewString())
	for _, row := range []struct {
		id, name string
		level    int
	}{{current, "original", 3}, {target, "replacement", 4}} {
		if _, err := db.Exec(`INSERT INTO purser.billing_tiers(id,tier_name,display_name,tier_level,base_price,currency)VALUES($1,$2,$2,$3,0,'EUR')`, row.id, row.name, row.level); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO purser.tenant_subscriptions(tenant_id,tier_id,status,billing_model,billing_period_start,billing_period_end)VALUES($1,$2,'active','postpaid',NOW()-INTERVAL '1 day',NOW()+INTERVAL '29 days')`, tenant, current); err != nil {
		t.Fatal(err)
	}
	server := &PurserServer{db: db, logger: logging.NewLogger(), tierReconciler: &assignTierReconciler{}, commodoreClient: &recordingCommodoreCache{}}
	request := &purserpb.AdminAssignTierRequest{TenantId: tenant, TierName: "replacement", Reason: "review the complete arrangement"}
	quote, err := server.PreviewAdminAssignTier(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if quote.EffectsJson == "" || len(quote.Fingerprint) != 64 || !quote.ExpiresAt.AsTime().After(time.Now()) {
		t.Fatalf("missing priced review: %+v", quote)
	}
	var tier string
	if err = db.QueryRow(`SELECT tier_id::text FROM purser.tenant_subscriptions WHERE tenant_id=$1`, tenant).Scan(&tier); err != nil || tier != current {
		t.Fatalf("preview changed subscription: %s %v", tier, err)
	}
	reviewed := &purserpb.AdminAssignTierReviewedRequest{Assignment: request, QuotedAt: quote.QuotedAt, ExpectedFingerprint: quote.Fingerprint}
	if _, err = db.Exec(`INSERT INTO purser.tier_entitlements(tier_id,key,value)VALUES($1,'max_streams','7')`, target); err != nil {
		t.Fatal(err)
	}
	if _, err = server.AdminAssignTierWithReview(ctx, reviewed); status.Code(err) != codes.Aborted {
		t.Fatalf("changed catalog bypassed review: %v", err)
	}
	quote, err = server.PreviewAdminAssignTier(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	reviewed.QuotedAt = quote.QuotedAt
	reviewed.ExpectedFingerprint = quote.Fingerprint
	result, err := server.AdminAssignTierWithReview(ctx, reviewed)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Changed || result.TierId != target {
		t.Fatalf("reviewed assignment not applied: %+v", result)
	}
}
func TestPricedAssignmentReview_RealYugabyte(t *testing.T) { TestPricedAssignmentReview_RealPG(t) }

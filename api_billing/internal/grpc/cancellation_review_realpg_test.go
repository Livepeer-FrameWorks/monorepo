//go:build schema_verify

package grpc

import (
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"testing"
	"time"
)

func TestFreeCancellationReview_RealPG(t *testing.T) {
	f := newFreeDowngrade(t, "stripe")
	ctx := operatorAssignCtx(uuid.NewString())
	request := &purserpb.ChangeBillingTierRequest{TenantId: f.tenant, TierId: f.free}
	quote, err := f.server.PreviewFreeCancellation(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if quote.DueNowMinor != 0 || quote.RefundNowMinor != 0 || quote.WalletCreditNowMinor != 0 || quote.EffectiveAt == nil || quote.FutureUsageMessage == "" || len(quote.Fingerprint) != 64 {
		t.Fatalf("incomplete financial review: %+v", quote)
	}
	var pending bool
	var intents int
	if err = f.db.QueryRow(`SELECT pending_tier_id IS NOT NULL FROM purser.tenant_subscriptions WHERE tenant_id=$1`, f.tenant).Scan(&pending); err != nil || pending {
		t.Fatalf("preview scheduled a cancellation: pending=%t err=%v", pending, err)
	}
	if err = f.db.QueryRow(`SELECT count(*) FROM purser.provider_webhook_inbox WHERE headers->>'tenant_id'=$1`, f.tenant).Scan(&intents); err != nil || intents != 0 || len(f.calls) != 0 {
		t.Fatalf("preview performed provider work: queued=%d calls=%d err=%v", intents, len(f.calls), err)
	}
	confirm := &purserpb.ConfirmFreeCancellationRequest{TenantId: f.tenant, TierId: f.free, QuotedAt: quote.QuotedAt, ExpectedFingerprint: quote.Fingerprint}
	f.exec(t, `UPDATE purser.tenant_subscriptions SET billing_email='changed@example.test',updated_at=NOW() WHERE tenant_id=$1`, f.tenant)
	if _, err = f.server.ConfirmFreeCancellation(ctx, confirm); status.Code(err) != codes.Aborted {
		t.Fatalf("stale cancellation applied: %v", err)
	}
	quote, err = f.server.PreviewFreeCancellation(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	confirm.QuotedAt = quote.QuotedAt
	confirm.ExpectedFingerprint = quote.Fingerprint
	response, err := f.server.ConfirmFreeCancellation(ctx, confirm)
	if err != nil {
		t.Fatal(err)
	}
	if response.PendingTierId != f.free || response.EffectiveAt == nil || !response.EffectiveAt.AsTime().Equal(quote.EffectiveAt.AsTime()) {
		t.Fatalf("applied a different cancellation: %+v", response)
	}
	if quote.EffectiveAt.AsTime().Before(time.Now()) {
		t.Fatal("cancelled before the paid term ended")
	}
}

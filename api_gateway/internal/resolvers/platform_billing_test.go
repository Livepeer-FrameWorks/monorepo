package resolvers

import (
	"context"
	"frameworks/api_gateway/graph/model"
	"frameworks/api_gateway/internal/clients/clientstest"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
	"strings"
	"testing"
)

const billingRepairTenant = "11111111-1111-4111-8111-111111111111"
const billingRepairReference = "11111111-2222-4333-8444-555555555555"

func TestPlatformBillingWritesRequireOperatorAndReason(t *testing.T) {
	fake := &clientstest.FakePurser{}
	r := purserResolver(fake)
	for _, ctx := range []context.Context{context.Background(), clientstest.AuthedCtx(billingRepairTenant)} {
		if _, err := r.DoPlatformAdjustBalance(ctx, billingRepairTenant, 7900, "incident", billingRepairReference); err == nil {
			t.Fatal("credit admitted non-operator")
		}
		if _, err := r.DoPlatformAssignBillingTier(ctx, billingRepairTenant, "payg", "incident"); err == nil {
			t.Fatal("assignment admitted non-operator")
		}
		if _, err := r.DoPlatformRecoverCheckout(ctx, billingRepairTenant, billingRepairReference, "incident", true); err == nil {
			t.Fatal("recovery admitted non-operator")
		}
	}
	if _, err := r.DoPlatformAdjustBalance(operatorCtx(), billingRepairTenant, 7900, "", billingRepairReference); err == nil {
		t.Fatal("credit without reason")
	}
	if _, err := r.DoPlatformAdjustBalance(operatorCtx(), billingRepairTenant, 7900, "incident", "bad"); err == nil {
		t.Fatal("credit without retry identity")
	}
	if fake.Calls != 0 {
		t.Fatalf("invalid requests reached backend: %d", fake.Calls)
	}
}

func TestPlatformBillingWritesPreserveTargetActorAndRetryIdentity(t *testing.T) {
	fake := &clientstest.FakePurser{
		AdjustBalanceFn: func(ctx context.Context, target string, amount int64, reason string, ref, kind *string) (*purserpb.BalanceTransaction, error) {
			if target != billingRepairTenant || amount != 7900 || reason != "operator credit: incident" || *ref != billingRepairReference || *kind != "operator_adjustment" || !ctxkeys.IsPlatformOperator(ctx) || ctxkeys.GetUserID(ctx) != "operator-1" {
				t.Fatal("credit target, actor or retry identity lost")
			}
			return &purserpb.BalanceTransaction{Id: "receipt", TenantId: target, AmountCents: amount, BalanceAfterCents: 8812, ReferenceId: ref, ReferenceType: kind, CreatedAt: timestamppb.Now()}, nil
		},
		AdminAssignTierFn: func(ctx context.Context, req *purserpb.AdminAssignTierRequest) (*purserpb.AdminAssignTierResponse, error) {
			if req.TenantId != billingRepairTenant || req.TierName != "payg" || req.Reason != "incident" || !ctxkeys.IsPlatformOperator(ctx) {
				t.Fatal("assignment identity lost")
			}
			return &purserpb.AdminAssignTierResponse{SubscriptionId: "sub", TierName: "payg", BillingModel: "prepaid", Changed: true}, nil
		},
		AdminRecoverCheckoutFn: func(ctx context.Context, req *purserpb.AdminRecoverCheckoutRequest) (*purserpb.AdminRecoverCheckoutResponse, error) {
			if req.TenantId != billingRepairTenant || req.ReservationId != billingRepairReference || req.Reason != "incident" || !ctxkeys.IsPlatformOperator(ctx) {
				t.Fatal("checkout identity lost")
			}
			outcome := "preview"
			if req.Confirmed {
				outcome = "cancelled"
			}
			return &purserpb.AdminRecoverCheckoutResponse{Outcome: outcome, AmountKnown: false}, nil
		},
	}
	r := purserResolver(fake)
	if out, err := r.DoPlatformAdjustBalance(operatorCtx(), billingRepairTenant, 7900, " incident ", billingRepairReference); err != nil || out.ID != "receipt" {
		t.Fatalf("credit=%+v err=%v", out, err)
	}
	if out, err := r.DoPlatformAssignBillingTier(operatorCtx(), billingRepairTenant, "payg", "incident"); err != nil || out.BillingModel != "prepaid" {
		t.Fatalf("assignment=%+v err=%v", out, err)
	}
	for _, confirmed := range []bool{false, true} {
		if _, err := r.DoPlatformRecoverCheckout(operatorCtx(), billingRepairTenant, billingRepairReference, "incident", confirmed); err != nil {
			t.Fatal(err)
		}
	}
}

func TestPlatformEntitlementReviewExplainsAnUnsupportedBillingServer(t *testing.T) {
	fake := &clientstest.FakePurser{UpdateSubscriptionFn: func(_ context.Context, req *purserpb.UpdateSubscriptionRequest) (*purserpb.TenantSubscription, error) {
		if !req.CheckEntitlementOverrides || req.Reason != "review" {
			t.Fatal("review contract lost")
		}
		return nil, status.Error(codes.Unimplemented, "older Purser")
	}}
	reason := "review"
	_, err := purserResolver(fake).DoUpdateSubscriptionCustomTerms(operatorCtx(), billingRepairTenant, model.UpdateSubscriptionCustomTermsInput{Reason: &reason, ExpectedEntitlementOverrides: []*model.EntitlementEntryInput{}, EntitlementOverrides: []*model.EntitlementEntryInput{}})
	if err == nil || !strings.Contains(err.Error(), "Upgrade Purser") {
		t.Fatalf("missing server upgrade guidance: %v", err)
	}
}

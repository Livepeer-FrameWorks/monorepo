package resolvers

import (
	"context"
	"errors"
	"strings"
	"testing"

	"frameworks/api_gateway/graph/model"
	"frameworks/api_gateway/internal/clients/clientstest"

	commonpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/common"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	"google.golang.org/protobuf/types/known/emptypb"
)

// qmPurserR builds a resolver backed by both a FakeQuartermaster and a
// FakePurser. The cluster-lifecycle mutations split work across the two
// backends (Purser gates pricing/access, Quartermaster writes entitlement), so
// a non-stubbed method on either fake panics, proving an unexpected hop.
func qmPurserR(qm *clientstest.FakeQuartermaster, p *clientstest.FakePurser) *Resolver {
	return &Resolver{
		Clients: clientstest.Clients(clientstest.WithQuartermaster(qm), clientstest.WithPurser(p)),
		Logger:  clientstest.DiscardLogger(),
	}
}

// ---- DoSubscribeToCluster: Purser.CreateClusterSubscription, status union ----

func TestDoSubscribeToCluster_ActiveAndPendingAndGuard(t *testing.T) {
	var gotTenant, gotCluster, gotInvite string
	p := &clientstest.FakePurser{
		CreateClusterSubscriptionFn: func(_ context.Context, tenantID, clusterID, inviteToken string) (*purserpb.ClusterSubscriptionResponse, error) {
			gotTenant, gotCluster, gotInvite = tenantID, clusterID, inviteToken
			return &purserpb.ClusterSubscriptionResponse{Status: "active"}, nil
		},
	}
	ok, err := qmPurserR(&clientstest.FakeQuartermaster{}, p).DoSubscribeToCluster(qmUserCtx("t1"), "c1")
	if err != nil {
		t.Fatalf("DoSubscribeToCluster: %v", err)
	}
	if !ok {
		t.Fatal("active subscription should return true")
	}
	// Request is built from the user tenant + cluster arg; invite token is empty here.
	if gotTenant != "t1" || gotCluster != "c1" || gotInvite != "" {
		t.Fatalf("CreateClusterSubscription got (%q,%q,%q), want (t1,c1,\"\")", gotTenant, gotCluster, gotInvite)
	}

	// pending_payment surfaces a "status:pending_payment" error carrying the checkout URL.
	checkout := "https://checkout"
	pendingPay := &clientstest.FakePurser{
		CreateClusterSubscriptionFn: func(context.Context, string, string, string) (*purserpb.ClusterSubscriptionResponse, error) {
			return &purserpb.ClusterSubscriptionResponse{Status: "pending_payment", CheckoutUrl: &checkout}, nil
		},
	}
	_, perr := qmPurserR(&clientstest.FakeQuartermaster{}, pendingPay).DoSubscribeToCluster(qmUserCtx("t1"), "c1")
	if perr == nil || !strings.Contains(perr.Error(), "status:pending_payment") || !strings.Contains(perr.Error(), checkout) {
		t.Fatalf("pending_payment error = %v, want status:pending_payment + checkout url", perr)
	}

	// pending_approval surfaces a distinct status error.
	pendingApprove := &clientstest.FakePurser{
		CreateClusterSubscriptionFn: func(context.Context, string, string, string) (*purserpb.ClusterSubscriptionResponse, error) {
			return &purserpb.ClusterSubscriptionResponse{Status: "pending_approval"}, nil
		},
	}
	if _, aerr := qmPurserR(&clientstest.FakeQuartermaster{}, pendingApprove).DoSubscribeToCluster(qmUserCtx("t1"), "c1"); aerr == nil || !strings.Contains(aerr.Error(), "status:pending_approval") {
		t.Fatalf("pending_approval error = %v, want status:pending_approval", aerr)
	}

	// No user-derived tenant: guard short-circuits before the Purser call.
	guardP := &clientstest.FakePurser{}
	if _, gerr := qmPurserR(&clientstest.FakeQuartermaster{}, guardP).DoSubscribeToCluster(clientstest.AuthedCtx("t1"), "c1"); gerr == nil {
		t.Fatal("expected tenant-required error")
	}
	if guardP.Calls != 0 {
		t.Fatalf("guard leaked a Purser call: Calls=%d", guardP.Calls)
	}
}

func TestDoSubscribeToCluster_BackendError(t *testing.T) {
	p := &clientstest.FakePurser{
		CreateClusterSubscriptionFn: func(context.Context, string, string, string) (*purserpb.ClusterSubscriptionResponse, error) {
			return nil, errors.New("boom")
		},
	}
	if _, err := qmPurserR(&clientstest.FakeQuartermaster{}, p).DoSubscribeToCluster(qmUserCtx("t1"), "c1"); err == nil {
		t.Fatal("expected Purser error to propagate")
	}
}

// ---- DoUnsubscribeFromCluster: Quartermaster.UnsubscribeFromCluster ----

func TestDoUnsubscribeFromCluster_HappyAndGuard(t *testing.T) {
	var gotReq *quartermasterpb.UnsubscribeFromClusterRequest
	qm := &clientstest.FakeQuartermaster{
		UnsubscribeFromClusterFn: func(_ context.Context, req *quartermasterpb.UnsubscribeFromClusterRequest) (*emptypb.Empty, error) {
			gotReq = req
			return nil, nil
		},
	}
	ok, err := qmR(qm).DoUnsubscribeFromCluster(qmUserCtx("t1"), "c1")
	if err != nil {
		t.Fatalf("DoUnsubscribeFromCluster: %v", err)
	}
	if !ok {
		t.Fatal("expected success true")
	}
	// Request carries the user tenant + cluster arg.
	if gotReq.GetTenantId() != "t1" || gotReq.GetClusterId() != "c1" {
		t.Fatalf("req = (tenant %q, cluster %q), want (t1, c1)", gotReq.GetTenantId(), gotReq.GetClusterId())
	}

	guard := &clientstest.FakeQuartermaster{}
	if _, gerr := qmR(guard).DoUnsubscribeFromCluster(clientstest.AuthedCtx("t1"), "c1"); gerr == nil {
		t.Fatal("expected tenant-required error")
	}
	if guard.Calls != 0 {
		t.Fatalf("guard leaked a backend call: Calls=%d", guard.Calls)
	}

	failing := &clientstest.FakeQuartermaster{
		UnsubscribeFromClusterFn: func(context.Context, *quartermasterpb.UnsubscribeFromClusterRequest) (*emptypb.Empty, error) {
			return nil, errors.New("boom")
		},
	}
	if _, ferr := qmR(failing).DoUnsubscribeFromCluster(qmUserCtx("t1"), "c1"); ferr == nil {
		t.Fatal("expected backend error to propagate")
	}
}

// ---- DoCheckIsSubscribed: owner vs visible classification (no backend) ----

func TestDoCheckIsSubscribed_OwnerVisibleAndNoTenant(t *testing.T) {
	owner := "t1"
	// Owner of the cluster is not counted as a subscriber.
	isOwner, err := qmR(&clientstest.FakeQuartermaster{}).DoCheckIsSubscribed(qmUserCtx("t1"), &quartermasterpb.InfrastructureCluster{OwnerTenantId: &owner})
	if err != nil {
		t.Fatalf("DoCheckIsSubscribed(owner): %v", err)
	}
	if isOwner {
		t.Fatal("owner should report not-subscribed")
	}

	// Visible cluster the tenant does not own implies access/subscription.
	other := "t9"
	isSub, err := qmR(&clientstest.FakeQuartermaster{}).DoCheckIsSubscribed(qmUserCtx("t1"), &quartermasterpb.InfrastructureCluster{OwnerTenantId: &other})
	if err != nil {
		t.Fatalf("DoCheckIsSubscribed(non-owner): %v", err)
	}
	if !isSub {
		t.Fatal("non-owner with visible cluster should report subscribed")
	}

	// No user tenant in context: not subscribed, no error.
	noTenant, err := qmR(&clientstest.FakeQuartermaster{}).DoCheckIsSubscribed(clientstest.AuthedCtx("t1"), &quartermasterpb.InfrastructureCluster{})
	if err != nil {
		t.Fatalf("DoCheckIsSubscribed(no tenant): %v", err)
	}
	if noTenant {
		t.Fatal("missing tenant should report not-subscribed")
	}
}

// ---- DoRequestClusterSubscription: Purser access gate then Quartermaster write ----

func TestDoRequestClusterSubscription_HappyGatesThenWrites(t *testing.T) {
	var gotAccessTenant, gotAccessCluster string
	var gotReq *quartermasterpb.RequestClusterSubscriptionRequest
	p := &clientstest.FakePurser{
		CheckClusterAccessFn: func(_ context.Context, tenantID, clusterID string) (*purserpb.CheckClusterAccessResponse, error) {
			gotAccessTenant, gotAccessCluster = tenantID, clusterID
			return &purserpb.CheckClusterAccessResponse{Allowed: true, PricingModel: "free_unmetered"}, nil
		},
	}
	qm := &clientstest.FakeQuartermaster{
		RequestClusterSubscriptionFn: func(_ context.Context, req *quartermasterpb.RequestClusterSubscriptionRequest) (*quartermasterpb.ClusterSubscription, error) {
			gotReq = req
			return &quartermasterpb.ClusterSubscription{Id: "sub1", ClusterId: "c1"}, nil
		},
	}
	res, err := qmPurserR(qm, p).DoRequestClusterSubscription(qmUserCtx("t1"), "c1", nil)
	if err != nil {
		t.Fatalf("DoRequestClusterSubscription: %v", err)
	}
	// The commercial gate is consulted with the user tenant + cluster before any write.
	if gotAccessTenant != "t1" || gotAccessCluster != "c1" {
		t.Fatalf("CheckClusterAccess got (%q,%q), want (t1,c1)", gotAccessTenant, gotAccessCluster)
	}
	if gotReq.GetClusterId() != "c1" || gotReq.GetTenantId() != "t1" {
		t.Fatalf("request req = (cluster %q, tenant %q), want (c1, t1)", gotReq.GetClusterId(), gotReq.GetTenantId())
	}
	sub, ok := res.(*quartermasterpb.ClusterSubscription)
	if !ok || sub.GetId() != "sub1" {
		t.Fatalf("result = %T %+v, want sub1", res, res)
	}
}

func TestDoRequestClusterSubscription_MonthlyGateBlocksWrite(t *testing.T) {
	// Monthly clusters must route through subscribeToCluster (Stripe), so the
	// access gate returns a ValidationError and Quartermaster is never called.
	p := &clientstest.FakePurser{
		CheckClusterAccessFn: func(context.Context, string, string) (*purserpb.CheckClusterAccessResponse, error) {
			return &purserpb.CheckClusterAccessResponse{Allowed: true, PricingModel: "monthly"}, nil
		},
	}
	qm := &clientstest.FakeQuartermaster{} // RequestClusterSubscription must NOT be called
	res, err := qmPurserR(qm, p).DoRequestClusterSubscription(qmUserCtx("t1"), "c1", nil)
	if err != nil {
		t.Fatalf("DoRequestClusterSubscription: %v", err)
	}
	if _, ok := res.(*model.ValidationError); !ok {
		t.Fatalf("result = %T, want *model.ValidationError for monthly gate", res)
	}
	// Quartermaster never touched (only Purser's CheckClusterAccess ran).
	if qm.Calls != 0 {
		t.Fatalf("monthly gate leaked a Quartermaster call: Calls=%d", qm.Calls)
	}
}

func TestDoRequestClusterSubscription_AuthGate(t *testing.T) {
	// No user tenant: typed AuthError, no backend call on either fake.
	qm := &clientstest.FakeQuartermaster{}
	p := &clientstest.FakePurser{}
	res, err := qmPurserR(qm, p).DoRequestClusterSubscription(clientstest.AuthedCtx("t1"), "c1", nil)
	if err != nil {
		t.Fatalf("auth gate should return typed result: %v", err)
	}
	if _, ok := res.(*model.AuthError); !ok {
		t.Fatalf("result = %T, want *model.AuthError", res)
	}
	if qm.Calls != 0 || p.Calls != 0 {
		t.Fatalf("auth gate leaked backend calls: qm=%d purser=%d", qm.Calls, p.Calls)
	}
}

// ---- DoAcceptClusterInvite: resolve token -> cluster, gate, then accept ----

func TestDoAcceptClusterInvite_HappyAndUnknownToken(t *testing.T) {
	var gotReq *quartermasterpb.AcceptClusterInviteRequest
	p := &clientstest.FakePurser{
		CheckClusterAccessFn: func(context.Context, string, string) (*purserpb.CheckClusterAccessResponse, error) {
			return &purserpb.CheckClusterAccessResponse{Allowed: true, PricingModel: "free_unmetered"}, nil
		},
	}
	qm := &clientstest.FakeQuartermaster{
		// clusterIDForInviteToken resolves the token to its cluster via my-invites.
		ListMyClusterInvitesFn: func(context.Context, *quartermasterpb.ListMyClusterInvitesRequest) (*quartermasterpb.ListClusterInvitesResponse, error) {
			return &quartermasterpb.ListClusterInvitesResponse{
				Invites: []*quartermasterpb.ClusterInvite{{Id: "inv1", ClusterId: "c1", InviteToken: "tok"}},
			}, nil
		},
		AcceptClusterInviteFn: func(_ context.Context, req *quartermasterpb.AcceptClusterInviteRequest) (*quartermasterpb.ClusterSubscription, error) {
			gotReq = req
			return &quartermasterpb.ClusterSubscription{Id: "sub1", ClusterId: "c1"}, nil
		},
	}
	res, err := qmPurserR(qm, p).DoAcceptClusterInvite(qmUserCtx("t1"), "tok")
	if err != nil {
		t.Fatalf("DoAcceptClusterInvite: %v", err)
	}
	if gotReq.GetInviteToken() != "tok" || gotReq.GetTenantId() != "t1" {
		t.Fatalf("accept req = (token %q, tenant %q), want (tok, t1)", gotReq.GetInviteToken(), gotReq.GetTenantId())
	}
	sub, ok := res.(*quartermasterpb.ClusterSubscription)
	if !ok || sub.GetId() != "sub1" {
		t.Fatalf("result = %T %+v, want sub1", res, res)
	}

	// Token not among my invites: ValidationError before any accept/gate call.
	unknown := &clientstest.FakeQuartermaster{
		ListMyClusterInvitesFn: func(context.Context, *quartermasterpb.ListMyClusterInvitesRequest) (*quartermasterpb.ListClusterInvitesResponse, error) {
			return &quartermasterpb.ListClusterInvitesResponse{}, nil
		},
	}
	ures, uerr := qmPurserR(unknown, &clientstest.FakePurser{}).DoAcceptClusterInvite(qmUserCtx("t1"), "nope")
	if uerr != nil {
		t.Fatalf("unknown token should return typed result: %v", uerr)
	}
	if _, ok := ures.(*model.ValidationError); !ok {
		t.Fatalf("result = %T, want *model.ValidationError for unknown token", ures)
	}
}

// ---- DoApproveClusterSubscription: owner-scoped approve ----

func TestDoApproveClusterSubscription_HappyAndError(t *testing.T) {
	var gotReq *quartermasterpb.ApproveClusterSubscriptionRequest
	qm := &clientstest.FakeQuartermaster{
		ApproveClusterSubscriptionFn: func(_ context.Context, req *quartermasterpb.ApproveClusterSubscriptionRequest) (*quartermasterpb.ClusterSubscription, error) {
			gotReq = req
			return &quartermasterpb.ClusterSubscription{Id: "sub1", ClusterId: "c1"}, nil
		},
	}
	res, err := qmR(qm).DoApproveClusterSubscription(qmUserCtx("t1"), "sub1")
	if err != nil {
		t.Fatalf("DoApproveClusterSubscription: %v", err)
	}
	if gotReq.GetSubscriptionId() != "sub1" || gotReq.GetOwnerTenantId() != "t1" {
		t.Fatalf("req = (sub %q, owner %q), want (sub1, t1)", gotReq.GetSubscriptionId(), gotReq.GetOwnerTenantId())
	}
	if sub, ok := res.(*quartermasterpb.ClusterSubscription); !ok || sub.GetId() != "sub1" {
		t.Fatalf("result = %T %+v, want sub1", res, res)
	}

	// Backend error is classified into a ValidationError result (no Go error).
	failing := &clientstest.FakeQuartermaster{
		ApproveClusterSubscriptionFn: func(context.Context, *quartermasterpb.ApproveClusterSubscriptionRequest) (*quartermasterpb.ClusterSubscription, error) {
			return nil, errors.New("boom")
		},
	}
	fres, ferr := qmR(failing).DoApproveClusterSubscription(qmUserCtx("t1"), "sub1")
	if ferr != nil {
		t.Fatalf("backend error should be classified: %v", ferr)
	}
	if _, ok := fres.(*model.ValidationError); !ok {
		t.Fatalf("result = %T, want *model.ValidationError", fres)
	}
}

// ---- DoRejectClusterSubscription: owner-scoped reject with reason ----

func TestDoRejectClusterSubscription_HappyAndAuthGate(t *testing.T) {
	var gotReq *quartermasterpb.RejectClusterSubscriptionRequest
	qm := &clientstest.FakeQuartermaster{
		RejectClusterSubscriptionFn: func(_ context.Context, req *quartermasterpb.RejectClusterSubscriptionRequest) (*quartermasterpb.ClusterSubscription, error) {
			gotReq = req
			return &quartermasterpb.ClusterSubscription{Id: "sub1", ClusterId: "c1"}, nil
		},
	}
	reason := "spam"
	res, err := qmR(qm).DoRejectClusterSubscription(qmUserCtx("t1"), "sub1", &reason)
	if err != nil {
		t.Fatalf("DoRejectClusterSubscription: %v", err)
	}
	if gotReq.GetSubscriptionId() != "sub1" || gotReq.GetOwnerTenantId() != "t1" || gotReq.GetReason() != "spam" {
		t.Fatalf("req = %+v, want sub1/t1/spam", gotReq)
	}
	if sub, ok := res.(*quartermasterpb.ClusterSubscription); !ok || sub.GetId() != "sub1" {
		t.Fatalf("result = %T %+v, want sub1", res, res)
	}

	// No user tenant: typed AuthError, no backend call.
	guard := &clientstest.FakeQuartermaster{}
	gres, gerr := qmR(guard).DoRejectClusterSubscription(clientstest.AuthedCtx("t1"), "sub1", nil)
	if gerr != nil {
		t.Fatalf("auth gate should return typed result: %v", gerr)
	}
	if _, ok := gres.(*model.AuthError); !ok {
		t.Fatalf("result = %T, want *model.AuthError", gres)
	}
	if guard.Calls != 0 {
		t.Fatalf("auth gate leaked a backend call: Calls=%d", guard.Calls)
	}
}

// ---- DoCreateClusterInvite: builds owner-scoped invite, returns proto ----

func TestDoCreateClusterInvite_HappyAndError(t *testing.T) {
	var gotReq *quartermasterpb.CreateClusterInviteRequest
	qm := &clientstest.FakeQuartermaster{
		CreateClusterInviteFn: func(_ context.Context, req *quartermasterpb.CreateClusterInviteRequest) (*quartermasterpb.ClusterInvite, error) {
			gotReq = req
			return &quartermasterpb.ClusterInvite{Id: "inv1", ClusterId: "c1"}, nil
		},
	}
	access := "write"
	days := 5
	res, err := qmR(qm).DoCreateClusterInvite(qmUserCtx("t1"), model.CreateClusterInviteInput{
		ClusterID:       "c1",
		InvitedTenantID: "t9",
		AccessLevel:     &access,
		ExpiresInDays:   &days,
	})
	if err != nil {
		t.Fatalf("DoCreateClusterInvite: %v", err)
	}
	// Owner is the user tenant; cluster/invited/access/expiry come from input.
	if gotReq.GetClusterId() != "c1" || gotReq.GetOwnerTenantId() != "t1" || gotReq.GetInvitedTenantId() != "t9" {
		t.Fatalf("req ids = %+v, want c1/t1/t9", gotReq)
	}
	if gotReq.GetAccessLevel() != "write" || gotReq.GetExpiresInDays() != 5 {
		t.Fatalf("req access/expiry = (%q,%d), want (write,5)", gotReq.GetAccessLevel(), gotReq.GetExpiresInDays())
	}
	if inv, ok := res.(*quartermasterpb.ClusterInvite); !ok || inv.GetId() != "inv1" {
		t.Fatalf("result = %T %+v, want inv1", res, res)
	}

	failing := &clientstest.FakeQuartermaster{
		CreateClusterInviteFn: func(context.Context, *quartermasterpb.CreateClusterInviteRequest) (*quartermasterpb.ClusterInvite, error) {
			return nil, errors.New("boom")
		},
	}
	fres, ferr := qmR(failing).DoCreateClusterInvite(qmUserCtx("t1"), model.CreateClusterInviteInput{ClusterID: "c1", InvitedTenantID: "t9"})
	if ferr != nil {
		t.Fatalf("backend error should be classified: %v", ferr)
	}
	if _, ok := fres.(*model.ValidationError); !ok {
		t.Fatalf("result = %T, want *model.ValidationError", fres)
	}
}

// ---- DoRevokeClusterInvite: owner-scoped revoke -> DeleteSuccess / NotFound ----

func TestDoRevokeClusterInvite_HappyAndError(t *testing.T) {
	var gotReq *quartermasterpb.RevokeClusterInviteRequest
	qm := &clientstest.FakeQuartermaster{
		RevokeClusterInviteFn: func(_ context.Context, req *quartermasterpb.RevokeClusterInviteRequest) error {
			gotReq = req
			return nil
		},
	}
	res, err := qmR(qm).DoRevokeClusterInvite(qmUserCtx("t1"), "inv1")
	if err != nil {
		t.Fatalf("DoRevokeClusterInvite: %v", err)
	}
	if gotReq.GetInviteId() != "inv1" || gotReq.GetOwnerTenantId() != "t1" {
		t.Fatalf("req = (invite %q, owner %q), want (inv1, t1)", gotReq.GetInviteId(), gotReq.GetOwnerTenantId())
	}
	if del, ok := res.(*model.DeleteSuccess); !ok || !del.Success {
		t.Fatalf("result = %T %+v, want DeleteSuccess{true}", res, res)
	}

	// Backend error becomes a NotFoundError result (no Go error).
	failing := &clientstest.FakeQuartermaster{
		RevokeClusterInviteFn: func(context.Context, *quartermasterpb.RevokeClusterInviteRequest) error {
			return errors.New("boom")
		},
	}
	fres, ferr := qmR(failing).DoRevokeClusterInvite(qmUserCtx("t1"), "inv1")
	if ferr != nil {
		t.Fatalf("backend error should be classified: %v", ferr)
	}
	if _, ok := fres.(*model.NotFoundError); !ok {
		t.Fatalf("result = %T, want *model.NotFoundError", fres)
	}

	// No user tenant: typed AuthError, no backend call.
	guard := &clientstest.FakeQuartermaster{}
	gres, _ := qmR(guard).DoRevokeClusterInvite(clientstest.AuthedCtx("t1"), "inv1")
	if _, ok := gres.(*model.AuthError); !ok {
		t.Fatalf("result = %T, want *model.AuthError", gres)
	}
	if guard.Calls != 0 {
		t.Fatalf("auth gate leaked a backend call: Calls=%d", guard.Calls)
	}
}

// ---- DoUpdateClusterMarketplace: Purser pricing + Quartermaster settings ----

func TestDoUpdateClusterMarketplace_PricingAndSettings(t *testing.T) {
	var gotPricingReq *purserpb.SetClusterPricingRequest
	var gotQMReq *quartermasterpb.UpdateClusterMarketplaceRequest
	var gotPricingLookup string
	p := &clientstest.FakePurser{
		SetClusterPricingFn: func(_ context.Context, req *purserpb.SetClusterPricingRequest) (*purserpb.ClusterPricing, error) {
			gotPricingReq = req
			return &purserpb.ClusterPricing{}, nil
		},
		// Post-update enrichment: pricing is re-read to fill the returned cluster.
		GetClusterPricingFn: func(_ context.Context, clusterID string) (*purserpb.ClusterPricing, error) {
			gotPricingLookup = clusterID
			return &purserpb.ClusterPricing{PricingModel: "monthly", BasePrice: "29.99"}, nil
		},
	}
	qm := &clientstest.FakeQuartermaster{
		UpdateClusterMarketplaceFn: func(_ context.Context, req *quartermasterpb.UpdateClusterMarketplaceRequest) (*quartermasterpb.ClusterResponse, error) {
			gotQMReq = req
			return &quartermasterpb.ClusterResponse{Cluster: &quartermasterpb.InfrastructureCluster{ClusterId: "c1"}}, nil
		},
	}
	model3000 := 3000
	visibility := quartermasterpb.ClusterVisibility_CLUSTER_VISIBILITY_PUBLIC
	pricing := quartermasterpb.ClusterPricingModel_CLUSTER_PRICING_MONTHLY
	res, err := qmPurserR(qm, p).DoUpdateClusterMarketplace(qmUserCtx("t1"), "c1", model.UpdateClusterMarketplaceInput{
		Visibility:        &visibility,
		PricingModel:      &pricing,
		MonthlyPriceCents: &model3000,
	})
	if err != nil {
		t.Fatalf("DoUpdateClusterMarketplace: %v", err)
	}
	// Pricing fields route to Purser; 3000 cents serializes to "30.00".
	if gotPricingReq.GetClusterId() != "c1" || gotPricingReq.GetPricingModel() != "monthly" || gotPricingReq.GetBasePrice() != "30.00" {
		t.Fatalf("pricing req = %+v, want c1/monthly/30.00", gotPricingReq)
	}
	// Operational settings route to Quartermaster.
	if gotQMReq.GetClusterId() != "c1" || gotQMReq.GetTenantId() != "t1" || gotQMReq.GetVisibility() != visibility {
		t.Fatalf("qm req = %+v, want c1/t1/PUBLIC", gotQMReq)
	}
	// Returned cluster is enriched from the re-read pricing (29.99 -> 2999 cents).
	if gotPricingLookup != "c1" {
		t.Fatalf("pricing re-read cluster = %q, want c1", gotPricingLookup)
	}
	cluster, ok := res.(*quartermasterpb.InfrastructureCluster)
	if !ok {
		t.Fatalf("result = %T, want *InfrastructureCluster", res)
	}
	if cluster.GetMonthlyPriceCents() != 2999 || cluster.GetPricingModel() != pricing {
		t.Fatalf("enriched cluster = (%d cents, %v), want (2999, MONTHLY)", cluster.GetMonthlyPriceCents(), cluster.GetPricingModel())
	}
}

func TestDoUpdateClusterMarketplace_PricingErrorAndAuthGate(t *testing.T) {
	// SetClusterPricing failure is classified into a ValidationError; QM untouched.
	qm := &clientstest.FakeQuartermaster{}
	p := &clientstest.FakePurser{
		SetClusterPricingFn: func(context.Context, *purserpb.SetClusterPricingRequest) (*purserpb.ClusterPricing, error) {
			return nil, errors.New("boom")
		},
	}
	cents := 100
	res, err := qmPurserR(qm, p).DoUpdateClusterMarketplace(qmUserCtx("t1"), "c1", model.UpdateClusterMarketplaceInput{MonthlyPriceCents: &cents})
	if err != nil {
		t.Fatalf("pricing error should be classified: %v", err)
	}
	if _, ok := res.(*model.ValidationError); !ok {
		t.Fatalf("result = %T, want *model.ValidationError", res)
	}
	if qm.Calls != 0 {
		t.Fatalf("pricing failure leaked a Quartermaster call: Calls=%d", qm.Calls)
	}

	// No user tenant: typed AuthError, no backend call on either fake.
	gqm := &clientstest.FakeQuartermaster{}
	gp := &clientstest.FakePurser{}
	gres, _ := qmPurserR(gqm, gp).DoUpdateClusterMarketplace(clientstest.AuthedCtx("t1"), "c1", model.UpdateClusterMarketplaceInput{})
	if _, ok := gres.(*model.AuthError); !ok {
		t.Fatalf("result = %T, want *model.AuthError", gres)
	}
	if gqm.Calls != 0 || gp.Calls != 0 {
		t.Fatalf("auth gate leaked backend calls: qm=%d purser=%d", gqm.Calls, gp.Calls)
	}
}

// ---- DoSetPreferredCluster: writes primary cluster, refetches cluster ----

func TestDoSetPreferredCluster_HappyAndUpdateError(t *testing.T) {
	var gotReq *quartermasterpb.UpdateTenantClusterRequest
	qm := &clientstest.FakeQuartermaster{
		UpdateTenantClusterFn: func(_ context.Context, req *quartermasterpb.UpdateTenantClusterRequest) error {
			gotReq = req
			return nil
		},
		// After the write, the resolver refetches the cluster (DoGetCluster:
		// GetCluster + ownership check).
		GetClusterFn: func(context.Context, string) (*quartermasterpb.ClusterResponse, error) {
			return &quartermasterpb.ClusterResponse{Cluster: &quartermasterpb.InfrastructureCluster{ClusterId: "c1", ClusterName: "Edge"}}, nil
		},
		ListClustersByOwnerFn: func(context.Context, string, *commonpb.CursorPaginationRequest) (*quartermasterpb.ListClustersResponse, error) {
			return &quartermasterpb.ListClustersResponse{Clusters: []*quartermasterpb.InfrastructureCluster{{ClusterId: "c1"}}}, nil
		},
	}
	res, err := qmR(qm).DoSetPreferredCluster(qmUserCtx("t1"), "c1")
	if err != nil {
		t.Fatalf("DoSetPreferredCluster: %v", err)
	}
	if gotReq.GetTenantId() != "t1" || gotReq.GetPrimaryClusterId() != "c1" {
		t.Fatalf("req = (tenant %q, primary %q), want (t1, c1)", gotReq.GetTenantId(), gotReq.GetPrimaryClusterId())
	}
	if cluster, ok := res.(*quartermasterpb.InfrastructureCluster); !ok || cluster.GetClusterName() != "Edge" {
		t.Fatalf("result = %T %+v, want Edge cluster", res, res)
	}

	// Write failure is classified into a ValidationError result.
	failing := &clientstest.FakeQuartermaster{
		UpdateTenantClusterFn: func(context.Context, *quartermasterpb.UpdateTenantClusterRequest) error {
			return errors.New("boom")
		},
	}
	fres, ferr := qmR(failing).DoSetPreferredCluster(qmUserCtx("t1"), "c1")
	if ferr != nil {
		t.Fatalf("write error should be classified: %v", ferr)
	}
	if _, ok := fres.(*model.ValidationError); !ok {
		t.Fatalf("result = %T, want *model.ValidationError", fres)
	}

	// No user tenant: typed AuthError, no backend call.
	guard := &clientstest.FakeQuartermaster{}
	gres, _ := qmR(guard).DoSetPreferredCluster(clientstest.AuthedCtx("t1"), "c1")
	if _, ok := gres.(*model.AuthError); !ok {
		t.Fatalf("result = %T, want *model.AuthError", gres)
	}
	if guard.Calls != 0 {
		t.Fatalf("auth gate leaked a backend call: Calls=%d", guard.Calls)
	}
}

// ---- DoGetMarketplaceCluster: QM metadata enriched by Purser pricing ----

func TestDoGetMarketplaceCluster_EnrichesPricing(t *testing.T) {
	var gotMetaReq *quartermasterpb.GetMarketplaceClusterRequest
	var gotPricingCluster []string
	qm := &clientstest.FakeQuartermaster{
		GetMarketplaceClusterFn: func(_ context.Context, req *quartermasterpb.GetMarketplaceClusterRequest) (*quartermasterpb.MarketplaceClusterEntry, error) {
			gotMetaReq = req
			return &quartermasterpb.MarketplaceClusterEntry{ClusterId: "c1", ClusterName: "Edge"}, nil
		},
	}
	denial := "tier too low"
	p := &clientstest.FakePurser{
		GetClustersPricingBatchFn: func(_ context.Context, tenantID string, clusterIDs []string) (map[string]*purserpb.ClusterPricing, error) {
			gotPricingCluster = clusterIDs
			return map[string]*purserpb.ClusterPricing{
				"c1": {PricingModel: "monthly", BasePrice: "19.99", IsEligible: false, DenialReason: &denial},
			}, nil
		},
	}
	got, err := qmPurserR(qm, p).DoGetMarketplaceCluster(qmUserCtx("t1"), "c1", nil)
	if err != nil {
		t.Fatalf("DoGetMarketplaceCluster: %v", err)
	}
	if gotMetaReq.GetClusterId() != "c1" || gotMetaReq.GetTenantId() != "t1" {
		t.Fatalf("meta req = %+v, want c1/t1", gotMetaReq)
	}
	if len(gotPricingCluster) != 1 || gotPricingCluster[0] != "c1" {
		t.Fatalf("pricing batch ids = %v, want [c1]", gotPricingCluster)
	}
	// 19.99 -> 1999 cents; ineligible carries the denial reason through.
	if got.GetMonthlyPriceCents() != 1999 || got.GetIsEligible() {
		t.Fatalf("enriched = (%d cents, eligible=%v), want (1999, false)", got.GetMonthlyPriceCents(), got.GetIsEligible())
	}
	if got.GetDenialReason() != "tier too low" {
		t.Fatalf("denial = %q, want 'tier too low'", got.GetDenialReason())
	}

	// QM lookup failure propagates as an error (cluster not found/available).
	failing := &clientstest.FakeQuartermaster{
		GetMarketplaceClusterFn: func(context.Context, *quartermasterpb.GetMarketplaceClusterRequest) (*quartermasterpb.MarketplaceClusterEntry, error) {
			return nil, errors.New("boom")
		},
	}
	if _, ferr := qmPurserR(failing, &clientstest.FakePurser{}).DoGetMarketplaceCluster(qmUserCtx("t1"), "c1", nil); ferr == nil {
		t.Fatal("expected QM lookup error to propagate")
	}
}

// ---- DoGetStreamingConfig: cluster routing -> per-cluster domains ----

func TestDoGetStreamingConfig_BuildsDomainsAndNilFallbacks(t *testing.T) {
	slug := "uswest"
	qm := &clientstest.FakeQuartermaster{
		GetClusterRoutingFn: func(_ context.Context, req *quartermasterpb.GetClusterRoutingRequest) (*quartermasterpb.ClusterRoutingResponse, error) {
			if req.GetTenantId() != "t1" {
				t.Fatalf("routing tenant = %q, want t1", req.GetTenantId())
			}
			return &quartermasterpb.ClusterRoutingResponse{
				ClusterSlug: &slug,
				BaseUrl:     "https://example.com",
				ClusterName: "US West",
			}, nil
		},
	}
	cfg, err := qmR(qm).DoGetStreamingConfig(clientstest.AuthedCtx("t1"))
	if err != nil {
		t.Fatalf("DoGetStreamingConfig: %v", err)
	}
	if cfg == nil {
		t.Fatal("expected non-nil streaming config")
	}
	// Per-cluster domains are built as "<prefix>.<slug>.<baseDomain>".
	if cfg.IngestDomain == nil || *cfg.IngestDomain != "edge-ingest.uswest.example.com" {
		t.Fatalf("ingest domain = %v, want edge-ingest.uswest.example.com", cfg.IngestDomain)
	}
	if cfg.PlayDomain == nil || *cfg.PlayDomain != "foghorn.uswest.example.com" {
		t.Fatalf("play domain = %v, want foghorn.uswest.example.com", cfg.PlayDomain)
	}
	if cfg.PreferredClusterLabel == nil || *cfg.PreferredClusterLabel != "US West" {
		t.Fatalf("preferred label = %v, want US West", cfg.PreferredClusterLabel)
	}

	// Routing error returns nil cfg + nil error (frontend falls back to env).
	failing := &clientstest.FakeQuartermaster{
		GetClusterRoutingFn: func(context.Context, *quartermasterpb.GetClusterRoutingRequest) (*quartermasterpb.ClusterRoutingResponse, error) {
			return nil, errors.New("boom")
		},
	}
	fcfg, ferr := qmR(failing).DoGetStreamingConfig(clientstest.AuthedCtx("t1"))
	if ferr != nil || fcfg != nil {
		t.Fatalf("routing error should yield (nil,nil), got (%v,%v)", fcfg, ferr)
	}

	// No tenant: nil cfg, nil error, no backend call.
	noTenant := &clientstest.FakeQuartermaster{}
	ncfg, nerr := qmR(noTenant).DoGetStreamingConfig(context.Background())
	if nerr != nil || ncfg != nil {
		t.Fatalf("no tenant should yield (nil,nil), got (%v,%v)", ncfg, nerr)
	}
	if noTenant.Calls != 0 {
		t.Fatalf("guard leaked a backend call: Calls=%d", noTenant.Calls)
	}
}

func TestDoGetStreamingConfig_EmptySlugReturnsNil(t *testing.T) {
	// Routing succeeds but slug/baseURL are empty: config is unusable -> nil.
	qm := &clientstest.FakeQuartermaster{
		GetClusterRoutingFn: func(context.Context, *quartermasterpb.GetClusterRoutingRequest) (*quartermasterpb.ClusterRoutingResponse, error) {
			return &quartermasterpb.ClusterRoutingResponse{BaseUrl: "https://example.com"}, nil
		},
	}
	cfg, err := qmR(qm).DoGetStreamingConfig(clientstest.AuthedCtx("t1"))
	if err != nil || cfg != nil {
		t.Fatalf("empty slug should yield (nil,nil), got (%v,%v)", cfg, err)
	}
}

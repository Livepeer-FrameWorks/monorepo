package grpc

import (
	"context"
	"database/sql"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func subscriptionJWTContext(tenantID string, operator bool) context.Context {
	ctx := context.WithValue(context.Background(), ctxkeys.KeyAuthType, "jwt")
	ctx = context.WithValue(ctx, ctxkeys.KeyTenantID, tenantID)
	if operator {
		ctx = context.WithValue(ctx, ctxkeys.KeyPlatformOperator, true)
	}
	return ctx
}

func TestRequireTenantSubscriptionActor(t *testing.T) {
	tests := []struct {
		name   string
		ctx    context.Context
		tenant string
		code   codes.Code
	}{
		{"same tenant", subscriptionJWTContext("tenant-a", false), "tenant-a", codes.OK},
		{"other tenant", subscriptionJWTContext("tenant-a", false), "tenant-b", codes.PermissionDenied},
		{"operator override", subscriptionJWTContext("operator-tenant", true), "tenant-b", codes.OK},
		{"service credential", context.WithValue(context.Background(), ctxkeys.KeyAuthType, "service"), "tenant-a", codes.PermissionDenied},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := requireTenantSubscriptionActor(test.ctx, test.tenant)
			if status.Code(err) != test.code {
				t.Fatalf("code = %s, want %s (err=%v)", status.Code(err), test.code, err)
			}
		})
	}
}

func TestDirectSubscriptionPolicyNeverInfersTierOrPayment(t *testing.T) {
	if err := rejectDirectCommercialClusterAccess("tenant", true, sql.NullString{}, "free", "requested"); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("platform-official direct request code = %s, want FailedPrecondition", status.Code(err))
	}
	if err := rejectDirectCommercialClusterAccess("tenant", false, sql.NullString{String: "owner", Valid: true}, "monthly", "requested"); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("marketplace direct request code = %s, want FailedPrecondition", status.Code(err))
	}
}

func TestPrivateInvitePolicyDoesNotBecomeMarketplaceAuthority(t *testing.T) {
	owner := sql.NullString{String: "owner", Valid: true}
	if err := validatePrivateInviteCluster(false, owner, "tenant_private", "private", "accepted"); err != nil {
		t.Fatalf("tenant-private invite rejected: %v", err)
	}
	if err := validatePrivateInviteCluster(false, owner, "", "private", "accepted"); err != nil {
		t.Fatalf("legacy private invite rejected: %v", err)
	}
	for _, tc := range []struct {
		name       string
		official   bool
		class      string
		visibility string
	}{
		{name: "marketplace", class: "third_party_marketplace", visibility: "unlisted"},
		{name: "official", official: true, class: "platform_official", visibility: "public"},
		{name: "unknown marketplace class", class: "", visibility: "unlisted"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := validatePrivateInviteCluster(tc.official, owner, tc.class, tc.visibility, "accepted"); status.Code(err) != codes.FailedPrecondition {
				t.Fatalf("code = %s, want FailedPrecondition (err=%v)", status.Code(err), err)
			}
		})
	}
}

func TestClusterSubscriptionAccessSourceNeverWritesUnknown(t *testing.T) {
	if source, err := clusterSubscriptionAccessSource("tenant", "invite", false, sql.NullString{String: "owner", Valid: true}); err != nil || source != "private_invite" {
		t.Fatalf("private invite source=%q err=%v", source, err)
	}
	if source, err := clusterSubscriptionAccessSource("tenant", "", false, sql.NullString{String: "tenant", Valid: true}); err != nil || source != "owner" {
		t.Fatalf("owner source=%q err=%v", source, err)
	}
	if source, err := clusterSubscriptionAccessSource("tenant", "", false, sql.NullString{String: "other", Valid: true}); status.Code(err) != codes.FailedPrecondition || source != "" {
		t.Fatalf("ambiguous source=%q code=%s err=%v", source, status.Code(err), err)
	}
}

func TestMarketplaceApprovalModelsMatchPurser(t *testing.T) {
	for _, model := range []string{"custom", "free_unmetered", "tier_inherit", "metered"} {
		if !marketplacePricingSupportsApproval(model) {
			t.Fatalf("pricing model %q must be approvable", model)
		}
	}
	for _, model := range []string{"monthly", "", "invented"} {
		if marketplacePricingSupportsApproval(model) {
			t.Fatalf("pricing model %q unexpectedly approvable", model)
		}
	}
}

func TestMarketplaceInvitePathsRejectBeforeMutation(t *testing.T) {
	const (
		consumer = "11111111-1111-1111-1111-111111111111"
		owner    = "22222222-2222-2222-2222-222222222222"
		inviteID = "33333333-3333-3333-3333-333333333333"
	)

	t.Run("create invite", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		server := NewQuartermasterServer(db, logging.NewLogger(), nil, nil, nil, nil, nil)
		mock.ExpectQuery("SELECT owner_tenant_id, cluster_name").WithArgs("market-1").
			WillReturnRows(sqlmock.NewRows([]string{"owner_tenant_id", "cluster_name", "cluster_class", "is_platform_official", "visibility"}).
				AddRow(owner, "Marketplace", "third_party_marketplace", false, "public"))
		_, callErr := server.CreateClusterInvite(subscriptionJWTContext(owner, false), &quartermasterpb.CreateClusterInviteRequest{
			OwnerTenantId: owner, InvitedTenantId: consumer, ClusterId: "market-1",
		})
		if status.Code(callErr) != codes.FailedPrecondition {
			t.Fatalf("code = %s, want FailedPrecondition (err=%v)", status.Code(callErr), callErr)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("request subscription", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		server := NewQuartermasterServer(db, logging.NewLogger(), nil, nil, nil, nil, nil)
		mock.ExpectQuery("SELECT visibility, pricing_model").WithArgs("market-1").
			WillReturnRows(sqlmock.NewRows([]string{"visibility", "pricing_model", "requires_approval", "owner_tenant_id", "cluster_class", "is_platform_official"}).
				AddRow("public", "monthly", false, owner, "third_party_marketplace", false))
		_, callErr := server.RequestClusterSubscription(subscriptionJWTContext(consumer, false), &quartermasterpb.RequestClusterSubscriptionRequest{
			TenantId: consumer, ClusterId: "market-1", InviteToken: strPtr("invite-token"),
		})
		if status.Code(callErr) != codes.FailedPrecondition {
			t.Fatalf("code = %s, want FailedPrecondition (err=%v)", status.Code(callErr), callErr)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("accept invite", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		server := NewQuartermasterServer(db, logging.NewLogger(), nil, nil, nil, nil, nil)
		mock.ExpectQuery("FROM quartermaster.cluster_invites i").WithArgs("invite-token").
			WillReturnRows(sqlmock.NewRows([]string{"id", "cluster_id", "invited_tenant_id", "access_level", "resource_limits", "pricing_model", "owner_tenant_id", "cluster_class", "is_platform_official", "visibility"}).
				AddRow(inviteID, "market-1", consumer, "subscriber", []byte("{}"), "monthly", owner, "third_party_marketplace", false, "public"))
		_, callErr := server.AcceptClusterInvite(subscriptionJWTContext(consumer, false), &quartermasterpb.AcceptClusterInviteRequest{
			TenantId: consumer, InviteToken: "invite-token",
		})
		if status.Code(callErr) != codes.FailedPrecondition {
			t.Fatalf("code = %s, want FailedPrecondition (err=%v)", status.Code(callErr), callErr)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	})
}

func TestUnlistedSubscriptionMustUseBillingBeforeMutation(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	server := NewQuartermasterServer(db, logging.NewLogger(), nil, nil, nil, nil, nil)
	mock.ExpectQuery("SELECT visibility, pricing_model").WithArgs("unlisted-1").
		WillReturnRows(sqlmock.NewRows([]string{"visibility", "pricing_model", "requires_approval", "owner_tenant_id", "cluster_class", "is_platform_official"}).
			AddRow("unlisted", "metered", false, "22222222-2222-2222-2222-222222222222", "third_party_marketplace", false))

	_, callErr := server.RequestClusterSubscription(subscriptionJWTContext("11111111-1111-1111-1111-111111111111", false), &quartermasterpb.RequestClusterSubscriptionRequest{
		TenantId: "11111111-1111-1111-1111-111111111111", ClusterId: "unlisted-1",
	})
	if status.Code(callErr) != codes.FailedPrecondition {
		t.Fatalf("code = %s, want FailedPrecondition (err=%v)", status.Code(callErr), callErr)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestSubscriptionHandlersRejectCrossTenantBeforeDatabase(t *testing.T) {
	server := &QuartermasterServer{}
	ctx := subscriptionJWTContext("attacker", false)
	tests := []struct {
		name string
		call func() error
	}{
		{"unsubscribe", func() error {
			_, err := server.UnsubscribeFromCluster(ctx, &quartermasterpb.UnsubscribeFromClusterRequest{TenantId: "victim", ClusterId: "cluster"})
			return err
		}},
		{"create invite", func() error {
			_, err := server.CreateClusterInvite(ctx, &quartermasterpb.CreateClusterInviteRequest{OwnerTenantId: "victim", InvitedTenantId: "other", ClusterId: "cluster"})
			return err
		}},
		{"revoke invite", func() error {
			_, err := server.RevokeClusterInvite(ctx, &quartermasterpb.RevokeClusterInviteRequest{OwnerTenantId: "victim", InviteId: "invite"})
			return err
		}},
		{"list cluster invites", func() error {
			_, err := server.ListClusterInvites(ctx, &quartermasterpb.ListClusterInvitesRequest{OwnerTenantId: "victim", ClusterId: "cluster"})
			return err
		}},
		{"list my invites", func() error {
			_, err := server.ListMyClusterInvites(ctx, &quartermasterpb.ListMyClusterInvitesRequest{TenantId: "victim"})
			return err
		}},
		{"request", func() error {
			_, err := server.RequestClusterSubscription(ctx, &quartermasterpb.RequestClusterSubscriptionRequest{TenantId: "victim", ClusterId: "cluster"})
			return err
		}},
		{"accept invite", func() error {
			_, err := server.AcceptClusterInvite(ctx, &quartermasterpb.AcceptClusterInviteRequest{TenantId: "victim", InviteToken: "token"})
			return err
		}},
		{"list pending", func() error {
			_, err := server.ListPendingSubscriptions(ctx, &quartermasterpb.ListPendingSubscriptionsRequest{OwnerTenantId: "victim", ClusterId: "cluster"})
			return err
		}},
		{"approve", func() error {
			_, err := server.ApproveClusterSubscription(ctx, &quartermasterpb.ApproveClusterSubscriptionRequest{OwnerTenantId: "victim", SubscriptionId: "sub"})
			return err
		}},
		{"reject", func() error {
			_, err := server.RejectClusterSubscription(ctx, &quartermasterpb.RejectClusterSubscriptionRequest{OwnerTenantId: "victim", SubscriptionId: "sub"})
			return err
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := test.call(); status.Code(err) != codes.PermissionDenied {
				t.Fatalf("code = %s, want PermissionDenied (err=%v)", status.Code(err), err)
			}
		})
	}
}

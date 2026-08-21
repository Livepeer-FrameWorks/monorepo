package grpc

import (
	"context"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	foghorncontrolpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn_control"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
)

type tierReconcileResult struct {
	eligible []string
	primary  string
}

type recordingTierReconciler struct {
	results []tierReconcileResult
	calls   []string
}

func (r *recordingTierReconciler) OfficialClusterIDs(context.Context) (map[string]bool, error) {
	return map[string]bool{}, nil
}

func (r *recordingTierReconciler) Reconcile(_ context.Context, _ string, _ int32, tierName string) ([]string, string, error) {
	r.calls = append(r.calls, tierName)
	result := r.results[len(r.calls)-1]
	return result.eligible, result.primary, nil
}

type recordingCommodoreCache struct {
	tenantID string
	reason   string
}

func (r *recordingCommodoreCache) TerminateTenantStreams(context.Context, string, string) (*foghorncontrolpb.TerminateTenantStreamsResponse, error) {
	panic("unexpected TerminateTenantStreams call")
}

func (r *recordingCommodoreCache) InvalidateTenantCache(_ context.Context, tenantID, reason string) (*foghorncontrolpb.InvalidateTenantCacheResponse, error) {
	r.tenantID = tenantID
	r.reason = reason
	return &foghorncontrolpb.InvalidateTenantCacheResponse{}, nil
}

func (r *recordingCommodoreCache) GetTenantUserCount(context.Context, string) (*commodorepb.GetTenantUserCountResponse, error) {
	panic("unexpected GetTenantUserCount call")
}

func (r *recordingCommodoreCache) GetTenantPrimaryUser(context.Context, string) (*commodorepb.GetTenantPrimaryUserResponse, error) {
	panic("unexpected GetTenantPrimaryUser call")
}

// CancelSubscription flips status to cancelled and enqueues a
// subscription_canceled outbox event inside the same tx — the cancel and its
// downstream notification must commit atomically.
func TestCancelSubscriptionHappyPathEnqueuesOutbox(t *testing.T) {
	s, mock := newReadServer(t, true)

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT id\s+FROM purser\.tenant_subscriptions\s+WHERE tenant_id = \$1::text::uuid AND status != 'cancelled'`).
		WithArgs("tenant-1").
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("91000000-0000-4000-8000-000000000001"))
	mock.ExpectExec(`INSERT INTO purser\.billing_collection_writeoffs`).
		WithArgs("tenant-1").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`UPDATE purser\.billing_collection_balances`).
		WithArgs("tenant-1").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`UPDATE purser\.tenant_subscriptions`).
		WithArgs("tenant-1").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`INSERT INTO purser\.billing_event_outbox`).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("93000000-0000-4000-8000-000000000001"))
	mock.ExpectCommit()

	_, err := s.CancelSubscription(context.Background(), &purserpb.CancelSubscriptionRequest{TenantId: "tenant-1"})
	if err != nil {
		t.Fatalf("CancelSubscription: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet: %v", err)
	}
}

// When the UPDATE matches no active subscription the call must report NotFound
// and roll back — no outbox row may be enqueued for a cancel that didn't happen.
func TestCancelSubscriptionNotFoundRollsBack(t *testing.T) {
	s, mock := newReadServer(t, true)

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT id FROM purser\.tenant_subscriptions`).
		WithArgs("tenant-x").
		WillReturnError(sqlmockNoRows())
	mock.ExpectExec(`INSERT INTO purser\.billing_collection_writeoffs`).
		WithArgs("tenant-x").
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`UPDATE purser\.billing_collection_balances`).
		WithArgs("tenant-x").
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`UPDATE purser\.tenant_subscriptions`).
		WithArgs("tenant-x").
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectRollback()

	_, err := s.CancelSubscription(context.Background(), &purserpb.CancelSubscriptionRequest{TenantId: "tenant-x"})
	if status.Code(err) != codes.NotFound {
		t.Fatalf("err = %v, want NotFound", err)
	}
}

func TestCancelSubscriptionEmptyTenantGuard(t *testing.T) {
	s := newGuardServer(t)
	_, err := s.CancelSubscription(context.Background(), &purserpb.CancelSubscriptionRequest{})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("err = %v, want InvalidArgument", err)
	}
}

// PromoteToPaid moves a prepaid tenant to the default postpaid tier, carries the
// prepaid balance forward as credit, and returns the new tier level.
func TestPromoteToPaidDefaultTierCarriesCredit(t *testing.T) {
	s, mock := newReadServer(t, true)
	reconciler := &recordingTierReconciler{results: []tierReconcileResult{{eligible: []string{"cluster-paid"}, primary: "cluster-paid"}}}
	cache := &recordingCommodoreCache{}
	s.tierReconciler = reconciler
	s.commodoreClient = cache
	mock.ExpectBegin()
	expectLockedPromotionSubscription(mock, "tenant-1", "prepaid", "tier-prepaid", "stripe", "sub_ready", "billing@example.com", "Example Customer", []byte(`{"street":"Main 1","city":"Amsterdam","postal_code":"1000AA","country":"NL"}`))
	mock.ExpectQuery(`SELECT id::text AS id, tier_level, tier_name, is_default_prepaid, is_active`).
		WithArgs(false, "").
		WillReturnRows(sqlmock.NewRows([]string{"id", "tier_level", "tier_name", "is_default_prepaid", "is_active"}).AddRow("tier-paid", int32(2), "supporter", false, true))
	mock.ExpectQuery(`UPDATE purser\.tenant_subscriptions\s+SET billing_model = 'postpaid'`).
		WithArgs("tier-paid", "tenant-1").
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("sub-1"))
	mock.ExpectCommit()
	expectCanonicalPromotion(mock, "tenant-1", "sub-1", "tier-paid", int32(2), "supporter", 1500)
	expectCanonicalTierReconcile(mock, "tenant-1", "tier-paid", int32(2), "supporter", "tier-paid")

	resp, err := s.PromoteToPaid(context.Background(), &purserpb.PromoteToPaidRequest{TenantId: "tenant-1"})
	if err != nil {
		t.Fatalf("PromoteToPaid: %v", err)
	}
	if !resp.Success || resp.NewBillingModel != "postpaid" {
		t.Fatalf("unexpected resp: %+v", resp)
	}
	if resp.CreditBalanceCents != 1500 {
		t.Fatalf("CreditBalanceCents = %d, want 1500 (prepaid balance carried forward)", resp.CreditBalanceCents)
	}
	if resp.TierLevel != 2 || resp.SubscriptionId != "sub-1" {
		t.Fatalf("tier/sub mapping wrong: %+v", resp)
	}
	if len(resp.GetEligibleClusterIds()) != 1 || resp.GetEligibleClusterIds()[0] != "cluster-paid" || resp.GetPrimaryClusterId() != "cluster-paid" {
		t.Fatalf("cluster entitlements do not reflect canonical paid tier: %+v", resp)
	}
	if len(reconciler.calls) != 1 || reconciler.calls[0] != "supporter" {
		t.Fatalf("reconciled tiers = %v, want [supporter]", reconciler.calls)
	}
	if cache.tenantID != "tenant-1" || cache.reason != "tier_changed" {
		t.Fatalf("cache invalidation = %q/%q", cache.tenantID, cache.reason)
	}
}

func TestReconcileCanonicalTierClusterAccessRetriesSupersededWinner(t *testing.T) {
	s, mock := newReadServer(t, true)
	reconciler := &recordingTierReconciler{results: []tierReconcileResult{
		{eligible: []string{"cluster-old"}, primary: "cluster-old"},
		{eligible: []string{"cluster-new"}, primary: "cluster-new"},
	}}
	s.tierReconciler = reconciler
	expectCanonicalTierReconcile(mock, "tenant-1", "tier-old", int32(1), "free", "tier-new")
	expectCanonicalTierReconcile(mock, "tenant-1", "tier-new", int32(4), "production", "tier-new")

	eligible, primary, err := s.reconcileCanonicalTierClusterAccess(context.Background(), "tenant-1")
	if err != nil {
		t.Fatalf("reconcileCanonicalTierClusterAccess: %v", err)
	}
	if len(eligible) != 1 || eligible[0] != "cluster-new" || primary != "cluster-new" {
		t.Fatalf("result = %v/%q, want canonical winner", eligible, primary)
	}
	if len(reconciler.calls) != 2 || reconciler.calls[0] != "free" || reconciler.calls[1] != "production" {
		t.Fatalf("reconciled tiers = %v", reconciler.calls)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func expectCanonicalTierReconcile(mock sqlmock.Sqlmock, tenantID, tierID string, tierLevel int32, tierName, verifiedTierID string) {
	mock.ExpectQuery(`SELECT ts\.tier_id::text AS tier_id, COALESCE\(bt\.tier_level, 0\)::integer AS tier_level, bt\.tier_name`).WithArgs(tenantID).
		WillReturnRows(sqlmock.NewRows([]string{"tier_id", "tier_level", "tier_name"}).AddRow(tierID, tierLevel, tierName))
	mock.ExpectQuery(`SELECT tier_id::text AS tier_id\s+FROM purser\.tenant_subscriptions`).WithArgs(tenantID).
		WillReturnRows(sqlmock.NewRows([]string{"tier_id"}).AddRow(verifiedTierID))
}

func TestPromoteToPaidRequiresCompleteBillingDetails(t *testing.T) {
	s, mock := newReadServer(t, true)
	mock.ExpectBegin()
	expectLockedPromotionSubscription(mock, "tenant-1", "prepaid", "tier-prepaid", nil, nil, "billing@example.com", nil, nil)
	mock.ExpectQuery(`SELECT id::text AS id, tier_level, tier_name, is_default_prepaid, is_active`).
		WithArgs(false, "").
		WillReturnRows(sqlmock.NewRows([]string{"id", "tier_level", "tier_name", "is_default_prepaid", "is_active"}).AddRow("tier-paid", int32(2), "supporter", false, true))
	mock.ExpectRollback()

	_, err := s.PromoteToPaid(context.Background(), &purserpb.PromoteToPaidRequest{TenantId: "tenant-1"})
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("err = %v, want FailedPrecondition", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet: %v", err)
	}
}

// An explicit tier_id is honored only when active and postpaid-eligible
// (tier_level >= 1 and not the default prepaid tier).
func TestPromoteToPaidExplicitTierRejectsNonEligible(t *testing.T) {
	cases := []struct {
		name      string
		isPrepaid bool
		isActive  bool
		tierLevel int32
		wantCode  codes.Code
	}{
		{"inactive tier", false, false, 3, codes.FailedPrecondition},
		{"default-prepaid tier", true, true, 3, codes.FailedPrecondition},
		{"zero tier level", false, true, 0, codes.FailedPrecondition},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, mock := newReadServer(t, true)
			mock.ExpectBegin()
			expectLockedPromotionSubscription(mock, "tenant-1", "prepaid", "tier-prepaid", nil, nil, nil, nil, nil)
			mock.ExpectQuery(`SELECT id::text AS id, tier_level, tier_name, is_default_prepaid, is_active`).
				WithArgs(true, "tier-req").
				WillReturnRows(sqlmock.NewRows([]string{"id", "tier_level", "tier_name", "is_default_prepaid", "is_active"}).
					AddRow("tier-req", tc.tierLevel, "some-tier", tc.isPrepaid, tc.isActive))
			mock.ExpectRollback()

			tierID := "tier-req"
			_, err := s.PromoteToPaid(context.Background(), &purserpb.PromoteToPaidRequest{TenantId: "tenant-1", TierId: tierID})
			if status.Code(err) != tc.wantCode {
				t.Fatalf("err = %v, want %v", err, tc.wantCode)
			}
		})
	}
}

func TestPromoteToPaidAllowsVerifiedFreePathWithoutBillingProfileOrProvider(t *testing.T) {
	s, mock := newReadServer(t, true)
	mock.ExpectBegin()
	expectLockedPromotionSubscription(mock, "tenant-1", "prepaid", "tier-prepaid", nil, nil, nil, nil, nil)
	mock.ExpectQuery(`SELECT id::text AS id, tier_level, tier_name, is_default_prepaid, is_active`).
		WithArgs(true, "tier-free").
		WillReturnRows(sqlmock.NewRows([]string{"id", "tier_level", "tier_name", "is_default_prepaid", "is_active"}).
			AddRow("tier-free", int32(1), "free", false, true))
	mock.ExpectQuery(`UPDATE purser\.tenant_subscriptions\s+SET billing_model = 'postpaid'`).
		WithArgs("tier-free", "tenant-1").
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("sub-1"))
	mock.ExpectCommit()
	expectCanonicalPromotion(mock, "tenant-1", "sub-1", "tier-free", int32(1), "free", 1500)

	resp, err := s.PromoteToPaid(context.Background(), &purserpb.PromoteToPaidRequest{TenantId: "tenant-1", TierId: "tier-free"})
	if err != nil || !resp.GetSuccess() || resp.GetCreditBalanceCents() != 1500 {
		t.Fatalf("free promotion = %+v, %v", resp, err)
	}
}

func expectCompleteBillingDetails(mock sqlmock.Sqlmock, tenantID string) {
	mock.ExpectQuery(`SELECT billing_email, billing_name, billing_company, tax_id, billing_address, updated_at`).
		WithArgs(tenantID).
		WillReturnRows(sqlmock.NewRows([]string{"billing_email", "billing_name", "billing_company", "tax_id", "billing_address", "updated_at"}).
			AddRow("billing@example.com", "Example Customer", "Example", nil, []byte(`{"street":"Main 1","city":"Amsterdam","postal_code":"1000AA","country":"NL"}`), time.Now()))
}

func TestPromoteToPaidNotFoundAndAlreadyPostpaid(t *testing.T) {
	t.Run("no subscription", func(t *testing.T) {
		s, mock := newReadServer(t, true)
		mock.ExpectBegin()
		mock.ExpectQuery(`SELECT id::text AS id, billing_model, tier_id::text AS tier_id`).
			WithArgs("tenant-x").
			WillReturnError(sqlmockNoRows())
		mock.ExpectRollback()
		_, err := s.PromoteToPaid(context.Background(), &purserpb.PromoteToPaidRequest{TenantId: "tenant-x"})
		if status.Code(err) != codes.NotFound {
			t.Fatalf("err = %v, want NotFound", err)
		}
	})
	t.Run("same postpaid tier is idempotent", func(t *testing.T) {
		s, mock := newReadServer(t, true)
		mock.ExpectBegin()
		expectLockedPromotionSubscription(mock, "tenant-1", "postpaid", "tier-free", nil, nil, nil, nil, nil)
		mock.ExpectQuery(`SELECT id::text AS id, tier_level, tier_name, is_default_prepaid, is_active`).
			WithArgs(true, "tier-free").
			WillReturnRows(sqlmock.NewRows([]string{"id", "tier_level", "tier_name", "is_default_prepaid", "is_active"}).AddRow("tier-free", int32(1), "free", false, true))
		mock.ExpectCommit()
		expectCanonicalPromotion(mock, "tenant-1", "sub-1", "tier-free", int32(1), "free", 1500)
		response, err := s.PromoteToPaid(context.Background(), &purserpb.PromoteToPaidRequest{TenantId: "tenant-1"})
		if err != nil || !response.GetSuccess() {
			t.Fatalf("response=%+v err=%v", response, err)
		}
	})
}

func expectLockedPromotionSubscription(mock sqlmock.Sqlmock, tenantID, model, tierID string, paymentMethod, stripeSubscription, email, name, address any) {
	if address == nil {
		address = []byte(`{}`)
	}
	mock.ExpectQuery(`SELECT id::text AS id, billing_model, tier_id::text AS tier_id, payment_method`).WithArgs(tenantID).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "billing_model", "tier_id", "payment_method", "stripe_subscription_id", "mollie_subscription_id",
			"billing_email", "billing_name", "billing_address",
		}).AddRow("sub-1", model, tierID, paymentMethod, stripeSubscription, nil, email, name, address))
}

func expectCanonicalPromotion(mock sqlmock.Sqlmock, tenantID, subscriptionID, tierID string, tierLevel int32, tierName string, balance int64) {
	mock.ExpectQuery(`SELECT subscription.id::text AS subscription_id, subscription.billing_model`).WithArgs("EUR", tenantID).
		WillReturnRows(sqlmock.NewRows([]string{"id", "billing_model", "tier_id", "tier_level", "tier_name", "balance_cents"}).
			AddRow(subscriptionID, "postpaid", tierID, tierLevel, tierName, balance))
}

func TestPromoteToPaidEmptyTenantGuard(t *testing.T) {
	s := newGuardServer(t)
	_, err := s.PromoteToPaid(context.Background(), &purserpb.PromoteToPaidRequest{})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("err = %v, want InvalidArgument", err)
	}
}

// UpdateBillingDetails builds a dynamic UPDATE from the supplied fields, then
// re-reads via GetBillingDetails. A successful update echoes the new details.
func TestUpdateBillingDetailsAppliesAndRereads(t *testing.T) {
	s, mock := newReadServer(t, true)
	now := time.Now()

	mock.ExpectExec(`UPDATE purser\.tenant_subscriptions`).
		WillReturnResult(sqlmock.NewResult(0, 1))
	// trailing GetBillingDetails read
	mock.ExpectQuery(`FROM purser\.tenant_subscriptions`).
		WithArgs("tenant-1").
		WillReturnRows(sqlmock.NewRows([]string{"billing_email", "billing_name", "billing_company", "tax_id", "billing_address", "updated_at"}).
			AddRow("new@example.com", nil, nil, nil, []byte(`{}`), now))

	email := "new@example.com"
	resp, err := s.UpdateBillingDetails(context.Background(), &purserpb.UpdateBillingDetailsRequest{TenantId: "tenant-1", Email: &email})
	if err != nil {
		t.Fatalf("UpdateBillingDetails: %v", err)
	}
	if resp.Email != "new@example.com" {
		t.Fatalf("echoed email = %q", resp.Email)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet: %v", err)
	}
}

// An invalid ISO country code must be rejected before any DB write.
func TestUpdateBillingDetailsInvalidCountry(t *testing.T) {
	s := newGuardServer(t)
	_, err := s.UpdateBillingDetails(context.Background(), &purserpb.UpdateBillingDetailsRequest{
		TenantId: "tenant-1",
		Address:  &purserpb.BillingAddress{Country: "Nowhere"},
	})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("err = %v, want InvalidArgument", err)
	}
}

// With no mutable fields the handler short-circuits to a plain read.
func TestUpdateBillingDetailsNoFieldsDelegatesToRead(t *testing.T) {
	s, mock := newReadServer(t, true)
	now := time.Now()
	mock.ExpectQuery(`FROM purser\.tenant_subscriptions`).
		WithArgs("tenant-1").
		WillReturnRows(sqlmock.NewRows([]string{"billing_email", "billing_name", "billing_company", "tax_id", "billing_address", "updated_at"}).
			AddRow("a@b.com", nil, nil, nil, []byte(`{}`), now))

	resp, err := s.UpdateBillingDetails(context.Background(), &purserpb.UpdateBillingDetailsRequest{TenantId: "tenant-1"})
	if err != nil {
		t.Fatalf("UpdateBillingDetails: %v", err)
	}
	if resp.Email != "a@b.com" {
		t.Fatalf("delegated read email = %q", resp.Email)
	}
}

func TestUpdateBillingDetailsNotFound(t *testing.T) {
	s, mock := newReadServer(t, true)
	email := "x@y.com"
	mock.ExpectExec(`UPDATE purser\.tenant_subscriptions`).
		WillReturnResult(sqlmock.NewResult(0, 0))
	_, err := s.UpdateBillingDetails(context.Background(), &purserpb.UpdateBillingDetailsRequest{TenantId: "tenant-1", Email: &email})
	if status.Code(err) != codes.NotFound {
		t.Fatalf("err = %v, want NotFound", err)
	}
}

// InitializePrepaidAccount atomically creates a prepaid subscription + a
// zero-balance prepaid_balances row, then reads back the (possibly pre-existing)
// IDs after commit.
func TestInitializePrepaidAccountHappyPath(t *testing.T) {
	s, mock := newReadServer(t, true)
	tierID := "81000000-0000-4000-8000-000000000001"
	subID := "82000000-0000-4000-8000-000000000001"
	balID := "83000000-0000-4000-8000-000000000001"

	mock.ExpectBegin()
	mock.ExpectQuery(`FROM purser\.billing_tiers\s+WHERE \(`).WithArgs(true).
		WillReturnRows(sqlmock.NewRows([]string{"id", "tier_level", "tier_name"}).AddRow(tierID, int32(1), "payg"))
	mock.ExpectExec(`INSERT INTO purser\.tenant_subscriptions`).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`INSERT INTO purser\.prepaid_balances`).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	mock.ExpectQuery(`SELECT ts\.id AS subscription_id, pb\.id AS balance_id`).
		WithArgs("EUR", "tenant-1").
		WillReturnRows(sqlmock.NewRows([]string{"id", "balance_id", "tier_level", "tier_name"}).AddRow(subID, balID, int32(1), "payg"))

	resp, err := s.InitializePrepaidAccount(context.Background(), &purserpb.InitializePrepaidAccountRequest{TenantId: "tenant-1"})
	if err != nil {
		t.Fatalf("InitializePrepaidAccount: %v", err)
	}
	if resp.SubscriptionId != subID || resp.BalanceId != balID || resp.TierLevel != 1 {
		t.Fatalf("response mapping wrong: %+v", resp)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet: %v", err)
	}
}

func TestInitializePrepaidAccountReportsSubscriptionThatWonConcurrentProvisioning(t *testing.T) {
	s, mock := newReadServer(t, true)
	tierID := "81000000-0000-4000-8000-000000000002"
	subID := "82000000-0000-4000-8000-000000000002"
	balID := "83000000-0000-4000-8000-000000000002"
	mock.ExpectBegin()
	mock.ExpectQuery(`FROM purser\.billing_tiers\s+WHERE \(`).WithArgs(true).
		WillReturnRows(sqlmock.NewRows([]string{"id", "tier_level", "tier_name"}).AddRow(tierID, int32(0), "payg"))
	mock.ExpectExec(`INSERT INTO purser\.tenant_subscriptions`).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`INSERT INTO purser\.prepaid_balances`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	mock.ExpectQuery(`SELECT ts\.id AS subscription_id, pb\.id AS balance_id`).
		WithArgs("EUR", "tenant-1").
		WillReturnRows(sqlmock.NewRows([]string{"id", "balance_id", "tier_level", "tier_name"}).AddRow(subID, balID, int32(1), "free"))

	resp, err := s.InitializePrepaidAccount(context.Background(), &purserpb.InitializePrepaidAccountRequest{TenantId: "tenant-1"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.GetSubscriptionId() != subID || resp.GetTierLevel() != 1 {
		t.Fatalf("response did not report canonical winner: %+v", resp)
	}
}

// No configured default prepaid tier is a precondition failure, not a silent
// account with a missing tier.
func TestInitializePrepaidAccountNoDefaultTier(t *testing.T) {
	s, mock := newReadServer(t, true)
	mock.ExpectBegin()
	mock.ExpectQuery(`FROM purser\.billing_tiers\s+WHERE \(`).WithArgs(true).
		WillReturnError(sqlmockNoRows())
	mock.ExpectRollback()

	_, err := s.InitializePrepaidAccount(context.Background(), &purserpb.InitializePrepaidAccountRequest{TenantId: "tenant-1"})
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("err = %v, want FailedPrecondition", err)
	}
}

func TestInitializePostpaidAccountHappyPath(t *testing.T) {
	s, mock := newReadServer(t, true)
	tierID := "81000000-0000-4000-8000-000000000003"
	subID := "82000000-0000-4000-8000-000000000003"
	mock.ExpectBegin()
	mock.ExpectQuery(`FROM purser\.billing_tiers\s+WHERE \(`).WithArgs(false).
		WillReturnRows(sqlmock.NewRows([]string{"id", "tier_level", "tier_name"}).AddRow(tierID, int32(2), "free"))
	mock.ExpectExec(`INSERT INTO purser\.tenant_subscriptions`).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	mock.ExpectQuery(`SELECT ts\.id AS subscription_id, COALESCE\(bt\.tier_level, 0\)::integer AS tier_level`).
		WithArgs("tenant-1").
		WillReturnRows(sqlmock.NewRows([]string{"id", "tier_level", "tier_name"}).AddRow(subID, int32(2), "free"))

	resp, err := s.InitializePostpaidAccount(context.Background(), &purserpb.InitializePostpaidAccountRequest{TenantId: "tenant-1"})
	if err != nil {
		t.Fatalf("InitializePostpaidAccount: %v", err)
	}
	if resp.SubscriptionId != subID || resp.TierLevel != 2 {
		t.Fatalf("response mapping wrong: %+v", resp)
	}
}

func TestEnsureFreeAccountReportsSubscriptionThatWonConcurrentProvisioning(t *testing.T) {
	s, mock := newReadServer(t, true)
	tierID := "81000000-0000-4000-8000-000000000004"
	subID := "82000000-0000-4000-8000-000000000004"
	mock.ExpectBegin()
	mock.ExpectQuery(`FROM purser\.billing_tiers\s+WHERE \(`).WithArgs(false).
		WillReturnRows(sqlmock.NewRows([]string{"id", "tier_level", "tier_name"}).AddRow(tierID, int32(1), "free"))
	mock.ExpectExec(`INSERT INTO purser\.tenant_subscriptions`).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectCommit()
	mock.ExpectQuery(`SELECT ts\.id AS subscription_id, COALESCE\(bt\.tier_level, 0\)::integer AS tier_level`).
		WithArgs("tenant-1").
		WillReturnRows(sqlmock.NewRows([]string{"id", "tier_level", "tier_name"}).AddRow(subID, int32(0), "payg"))

	resp, err := s.EnsureFreeAccount(context.Background(), &purserpb.InitializePostpaidAccountRequest{TenantId: "tenant-1"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.GetSubscriptionId() != subID || resp.GetTierLevel() != 0 {
		t.Fatalf("response did not report canonical winner: %+v", resp)
	}
}

func TestInitializeAccountEmptyTenantGuards(t *testing.T) {
	s := newGuardServer(t)
	if _, err := s.InitializePrepaidAccount(context.Background(), &purserpb.InitializePrepaidAccountRequest{}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("prepaid: err = %v, want InvalidArgument", err)
	}
	if _, err := s.InitializePostpaidAccount(context.Background(), &purserpb.InitializePostpaidAccountRequest{}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("postpaid: err = %v, want InvalidArgument", err)
	}
}

// UpdateSubscription refuses tier_id changes (those go through ChangeBillingTier)
// and only accepts an idempotent re-state of the current tier.
func TestUpdateSubscriptionTierGuards(t *testing.T) {
	t.Run("mismatch rejected", func(t *testing.T) {
		s, mock := newReadServer(t, true)
		mock.ExpectQuery(`SELECT tier_id::text AS tier_id\s+FROM purser\.tenant_subscriptions\s+WHERE tenant_id = \$1::text::uuid`).
			WithArgs("tenant-1").
			WillReturnRows(sqlmock.NewRows([]string{"tier_id"}).AddRow("92000000-0000-4000-8000-000000000001"))
		other := "92000000-0000-4000-8000-000000000002"
		_, err := s.UpdateSubscription(context.Background(), &purserpb.UpdateSubscriptionRequest{TenantId: "tenant-1", TierId: &other})
		if status.Code(err) != codes.FailedPrecondition {
			t.Fatalf("err = %v, want FailedPrecondition", err)
		}
	})
	t.Run("subscription not found", func(t *testing.T) {
		s, mock := newReadServer(t, true)
		mock.ExpectQuery(`SELECT tier_id::text AS tier_id\s+FROM purser\.tenant_subscriptions`).
			WithArgs("tenant-x").
			WillReturnError(sqlmockNoRows())
		tid := "tier-any"
		_, err := s.UpdateSubscription(context.Background(), &purserpb.UpdateSubscriptionRequest{TenantId: "tenant-x", TierId: &tid})
		if status.Code(err) != codes.NotFound {
			t.Fatalf("err = %v, want NotFound", err)
		}
	})
	t.Run("empty tenant", func(t *testing.T) {
		s := newGuardServer(t)
		_, err := s.UpdateSubscription(context.Background(), &purserpb.UpdateSubscriptionRequest{})
		if status.Code(err) != codes.InvalidArgument {
			t.Fatalf("err = %v, want InvalidArgument", err)
		}
	})
}

package grpc

import (
	"context"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
)

// CancelSubscription flips status to cancelled and enqueues a
// subscription_canceled outbox event inside the same tx — the cancel and its
// downstream notification must commit atomically.
func TestCancelSubscriptionHappyPathEnqueuesOutbox(t *testing.T) {
	s, mock := newReadServer(t, true)

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT id FROM purser\.tenant_subscriptions WHERE tenant_id = \$1 AND status != 'cancelled'`).
		WithArgs("tenant-1").
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("sub-1"))
	mock.ExpectExec(`UPDATE purser\.tenant_subscriptions`).
		WithArgs("tenant-1").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`INSERT INTO purser\.billing_event_outbox`).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("evt-1"))
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

	mock.ExpectQuery(`SELECT billing_model FROM purser\.tenant_subscriptions WHERE tenant_id = \$1`).
		WithArgs("tenant-1").
		WillReturnRows(sqlmock.NewRows([]string{"billing_model"}).AddRow("prepaid"))
	mock.ExpectQuery(`SELECT id, tier_level FROM purser\.billing_tiers\s+WHERE is_default_postpaid = true`).
		WillReturnRows(sqlmock.NewRows([]string{"id", "tier_level"}).AddRow("tier-paid", int32(2)))
	mock.ExpectQuery(`SELECT COALESCE\(balance_cents, 0\) FROM purser\.prepaid_balances WHERE tenant_id = \$1`).
		WithArgs("tenant-1").
		WillReturnRows(sqlmock.NewRows([]string{"balance_cents"}).AddRow(int64(1500)))
	mock.ExpectBegin()
	mock.ExpectQuery(`UPDATE purser\.tenant_subscriptions\s+SET billing_model = 'postpaid'`).
		WithArgs("tier-paid", "tenant-1").
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("sub-1"))
	mock.ExpectCommit()

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
			mock.ExpectQuery(`SELECT billing_model FROM purser\.tenant_subscriptions`).
				WithArgs("tenant-1").
				WillReturnRows(sqlmock.NewRows([]string{"billing_model"}).AddRow("prepaid"))
			mock.ExpectQuery(`SELECT id, tier_level, is_default_prepaid, is_active\s+FROM purser\.billing_tiers\s+WHERE id = \$1`).
				WithArgs("tier-req").
				WillReturnRows(sqlmock.NewRows([]string{"id", "tier_level", "is_default_prepaid", "is_active"}).
					AddRow("tier-req", tc.tierLevel, tc.isPrepaid, tc.isActive))

			tierID := "tier-req"
			_, err := s.PromoteToPaid(context.Background(), &purserpb.PromoteToPaidRequest{TenantId: "tenant-1", TierId: tierID})
			if status.Code(err) != tc.wantCode {
				t.Fatalf("err = %v, want %v", err, tc.wantCode)
			}
		})
	}
}

func TestPromoteToPaidNotFoundAndAlreadyPostpaid(t *testing.T) {
	t.Run("no subscription", func(t *testing.T) {
		s, mock := newReadServer(t, true)
		mock.ExpectQuery(`SELECT billing_model FROM purser\.tenant_subscriptions`).
			WithArgs("tenant-x").
			WillReturnError(sqlmockNoRows())
		_, err := s.PromoteToPaid(context.Background(), &purserpb.PromoteToPaidRequest{TenantId: "tenant-x"})
		if status.Code(err) != codes.NotFound {
			t.Fatalf("err = %v, want NotFound", err)
		}
	})
	t.Run("already postpaid", func(t *testing.T) {
		s, mock := newReadServer(t, true)
		mock.ExpectQuery(`SELECT billing_model FROM purser\.tenant_subscriptions`).
			WithArgs("tenant-1").
			WillReturnRows(sqlmock.NewRows([]string{"billing_model"}).AddRow("postpaid"))
		_, err := s.PromoteToPaid(context.Background(), &purserpb.PromoteToPaidRequest{TenantId: "tenant-1"})
		if status.Code(err) != codes.FailedPrecondition {
			t.Fatalf("err = %v, want FailedPrecondition", err)
		}
	})
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
		WillReturnRows(sqlmock.NewRows([]string{"billing_email", "billing_company", "tax_id", "billing_address", "updated_at"}).
			AddRow("new@example.com", nil, nil, nil, now))

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
		WillReturnRows(sqlmock.NewRows([]string{"billing_email", "billing_company", "tax_id", "billing_address", "updated_at"}).
			AddRow("a@b.com", nil, nil, nil, now))

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

	mock.ExpectBegin()
	mock.ExpectQuery(`FROM purser\.billing_tiers\s+WHERE is_default_prepaid = true`).
		WillReturnRows(sqlmock.NewRows([]string{"id", "tier_level"}).AddRow("tier-pp", int32(1)))
	mock.ExpectExec(`INSERT INTO purser\.tenant_subscriptions`).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`INSERT INTO purser\.prepaid_balances`).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	mock.ExpectQuery(`SELECT id FROM purser\.tenant_subscriptions WHERE tenant_id = \$1`).
		WithArgs("tenant-1").
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("sub-1"))
	mock.ExpectQuery(`SELECT id FROM purser\.prepaid_balances WHERE tenant_id = \$1 AND currency = \$2`).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("bal-1"))

	resp, err := s.InitializePrepaidAccount(context.Background(), &purserpb.InitializePrepaidAccountRequest{TenantId: "tenant-1"})
	if err != nil {
		t.Fatalf("InitializePrepaidAccount: %v", err)
	}
	if resp.SubscriptionId != "sub-1" || resp.BalanceId != "bal-1" || resp.TierLevel != 1 {
		t.Fatalf("response mapping wrong: %+v", resp)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet: %v", err)
	}
}

// No configured default prepaid tier is a precondition failure, not a silent
// account with a missing tier.
func TestInitializePrepaidAccountNoDefaultTier(t *testing.T) {
	s, mock := newReadServer(t, true)
	mock.ExpectBegin()
	mock.ExpectQuery(`FROM purser\.billing_tiers\s+WHERE is_default_prepaid = true`).
		WillReturnError(sqlmockNoRows())
	mock.ExpectRollback()

	_, err := s.InitializePrepaidAccount(context.Background(), &purserpb.InitializePrepaidAccountRequest{TenantId: "tenant-1"})
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("err = %v, want FailedPrecondition", err)
	}
}

func TestInitializePostpaidAccountHappyPath(t *testing.T) {
	s, mock := newReadServer(t, true)
	mock.ExpectBegin()
	mock.ExpectQuery(`FROM purser\.billing_tiers\s+WHERE is_default_postpaid = true`).
		WillReturnRows(sqlmock.NewRows([]string{"id", "tier_level"}).AddRow("tier-post", int32(2)))
	mock.ExpectExec(`INSERT INTO purser\.tenant_subscriptions`).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	mock.ExpectQuery(`SELECT id FROM purser\.tenant_subscriptions WHERE tenant_id = \$1`).
		WithArgs("tenant-1").
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("sub-2"))

	resp, err := s.InitializePostpaidAccount(context.Background(), &purserpb.InitializePostpaidAccountRequest{TenantId: "tenant-1"})
	if err != nil {
		t.Fatalf("InitializePostpaidAccount: %v", err)
	}
	if resp.SubscriptionId != "sub-2" || resp.TierLevel != 2 {
		t.Fatalf("response mapping wrong: %+v", resp)
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
		mock.ExpectQuery(`SELECT tier_id FROM purser\.tenant_subscriptions WHERE tenant_id = \$1`).
			WithArgs("tenant-1").
			WillReturnRows(sqlmock.NewRows([]string{"tier_id"}).AddRow("tier-current"))
		other := "tier-other"
		_, err := s.UpdateSubscription(context.Background(), &purserpb.UpdateSubscriptionRequest{TenantId: "tenant-1", TierId: &other})
		if status.Code(err) != codes.FailedPrecondition {
			t.Fatalf("err = %v, want FailedPrecondition", err)
		}
	})
	t.Run("subscription not found", func(t *testing.T) {
		s, mock := newReadServer(t, true)
		mock.ExpectQuery(`SELECT tier_id FROM purser\.tenant_subscriptions`).
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

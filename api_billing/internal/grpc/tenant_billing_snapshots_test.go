package grpc

import (
	"context"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Tenant- or user-scoped callers must be rejected: the snapshot is
// cross-tenant by design and only service credentials may read it.
func TestListTenantBillingSnapshotsRejectsScopedCallers(t *testing.T) {
	s := newGuardServer(t)

	ctx := context.WithValue(context.Background(), ctxkeys.KeyTenantID, "tenant-1")
	_, err := s.ListTenantBillingSnapshots(ctx, &purserpb.ListTenantBillingSnapshotsRequest{})
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("expected PermissionDenied for tenant-scoped caller, got %v", err)
	}

	ctx = context.WithValue(context.Background(), ctxkeys.KeyUserID, "user-1")
	_, err = s.ListTenantBillingSnapshots(ctx, &purserpb.ListTenantBillingSnapshotsRequest{})
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("expected PermissionDenied for user-scoped caller, got %v", err)
	}
}

func TestListTenantBillingSnapshotsMapsRows(t *testing.T) {
	s, mock := newReadServer(t, true)
	now := time.Now().UTC().Truncate(time.Second)
	firstTenantID := "49a6870d-6952-4978-b38e-fb2a2012a782"
	secondTenantID := "d1560196-ec40-4191-b0c0-228533bfe292"
	firstTierID := "1f8cdaf4-e9eb-4ee0-a1f1-30b63b66ed4a"
	secondTierID := "e4d40201-19cb-4107-9c78-9627778f1e1e"

	rows := sqlmock.NewRows([]string{
		"tenant_id", "billing_model", "status", "tier_id", "tier_name",
		"trial_ends_at", "next_billing_date", "created_at",
		"prepaid_balance_cents", "outstanding_amount", "overdue_invoices",
	}).
		AddRow(firstTenantID, "postpaid", "active", firstTierID, "pro",
			nil, now, now.Add(-30*24*time.Hour),
			int64(0), 49.99, int32(1)).
		AddRow(secondTenantID, "prepaid", "active", secondTierID, "free",
			now.Add(7*24*time.Hour), nil, now,
			int64(1250), 0.0, int32(0))

	mock.ExpectQuery("FROM purser.tenant_subscriptions ts").WillReturnRows(rows)

	resp, err := s.ListTenantBillingSnapshots(serviceTestContext(), &purserpb.ListTenantBillingSnapshotsRequest{})
	if err != nil {
		t.Fatalf("ListTenantBillingSnapshots: %v", err)
	}
	if len(resp.Snapshots) != 2 {
		t.Fatalf("expected 2 snapshots, got %d", len(resp.Snapshots))
	}

	a := resp.Snapshots[0]
	if a.TenantId != firstTenantID || a.BillingModel != "postpaid" || a.TierName != "pro" {
		t.Fatalf("snapshot a mismatch: %+v", a)
	}
	if a.OutstandingAmount != 49.99 || a.OverdueInvoices != 1 {
		t.Fatalf("snapshot a invoice rollup mismatch: %+v", a)
	}
	if a.TrialEndsAt != nil {
		t.Fatalf("snapshot a should have no trial end: %+v", a)
	}
	if a.NextBillingDate == nil || !a.NextBillingDate.AsTime().Equal(now) {
		t.Fatalf("snapshot a next billing date mismatch: %+v", a)
	}
	if a.Currency == "" {
		t.Fatalf("currency must be stamped")
	}

	b := resp.Snapshots[1]
	if b.PrepaidBalanceCents != 1250 {
		t.Fatalf("snapshot b balance mismatch: %+v", b)
	}
	// NULL outstanding (no invoices) maps to zero, not an error.
	if b.OutstandingAmount != 0 || b.OverdueInvoices != 0 {
		t.Fatalf("snapshot b should have zero outstanding: %+v", b)
	}
	if b.TrialEndsAt == nil {
		t.Fatalf("snapshot b should carry trial end: %+v", b)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestListTenantBillingSnapshotsPassesFilterAndLimit(t *testing.T) {
	s, mock := newReadServer(t, true)

	mock.ExpectQuery("FROM purser.tenant_subscriptions ts").
		WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg(), int32(25)).
		WillReturnRows(sqlmock.NewRows([]string{
			"tenant_id", "billing_model", "status", "tier_id", "tier_name",
			"trial_ends_at", "next_billing_date", "created_at",
			"prepaid_balance_cents", "outstanding_amount", "overdue_invoices",
		}))

	_, err := s.ListTenantBillingSnapshots(serviceTestContext(), &purserpb.ListTenantBillingSnapshotsRequest{
		TenantIds: []string{"tenant-a"},
		Limit:     25,
	})
	if err != nil {
		t.Fatalf("ListTenantBillingSnapshots: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

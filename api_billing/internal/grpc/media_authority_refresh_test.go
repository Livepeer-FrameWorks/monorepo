package grpc

import (
	"context"
	"errors"
	"testing"
	"time"

	"frameworks/api_billing/internal/database/purserdb"
	"github.com/DATA-DOG/go-sqlmock"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	"github.com/google/uuid"
)

type failingRefreshTierReconciler struct{}

func (failingRefreshTierReconciler) OfficialClusterIDs(context.Context) (map[string]bool, error) {
	return nil, nil
}

func (failingRefreshTierReconciler) Reconcile(context.Context, string, int32, string) ([]string, string, error) {
	return nil, "", errors.New("quartermaster unavailable")
}

func (failingRefreshTierReconciler) RevokeDNSEntitlements(context.Context, string) error {
	return nil
}

type recordingRefreshClient struct{ calls int }

func (c *recordingRefreshClient) RequestMediaAuthorityRefresh(context.Context, string, string, string, string) (*commodorepb.RequestMediaAuthorityRefreshResponse, error) {
	c.calls++
	return &commodorepb.RequestMediaAuthorityRefreshResponse{Accepted: true}, nil
}

type blockingPurserRefreshClient struct {
	arrived chan<- struct{}
	release <-chan struct{}
}

func (c blockingPurserRefreshClient) RequestMediaAuthorityRefresh(context.Context, string, string, string, string) (*commodorepb.RequestMediaAuthorityRefreshResponse, error) {
	c.arrived <- struct{}{}
	<-c.release
	return &commodorepb.RequestMediaAuthorityRefreshResponse{Accepted: true}, nil
}

func TestPurserMediaAuthorityRefreshBatchDeliversConcurrently(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.MatchExpectationsInOrder(false)
	id1 := "10000000-0000-0000-0000-000000000001"
	id2 := "10000000-0000-0000-0000-000000000002"
	mock.ExpectQuery(`(?s)WITH candidates AS.*UPDATE purser\.media_authority_refresh_outbox`).
		WithArgs(mediaAuthorityRefreshLease.Milliseconds(), mediaAuthorityRefreshBatchSize).
		WillReturnRows(sqlmock.NewRows([]string{"id", "source_event_id", "tenant_id", "reason", "attempts", "revision"}).
			AddRow(id1, "event-1", "tenant-1", "billing_changed", 1, 1).
			AddRow(id2, "event-2", "tenant-2", "billing_changed", 1, 1))
	mock.ExpectExec(`UPDATE purser\.media_authority_refresh_outbox`).WithArgs(uuid.MustParse(id1), int64(1)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`UPDATE purser\.media_authority_refresh_outbox`).WithArgs(uuid.MustParse(id2), int64(1)).WillReturnResult(sqlmock.NewResult(0, 1))

	arrived := make(chan struct{}, 2)
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- (&PurserServer{db: db}).deliverMediaAuthorityRefreshBatch(context.Background(), blockingPurserRefreshClient{arrived: arrived, release: release})
	}()
	for range 2 {
		select {
		case <-arrived:
		case <-time.After(time.Second):
			t.Fatal("refresh rows were not delivered concurrently")
		}
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestPurserMediaAuthorityRefreshRetainsRowWhenEntitlementReconcileFails(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	id := uuid.MustParse("10000000-0000-0000-0000-000000000003")
	mock.ExpectQuery(`SELECT ts\.tier_id::text AS tier_id, COALESCE\(bt\.tier_level, 0\)::integer AS tier_level, bt\.tier_name`).
		WithArgs("tenant-1").
		WillReturnRows(sqlmock.NewRows([]string{"tier_id", "tier_level", "tier_name"}).AddRow("tier-paid", int32(2), "supporter"))
	mock.ExpectExec(`UPDATE purser\.media_authority_refresh_outbox`).
		WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg(), id, int64(4)).
		WillReturnResult(sqlmock.NewResult(0, 1))

	client := &recordingRefreshClient{}
	s := &PurserServer{db: db, tierReconciler: failingRefreshTierReconciler{}}
	err = s.deliverMediaAuthorityRefreshRow(context.Background(), client, purserdb.ClaimMediaAuthorityRefreshBatchRow{
		ID: id.String(), TenantID: "tenant-1", SourceEventID: "event-3", Reason: "subscription_authority_changed", Attempts: 2, Revision: 4,
	})
	if err != nil {
		t.Fatal(err)
	}
	if client.calls != 0 {
		t.Fatalf("Commodore refresh calls = %d, want 0 before entitlement convergence", client.calls)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestUsageRefreshDoesNotDependOnQuartermasterEntitlementReconcile(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	id := uuid.MustParse("10000000-0000-0000-0000-000000000004")
	mock.ExpectExec(`UPDATE purser\.media_authority_refresh_outbox`).
		WithArgs(id, int64(1)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	client := &recordingRefreshClient{}
	s := &PurserServer{db: db, tierReconciler: failingRefreshTierReconciler{}}
	if err := s.deliverMediaAuthorityRefreshRow(context.Background(), client, purserdb.ClaimMediaAuthorityRefreshBatchRow{
		ID: id.String(), TenantID: "tenant-1", SourceEventID: "event-4", Reason: "allowance_usage_changed", Attempts: 1, Revision: 1,
	}); err != nil {
		t.Fatal(err)
	}
	if client.calls != 1 {
		t.Fatalf("Commodore refresh calls = %d, want 1 without Quartermaster dependency", client.calls)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestMediaAuthorityRefreshReleasesSupersededCompletionFence(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	id := uuid.MustParse("10000000-0000-0000-0000-000000000005")
	mock.ExpectExec(`UPDATE purser\.media_authority_refresh_outbox`).WithArgs(id, int64(2)).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`UPDATE purser\.media_authority_refresh_outbox`).WithArgs(id, int64(2)).WillReturnResult(sqlmock.NewResult(0, 1))
	client := &recordingRefreshClient{}
	if err := (&PurserServer{db: db}).deliverMediaAuthorityRefreshRow(context.Background(), client, purserdb.ClaimMediaAuthorityRefreshBatchRow{
		ID: id.String(), TenantID: "tenant-1", SourceEventID: "event-5", Reason: "allowance_usage_changed", Attempts: 1, Revision: 2,
	}); err != nil {
		t.Fatal(err)
	}
	if client.calls != 1 {
		t.Fatalf("Commodore refresh calls = %d, want 1", client.calls)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestMediaAuthorityRefreshEntitlementDependencyReasons(t *testing.T) {
	for _, reason := range []string{"subscription_authority_changed", "subscription_entitlement_changed", "tier_entitlement_changed", "billing_tier_authority_changed"} {
		if !mediaAuthorityRefreshRequiresEntitlementReconcile(reason) {
			t.Fatalf("%q must reconcile entitlements", reason)
		}
	}
	for _, reason := range []string{"allowance_usage_changed", "prepaid_admission_gate_changed", "tier_allowance_changed"} {
		if mediaAuthorityRefreshRequiresEntitlementReconcile(reason) {
			t.Fatalf("%q must not depend on entitlement reconciliation", reason)
		}
	}
}

package handlers

import (
	"context"
	"database/sql"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	clusterpeerpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/cluster_peer"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
)

type revocationQuartermasterStub struct {
	revoked *quartermasterpb.RevokeMaterializedClusterAccessRequest
}

func (*revocationQuartermasterStub) GetCluster(context.Context, string) (*quartermasterpb.ClusterResponse, error) {
	return nil, nil
}

func (*revocationQuartermasterStub) GetTenant(context.Context, string) (*quartermasterpb.GetTenantResponse, error) {
	return nil, nil
}

func (*revocationQuartermasterStub) MaterializeClusterAccess(context.Context, *quartermasterpb.MaterializeClusterAccessRequest) error {
	return nil
}

func (s *revocationQuartermasterStub) RevokeMaterializedClusterAccess(_ context.Context, req *quartermasterpb.RevokeMaterializedClusterAccessRequest) error {
	s.revoked = req
	return nil
}

func TestCancelledStripeClusterSubscriptionRevokesMarketplaceAuthority(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()
	qm := &revocationQuartermasterStub{}
	service := &Service{db: db, logger: logging.NewLogger(), qmClient: qm}

	mock.ExpectExec("UPDATE purser.cluster_subscriptions").
		WithArgs(sql.NullString{String: "canceled", Valid: true}, "cancelled", sql.NullTime{}, sql.NullString{String: "sub-1", Valid: true}).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery("SELECT cluster_id, tenant_id::text").
		WithArgs(sql.NullString{String: "sub-1", Valid: true}).
		WillReturnRows(sqlmock.NewRows([]string{"cluster_id", "tenant_id"}).AddRow("market-1", "tenant-1"))

	obj := StripeSubscriptionObject{ID: "sub-1", Status: "canceled"}
	if err := service.updateClusterSubscriptionFromStripe(obj, "cancelled", nil); err != nil {
		t.Fatalf("updateClusterSubscriptionFromStripe: %v", err)
	}
	if qm.revoked == nil {
		t.Fatal("cancelled Stripe subscription did not revoke cluster access")
	}
	if qm.revoked.GetTenantId() != "tenant-1" || qm.revoked.GetClusterId() != "market-1" {
		t.Fatalf("revocation target = %+v", qm.revoked)
	}
	if qm.revoked.GetAccessSource() != clusterpeerpb.TenantClusterAccessSource_TENANT_CLUSTER_ACCESS_SOURCE_MARKETPLACE_SUBSCRIPTION {
		t.Fatalf("revocation source = %v", qm.revoked.GetAccessSource())
	}
	if qm.revoked.GetAuthorizationReference() != "stripe:sub-1" {
		t.Fatalf("revocation reference = %q", qm.revoked.GetAuthorizationReference())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

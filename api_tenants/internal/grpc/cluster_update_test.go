package grpc

import (
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

func TestUpdateClusterInfrastructureFieldsRequireOperator(t *testing.T) {
	for _, req := range []*quartermasterpb.UpdateClusterRequest{
		{DatabaseUrl: proto.String("postgres://foreign.example/db")},
		{PeriscopeUrl: proto.String("https://foreign.example")},
		{KafkaBrokers: []string{"foreign.example:9092"}},
	} {
		server, _, mock := newMockQuartermasterServer(t)
		mock.ExpectQuery(`SELECT owner_tenant_id[\s\S]*WHERE cluster_id = \$1`).
			WithArgs("edge-1").WillReturnRows(sqlmock.NewRows([]string{"owner_tenant_id"}).AddRow("tenant-1"))
		req.ClusterId = "edge-1"
		_, err := server.UpdateCluster(tenantCtx("tenant-1", "owner"), req)
		if status.Code(err) != codes.PermissionDenied {
			t.Errorf("owner infrastructure update %v: status %v, want PermissionDenied", req, status.Code(err))
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestUpdateClusterRejectsNegativeLimits(t *testing.T) {
	for _, req := range []*quartermasterpb.UpdateClusterRequest{
		{MaxConcurrentStreams: proto.Int32(-1)},
		{MaxConcurrentViewers: proto.Int32(-1)},
	} {
		server, _, mock := newMockQuartermasterServer(t)
		mock.ExpectQuery(`SELECT owner_tenant_id[\s\S]*WHERE cluster_id = \$1`).
			WithArgs("edge-1").WillReturnRows(sqlmock.NewRows([]string{"owner_tenant_id"}).AddRow("tenant-1"))
		req.ClusterId = "edge-1"
		_, err := server.UpdateCluster(tenantCtx("tenant-1", "owner"), req)
		if status.Code(err) != codes.InvalidArgument || status.Convert(err).Message() == "no fields to update" {
			t.Errorf("negative limit %v: want specific InvalidArgument, got %v", req, err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	}
}

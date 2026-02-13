package grpc

import (
	"context"
	"database/sql"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/sirupsen/logrus"
	"google.golang.org/grpc/codes"

	pb "frameworks/pkg/proto"
)

func TestGetNodeOwner_FormatsIPv6FoghornAddress(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer db.Close()

	mock.ExpectQuery("FROM quartermaster.infrastructure_nodes").
		WithArgs("node-v6").
		WillReturnRows(sqlmock.NewRows([]string{"node_id", "cluster_id", "cluster_name", "owner_tenant_id", "name", "advertise_host", "port"}).
			AddRow("node-v6", "cluster-1", "Cluster One", "tenant-1", "Tenant One", "2001:db8::20", int32(50051)))

	server := &QuartermasterServer{db: db, logger: logrus.New()}
	resp, err := server.GetNodeOwner(context.Background(), &pb.GetNodeOwnerRequest{NodeId: "node-v6"})
	if err != nil {
		t.Fatalf("GetNodeOwner returned error: %v", err)
	}

	if resp.GetFoghornGrpcAddr() != "[2001:db8::20]:50051" {
		t.Fatalf("unexpected foghorn addr: %s", resp.GetFoghornGrpcAddr())
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestGetNodeOwner_NotFound(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer db.Close()

	mock.ExpectQuery("FROM quartermaster.infrastructure_nodes").
		WithArgs("node-missing").
		WillReturnError(sql.ErrNoRows)

	server := &QuartermasterServer{db: db, logger: logrus.New()}
	_, err = server.GetNodeOwner(context.Background(), &pb.GetNodeOwnerRequest{NodeId: "node-missing"})
	assertGRPCCode(t, err, codes.NotFound)

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

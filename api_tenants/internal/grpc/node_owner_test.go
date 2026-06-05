package grpc

import (
	"context"
	"database/sql"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	"github.com/sirupsen/logrus"
	"google.golang.org/grpc/codes"
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
	resp, err := server.GetNodeOwner(context.Background(), &quartermasterpb.GetNodeOwnerRequest{NodeId: "node-v6"})
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

func TestGetNodeOwnerReturnsFoghornControlListener(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer db.Close()

	controlListenerFilter := `(?s)\(si\.metadata->>'foghorn_listener' = 'internal_control' OR si\.port = 18019 OR si\.metadata->>'foghorn_listener' = 'control'\).*FROM quartermaster\.infrastructure_nodes`
	mock.ExpectQuery(controlListenerFilter).
		WithArgs("edge-eu-1").
		WillReturnRows(sqlmock.NewRows([]string{"node_id", "cluster_id", "cluster_name", "owner_tenant_id", "name", "advertise_host", "port"}).
			AddRow("edge-eu-1", "media-eu-1", "Media EU", "tenant-1", "Tenant One", "10.88.158.227", int32(18019)))

	server := &QuartermasterServer{db: db, logger: logrus.New()}
	resp, err := server.GetNodeOwner(context.Background(), &quartermasterpb.GetNodeOwnerRequest{NodeId: "edge-eu-1"})
	if err != nil {
		t.Fatalf("GetNodeOwner returned error: %v", err)
	}
	if resp.GetFoghornGrpcAddr() != "10.88.158.227:18019" {
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
	_, err = server.GetNodeOwner(context.Background(), &quartermasterpb.GetNodeOwnerRequest{NodeId: "node-missing"})
	assertGRPCCode(t, err, codes.NotFound)

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

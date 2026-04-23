package grpc

import (
	"context"
	"database/sql/driver"
	"testing"
	"time"

	"frameworks/pkg/logging"
	pb "frameworks/pkg/proto"

	"github.com/DATA-DOG/go-sqlmock"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestCreateNode_Success(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()

	server := NewQuartermasterServer(db, logging.NewLogger(), nil, nil, nil, nil, nil)

	// Cluster existence check
	mock.ExpectQuery(`SELECT EXISTS`).
		WithArgs("cluster-1").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))

	// INSERT ... ON CONFLICT (upsert)
	mock.ExpectExec(`INSERT INTO quartermaster\.infrastructure_nodes`).
		WithArgs(
			"node-1", "cluster-1", "my-node", "core",
			nil, nil, nil, nil, nil, // internal_ip, external_ip, wireguard_ip, wireguard_public_key, wireguard_listen_port
			nil, nil, // region, availability_zone
			nil, nil, nil, // cpu_cores, memory_gb, disk_gb
			sqlmock.AnyArg(), // now
		).
		WillReturnResult(sqlmock.NewResult(0, 1))

	// queryNode re-read
	now := time.Now()
	mock.ExpectQuery(`SELECT id, node_id, cluster_id, node_name, node_type`).
		WithArgs("node-1").
		WillReturnRows(sqlmock.NewRows(queryNodeColumns).AddRow([]driver.Value{
			"uuid-1", "node-1", "cluster-1", "my-node", "core",
			nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil,
			nil, now, now,
		}...))

	resp, err := server.CreateNode(context.Background(), &pb.CreateNodeRequest{
		NodeId:    "node-1",
		ClusterId: "cluster-1",
		NodeName:  "my-node",
		NodeType:  "core",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.GetNode().GetNodeId() != "node-1" {
		t.Fatalf("expected node_id=node-1, got %s", resp.GetNode().GetNodeId())
	}
	if resp.GetNode().GetClusterId() != "cluster-1" {
		t.Fatalf("expected cluster_id=cluster-1, got %s", resp.GetNode().GetClusterId())
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

func TestCreateNode_MissingNodeID(t *testing.T) {
	db, _, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()

	server := NewQuartermasterServer(db, logging.NewLogger(), nil, nil, nil, nil, nil)

	_, err = server.CreateNode(context.Background(), &pb.CreateNodeRequest{
		ClusterId: "cluster-1",
		NodeName:  "my-node",
		NodeType:  "core",
	})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v", err)
	}
}

func TestCreateNode_MissingClusterID(t *testing.T) {
	db, _, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()

	server := NewQuartermasterServer(db, logging.NewLogger(), nil, nil, nil, nil, nil)

	_, err = server.CreateNode(context.Background(), &pb.CreateNodeRequest{
		NodeId:   "node-1",
		NodeName: "my-node",
		NodeType: "core",
	})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v", err)
	}
}

func TestCreateNode_Idempotent(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()

	server := NewQuartermasterServer(db, logging.NewLogger(), nil, nil, nil, nil, nil)
	now := time.Now()
	extIP := "1.2.3.4"

	req := &pb.CreateNodeRequest{
		NodeId:     "node-1",
		ClusterId:  "cluster-1",
		NodeName:   "my-node",
		NodeType:   "core",
		ExternalIp: &extIP,
	}

	// Two identical calls should both succeed via ON CONFLICT DO UPDATE.
	for i := 0; i < 2; i++ {
		mock.ExpectQuery(`SELECT EXISTS`).
			WithArgs("cluster-1").
			WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))

		mock.ExpectExec(`INSERT INTO quartermaster\.infrastructure_nodes`).
			WithArgs(
				"node-1", "cluster-1", "my-node", "core",
				nil, &extIP, nil, nil, nil,
				nil, nil,
				nil, nil, nil,
				sqlmock.AnyArg(),
			).
			WillReturnResult(sqlmock.NewResult(0, 1))

		mock.ExpectQuery(`SELECT id, node_id, cluster_id, node_name, node_type`).
			WithArgs("node-1").
			WillReturnRows(sqlmock.NewRows(queryNodeColumns).AddRow([]driver.Value{
				"uuid-1", "node-1", "cluster-1", "my-node", "core",
				nil, "1.2.3.4", nil, nil, nil, nil, nil, nil, nil, nil, nil, nil,
				nil, now, now,
			}...))

		resp, callErr := server.CreateNode(context.Background(), req)
		if callErr != nil {
			t.Fatalf("call %d: unexpected error: %v", i+1, callErr)
		}
		if resp.GetNode().GetNodeId() != "node-1" {
			t.Fatalf("call %d: expected node_id=node-1, got %s", i+1, resp.GetNode().GetNodeId())
		}
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

func TestCreateNode_ClusterNotFound(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()

	server := NewQuartermasterServer(db, logging.NewLogger(), nil, nil, nil, nil, nil)

	mock.ExpectQuery(`SELECT EXISTS`).
		WithArgs("nonexistent-cluster").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))

	_, err = server.CreateNode(context.Background(), &pb.CreateNodeRequest{
		NodeId:    "node-1",
		ClusterId: "nonexistent-cluster",
		NodeName:  "my-node",
		NodeType:  "core",
	})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v", err)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

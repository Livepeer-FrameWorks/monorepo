package grpc

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"testing"
	"time"

	"frameworks/pkg/logging"
	pb "frameworks/pkg/proto"

	"github.com/DATA-DOG/go-sqlmock"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestGetNode_Success(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()

	server := NewQuartermasterServer(db, logging.NewLogger(), nil, nil, nil, nil, nil)

	now := time.Now()
	mock.ExpectQuery(`SELECT id, node_id, cluster_id, node_name, node_type`).
		WithArgs("node-1").
		WillReturnRows(sqlmock.NewRows(queryNodeColumns).AddRow([]driver.Value{
			"uuid-1", "node-1", "cluster-1", "my-node", "core",
			"10.0.0.1", "1.2.3.4", nil, nil, nil,
			"us-east-1", "us-east-1a",
			nil, nil,
			int32(4), int32(16), int32(100),
			nil, "gitops_seed", now, now,
		}...))

	resp, err := server.GetNode(context.Background(), &pb.GetNodeRequest{NodeId: "node-1"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	node := resp.GetNode()
	if node.GetNodeId() != "node-1" {
		t.Fatalf("expected node_id=node-1, got %s", node.GetNodeId())
	}
	if node.GetNodeType() != "core" {
		t.Fatalf("expected node_type=core, got %s", node.GetNodeType())
	}
	if node.GetRegion() != "us-east-1" {
		t.Fatalf("expected region=us-east-1, got %s", node.GetRegion())
	}
	if node.GetCpuCores() != 4 {
		t.Fatalf("expected cpu_cores=4, got %d", node.GetCpuCores())
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

func TestGetNode_MissingNodeID(t *testing.T) {
	db, _, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()

	server := NewQuartermasterServer(db, logging.NewLogger(), nil, nil, nil, nil, nil)

	_, err = server.GetNode(context.Background(), &pb.GetNodeRequest{})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v", err)
	}
}

func TestGetNode_NotFound(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()

	server := NewQuartermasterServer(db, logging.NewLogger(), nil, nil, nil, nil, nil)

	mock.ExpectQuery(`SELECT id, node_id`).
		WithArgs("nonexistent").
		WillReturnError(sql.ErrNoRows)

	_, err = server.GetNode(context.Background(), &pb.GetNodeRequest{NodeId: "nonexistent"})
	if status.Code(err) != codes.NotFound {
		t.Fatalf("expected NotFound, got %v", err)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

func TestUpdateNodeHardware_Success(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()

	server := NewQuartermasterServer(db, logging.NewLogger(), nil, nil, nil, nil, nil)

	cores := int32(8)
	mem := int32(32)
	disk := int32(500)

	mock.ExpectExec(`UPDATE quartermaster\.infrastructure_nodes`).
		WithArgs("node-1", &cores, &mem, &disk).
		WillReturnResult(sqlmock.NewResult(0, 1))

	_, err = server.UpdateNodeHardware(context.Background(), &pb.UpdateNodeHardwareRequest{
		NodeId:   "node-1",
		CpuCores: &cores,
		MemoryGb: &mem,
		DiskGb:   &disk,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

func TestUpdateNodeHardware_MissingNodeID(t *testing.T) {
	db, _, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()

	server := NewQuartermasterServer(db, logging.NewLogger(), nil, nil, nil, nil, nil)

	_, err = server.UpdateNodeHardware(context.Background(), &pb.UpdateNodeHardwareRequest{})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v", err)
	}
}

func TestUpdateNodeHardware_NodeNotFoundIsOK(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()

	server := NewQuartermasterServer(db, logging.NewLogger(), nil, nil, nil, nil, nil)

	mock.ExpectExec(`UPDATE quartermaster\.infrastructure_nodes`).
		WithArgs("unknown-node", nil, nil, nil).
		WillReturnResult(sqlmock.NewResult(0, 0)) // 0 rows affected

	_, err = server.UpdateNodeHardware(context.Background(), &pb.UpdateNodeHardwareRequest{
		NodeId: "unknown-node",
	})
	if err != nil {
		t.Fatalf("expected success even when node not found, got: %v", err)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

func TestGetNodeByLogicalName_Success(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()

	server := NewQuartermasterServer(db, logging.NewLogger(), nil, nil, nil, nil, nil)

	now := time.Now()
	mock.ExpectQuery(`SELECT id, node_id`).
		WithArgs("edge-node-1").
		WillReturnRows(sqlmock.NewRows(queryNodeColumns).AddRow([]driver.Value{
			"uuid-1", "edge-node-1", "cluster-1", "edge-1", "edge",
			nil, "5.6.7.8", nil, nil, nil, nil, nil, nil, nil, nil, nil, nil,
			nil, "gitops_seed", now, now,
		}...))

	resp, err := server.GetNodeByLogicalName(context.Background(), &pb.GetNodeByLogicalNameRequest{NodeId: "edge-node-1"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.GetNode().GetNodeId() != "edge-node-1" {
		t.Fatalf("expected node_id=edge-node-1, got %s", resp.GetNode().GetNodeId())
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

func TestGetNodeByLogicalName_MissingNodeID(t *testing.T) {
	db, _, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()

	server := NewQuartermasterServer(db, logging.NewLogger(), nil, nil, nil, nil, nil)

	_, err = server.GetNodeByLogicalName(context.Background(), &pb.GetNodeByLogicalNameRequest{})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v", err)
	}
}

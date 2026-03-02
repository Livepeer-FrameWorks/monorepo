package grpc

import (
	"context"
	"testing"
	"time"

	"frameworks/pkg/logging"
	pb "frameworks/pkg/proto"

	"github.com/DATA-DOG/go-sqlmock"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestDiscoverServices_MissingServiceType(t *testing.T) {
	db, _, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()

	server := NewQuartermasterServer(db, logging.NewLogger(), nil, nil, nil, nil, nil)

	_, err = server.DiscoverServices(context.Background(), &pb.ServiceDiscoveryRequest{})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v", err)
	}
}

func TestDiscoverServices_ReturnsInstances(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()

	server := NewQuartermasterServer(db, logging.NewLogger(), nil, nil, nil, nil, nil)

	now := time.Now()
	instanceCols := []string{
		"id", "instance_id", "service_id", "cluster_id", "node_id",
		"protocol", "advertise_host", "port", "health_endpoint_override", "status",
		"last_health_check", "created_at", "updated_at",
	}

	// Unauthenticated path: filters by default cluster
	mock.ExpectQuery(`SELECT si\.id, si\.instance_id`).
		WithArgs("bridge", int32(51)). // limit = default 25 + 1
		WillReturnRows(sqlmock.NewRows(instanceCols).
			AddRow("uuid-1", "inst-bridge-1", "bridge", "cluster-1", "node-1",
				"http", "10.0.0.1", int32(18000), nil, "running",
				now, now, now))

	resp, err := server.DiscoverServices(context.Background(), &pb.ServiceDiscoveryRequest{
		ServiceType: "bridge",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.GetInstances()) != 1 {
		t.Fatalf("expected 1 instance, got %d", len(resp.GetInstances()))
	}
	inst := resp.GetInstances()[0]
	if inst.GetInstanceId() != "inst-bridge-1" {
		t.Fatalf("expected instance_id=inst-bridge-1, got %s", inst.GetInstanceId())
	}
	if inst.GetServiceId() != "bridge" {
		t.Fatalf("expected service_id=bridge, got %s", inst.GetServiceId())
	}
	if inst.GetClusterId() != "cluster-1" {
		t.Fatalf("expected cluster_id=cluster-1, got %s", inst.GetClusterId())
	}
	if inst.GetPort() != 18000 {
		t.Fatalf("expected port=18000, got %d", inst.GetPort())
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

func TestDiscoverServices_EmptyResult(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()

	server := NewQuartermasterServer(db, logging.NewLogger(), nil, nil, nil, nil, nil)

	instanceCols := []string{
		"id", "instance_id", "service_id", "cluster_id", "node_id",
		"protocol", "advertise_host", "port", "health_endpoint_override", "status",
		"last_health_check", "created_at", "updated_at",
	}

	mock.ExpectQuery(`SELECT si\.id, si\.instance_id`).
		WithArgs("nonexistent-service", int32(51)).
		WillReturnRows(sqlmock.NewRows(instanceCols))

	resp, err := server.DiscoverServices(context.Background(), &pb.ServiceDiscoveryRequest{
		ServiceType: "nonexistent-service",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.GetInstances()) != 0 {
		t.Fatalf("expected 0 instances, got %d", len(resp.GetInstances()))
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

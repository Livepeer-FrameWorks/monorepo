package grpc

import (
	"context"
	"database/sql"
	"testing"

	"frameworks/pkg/logging"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestEnsureServiceExists_FindsExistingService(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()

	server := NewQuartermasterServer(db, logging.NewLogger(), nil, nil, nil, nil, nil)

	mock.ExpectBegin()
	mock.ExpectExec("SELECT pg_advisory_xact_lock").
		WithArgs("bridge").
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery("SELECT service_id FROM quartermaster.services").
		WithArgs("bridge").
		WillReturnRows(sqlmock.NewRows([]string{"service_id"}).AddRow("bridge"))
	mock.ExpectCommit()

	serviceID, err := server.ensureServiceExists(context.Background(), "bridge", "http")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if serviceID != "bridge" {
		t.Fatalf("expected service_id=bridge, got %s", serviceID)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

func TestEnsureServiceExists_CreatesNewService(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()

	server := NewQuartermasterServer(db, logging.NewLogger(), nil, nil, nil, nil, nil)

	mock.ExpectBegin()
	mock.ExpectExec("SELECT pg_advisory_xact_lock").
		WithArgs("new-service").
		WillReturnResult(sqlmock.NewResult(0, 0))
	// Service not found
	mock.ExpectQuery("SELECT service_id FROM quartermaster.services").
		WithArgs("new-service").
		WillReturnError(sql.ErrNoRows)
	// Creates service: service_id=serviceType, name=serviceType, type=serviceType
	mock.ExpectExec("INSERT INTO quartermaster.services").
		WithArgs("new-service", "new-service", "new-service", "grpc").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	serviceID, err := server.ensureServiceExists(context.Background(), "new-service", "grpc")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if serviceID != "new-service" {
		t.Fatalf("expected service_id=new-service, got %s", serviceID)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

func TestEnsureServiceExists_SetsTypeEqualToServiceType(t *testing.T) {
	// Verifies the contract: when creating a new service, service_id = name = type
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()

	server := NewQuartermasterServer(db, logging.NewLogger(), nil, nil, nil, nil, nil)

	mock.ExpectBegin()
	mock.ExpectExec("SELECT pg_advisory_xact_lock").
		WithArgs("periscope-query").
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery("SELECT service_id FROM quartermaster.services").
		WithArgs("periscope-query").
		WillReturnError(sql.ErrNoRows)
	// INSERT: $1=service_id, $2=name, $3=type, $4=protocol
	// All three should be "periscope-query" (the canonical type)
	mock.ExpectExec("INSERT INTO quartermaster.services").
		WithArgs("periscope-query", "periscope-query", "periscope-query", "http").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	serviceID, err := server.ensureServiceExists(context.Background(), "periscope-query", "http")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if serviceID != "periscope-query" {
		t.Fatalf("expected service_id=periscope-query, got %s", serviceID)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

package control

import (
	"context"
	"os"
	"sync"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestIsServedCluster_LazyDiscoversAssignmentFromDatabase(t *testing.T) {
	servedClusters = sync.Map{}

	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer mockDB.Close()

	prevDB := db
	db = mockDB
	t.Cleanup(func() { db = prevDB })

	prevInstanceID := os.Getenv("FOGHORN_INSTANCE_ID")
	if err := os.Setenv("FOGHORN_INSTANCE_ID", "foghorn-instance-1"); err != nil {
		t.Fatalf("Setenv: %v", err)
	}
	t.Cleanup(func() {
		if prevInstanceID == "" {
			_ = os.Unsetenv("FOGHORN_INSTANCE_ID")
		} else {
			_ = os.Setenv("FOGHORN_INSTANCE_ID", prevInstanceID)
		}
	})

	mock.ExpectQuery("SELECT EXISTS").
		WithArgs("foghorn-instance-1", "cluster-b").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))

	if !isServedCluster("cluster-b") {
		t.Fatalf("expected cluster-b to be lazily discovered as served")
	}

	if !isServedCluster("cluster-b") {
		t.Fatalf("expected cluster-b to be served from in-memory cache")
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestIsServedCluster_DoesNotDiscoverWithoutInstanceID(t *testing.T) {
	servedClusters = sync.Map{}

	prevDB := db
	db = nil
	t.Cleanup(func() { db = prevDB })

	prevInstanceID := os.Getenv("FOGHORN_INSTANCE_ID")
	_ = os.Unsetenv("FOGHORN_INSTANCE_ID")
	t.Cleanup(func() {
		if prevInstanceID == "" {
			_ = os.Unsetenv("FOGHORN_INSTANCE_ID")
		} else {
			_ = os.Setenv("FOGHORN_INSTANCE_ID", prevInstanceID)
		}
	})

	if isServedCluster("cluster-x") {
		t.Fatalf("expected cluster-x to be unresolved when instance id is missing")
	}
}

func TestDiscoverServedCluster_DBErrorReturnsFalse(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer mockDB.Close()

	prevDB := db
	db = mockDB
	t.Cleanup(func() { db = prevDB })

	prevInstanceID := os.Getenv("FOGHORN_INSTANCE_ID")
	_ = os.Setenv("FOGHORN_INSTANCE_ID", "foghorn-instance-1")
	t.Cleanup(func() {
		if prevInstanceID == "" {
			_ = os.Unsetenv("FOGHORN_INSTANCE_ID")
		} else {
			_ = os.Setenv("FOGHORN_INSTANCE_ID", prevInstanceID)
		}
	})

	mock.ExpectQuery("SELECT EXISTS").
		WithArgs("foghorn-instance-1", "cluster-z").
		WillReturnError(context.DeadlineExceeded)

	if discoverServedCluster("cluster-z") {
		t.Fatalf("expected discovery to fail on db error")
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

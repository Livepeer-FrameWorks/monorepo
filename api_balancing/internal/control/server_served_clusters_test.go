package control

import (
	"context"
	"os"
	"sync"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

// resetServedClusters swaps in a fresh empty sync.Map and restores original on cleanup.
func resetServedClusters(t *testing.T) {
	t.Helper()
	prev := servedClusters.Load()
	servedClusters.Store(&sync.Map{})
	t.Cleanup(func() { servedClusters.Store(prev) })
}

func setInstanceID(t *testing.T, id string) {
	t.Helper()
	prev := os.Getenv("FOGHORN_INSTANCE_ID")
	if id == "" {
		_ = os.Unsetenv("FOGHORN_INSTANCE_ID")
	} else {
		if err := os.Setenv("FOGHORN_INSTANCE_ID", id); err != nil {
			t.Fatalf("Setenv: %v", err)
		}
	}
	t.Cleanup(func() {
		if prev == "" {
			_ = os.Unsetenv("FOGHORN_INSTANCE_ID")
		} else {
			_ = os.Setenv("FOGHORN_INSTANCE_ID", prev)
		}
	})
}

func TestLoadServedClusters_PopulatesFromDB(t *testing.T) {
	resetServedClusters(t)

	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer mockDB.Close()

	prevDB := db
	db = mockDB
	t.Cleanup(func() { db = prevDB })
	setInstanceID(t, "foghorn-instance-1")

	mock.ExpectQuery("SELECT fca.cluster_id").
		WithArgs("foghorn-instance-1").
		WillReturnRows(sqlmock.NewRows([]string{"cluster_id"}).
			AddRow("cluster-a").
			AddRow("cluster-b"))

	LoadServedClusters()

	if !isServedCluster("cluster-a") {
		t.Fatalf("expected cluster-a to be served")
	}
	if !isServedCluster("cluster-b") {
		t.Fatalf("expected cluster-b to be served")
	}
	if isServedCluster("cluster-c") {
		t.Fatalf("expected cluster-c to NOT be served")
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestLoadServedClusters_SwapsOutStaleEntries(t *testing.T) {
	resetServedClusters(t)

	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer mockDB.Close()

	prevDB := db
	db = mockDB
	t.Cleanup(func() { db = prevDB })
	setInstanceID(t, "foghorn-instance-1")

	// First load: cluster-a + cluster-b
	mock.ExpectQuery("SELECT fca.cluster_id").
		WithArgs("foghorn-instance-1").
		WillReturnRows(sqlmock.NewRows([]string{"cluster_id"}).
			AddRow("cluster-a").
			AddRow("cluster-b"))

	LoadServedClusters()

	if !isServedCluster("cluster-a") || !isServedCluster("cluster-b") {
		t.Fatalf("expected both clusters after first load")
	}

	// Second load: only cluster-b (cluster-a de-assigned)
	mock.ExpectQuery("SELECT fca.cluster_id").
		WithArgs("foghorn-instance-1").
		WillReturnRows(sqlmock.NewRows([]string{"cluster_id"}).
			AddRow("cluster-b"))

	LoadServedClusters()

	if isServedCluster("cluster-a") {
		t.Fatalf("expected cluster-a to be removed after refresh")
	}
	if !isServedCluster("cluster-b") {
		t.Fatalf("expected cluster-b to survive refresh")
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestLoadServedClusters_PreservesLocalClusterID(t *testing.T) {
	resetServedClusters(t)

	prevLocal := localClusterID
	localClusterID = "local-primary"
	t.Cleanup(func() { localClusterID = prevLocal })

	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer mockDB.Close()

	prevDB := db
	db = mockDB
	t.Cleanup(func() { db = prevDB })
	setInstanceID(t, "foghorn-instance-1")

	// DB returns cluster-b only (local-primary not in DB result)
	mock.ExpectQuery("SELECT fca.cluster_id").
		WithArgs("foghorn-instance-1").
		WillReturnRows(sqlmock.NewRows([]string{"cluster_id"}).
			AddRow("cluster-b"))

	LoadServedClusters()

	if !isServedCluster("local-primary") {
		t.Fatalf("expected localClusterID to be preserved even when not in DB")
	}
	if !isServedCluster("cluster-b") {
		t.Fatalf("expected cluster-b from DB")
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestLoadServedClusters_NilDB(t *testing.T) {
	resetServedClusters(t)

	// Pre-populate a cluster
	servedClusters.Load().Store("existing", true)

	prevDB := db
	db = nil
	t.Cleanup(func() { db = prevDB })

	LoadServedClusters()

	// Existing entry should remain (no-op when db is nil)
	if !isServedCluster("existing") {
		t.Fatalf("expected existing cluster to remain when db is nil")
	}
}

func TestLoadServedClusters_DBError(t *testing.T) {
	resetServedClusters(t)

	// Pre-populate a cluster
	servedClusters.Load().Store("existing", true)

	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer mockDB.Close()

	prevDB := db
	db = mockDB
	t.Cleanup(func() { db = prevDB })
	setInstanceID(t, "foghorn-instance-1")

	mock.ExpectQuery("SELECT fca.cluster_id").
		WithArgs("foghorn-instance-1").
		WillReturnError(context.DeadlineExceeded)

	LoadServedClusters()

	// Existing entry should remain (load failed, no swap)
	if !isServedCluster("existing") {
		t.Fatalf("expected existing cluster to remain on DB error")
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestIsServedCluster_EmptyString(t *testing.T) {
	if isServedCluster("") {
		t.Fatalf("expected empty string to return false")
	}
}

func TestServedClustersSnapshot(t *testing.T) {
	resetServedClusters(t)

	servedClusters.Load().Store("cluster-c", true)
	servedClusters.Load().Store("cluster-a", true)
	servedClusters.Load().Store("cluster-b", true)

	snap := ServedClustersSnapshot()

	if len(snap) != 3 {
		t.Fatalf("expected 3 clusters, got %d", len(snap))
	}
	if snap[0] != "cluster-a" || snap[1] != "cluster-b" || snap[2] != "cluster-c" {
		t.Fatalf("expected sorted [cluster-a cluster-b cluster-c], got %v", snap)
	}
}

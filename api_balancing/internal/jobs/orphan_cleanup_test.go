package jobs

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
)

// TestNewOrphanCleanupJobDefaults pins the cadence/age fallbacks (5m / 30m).
func TestNewOrphanCleanupJobDefaults(t *testing.T) {
	j := NewOrphanCleanupJob(OrphanCleanupConfig{})
	if j.interval != 5*time.Minute {
		t.Errorf("interval = %v, want 5m", j.interval)
	}
	if j.maxAge != 30*time.Minute {
		t.Errorf("maxAge = %v, want 30m", j.maxAge)
	}
	custom := NewOrphanCleanupJob(OrphanCleanupConfig{Interval: time.Minute, MaxAge: time.Hour})
	if custom.interval != time.Minute || custom.maxAge != time.Hour {
		t.Errorf("custom config not honored: interval=%v maxAge=%v", custom.interval, custom.maxAge)
	}
}

// TestFindOrphanedClips pins the orphan-detection query: deleted clips that
// still have a non-orphaned node copy older than maxAge, mapped to (hash,node).
// The maxAge interval is passed as the bound so the test also documents that
// orphan detection respects the grace window.
func TestFindOrphanedClips(t *testing.T) {
	ctx := context.Background()

	t.Run("maps rows to orphan structs", func(t *testing.T) {
		mockDB, mock, err := sqlmock.New()
		if err != nil {
			t.Fatal(err)
		}
		defer mockDB.Close()
		mock.ExpectQuery(`artifact_type = 'clip'`).WithArgs("30m0s").
			WillReturnRows(sqlmock.NewRows([]string{"artifact_hash", "node_id"}).
				AddRow("clip-1", "node-a").
				AddRow("clip-2", "node-b"))

		j := NewOrphanCleanupJob(OrphanCleanupConfig{DB: mockDB, Logger: logging.NewLogger(), MaxAge: 30 * time.Minute})
		got, err := j.findOrphanedClips(ctx)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(got) != 2 || got[0].ClipHash != "clip-1" || got[0].NodeID != "node-a" || got[1].ClipHash != "clip-2" {
			t.Fatalf("unexpected orphans: %+v", got)
		}
	})

	t.Run("empty result returns no orphans", func(t *testing.T) {
		mockDB, mock, err := sqlmock.New()
		if err != nil {
			t.Fatal(err)
		}
		defer mockDB.Close()
		mock.ExpectQuery(`artifact_type = 'clip'`).WithArgs("30m0s").
			WillReturnRows(sqlmock.NewRows([]string{"artifact_hash", "node_id"}))
		j := NewOrphanCleanupJob(OrphanCleanupConfig{DB: mockDB, Logger: logging.NewLogger(), MaxAge: 30 * time.Minute})
		got, err := j.findOrphanedClips(ctx)
		if err != nil || len(got) != 0 {
			t.Fatalf("expected no orphans, got %+v err %v", got, err)
		}
	})

	t.Run("query error propagates", func(t *testing.T) {
		mockDB, mock, err := sqlmock.New()
		if err != nil {
			t.Fatal(err)
		}
		defer mockDB.Close()
		mock.ExpectQuery(`artifact_type = 'clip'`).WithArgs("30m0s").WillReturnError(errors.New("boom"))
		j := NewOrphanCleanupJob(OrphanCleanupConfig{DB: mockDB, Logger: logging.NewLogger(), MaxAge: 30 * time.Minute})
		if _, err := j.findOrphanedClips(ctx); err == nil {
			t.Fatal("query error must propagate")
		}
	})
}

// TestFindOrphanedDVRsAndVODs covers the parallel DVR/VOD variants, which differ
// only by the artifact_type filter.
func TestFindOrphanedDVRsAndVODs(t *testing.T) {
	ctx := context.Background()

	t.Run("dvr", func(t *testing.T) {
		mockDB, mock, _ := sqlmock.New()
		defer mockDB.Close()
		mock.ExpectQuery(`artifact_type = 'dvr'`).WithArgs("15m0s").
			WillReturnRows(sqlmock.NewRows([]string{"artifact_hash", "node_id"}).AddRow("dvr-1", "node-a"))
		j := NewOrphanCleanupJob(OrphanCleanupConfig{DB: mockDB, Logger: logging.NewLogger(), MaxAge: 15 * time.Minute})
		got, err := j.findOrphanedDVRs(ctx)
		if err != nil || len(got) != 1 || got[0].DVRHash != "dvr-1" {
			t.Fatalf("unexpected: %+v err %v", got, err)
		}
	})

	t.Run("vod", func(t *testing.T) {
		mockDB, mock, _ := sqlmock.New()
		defer mockDB.Close()
		mock.ExpectQuery(`artifact_type = 'vod'`).WithArgs("15m0s").
			WillReturnRows(sqlmock.NewRows([]string{"artifact_hash", "node_id"}).AddRow("vod-1", "node-a"))
		j := NewOrphanCleanupJob(OrphanCleanupConfig{DB: mockDB, Logger: logging.NewLogger(), MaxAge: 15 * time.Minute})
		got, err := j.findOrphanedVODs(ctx)
		if err != nil || len(got) != 1 || got[0].VODHash != "vod-1" {
			t.Fatalf("unexpected: %+v err %v", got, err)
		}
	})
}

// TestCleanupStaleRegistryEntries pins the GC of long-orphaned node rows
// (is_orphaned + last_seen > 24h). The job must tolerate a DELETE error.
func TestCleanupStaleRegistryEntries(t *testing.T) {
	ctx := context.Background()

	t.Run("executes delete", func(t *testing.T) {
		mockDB, mock, _ := sqlmock.New()
		defer mockDB.Close()
		mock.ExpectExec(`DELETE FROM foghorn.artifact_nodes`).WillReturnResult(sqlmock.NewResult(0, 3))
		j := NewOrphanCleanupJob(OrphanCleanupConfig{DB: mockDB, Logger: logging.NewLogger()})
		j.cleanupStaleRegistryEntries(ctx)
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("delete error is swallowed", func(t *testing.T) {
		mockDB, mock, _ := sqlmock.New()
		defer mockDB.Close()
		mock.ExpectExec(`DELETE FROM foghorn.artifact_nodes`).WillReturnError(errors.New("boom"))
		j := NewOrphanCleanupJob(OrphanCleanupConfig{DB: mockDB, Logger: logging.NewLogger()})
		j.cleanupStaleRegistryEntries(ctx) // must not panic
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	})
}

// TestReconcileErrorTolerance pins that a failure in one find stage does not
// abort the others or the stale-registry GC — each storage class is reconciled
// best-effort and independently. (All finds return empty/error so no retry RPCs
// are dispatched.)
func TestReconcileErrorTolerance(t *testing.T) {
	mockDB, mock, _ := sqlmock.New()
	defer mockDB.Close()

	mock.ExpectQuery(`artifact_type = 'clip'`).WithArgs("30m0s").WillReturnError(errors.New("clip query down"))
	mock.ExpectQuery(`artifact_type = 'dvr'`).WithArgs("30m0s").
		WillReturnRows(sqlmock.NewRows([]string{"artifact_hash", "node_id"}))
	mock.ExpectQuery(`artifact_type = 'vod'`).WithArgs("30m0s").
		WillReturnRows(sqlmock.NewRows([]string{"artifact_hash", "node_id"}))
	mock.ExpectExec(`DELETE FROM foghorn.artifact_nodes`).WillReturnResult(sqlmock.NewResult(0, 0))

	j := NewOrphanCleanupJob(OrphanCleanupConfig{DB: mockDB, Logger: logging.NewLogger(), MaxAge: 30 * time.Minute})
	j.reconcile()

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

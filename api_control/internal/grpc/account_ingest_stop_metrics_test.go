package grpc

import (
	"context"
	"testing"
	"time"

	"frameworks/api_control/internal/database/commodoredb"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

// The cell is read back from the error stopAccountIngest itself returns, so a
// change to that error's wording fails here rather than unlabeling the alert.
func TestIngestStopAttributesOnlyTheCellThatFailed(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.ExpectQuery("ListAccountIngestCells").WithArgs("tenant", "").WillReturnRows(sqlmock.NewRows([]string{"cell_id"}).AddRow("eu-2"))
	mock.ExpectQuery("retired_ingest_cells").WillReturnRows(sqlmock.NewRows([]string{"cell_id"}))
	server := &CommodoreServer{db: db}
	server.foghornDiscovery.Store("eu-2", &foghornDiscoveryEntry{at: time.Now()})
	stopErr := server.stopAccountIngest(t.Context(), "tenant", "", nil)
	if stopErr == nil {
		t.Fatal("stop to an unreachable cell settled")
	}
	compileErr := &authorityCompileError{class: authorityCompileRevocationPending, code: "ingest_revocation_pending", err: stopErr}

	stop := commodoredb.PendingIngestStopRow{Cells: []string{"eu", "eu-2", "us"}, LastError: compileErr.Error()}
	if got := ingestStopFailingCells(stop); len(got) != 1 || got[0] != "eu-2" {
		t.Fatalf("failing cells=%v from %q, want only eu-2", got, stop.LastError)
	}
}

func TestIngestStopAgesGroupByFailingCell(t *testing.T) {
	now := time.Now()
	stops := []commodoredb.PendingIngestStopRow{
		{PendingSince: now.Add(-2 * time.Hour), Cells: []string{"eu", "us"}, LastError: "stop ingest on cell eu: rpc error: code = Unimplemented"},
		{PendingSince: now.Add(-10 * time.Minute), Cells: []string{"eu"}, LastError: "stop ingest on cell eu: unavailable"},
		{PendingSince: now.Add(-20 * time.Minute), Cells: []string{"us"}, LastError: "resolve historical ingest cell us: no instance"},
		{PendingSince: now.Add(-30 * time.Minute), Cells: []string{"us"}, LastError: ""},
	}
	got := ingestStopOldestPendingByCell(stops, now)
	want := map[string]float64{"eu": 7200, "us": 1200, ingestStopUnattributedCell: 1800}
	if len(got) != len(want) {
		t.Fatalf("ages=%v, want %v", got, want)
	}
	for cell, age := range want {
		if got[cell] < age-1 || got[cell] > age+1 {
			t.Fatalf("cell %s age=%v, want %v (all: %v)", cell, got[cell], age, got)
		}
	}
}

func TestIngestStopObservationDropsCellsThatCleared(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	columns := []string{"target_key", "target_kind", "tenant_id", "status", "attempts", "pending_since", "next_attempt_at", "last_error", "cells", "retired_cells"}
	since := time.Now().Add(-time.Hour)
	mock.ExpectQuery("ingest_revocation").WillReturnRows(sqlmock.NewRows(columns).
		AddRow("tenant:a", "tenant", "a", "pending", 3, since, since, "stop ingest on cell eu: down", "{eu}", "{}").
		AddRow("tenant:b", "tenant", "b", "pending", 3, since, since, "stop ingest on cell us: down", "{us}", "{}"))
	mock.ExpectQuery("ingest_revocation").WillReturnRows(sqlmock.NewRows(columns).
		AddRow("tenant:b", "tenant", "b", "pending", 4, since, since, "stop ingest on cell us: down", "{us}", "{}"))
	gauge := prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "test_account_ingest_stop_oldest_pending_seconds"}, []string{"cell"})
	server := &CommodoreServer{db: db, logger: logging.NewLogger(), metrics: &ServerMetrics{AccountIngestStopOldestPendingSeconds: gauge}}

	server.observeAccountIngestStops(context.Background())
	if n := testutil.CollectAndCount(gauge); n != 2 {
		t.Fatalf("series after first observation=%d, want eu and us", n)
	}
	server.observeAccountIngestStops(context.Background())
	if n := testutil.CollectAndCount(gauge); n != 1 {
		t.Fatalf("series after eu cleared=%d, want only us", n)
	}
	if age := testutil.ToFloat64(gauge.WithLabelValues("us")); age < 3500 {
		t.Fatalf("us age=%v", age)
	}
	if err = mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

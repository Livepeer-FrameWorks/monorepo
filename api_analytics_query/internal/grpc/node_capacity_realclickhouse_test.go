//go:build schema_verify

package grpc

import (
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	periscopepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/periscope"
	"testing"
	"time"
)

func TestNetworkCapacityUsesOneFreshEligibleNodeSet_RealClickHouse(t *testing.T) {
	db := startQueryPackClickHouse(t)
	server := NewPeriscopeServer(db, logging.NewLoggerWithService("node-capacity-real-ch"))
	now := time.Now().UTC().Truncate(time.Second)
	for _, row := range []struct {
		id, mode         string
		healthy          uint8
		at               time.Time
		budget, up, down uint64
	}{
		{"fresh", "normal", 1, now, 125000000, 25000000, 7000000},
		{"stale", "normal", 1, now.Add(-2 * time.Minute), 500000000, 100000000, 100000000},
		{"unhealthy", "normal", 0, now, 250000000, 50000000, 50000000},
		{"draining", "draining", 1, now, 1000000000, 100000000, 100000000},
	} {
		_, err := db.ExecContext(t.Context(), `INSERT INTO periscope.node_state_current
   (tenant_id,cluster_id,node_id,is_healthy,operational_mode,updated_at,egress_budget,budget_source,up_speed,down_speed)
   VALUES (?, 'capacity-fixture', ?, ?, ?, ?, ?, 'DEFAULT', ?, ?)`, queryPackTenantID, row.id, row.healthy, row.mode, row.at, row.budget, row.up, row.down)
		if err != nil {
			t.Fatal(err)
		}
	}
	_, err := db.ExecContext(t.Context(), `INSERT INTO periscope.node_state_current
 (tenant_id,cluster_id,node_id,is_healthy,operational_mode,updated_at,bw_limit,up_speed,down_speed)
 VALUES (?, 'capacity-legacy', 'legacy', 1, 'normal', ?, 250000000, 10000000, 2000000)`, queryPackTenantID, now)
	if err != nil {
		t.Fatal(err)
	}
	response, err := server.GetNetworkLiveStats(serviceTestContext(), &periscopepb.GetNetworkLiveStatsRequest{})
	if err != nil {
		t.Fatal(err)
	}
	var got, legacy *periscopepb.NetworkClusterLiveStats
	for _, cluster := range response.GetClusters() {
		if cluster.GetClusterId() == "capacity-fixture" {
			got = cluster
		}
		if cluster.GetClusterId() == "capacity-legacy" {
			legacy = cluster
		}
	}
	if legacy == nil || legacy.GetEgressBudgetBytesPerSec() != 250000000 || legacy.GetDefaultBudgetNodes() != 0 {
		t.Fatalf("legacy ingest budget was discarded: %+v", legacy)
	}
	if got == nil {
		t.Fatal("missing capacity fixture")
	}
	if got.GetEgressBudgetBytesPerSec() != 125000000 || got.GetEgressUsableBytesPerSec() != 106250000 || got.GetUploadBytesPerSec() != 25000000 || got.GetDownloadBytesPerSec() != 7000000 || got.GetEgressObservedBytesPerSec() != 25000000 || got.GetDefaultBudgetNodes() != 1 || got.GetActiveNodes() != 1 {
		t.Fatalf("capacity and observed load used different node sets: %+v", got)
	}
}

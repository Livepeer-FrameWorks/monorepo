//go:build schema_verify

package grpc

import (
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	periscopepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/periscope"
	"testing"
	"time"
)

func TestNetworkViewersFollowServingClusterAndFreshness_RealClickHouse(t *testing.T) {
	db := startQueryPackClickHouse(t)
	server := NewPeriscopeServer(db, logging.NewLoggerWithService("audience-real-ch"))
	now := time.Now().UTC().Truncate(time.Second)
	for _, row := range []struct {
		node, cluster string
		viewers       uint32
		at            time.Time
	}{
		{"audience-origin", "audience-A", 0, now},
		{"audience-edge", "audience-B", 5, now},
		{"audience-stale", "audience-B", 7, now.Add(-5 * time.Minute)},
	} {
		if _, err := db.ExecContext(t.Context(), `INSERT INTO periscope.node_state_current(tenant_id,cluster_id,node_id,viewers,is_healthy,operational_mode,updated_at) VALUES(?,?,?,?,1,'normal',?)`, queryPackTenantID, row.cluster, row.node, row.viewers, row.at); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.ExecContext(t.Context(), `INSERT INTO periscope.stream_state_current(tenant_id,stream_id,internal_name,node_id,status,current_viewers,updated_at) VALUES(?,'b0000000-0000-4000-8000-000000000013','audience-stream','audience-origin','live',5,?)`, queryPackTenantID, now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(t.Context(), `INSERT INTO periscope.stream_state_current(tenant_id,stream_id,internal_name,node_id,status,current_viewers,updated_at) VALUES(?,'b0000000-0000-4000-8000-000000000014','audience-stale-stream','audience-origin','live',99,?)`, queryPackTenantID, now.Add(-5*time.Minute)); err != nil {
		t.Fatal(err)
	}
	response, err := server.GetNetworkLiveStats(serviceTestContext(), &periscopepb.GetNetworkLiveStatsRequest{})
	if err != nil {
		t.Fatal(err)
	}
	counts := map[string]int32{}
	streams := map[string]int32{}
	for _, cluster := range response.GetClusters() {
		counts[cluster.GetClusterId()] = cluster.GetCurrentViewers()
		streams[cluster.GetClusterId()] = cluster.GetActiveStreams()
	}
	if counts["audience-A"] != 0 || counts["audience-B"] != 5 || streams["audience-A"] != 1 {
		t.Fatalf("viewers=%v streams=%v, want A 0 viewers/1 stream, B 5 viewers", counts, streams)
	}
}

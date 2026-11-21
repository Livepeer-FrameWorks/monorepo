//go:build schema_verify

package grpc

import (
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	commonpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/common"
	periscopepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/periscope"
	"google.golang.org/protobuf/types/known/timestamppb"
	"testing"
	"time"
)

func TestHistoricalAndCurrentNodeMemoryHaveSameByteUnits_RealClickHouse(t *testing.T) {
	db := startQueryPackClickHouse(t)
	now := time.Now().UTC().Truncate(time.Second)
	// Omission exercises the migration's default for existing KiB rows.
	_, err := db.ExecContext(t.Context(), `INSERT INTO periscope.node_metrics_samples(timestamp,tenant_id,cluster_id,node_id,ram_max,ram_current,shm_total_bytes,shm_used_bytes) VALUES(?,?,'memory-cutover','old',1048576,524288,262144,131072)`, now, queryPackTenantID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.ExecContext(t.Context(), `INSERT INTO periscope.node_metrics_samples(timestamp,tenant_id,cluster_id,node_id,ram_max,ram_current,shm_total_bytes,shm_used_bytes,memory_unit_bytes) VALUES(?,?,'memory-cutover','new',1073741824,536870912,268435456,134217728,1)`, now, queryPackTenantID)
	if err != nil {
		t.Fatal(err)
	}
	s := NewPeriscopeServer(db, logging.NewLoggerWithService("memory-units"))
	for _, id := range []string{"old", "new"} {
		response, err := s.GetNodeMetrics(serviceTestContext(), &periscopepb.GetNodeMetricsRequest{NodeId: &id, TimeRange: &commonpb.TimeRange{Start: timestamppb.New(now.Add(-time.Minute)), End: timestamppb.New(now.Add(time.Minute))}})
		if err != nil {
			t.Fatal(err)
		}
		if len(response.GetMetrics()) != 1 {
			t.Fatalf("%s metrics: %+v", id, response)
		}
		m := response.GetMetrics()[0]
		if m.GetRamMax() != 1073741824 || m.GetRamCurrent() != 536870912 || m.GetShmTotalBytes() != 268435456 || m.GetShmUsedBytes() != 134217728 {
			t.Fatalf("%s row changed memory scale at cutover: %+v", id, m)
		}
	}
}

//go:build schema_verify

package grpc

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"
	"time"

	obs "github.com/Livepeer-FrameWorks/monorepo/pkg/networkobservation"
	commonpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/common"
	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/periscope"
	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func verifyNetworkObservationProjection(t *testing.T, ctx context.Context, db *sql.DB, server *PeriscopeServer) {
	t.Helper()
	owner, customer, foreignCustomer := uuid.NewString(), uuid.NewString(), uuid.NewString()
	now := time.Now().UTC().Truncate(time.Second)
	first, second := uuid.NewString(), uuid.NewString()
	insert := func(id, tenant string, at time.Time, latency uint32) {
		t.Helper()
		metric := obs.Metric{EventID: id, Timestamp: at, Provider: "livepeer", Network: "42161", Environment: "staging", Kind: "segment", ResourceID: "0xabc", IP: "192.0.2.1", Region: "eu", Workload: "transcoding", ProfileKey: "720p", Success: true, Qualifying: true, LatencyMS: latency, SourceDurationMS: 2000}
		public, err := json.Marshal(metric)
		if err != nil {
			t.Fatal(err)
		}
		private, err := json.Marshal(map[string]any{"stream_tenant_id": customer, "delivery_session_id": "test-delivery", "seq_no": 7, "downstream_status": "unknown"})
		if err != nil {
			t.Fatal(err)
		}
		_, err = db.ExecContext(ctx, `INSERT INTO periscope.network_observations
		 (tenant_id,event_id,timestamp,provider,network_id,environment,kind,resource_id,resolved_ip,gateway_region,workload,profile_key,success,qualifying,latency_ms,source_duration_ms,public_data,private_tenant_id,private_data)
		 VALUES (?,?,?,'livepeer','42161','staging','segment','0xabc','192.0.2.1','eu','transcoding','720p',1,1,?,2000,?,?,?)`, tenant, id, at, latency, string(public), customer, string(private))
		if err != nil {
			t.Fatal(err)
		}
	}
	insert(first, owner, now.Add(-15*time.Minute), 100)
	insert(first, owner, now.Add(-15*time.Minute), 100)
	insert(second, owner, now.Add(-5*time.Minute), 300)
	insert(uuid.NewString(), foreignCustomer, now.Add(-5*time.Minute), 900)
	req := &pb.NetworkObservationsRequest{TenantIds: []string{owner}, Provider: "livepeer", NetworkId: "42161", Environment: "staging", ResourceId: "0xabc", ResolvedIp: "192.0.2.1", Interval: "5m", Limit: 1, TimeRange: &commonpb.TimeRange{Start: timestamppb.New(now.Add(-time.Hour)), End: timestamppb.New(now)}}
	query := func() obs.History {
		t.Helper()
		response, err := server.GetNetworkObservations(ctx, req)
		if err != nil {
			t.Fatal(err)
		}
		var history obs.History
		if err := json.Unmarshal(response.Json, &history); err != nil {
			t.Fatal(err)
		}
		return history
	}
	history := query()
	if !history.Truncated || len(history.Cohorts) != 1 || len(history.Summaries) != 1 || history.Summaries[0].Samples != 2 || len(history.Observations) != 1 || len(history.Details) != 0 {
		t.Fatalf("public bounded projection lost deduplication, scope or full-window totals: %+v", history)
	}
	req.IncludeDetails = true
	insertReceipt := func(tenant string, sequence int32) {
		t.Helper()
		_, err := db.ExecContext(ctx, `INSERT INTO periscope.processing_segments_final
		 (tenant_id,source_event_id,process_type,livepeer_session_id,segment_number,rendition_count,output_bytes_total,turnaround_ms,source_ended_at_ms,projection_version_ms)
		 VALUES (?,?,'Livepeer','test-delivery',?,2,1024,400,?,?)`, tenant, uuid.NewString(), sequence, now.UnixMilli(), now.UnixMilli())
		if err != nil {
			t.Fatal(err)
		}
	}
	insertReceipt(foreignCustomer, 7)
	insertReceipt(customer, 8)
	assertDownstream := func(want string) {
		t.Helper()
		history := query()
		var detail map[string]any
		if len(history.Details) != 1 || json.Unmarshal(history.Details[0], &detail) != nil || detail["downstream_status"] != want {
			t.Fatalf("private output correlation = %s, want %s", history.Details, want)
		}
	}
	assertDownstream("unknown")
	insertReceipt(customer, 7)
	assertDownstream("output_observed")
	req.IncludeDetails = false
	req.TimeRange.Start = timestamppb.New(now.Add(-90 * 24 * time.Hour))
	history = query()
	if !history.RolledUp || history.IntervalSeconds != 3600 {
		t.Fatalf("long-range projection did not select retained hourly snapshots: %+v", history)
	}
}

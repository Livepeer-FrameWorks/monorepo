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
	if history.Work == nil || len(history.Work.Summaries) != 1 || history.Work.Summaries[0].UnaccountedSamples != 2 || history.Work.Summaries[0].ServiceCharge != nil {
		t.Fatalf("legacy observations became zero-cost accounted work or inherited chart truncation: %+v", history.Work)
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

func verifyNetworkWorkProjection(t *testing.T, ctx context.Context, db *sql.DB, server *PeriscopeServer) {
	t.Helper()
	owner := uuid.NewString()
	now := time.Now().UTC().Truncate(time.Second)
	at := now.Truncate(time.Hour).Add(-time.Hour + time.Minute)
	insert := func(id, ip, kind string, numerator string) {
		t.Helper()
		_, err := db.ExecContext(ctx, `INSERT INTO periscope.network_observations
		(tenant_id,event_id,timestamp,provider,network_id,environment,kind,resource_id,resolved_ip,gateway_region,workload,profile_key,success,source_duration_ms,public_data,
		accounting_version,request_dispatched,returned_pixels_known,returned_pixels,accepted_pixels,accepted_renditions,service_charge_known,service_charge_numerator,service_charge_denominator,ticket_credit_known,ticket_credit_numerator,ticket_credit_denominator)
		VALUES (?,?,?,'livepeer','42161','staging',?,'0xwork',?,'eu','transcoding','720p',1,2000,'{}',1,?,?,?,?,?,?,?,?,?,?,?)`, owner, id, at, kind, ip,
			kind == "attempt", kind == "attempt", uint64(100), uint64(100), uint32(2), kind == "attempt", numerator, "3", kind == "attempt", numerator, "2")
		if err != nil {
			t.Fatal(err)
		}
	}
	first := uuid.NewString()
	insert(first, "192.0.2.1", "attempt", "1")
	insert(first, "192.0.2.1", "attempt", "1")
	insert(uuid.NewString(), "192.0.2.2", "attempt", "2")
	insert(uuid.NewString(), "192.0.2.2", "segment", "0")
	req := &pb.NetworkObservationsRequest{TenantIds: []string{owner}, Provider: "livepeer", NetworkId: "42161", Environment: "staging", ResourceId: "0xwork", Limit: 1, TimeRange: &commonpb.TimeRange{Start: timestamppb.New(at.Add(-time.Hour)), End: timestamppb.New(now)}}
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
	assertWork := func(history obs.History) {
		t.Helper()
		if history.Work == nil || history.Work.Truncated || len(history.Work.Summaries) != 2 {
			t.Fatalf("full-window work incorrectly bounded: %+v", history.Work)
		}
		var submitted, accepted, durations, outputs, returnedPixels, acceptedPixels uint64
		for _, summary := range history.Work.Summaries {
			submitted += summary.DispatchedSourceMS
			accepted += summary.AcceptedSourceMS
			durations += summary.AcceptedDurationSamples
			outputs += summary.AcceptedOutputSamples
			returnedPixels += summary.ReturnedPixels
			acceptedPixels += summary.AcceptedPixels
		}
		if submitted != 4000 || accepted != 2000 || durations != 1 || outputs != 1 || returnedPixels != 200 || acceptedPixels != 100 {
			t.Fatalf("work replay or attempt/final inflation: %d/%d", submitted, accepted)
		}
	}
	assertWork(history)
	if !history.Truncated {
		t.Fatal("fixture did not bound samples independently of work")
	}
	for _, item := range []struct {
		ip, n    string
		accepted uint64
	}{{"192.0.2.1", "1", 0}, {"192.0.2.2", "2", 2000}} {
		_, err := db.ExecContext(ctx, `INSERT INTO periscope.network_work_rollups
		(tenant_id,provider,network_id,environment,bucket,interval_seconds,resource_id,resolved_ip,gateway_region,profile_key,service_charge_denominator,ticket_credit_denominator,
		accounted_attempts,dispatched_attempts,dispatched_source_ms,returned_attempts,returned_pixels,accepted_segments,accepted_source_ms,accepted_pixels,accepted_rendition_ms,service_charge_samples,service_charge_numerator,ticket_credit_samples,ticket_credit_numerator,dispatched_duration_samples,accepted_duration_samples,accepted_output_samples,snapshot_version)
		VALUES (?,'livepeer','42161','staging',?,3600,'0xwork',?,'eu','720p','3','2',1,1,2000,1,100,?,?,?,?,1,?,1,?,1,?,?,?)`, owner, at.Truncate(time.Hour), item.ip, item.accepted/2000, item.accepted, item.accepted/2000*100, item.accepted*2, item.n, item.n, item.accepted/2000, item.accepted/2000, now)
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.ExecContext(ctx, `ALTER TABLE periscope.network_observations DELETE WHERE tenant_id=? SETTINGS mutations_sync=2`, owner); err != nil {
		t.Fatal(err)
	}
	req.TimeRange.Start = timestamppb.New(now.Add(-45 * 24 * time.Hour))
	history = query()
	assertWork(history)
	if !history.RolledUp || history.Work.IntervalSeconds != 3600 || len(history.Observations) != 0 || history.Work.Start.Minute() != 0 || history.Work.End.Minute() != 0 {
		t.Fatalf("retained work did not survive raw expiry with honest hour bounds: %+v", history)
	}
}

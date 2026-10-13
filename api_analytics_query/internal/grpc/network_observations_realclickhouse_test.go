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

func verifyNetworkWorkCreditVariety(t *testing.T, ctx context.Context, db *sql.DB, server *PeriscopeServer) {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Second)
	for _, retained := range []bool{false, true} {
		name := "raw"
		if retained {
			name = "retained"
		}
		t.Run("credit-variety/"+name, func(t *testing.T) {
			owner := uuid.NewString()
			start, end := now.Add(-time.Hour), now
			var statement string
			var at time.Time
			if retained {
				start = now.Add(-45 * 24 * time.Hour)
				at = now.Truncate(time.Hour).Add(-time.Hour)
				statement = `INSERT INTO periscope.network_work_rollups
				(tenant_id,provider,network_id,environment,bucket,interval_seconds,resource_id,resolved_ip,gateway_region,profile_key,
				service_charge_denominator,ticket_credit_denominator,accounted_attempts,dispatched_attempts,dispatched_source_ms,
				returned_attempts,returned_pixels,service_charge_samples,service_charge_numerator,ticket_credit_samples,ticket_credit_numerator,tickets_sent,dispatched_duration_samples,snapshot_version)
				SELECT ?,'livepeer','42161','staging',?,3600,'0xcredit-variety','192.0.2.99','eu','720p',
				'1','3',1,1,2000,1,100,1,1,1,toString(number+1),1,1,now64(3) FROM numbers(10001)`
			} else {
				at = now.Add(-time.Minute)
				statement = `INSERT INTO periscope.network_observations
				(tenant_id,provider,network_id,environment,timestamp,event_id,kind,resource_id,resolved_ip,gateway_region,profile_key,
				accounting_version,request_dispatched,source_duration_ms,returned_pixels_known,returned_pixels,
				service_charge_known,service_charge_numerator,service_charge_denominator,ticket_credit_known,ticket_credit_numerator,ticket_credit_denominator,tickets_sent)
				SELECT ?,'livepeer','42161','staging',?,generateUUIDv4(),'attempt','0xcredit-variety','192.0.2.99','eu','720p',
				1,1,2000,1,100,1,1,'1',1,toString(number+1),'3',1 FROM numbers(10001)`
			}
			if _, err := db.ExecContext(ctx, statement, owner, at); err != nil {
				t.Fatal(err)
			}
			where := "tenant_id=? AND provider=? AND network_id=? AND environment=? AND timestamp>=? AND timestamp<?"
			args := []any{owner, "livepeer", "42161", "staging", start, end}
			history, err := server.networkWork(ctx, where, args, start, end, retained)
			if err != nil {
				t.Fatal(err)
			}
			if history.Truncated || len(history.Summaries) != 1 {
				t.Fatalf("ticket-value variety suppressed complete work/charges: %+v", history)
			}
			total := history.Summaries[0]
			if !history.TicketCreditTruncated || !total.TicketCreditIncomplete || total.TicketCreditSent != nil || total.TicketCreditSamples != 10001 {
				t.Fatalf("bounded ticket credit was presented as complete: %+v", history)
			}
			if total.DispatchedAttempts != 10001 || total.DispatchedSourceMS != 20002000 || total.ReturnedPixels != 1000100 || total.ServiceCharge == nil || total.ServiceCharge.Numerator != "10001" || total.ServiceCharge.Denominator != "1" {
				t.Fatalf("ticket variety changed work/charges: %+v", total)
			}
		})
	}
}

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

// verifyOrchestratorPerformanceParity proves the GraphQL performance series
// counts a gateway's schema-3 observations exactly as the public projection
// does, excludes prober rows, and still shows legacy outcomes scoped by their
// cluster owner rather than their stream tenant.
func verifyOrchestratorPerformanceParity(t *testing.T, ctx context.Context, db *sql.DB, server *PeriscopeServer) {
	t.Helper()
	owner, customer := uuid.NewString(), uuid.NewString()
	const (
		orch    = "0xperfparity"
		ip      = "192.0.2.77"
		gateway = "gw-perf"
		region  = "eu"
	)
	now := time.Now().UTC().Truncate(time.Second)
	at := now.Add(-20 * time.Minute)
	bucket := at.Truncate(5 * time.Minute)
	insert := func(id, producer, observer, kind string, success, qualifying bool, latency uint32, pixels uint64) {
		t.Helper()
		_, err := db.ExecContext(ctx, `INSERT INTO periscope.network_observations
		(tenant_id,event_id,timestamp,provider,network_id,environment,gateway_id,gateway_region,producer_kind,kind,resource_id,resolved_ip,workload,
		 success,qualifying,latency_ms,returned_pixels_known,returned_pixels,public_data)
		VALUES (?,?,?,'livepeer','42161','staging',?,?,?,?,?,?,'transcoding',?,?,?,?,?,'{}')`,
			owner, id, at, observer, region, producer, kind, orch, ip, success, qualifying, latency, pixels > 0, pixels)
		if err != nil {
			t.Fatal(err)
		}
	}
	replayed := uuid.NewString()
	insert(replayed, "gateway", gateway, "discovery", true, true, 40, 0)
	insert(replayed, "gateway", gateway, "discovery", true, true, 40, 0)
	insert(uuid.NewString(), "gateway", gateway, "discovery", false, true, 0, 0)
	insert(uuid.NewString(), "gateway", gateway, "discovery", false, false, 0, 0)
	insert(uuid.NewString(), "gateway", gateway, "attempt", true, true, 300, 1000)
	insert(uuid.NewString(), "gateway", gateway, "attempt", false, true, 0, 0)
	insert(uuid.NewString(), "gateway", gateway, "decision", true, false, 5, 0)
	legacyAt := now.Add(-3 * time.Hour)
	if _, err := db.ExecContext(ctx, `INSERT INTO periscope.orchestrator_transcode_outcomes
		(timestamp,tenant_id,cluster_owner_tenant_id,gateway_id,gateway_region,orch_addr,resolved_ip,success,overall_ms,pixels)
		VALUES (?,?,?,?,?,?,?,1,500,2000)`, legacyAt, customer, owner, gateway, region, orch, ip); err != nil {
		t.Fatal(err)
	}

	response, err := server.GetNetworkObservations(ctx, &pb.NetworkObservationsRequest{TenantIds: []string{owner}, Provider: "livepeer", NetworkId: "42161", Environment: "staging",
		ResourceId: orch, ResolvedIp: ip, GatewayRegion: region, TimeRange: &commonpb.TimeRange{Start: timestamppb.New(now.Add(-time.Hour)), End: timestamppb.New(now)}})
	if err != nil {
		t.Fatal(err)
	}
	var history obs.History
	if err := json.Unmarshal(response.Json, &history); err != nil {
		t.Fatal(err)
	}
	rest := map[string]obs.Cohort{}
	for _, c := range history.Summaries {
		rest[c.Kind] = c
	}

	series := func(start time.Time, interval string) []*pb.OrchestratorPerformancePoint {
		t.Helper()
		resp, err := server.GetOrchestratorPerformanceSeries(ctx, &pb.GetOrchestratorPerformanceSeriesRequest{TenantId: owner, OrchAddr: orch, Interval: &interval,
			TimeRange: &commonpb.TimeRange{Start: timestamppb.New(start), End: timestamppb.New(now)}})
		if err != nil {
			t.Fatal(err)
		}
		return resp.GetPoints()
	}
	points := series(now.Add(-time.Hour), "5m")
	if len(points) != 1 {
		t.Fatalf("5m series = %+v, want one point for the gateway's bucket", points)
	}
	p := points[0]
	if !p.GetTimestamp().AsTime().Equal(bucket) || p.GetGatewayId() != gateway || p.GetGatewayRegion() != region || p.GetResolvedIp() != ip {
		t.Fatalf("series point key = %v %s %s %s", p.GetTimestamp().AsTime(), p.GetGatewayId(), p.GetGatewayRegion(), p.GetResolvedIp())
	}
	discovery, attempt := rest["discovery"], rest["attempt"]
	if discovery.Samples != 3 || attempt.Samples != 2 {
		t.Fatalf("public projection cohorts = %+v", history.Summaries)
	}
	if p.GetAttempts() != discovery.Samples || p.GetSuccesses() != discovery.Successes || p.GetFailures() != discovery.Failures || p.GetMeanLatencyMs() != 40 {
		t.Fatalf("series discovery %d/%d/%d mean %v != public %d/%d/%d", p.GetAttempts(), p.GetSuccesses(), p.GetFailures(), p.GetMeanLatencyMs(), discovery.Samples, discovery.Successes, discovery.Failures)
	}
	if p.GetTranscodeAttempts() != attempt.Samples || p.GetTranscodeSuccesses() != attempt.Successes || p.GetTranscodeFailures() != attempt.Failures || p.GetTranscodePixels() != 1000 || p.GetTranscodeMeanOverallMs() != 300 {
		t.Fatalf("series transcode %+v != public attempt cohort %+v", p, attempt)
	}

	points = series(now.Add(-6*time.Hour), "1h")
	var legacyAttempts, v3Attempts uint64
	for _, point := range points {
		if point.GetTimestamp().AsTime().Equal(legacyAt.Truncate(time.Hour)) {
			legacyAttempts += point.GetTranscodeAttempts()
		}
		if point.GetTimestamp().AsTime().Equal(at.Truncate(time.Hour)) {
			v3Attempts += point.GetAttempts()
		}
	}
	if legacyAttempts != 1 || v3Attempts != discovery.Samples {
		t.Fatalf("1h series legacy transcode attempts = %d (want 1, scoped by cluster owner), schema-3 discovery = %d (want %d): %+v", legacyAttempts, v3Attempts, discovery.Samples, points)
	}

	// Beyond raw retention the series reads the gateway-keyed snapshots, and
	// a long window agrees with the public route's region snapshots of the
	// same hour.
	old := now.Truncate(time.Hour).Add(-45 * 24 * time.Hour)
	for i, statement := range []string{
		`INSERT INTO periscope.network_gateway_rollups
		 (tenant_id,provider,network_id,environment,bucket,interval_seconds,kind,resource_id,resolved_ip,gateway_id,gateway_region,
		  samples,successes,failures,latency_sum,latency_count,max_latency,returned_pixels,snapshot_version)
		 VALUES (?,'livepeer','42161','staging',?,3600,'discovery','` + orch + `','` + ip + `','` + gateway + `','` + region + `',4,3,1,150,3,60,0,now64(3)),
		        (?,'livepeer','42161','staging',?,3600,'attempt','` + orch + `','` + ip + `','` + gateway + `','` + region + `',2,2,0,600,2,400,5000,now64(3))`,
		`INSERT INTO periscope.network_observation_rollups
		 (tenant_id,provider,network_id,environment,bucket,interval_seconds,kind,resource_id,resolved_ip,gateway_region,workload,profile_key,
		  samples,successes,failures,qualified_samples,latency_state,duration_samples,duration_ratio_state,retries,fallbacks,deadline_misses,snapshot_version)
		 SELECT ?,'livepeer','42161','staging',?,3600,'discovery','` + orch + `','` + ip + `','` + region + `','transcoding','',
		  4,3,1,4,quantilesTDigestIfState(0.5,0.95,0.99)(toUInt32(50),toUInt8(1)),0,quantileTDigestIfState(0.95)(toFloat64(0),toUInt8(0)),0,0,0,now64(3)`,
	} {
		args := []any{owner, old}
		if i == 0 {
			args = append(args, owner, old)
		}
		if _, err := db.ExecContext(ctx, statement, args...); err != nil {
			t.Fatal(err)
		}
	}
	points = series(now.Add(-60*24*time.Hour), "1h")
	var retained *pb.OrchestratorPerformancePoint
	for _, point := range points {
		if point.GetTimestamp().AsTime().Equal(old) {
			retained = point
		}
	}
	if retained == nil || retained.GetGatewayId() != gateway || retained.GetAttempts() != 4 || retained.GetSuccesses() != 3 || retained.GetFailures() != 1 ||
		retained.GetMeanLatencyMs() != 50 || retained.GetMaxLatencyMs() != 60 || retained.GetTranscodeAttempts() != 2 || retained.GetTranscodePixels() != 5000 {
		t.Fatalf("series beyond raw retention lost the gateway's retained history: %+v", retained)
	}
	response, err = server.GetNetworkObservations(ctx, &pb.NetworkObservationsRequest{TenantIds: []string{owner}, Provider: "livepeer", NetworkId: "42161", Environment: "staging",
		ResourceId: orch, ResolvedIp: ip, GatewayRegion: region, TimeRange: &commonpb.TimeRange{Start: timestamppb.New(old), End: timestamppb.New(old.Add(time.Hour))}})
	if err != nil {
		t.Fatal(err)
	}
	history = obs.History{}
	if err := json.Unmarshal(response.Json, &history); err != nil {
		t.Fatal(err)
	}
	if !history.RolledUp || len(history.Summaries) != 1 || history.Summaries[0].Samples != retained.GetAttempts() ||
		history.Summaries[0].Successes != retained.GetSuccesses() || history.Summaries[0].Failures != retained.GetFailures() {
		t.Fatalf("public long window %+v differs from the series point %+v", history.Summaries, retained)
	}
}

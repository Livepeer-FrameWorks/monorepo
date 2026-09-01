//go:build schema_verify

package grpc

import (
	"strings"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	commonpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/common"
	periscopepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/periscope"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestClusterWorkloadSeparatesStorageFlowStockAndScope_RealClickHouse(t *testing.T) {
	db := startQueryPackClickHouse(t)
	server := NewPeriscopeServer(db, logging.NewLoggerWithService("cluster-workload-real-ch-test"))
	ctx := serviceTestContext()
	now := time.Now().UTC().Truncate(time.Second)
	const clusterID = "cluster-storage"

	insertSnapshot := `INSERT INTO periscope.storage_snapshots
		(timestamp, tenant_id, node_id, cluster_id, storage_scope, storage_provider_cluster_id,
		 total_bytes, file_count, dvr_bytes, clip_bytes, vod_bytes)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, 0, 0, 0)`
	for _, row := range []struct {
		at    time.Time
		node  string
		scope string
		bytes uint64
		files uint32
	}{
		{now.Add(-20 * time.Minute), "storage-1", "hot", 100, 1},
		{now.Add(-15 * time.Minute), "storage-1", "cold", 200, 2},
		{now.Add(-3 * time.Hour), "storage-stale", "hot", 999, 9},
	} {
		if _, err := db.ExecContext(ctx, insertSnapshot, row.at, queryPackTenantID, row.node, clusterID, row.scope, clusterID, row.bytes, row.files); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO periscope.storage_events
		(timestamp, tenant_id, stream_id, internal_name, asset_hash, action, asset_type, size_bytes, node_id, cluster_id)
		VALUES (?, ?, ?, 'live+storage', 'asset-1', 'cached', 'dvr', 7, 'storage-1', ?)`,
		now.Add(-10*time.Minute), queryPackTenantID, queryPackStreamID, clusterID); err != nil {
		t.Fatal(err)
	}

	resp, err := server.GetClusterWorkload(ctx, &periscopepb.GetClusterWorkloadRequest{
		TenantId:   queryPackTenantID,
		ClusterIds: []string{clusterID},
		TimeRange: &commonpb.TimeRange{
			Start: timestamppb.New(now.Add(-time.Hour)),
			End:   timestamppb.New(now.Add(time.Second)),
		},
	})
	if err != nil {
		t.Fatalf("GetClusterWorkload: %v", err)
	}

	rows := make(map[string]*periscopepb.ClusterWorkload)
	for _, row := range resp.GetRows() {
		key := row.GetWorkKind() + "/" + row.GetMeasurementKind() + "/" + row.GetStorageScope()
		rows[key] = row
		if row.GetNodeId() == "storage-stale" {
			t.Fatalf("stale storage observation was returned: %+v", row)
		}
	}
	window := rows["storage/window/"]
	hot := rows["storage/current/hot"]
	cold := rows["storage/current/cold"]
	if window == nil || window.GetBytes() != 7 || window.GetActiveCount() != 0 {
		t.Fatalf("window storage flow = %+v, want 7 bytes and no current objects", window)
	}
	if hot == nil || hot.GetBytes() != 100 || hot.GetActiveCount() != 1 || hot.GetObservedAt() == nil {
		t.Fatalf("hot current storage = %+v", hot)
	}
	if cold == nil || cold.GetBytes() != 200 || cold.GetActiveCount() != 2 || cold.GetObservedAt() == nil {
		t.Fatalf("cold current storage = %+v", cold)
	}
}

func TestClusterWorkloadUsesEventPlacementWithoutCrossTenantNodeFanout_RealClickHouse(t *testing.T) {
	db := startQueryPackClickHouse(t)
	server := NewPeriscopeServer(db, logging.NewLoggerWithService("cluster-workload-placement-real-ch-test"))
	ctx := serviceTestContext()
	now := time.Now().UTC().Truncate(time.Second)
	const (
		requestedCluster = "cluster-owned"
		foreignCluster   = "cluster-foreign"
		sharedNode       = "edge-shared-name"
		foreignTenant    = "5eed517e-ba5e-da7a-517e-ba5eda7a0002"
		foreignStreamA   = "5eedfeed-11fe-ca57-feed-11feca570002"
		foreignStreamB   = "5eedfeed-11fe-ca57-feed-11feca570003"
	)

	if _, err := db.ExecContext(ctx, `INSERT INTO periscope.viewer_connection_events
		(event_id, timestamp, tenant_id, stream_id, internal_name, session_id, node_id, cluster_id, event_type)
		VALUES
		 ('5eedfeed-11fe-ca57-feed-11feca570020', ?, ?, ?, 'owned-placement', 'session-owned', ?, ?, 'connect'),
		 ('5eedfeed-11fe-ca57-feed-11feca570021', ?, ?, ?, 'foreign-placement', 'session-foreign', ?, ?, 'connect')`,
		now, foreignTenant, foreignStreamA, sharedNode, requestedCluster,
		now, foreignTenant, foreignStreamB, sharedNode, foreignCluster,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO periscope.stream_state_current
		(tenant_id, stream_id, internal_name, node_id, cluster_id, status, buffer_state,
		 current_viewers, total_inputs, uploaded_bytes, downloaded_bytes, viewer_seconds,
		 started_at, updated_at)
		VALUES
		 (?, ?, 'owned-ingest', ?, ?, 'live', 'FULL', 0, 1, 0, 0, 0, ?, ?),
		 (?, ?, 'foreign-ingest', ?, ?, 'live', 'FULL', 0, 1, 0, 0, 0, ?, ?)`,
		foreignTenant, foreignStreamA, sharedNode, requestedCluster, now, now,
		foreignTenant, foreignStreamB, sharedNode, foreignCluster, now, now,
	); err != nil {
		t.Fatal(err)
	}

	resp, err := server.GetClusterWorkload(ctx, &periscopepb.GetClusterWorkloadRequest{
		TenantId: queryPackTenantID, ClusterIds: []string{requestedCluster},
		TimeRange: &commonpb.TimeRange{Start: timestamppb.New(now.Add(-time.Hour)), End: timestamppb.New(now.Add(time.Second))},
	})
	if err != nil {
		t.Fatalf("GetClusterWorkload: %v", err)
	}

	current := make(map[string]*periscopepb.ClusterWorkload)
	for _, row := range resp.GetRows() {
		if row.GetClusterId() == foreignCluster {
			t.Fatalf("foreign cluster placement leaked into response: %+v", row)
		}
		if row.GetMeasurementKind() == "current" && row.GetNodeId() == sharedNode {
			current[row.GetWorkKind()] = row
		}
	}
	if row := current["viewer"]; row == nil || row.GetActiveCount() != 1 {
		t.Fatalf("viewer current row = %+v, want one viewer on the requested cluster", row)
	}
	if row := current["ingest"]; row == nil || row.GetActiveCount() != 1 {
		t.Fatalf("ingest current row = %+v, want one ingest on the requested cluster", row)
	}
}

func TestFederationSummaryPreservesOperatorHistoryWithoutDualRollupDuplicates_RealClickHouse(t *testing.T) {
	db := startQueryPackClickHouse(t)
	server := NewPeriscopeServer(db, logging.NewLoggerWithService("federation-summary-real-ch-test"))
	ctx := serviceTestContext()
	now := time.Now().UTC().Truncate(time.Hour)
	const summaryTenant = "5eed517e-ba5e-da7a-517e-ba5eda7a0099"
	const foreignInfrastructureTenant = "5eed517e-ba5e-da7a-517e-ba5eda7a0002"

	if _, err := db.ExecContext(ctx, `INSERT INTO periscope.federation_hourly
		(hour, tenant_id, local_cluster, remote_cluster, event_type, event_count, sum_latency_ms, sum_time_to_live_ms, failure_count)
		VALUES (?, ?, 'cluster-a', 'cluster-b', 'origin_pull_completed', 2, 30, 0, 0)`, now, summaryTenant); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO periscope.federation_hourly_v2
		(hour, tenant_id, content_tenant_id, local_cluster, remote_cluster, event_type, event_count, sum_latency_ms, sum_time_to_live_ms, failure_count)
		VALUES
			(?, ?, ?, 'cluster-a', 'cluster-b', 'origin_pull_completed', 2, 30, 0, 0),
			(?, ?, ?, 'cluster-c', 'cluster-a', 'origin_pull_completed', 3, 90, 0, 1)`,
		now, summaryTenant, summaryTenant,
		now, foreignInfrastructureTenant, summaryTenant,
	); err != nil {
		t.Fatal(err)
	}

	resp, err := server.GetFederationSummary(ctx, &periscopepb.GetFederationSummaryRequest{
		TenantId: summaryTenant,
		TimeRange: &commonpb.TimeRange{
			Start: timestamppb.New(now.Add(-time.Hour)),
			End:   timestamppb.New(now.Add(time.Hour)),
		},
	})
	if err != nil {
		t.Fatalf("GetFederationSummary: %v", err)
	}

	summary := resp.GetSummary()
	if summary.GetTotalEvents() != 5 {
		t.Fatalf("total events = %d, want historical 2 + cross-tenant content 3", summary.GetTotalEvents())
	}
	if len(summary.GetEventCounts()) != 1 || summary.GetEventCounts()[0].GetCount() != 5 {
		t.Fatalf("unexpected event counts: %+v", summary.GetEventCounts())
	}
	if summary.GetOverallAvgLatencyMs() != 24 || summary.GetOverallFailureRate() != 0.2 {
		t.Fatalf("summary averages = latency %v failure %v, want 24 and 0.2",
			summary.GetOverallAvgLatencyMs(), summary.GetOverallFailureRate())
	}
}

func TestFederationEventsSplitOperatorAndContentScopes_RealClickHouse(t *testing.T) {
	db := startQueryPackClickHouse(t)
	server := NewPeriscopeServer(db, logging.NewLoggerWithService("federation-events-scope-real-ch-test"))
	ctx := serviceTestContext()
	now := time.Now().UTC().Truncate(time.Second)
	const (
		operatorTenant = queryPackTenantID
		contentTenant  = "5eed517e-ba5e-da7a-517e-ba5eda7a0002"
		otherInfra     = "5eed517e-ba5e-da7a-517e-ba5eda7a0003"
		eventType      = "test_split_scope"
	)

	if _, err := db.ExecContext(ctx, `INSERT INTO periscope.federation_events
		(timestamp, tenant_id, stream_tenant_id, event_type, local_cluster, remote_cluster, stream_name, stream_id)
		VALUES
		 (?, ?, ?, ?, 'operator-cluster', 'remote-a', 'foreign-content', '5eedfeed-11fe-ca57-feed-11feca570010'),
		 (?, ?, ?, ?, 'other-cluster', 'remote-b', 'owned-content', '5eedfeed-11fe-ca57-feed-11feca570011'),
		 (?, ?, ?, ?, 'operator-cluster', 'remote-c', 'operator-content', '5eedfeed-11fe-ca57-feed-11feca570012')`,
		now, operatorTenant, contentTenant, eventType,
		now.Add(-time.Second), otherInfra, contentTenant, eventType,
		now.Add(-2*time.Second), operatorTenant, operatorTenant, eventType,
	); err != nil {
		t.Fatal(err)
	}

	read := func(tenantID string) []*periscopepb.FederationEvent {
		t.Helper()
		eventTypeFilter := eventType
		resp, err := server.GetFederationEvents(ctx, &periscopepb.GetFederationEventsRequest{
			TenantId: tenantID, EventType: &eventTypeFilter,
			TimeRange: &commonpb.TimeRange{Start: timestamppb.New(now.Add(-time.Minute)), End: timestamppb.New(now.Add(time.Second))},
		})
		if err != nil {
			t.Fatalf("GetFederationEvents(%s): %v", tenantID, err)
		}
		return resp.GetEvents()
	}

	operatorRows := read(operatorTenant)
	if len(operatorRows) != 2 {
		t.Fatalf("operator rows = %d, want two infrastructure-partition rows", len(operatorRows))
	}
	for _, row := range operatorRows {
		switch row.GetRemoteCluster() {
		case "remote-a":
			if row.StreamName != nil || row.StreamId != nil || row.StreamTenantId != nil {
				t.Fatalf("operator saw foreign content identity: %+v", row)
			}
		case "remote-c":
			if row.GetStreamName() != "operator-content" {
				t.Fatalf("operator's own content was redacted: %+v", row)
			}
		}
	}

	contentRows := read(contentTenant)
	if len(contentRows) != 2 {
		t.Fatalf("content rows = %d, want own content on both infrastructure tenants", len(contentRows))
	}
	for _, row := range contentRows {
		if row.GetStreamTenantId() != contentTenant || row.GetStreamName() == "" {
			t.Fatalf("content-owner identity missing: %+v", row)
		}
	}

	var indexCount int
	if err := db.QueryRowContext(ctx, `SELECT count() FROM system.data_skipping_indices
		WHERE database = 'periscope' AND (table, name) IN
		(('routing_decisions', 'idx_routing_stream_tenant'), ('federation_events', 'idx_federation_stream_tenant'))`).Scan(&indexCount); err != nil {
		t.Fatal(err)
	}
	if indexCount != 2 {
		t.Fatalf("content-tenant skip indexes = %d, want 2", indexCount)
	}
	for table, indexName := range map[string]string{
		"routing_decisions": "idx_routing_stream_tenant",
		"federation_events": "idx_federation_stream_tenant",
	} {
		rows, err := db.QueryContext(ctx, `EXPLAIN indexes = 1 SELECT count() FROM periscope.`+table+
			` WHERE stream_tenant_id = toUUID('`+contentTenant+`')`)
		if err != nil {
			t.Fatal(err)
		}
		var plan strings.Builder
		for rows.Next() {
			var line string
			if err := rows.Scan(&line); err != nil {
				_ = rows.Close()
				t.Fatal(err)
			}
			plan.WriteString(line)
			plan.WriteByte('\n')
		}
		_ = rows.Close()
		if !strings.Contains(plan.String(), indexName) {
			t.Fatalf("%s content-owner query plan did not use %s:\n%s", table, indexName, plan.String())
		}
	}
}

//go:build schema_verify

package handlers

import (
	"context"
	"maps"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/kafka"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/testutil/dockerch"
	"github.com/google/uuid"
)

// TestAPIUsageRootFieldSignatures_RealClickHouse drives Bridge batches through
// the service-event writer and the api_usage_5m rebuild on the current
// Replicated baseline: one window's signatures are all counted, and a source
// event replayed with root fields keeps the signature its first copy stored.
func TestAPIUsageRootFieldSignatures_RealClickHouse(t *testing.T) {
	root, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	conn := dockerch.StartCurrent(t, root, "fw-api-usage-root-fields").Native
	h := NewAnalyticsHandler(conn, logging.NewLogger(), nil)
	ctx := context.Background()
	tenantID := uuid.New()
	window := time.Now().UTC().Truncate(5 * time.Minute).Add(-time.Hour)

	insertRequest := func(sourceEventID string, ingestedAt time.Time, requests uint32, rootFields []string) {
		t.Helper()
		if err := conn.Exec(ctx, `INSERT INTO periscope.api_requests
			(timestamp, tenant_id, source_node, source_event_id, ingested_at_ms, auth_type, operation_name,
			 operation_type, request_count, error_count, total_duration_ms, total_complexity, user_hashes, token_hashes, root_fields)
			VALUES (?, ?, 'bridge-1', ?, ?, 'api_token', NULL, 'query', ?, 0, 10, 1, [], [], ?)`,
			window.Add(time.Minute), tenantID, sourceEventID, ingestedAt.UnixMilli(), requests, rootFields); err != nil {
			t.Fatal(err)
		}
	}
	rebuild := func(from, to time.Time) {
		t.Helper()
		if err := h.rebuildApiUsage5m(ctx, from, to); err != nil {
			t.Fatalf("rebuildApiUsage5m: %v", err)
		}
		time.Sleep(5 * time.Millisecond) // distinct projection_version_ms per rebuild
	}
	signatures := func() map[string]uint64 {
		t.Helper()
		rows, err := conn.Query(ctx, `SELECT arrayStringConcat(root_fields, ','), sum(requests)
			FROM periscope.api_usage_5m_v WHERE tenant_id = ? GROUP BY root_fields`, tenantID)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		out := map[string]uint64{}
		for rows.Next() {
			var signature string
			var requests uint64
			if err := rows.Scan(&signature, &requests); err != nil {
				t.Fatal(err)
			}
			out[signature] = requests
		}
		return out
	}

	// Requests stored before Bridge sent root fields carry an empty signature.
	earlier := time.Now().Add(-10 * time.Minute)
	insertRequest("pre-upgrade:0", earlier, 4, []string{})
	rebuild(earlier.Add(-time.Minute), earlier.Add(time.Minute))

	// A new Bridge batch through the real service-event path: two signatures of
	// one anonymous operation in the same window.
	batch := &ipcpb.APIRequestBatch{Timestamp: window.Unix(), SourceNode: "bridge-1", Aggregates: []*ipcpb.APIRequestAggregate{
		{TenantId: tenantID.String(), AuthType: "api_token", OperationType: "query", RequestCount: 3, Timestamp: window.Add(2 * time.Minute).Unix(), RootFields: []string{"clips", "streams"}},
		{TenantId: tenantID.String(), AuthType: "api_token", OperationType: "query", RequestCount: 2, Timestamp: window.Add(3 * time.Minute).Unix(), RootFields: []string{"invoices"}},
	}}
	before := time.Now()
	if err := h.HandleServiceEvent(kafka.ServiceEvent{
		EventID: uuid.NewString(), EventType: "api_request_batch", Timestamp: time.Now(), Source: "bridge",
		TenantID: tenantID.String(), Data: serviceBatchData(t, batch),
	}); err != nil {
		t.Fatalf("HandleServiceEvent: %v", err)
	}
	rebuild(before.Add(-time.Second), time.Now().Add(time.Second))

	want := map[string]uint64{"": 4, "clips,streams": 3, "invoices": 2}
	if got := signatures(); !maps.Equal(got, want) {
		t.Fatalf("signatures after split projection = %v, want %v", got, want)
	}

	// The pre-upgrade event replayed by a newer writer with root fields: its
	// counts stay under the empty signature instead of moving to "clips" while
	// the earlier projection's empty-signature row still holds them.
	replayAt := time.Now()
	insertRequest("pre-upgrade:0", replayAt, 4, []string{"clips"})
	rebuild(replayAt.Add(-time.Second), replayAt.Add(time.Second))

	if got := signatures(); !maps.Equal(got, want) {
		t.Fatalf("signatures after replay = %v, want %v (total must stay 9)", got, want)
	}
	var stored []string
	if err := conn.QueryRow(ctx, `SELECT root_fields FROM periscope.api_requests
		WHERE tenant_id = ? AND source_event_id LIKE '%:1' LIMIT 1`, tenantID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if strings.Join(stored, ",") != "invoices" {
		t.Fatalf("stored root_fields of the second aggregate = %v, want [invoices]", stored)
	}
}

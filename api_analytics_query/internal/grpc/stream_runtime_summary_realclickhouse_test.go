//go:build schema_verify

package grpc

import (
	"context"
	"math"
	"path/filepath"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/testutil/dockerch"
)

// A live session's runtime sits both in stream_runtime_5m (up to the ledger's
// last rebuild) and in stream_state_current. The summary counts the ledger's
// part once and adds only the live time after it.
func TestStreamRuntimeSummaryCountsLiveSessionOnce_RealClickHouse(t *testing.T) {
	root, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	db := dockerch.StartCurrent(t, root, "fw-stream-runtime-summary").SQL
	ctx := context.Background()
	const tenantID = "5eed0001-7a11-4e5e-8001-5eed00000001"
	const streamID = "5eed0001-7a11-4e5e-8001-5eed00000002"
	const node, cluster, internal = "edge-live", "media-live", "summary-live"

	now := time.Now().UTC().Truncate(time.Second)
	startedAt := now.Add(-20 * time.Minute)
	coveredUntil := now.Add(-5 * time.Minute)

	if _, err := db.ExecContext(ctx, `INSERT INTO periscope.stream_event_log
		(event_id, timestamp, tenant_id, stream_id, internal_name, node_id, cluster_id, event_type, status, event_data)
		VALUES (generateUUIDv4(), ?, ?, ?, ?, ?, ?, 'stream_start', 'live', '{}')`,
		startedAt, tenantID, streamID, internal, node, cluster); err != nil {
		t.Fatalf("seed stream_start: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO periscope.stream_state_current
		(tenant_id, stream_id, internal_name, node_id, cluster_id, status, buffer_state,
		 current_viewers, total_inputs, uploaded_bytes, downloaded_bytes, viewer_seconds, started_at, updated_at)
		VALUES (?, ?, ?, ?, ?, 'live', 'FULL', 3, 1, 0, 0, 0, ?, ?)`,
		tenantID, streamID, internal, node, cluster, startedAt.Add(time.Second), now.Add(-30*time.Second)); err != nil {
		t.Fatalf("seed live stream_state_current: %v", err)
	}
	for window := startedAt.Truncate(5 * time.Minute); window.Before(coveredUntil); window = window.Add(5 * time.Minute) {
		from, to := window, window.Add(5*time.Minute)
		if from.Before(startedAt) {
			from = startedAt
		}
		if to.After(coveredUntil) {
			to = coveredUntil
		}
		if _, err := db.ExecContext(ctx, `INSERT INTO periscope.stream_runtime_5m
			(window_start, tenant_id, cluster_id, stream_id, source_event_id, active_seconds, peak_viewers, projection_version_ms)
			VALUES (?, ?, ?, ?, 'live-session', ?, 3, ?)`,
			window, tenantID, cluster, streamID, uint32(to.Sub(from)/time.Second), now.UnixMilli()); err != nil {
			t.Fatalf("seed live ledger window %s: %v", window, err)
		}
	}

	server := NewPeriscopeServer(db, logging.NewLogger())
	hours, _, streams, err := server.queryStreamRuntimeSummary(ctx, tenantID, now.Add(-time.Hour), now.Add(time.Hour))
	if err != nil {
		t.Fatalf("queryStreamRuntimeSummary: %v", err)
	}
	elapsed := time.Since(startedAt).Seconds()
	if got := hours * 3600; math.Abs(got-elapsed) > 5 || streams != 1 {
		t.Fatalf("stream runtime summary = %.0fs over %d streams, want one stream of %.0fs", got, streams, elapsed)
	}
}

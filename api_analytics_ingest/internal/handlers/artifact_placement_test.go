package handlers

import (
	"context"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/kafka"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"

	"github.com/google/uuid"
)

func nodeCopyEvent(tenant string, data map[string]any) kafka.ServiceEvent {
	return kafka.ServiceEvent{
		EventID:   uuid.NewString(),
		EventType: "artifact_node_copy",
		Timestamp: time.Unix(1710000000, 0),
		TenantID:  tenant,
		Data:      data,
	}
}

// A transition/role outside the supported contract must be rejected, not coerced into
// a present copy — otherwise malformed input silently corrupts current state.
func TestProcessArtifactNodeCopy_RejectsUnsupportedValues(t *testing.T) {
	tenant := uuid.NewString()
	base := func() map[string]any {
		return map[string]any{
			"artifact_hash": "abc123def456",
			"node_id":       "node-1",
			"role":          "cache",
			"transition":    "GAINED",
			"version":       int64(7),
		}
	}
	cases := []struct {
		name   string
		mutate func(map[string]any)
	}{
		{"empty transition", func(d map[string]any) { d["transition"] = "" }},
		{"unspecified transition", func(d map[string]any) { d["transition"] = "TRANSITION_UNSPECIFIED" }},
		{"misspelled transition", func(d map[string]any) { d["transition"] = "gaind" }},
		{"empty role", func(d map[string]any) { d["role"] = "" }},
		{"unknown role", func(d map[string]any) { d["role"] = "mirror" }},
		{"missing version", func(d map[string]any) { delete(d, "version") }},
		{"zero version", func(d map[string]any) { d["version"] = int64(0) }},
		// version as a protojson string must parse (not be rejected as missing).
		{"unknown role, string version", func(d map[string]any) { d["role"] = "x"; d["version"] = "7" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			conn := newFakeClickhouseConn()
			h := NewAnalyticsHandler(conn, logging.NewLogger(), nil)
			data := base()
			tc.mutate(data)
			if err := h.processArtifactNodeCopy(context.Background(), nodeCopyEvent(tenant, data)); err == nil {
				t.Fatalf("expected error for %q, got nil", tc.name)
			}
			if len(conn.batches) != 0 {
				t.Fatalf("expected no write for %q, got %#v", tc.name, conn.batches)
			}
		})
	}
}

// GAINED/UPDATED mark the node present; LOST clears it. All dual-write the log and
// current tables.
func TestProcessArtifactNodeCopy_PresenceByTransition(t *testing.T) {
	tenant := uuid.NewString()
	for _, tc := range []struct {
		transition string
		present    bool
	}{
		{"GAINED", true},
		{"UPDATED", true},
		{"LOST", false},
	} {
		t.Run(tc.transition, func(t *testing.T) {
			conn := newFakeClickhouseConn()
			h := NewAnalyticsHandler(conn, logging.NewLogger(), nil)
			data := map[string]any{
				"artifact_hash": "abc123def456",
				"node_id":       "node-1",
				"role":          "origin",
				"transition":    tc.transition,
				"is_complete":   true,
				"version":       int64(7),
			}
			if err := h.processArtifactNodeCopy(context.Background(), nodeCopyEvent(tenant, data)); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			logBatch := conn.batches["artifact_node_copy_events"]
			curBatch := conn.batches["artifact_node_copy_current"]
			if logBatch == nil || len(logBatch.rows) != 1 {
				t.Fatalf("log table: want 1 row, got %#v", logBatch)
			}
			if curBatch == nil || len(curBatch.rows) != 1 {
				t.Fatalf("current table: want 1 row, got %#v", curBatch)
			}
			// present is column index 4 in the current-state INSERT.
			if got := curBatch.rows[0][4]; got != tc.present {
				t.Fatalf("present = %v, want %v", got, tc.present)
			}
		})
	}
}

// version arrives as a protojson-encoded decimal string over the Decklog transport;
// it must parse and land as the ClickHouse version (column index 7), not be dropped.
func TestProcessArtifactNodeCopy_ParsesStringVersion(t *testing.T) {
	conn := newFakeClickhouseConn()
	h := NewAnalyticsHandler(conn, logging.NewLogger(), nil)
	data := map[string]any{
		"artifact_hash": "abc123def456",
		"node_id":       "node-1",
		"role":          "cache",
		"transition":    "GAINED",
		"is_complete":   false,
		"version":       "42", // protojson int64/uint64 → JSON string
		"size_bytes":    "1048576",
	}
	if err := h.processArtifactNodeCopy(context.Background(), nodeCopyEvent(uuid.NewString(), data)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	cur := conn.batches["artifact_node_copy_current"]
	if cur == nil || len(cur.rows) != 1 {
		t.Fatalf("current table: want 1 row, got %#v", cur)
	}
	// version is column index 7 in the current-state INSERT.
	if got := cur.rows[0][7]; got != uint64(42) {
		t.Fatalf("version = %v (%T), want uint64(42)", got, got)
	}
}

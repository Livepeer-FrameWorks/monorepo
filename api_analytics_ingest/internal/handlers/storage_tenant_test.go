package handlers

import (
	"context"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/kafka"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"

	"github.com/google/uuid"
)

// TestProcessStorageSnapshotDefaultsAndSkipsInvalidTenant covers the hot-storage
// ingest path: a missing storage_scope defaults to "hot" with an "edge_disk"
// backend, the ingest wall-clock is stamped, and a usage row whose tenant_id is
// not a UUID is dropped mid-loop rather than written with a garbage key.
func TestProcessStorageSnapshotDefaultsAndSkipsInvalidTenant(t *testing.T) {
	conn := newFakeClickhouseConn()
	h := NewAnalyticsHandler(conn, logging.NewLogger(), nil)
	goodTenant := uuid.NewString()
	clusterID := "cluster-1"

	data := mustMistTriggerData(t, &ipcpb.MistTrigger{
		NodeId:    "node-1",
		ClusterId: &clusterID,
		TriggerPayload: &ipcpb.MistTrigger_StorageSnapshot{
			StorageSnapshot: &ipcpb.StorageSnapshot{
				NodeId: "node-1",
				Usage: []*ipcpb.TenantStorageUsage{
					{TenantId: goodTenant, TotalBytes: 1024, DvrBytes: 512},
					{TenantId: "not-a-uuid", TotalBytes: 99},
				},
			},
		},
	})
	event := kafka.AnalyticsEvent{
		EventID:   uuid.NewString(),
		EventType: "storage_snapshot",
		Timestamp: time.Unix(1710000000, 0),
		Data:      data,
	}

	if err := h.processStorageSnapshot(context.Background(), event); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	batch := conn.batches["storage_snapshots"]
	if batch == nil || !batch.sent {
		t.Fatalf("expected a sent storage_snapshots batch, got %#v", batch)
	}
	if len(batch.rows) != 1 {
		t.Fatalf("expected 1 row (invalid tenant dropped), got %d", len(batch.rows))
	}
	row := batch.rows[0]
	// Columns: ts, node, tenant, cluster, scope, providerTenant, providerCluster, backend, totalBytes, ...
	if row[2] != goodTenant {
		t.Errorf("tenant_id = %#v, want %s", row[2], goodTenant)
	}
	if row[3] != "cluster-1" {
		t.Errorf("cluster_id = %#v, want cluster-1", row[3])
	}
	if row[4] != "hot" {
		t.Errorf("storage_scope = %#v, want defaulted hot", row[4])
	}
	if row[7] != "edge_disk" {
		t.Errorf("storage_backend = %#v, want hot default edge_disk", row[7])
	}
	if row[0] != event.Timestamp {
		t.Errorf("timestamp = %#v, want event timestamp fallback", row[0])
	}
	if ms, ok := row[16].(int64); !ok || ms <= 0 {
		t.Errorf("ingested_at_ms = %#v, want a positive wall-clock stamp", row[16])
	}
}

// TestProcessStorageSnapshotColdScopeUsesS3Backend pins the backend-by-scope
// rule for cold storage: an unspecified backend on a cold snapshot resolves to
// "s3", not the hot-path edge_disk default.
func TestProcessStorageSnapshotColdScopeUsesS3Backend(t *testing.T) {
	conn := newFakeClickhouseConn()
	h := NewAnalyticsHandler(conn, logging.NewLogger(), nil)

	coldScope := "cold"
	data := mustMistTriggerData(t, &ipcpb.MistTrigger{
		NodeId: "node-1",
		TriggerPayload: &ipcpb.MistTrigger_StorageSnapshot{
			StorageSnapshot: &ipcpb.StorageSnapshot{
				NodeId:       "node-1",
				StorageScope: &coldScope,
				Usage:        []*ipcpb.TenantStorageUsage{{TenantId: uuid.NewString(), VodBytes: 4096}},
			},
		},
	})
	event := kafka.AnalyticsEvent{EventID: uuid.NewString(), EventType: "storage_snapshot", Timestamp: time.Unix(1710000000, 0), Data: data}

	if err := h.processStorageSnapshot(context.Background(), event); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	row := conn.batches["storage_snapshots"].rows[0]
	if row[4] != "cold" || row[7] != "s3" {
		t.Fatalf("scope/backend = %#v/%#v, want cold/s3", row[4], row[7])
	}
}

// TestProcessTenantCreatedEarlyExits proves the data-quality gate: a
// tenant_created audit row is only written when the event carries a valid
// tenant, an attribution map, and a signup_channel. Each missing piece short-
// circuits to a clean no-op with no ClickHouse write.
func TestProcessTenantCreatedEarlyExits(t *testing.T) {
	cases := []struct {
		name  string
		event kafka.ServiceEvent
	}{
		{"invalid tenant", kafka.ServiceEvent{TenantID: "not-a-uuid", Data: map[string]any{"attribution": map[string]any{"signup_channel": "web"}}}},
		{"no attribution", kafka.ServiceEvent{TenantID: uuid.NewString(), Data: map[string]any{}}},
		{"blank signup_channel", kafka.ServiceEvent{TenantID: uuid.NewString(), Data: map[string]any{"attribution": map[string]any{"signup_channel": ""}}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			conn := newFakeClickhouseConn()
			h := NewAnalyticsHandler(conn, logging.NewLogger(), nil)
			if err := h.processTenantCreated(context.Background(), tc.event); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(conn.batches) != 0 {
				t.Fatalf("expected no write for %q, got %#v", tc.name, conn.batches)
			}
		})
	}
}

// TestProcessTenantCreatedWritesAttribution covers the happy path: UTM fields
// present become values, absent ones coerce to nil (so they read as NULL, not
// empty string), and is_agent maps to its uint8 flag.
func TestProcessTenantCreatedWritesAttribution(t *testing.T) {
	conn := newFakeClickhouseConn()
	h := NewAnalyticsHandler(conn, logging.NewLogger(), nil)
	tenant := uuid.New()

	event := kafka.ServiceEvent{
		EventID:   uuid.NewString(),
		EventType: "tenant_created",
		Timestamp: time.Unix(1710000000, 0),
		TenantID:  tenant.String(),
		Data: map[string]any{
			"attribution": map[string]any{
				"signup_channel": "web",
				"utm_source":     "newsletter",
				"is_agent":       true,
				// utm_medium intentionally absent -> should be nil
			},
		},
	}

	if err := h.processTenantCreated(context.Background(), event); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	batch := conn.batches["tenant_acquisition_events"]
	if batch == nil || len(batch.rows) != 1 || !batch.sent {
		t.Fatalf("expected one sent acquisition row, got %#v", batch)
	}
	row := batch.rows[0]
	// Columns: ts, tenant, user, signup_channel, signup_method, utm_source, utm_medium, ... is_agent(13)
	if row[1] != tenant {
		t.Errorf("tenant_id = %#v, want %v", row[1], tenant)
	}
	if row[3] != "web" {
		t.Errorf("signup_channel = %#v, want web", row[3])
	}
	if row[5] != "newsletter" {
		t.Errorf("utm_source = %#v, want newsletter", row[5])
	}
	if row[6] != nil {
		t.Errorf("absent utm_medium = %#v, want nil (NULL)", row[6])
	}
	if row[13] != uint8(1) {
		t.Errorf("is_agent = %#v, want uint8(1)", row[13])
	}
}

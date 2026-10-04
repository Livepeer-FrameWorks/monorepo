package handlers

import (
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/kafka"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	"github.com/google/uuid"
)

const testArtifactHash = "20261004093042f2da667dcd8ab1ee"

func artifactEvent(t *testing.T, eventType string, trigger *ipcpb.MistTrigger) kafka.AnalyticsEvent {
	t.Helper()
	return kafka.AnalyticsEvent{
		EventID: uuid.NewString(), EventType: eventType, Timestamp: time.Now(),
		Source: "decklog", TenantID: uuid.NewString(), Data: mustMistTriggerData(t, trigger),
	}
}

// A viewer of an uploaded VOD has no stream; its connection is kept under the
// artifact, with the zero stream_id.
func TestViewerConnectKeepsArtifactOnlyContent(t *testing.T) {
	conn := newFakeClickhouseConn()
	handler := NewAnalyticsHandler(conn, logging.NewLogger(), nil)
	hash := testArtifactHash
	event := artifactEvent(t, "viewer_connect", &ipcpb.MistTrigger{
		ArtifactHash: &hash,
		TriggerPayload: &ipcpb.MistTrigger_ViewerConnect{ViewerConnect: &ipcpb.ViewerConnectTrigger{
			StreamName: "vod+vodInternal", SessionId: "sess-vod", Connector: "hls", Host: "1.2.3.4",
		}},
	})

	if err := handler.HandleAnalyticsEvent(event); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	batch := conn.batches["viewer_connection_events"]
	if batch == nil || len(batch.rows) != 1 {
		t.Fatalf("expected a viewer_connection_events row, got %#v", batch)
	}
	if got := batch.rows[0][3]; got != uuid.Nil {
		t.Fatalf("stream_id = %#v, want the zero UUID", got)
	}
	if got := batch.rows[0][4]; got != testArtifactHash {
		t.Fatalf("artifact_hash = %#v, want %s", got, testArtifactHash)
	}
}

func TestLoadBalancingKeepsArtifactOnlyContent(t *testing.T) {
	conn := newFakeClickhouseConn()
	handler := NewAnalyticsHandler(conn, logging.NewLogger(), nil)
	hash := testArtifactHash
	status := "success"
	event := artifactEvent(t, "load_balancing", &ipcpb.MistTrigger{
		TriggerType: "LOAD_BALANCING",
		TriggerPayload: &ipcpb.MistTrigger_LoadBalancingData{LoadBalancingData: &ipcpb.LoadBalancingData{
			ArtifactHash: &hash, Status: status, SelectedNode: "edge-1",
		}},
	})

	if err := handler.HandleAnalyticsEvent(event); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	batch := conn.batches["routing_decisions"]
	if batch == nil || len(batch.rows) != 1 {
		t.Fatalf("expected a routing_decisions row, got %#v", batch)
	}
	if got := batch.rows[0][3]; got != testArtifactHash {
		t.Fatalf("artifact_hash = %#v, want %s", got, testArtifactHash)
	}
}

// A client QoE batch keeps samples that name their content by stream or
// artifact and skips those that name neither.
func TestClientLifecycleBatchKeepsArtifactSamples(t *testing.T) {
	conn := newFakeClickhouseConn()
	handler := NewAnalyticsHandler(conn, logging.NewLogger(), nil)
	hash := testArtifactHash
	event := artifactEvent(t, "client_lifecycle_batch", &ipcpb.MistTrigger{
		TriggerType: "CLIENT_LIFECYCLE_BATCH",
		TriggerPayload: &ipcpb.MistTrigger_ClientLifecycleBatch{ClientLifecycleBatch: &ipcpb.ClientLifecycleBatch{
			NodeId: "edge-1", ArtifactHash: &hash,
			Samples: []*ipcpb.ClientLifecycleUpdate{{NodeId: "edge-1", InternalName: "vod+vodInternal"}},
		}},
	})

	if err := handler.HandleAnalyticsEvent(event); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	batch := conn.batches["client_qoe_samples"]
	if batch == nil || len(batch.rows) != 1 {
		t.Fatalf("expected a client_qoe_samples row, got %#v", batch)
	}
	if got := batch.rows[0][4]; got != testArtifactHash {
		t.Fatalf("artifact_hash = %#v, want %s", got, testArtifactHash)
	}

	none := newFakeClickhouseConn()
	handler = NewAnalyticsHandler(none, logging.NewLogger(), nil)
	event = artifactEvent(t, "client_lifecycle_batch", &ipcpb.MistTrigger{
		TriggerType: "CLIENT_LIFECYCLE_BATCH",
		TriggerPayload: &ipcpb.MistTrigger_ClientLifecycleBatch{ClientLifecycleBatch: &ipcpb.ClientLifecycleBatch{
			NodeId: "edge-1", Samples: []*ipcpb.ClientLifecycleUpdate{{NodeId: "edge-1", InternalName: "vod+other"}},
		}},
	})
	if err := handler.HandleAnalyticsEvent(event); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if batch := none.batches["client_qoe_samples"]; batch != nil && len(batch.rows) != 0 {
		t.Fatalf("a sample naming no content was written: %#v", batch.rows)
	}
}

func TestMistTriggerArtifactHashPrefersEnvelopeAndValidates(t *testing.T) {
	envelope, payload, bad := testArtifactHash, "20261004093042aaaaaaaaaaaaaaaa", "not a hash!"
	cases := []struct {
		name string
		mt   *ipcpb.MistTrigger
		want string
	}{
		{"envelope", &ipcpb.MistTrigger{ArtifactHash: &envelope, TriggerPayload: &ipcpb.MistTrigger_ViewerConnect{ViewerConnect: &ipcpb.ViewerConnectTrigger{ArtifactHash: &payload}}}, envelope},
		{"payload", &ipcpb.MistTrigger{TriggerPayload: &ipcpb.MistTrigger_ViewerDisconnect{ViewerDisconnect: &ipcpb.ViewerDisconnectTrigger{ArtifactHash: &payload}}}, payload},
		{"invalid", &ipcpb.MistTrigger{ArtifactHash: &bad}, ""},
		{"none", &ipcpb.MistTrigger{}, ""},
	}
	for _, tc := range cases {
		if got := mistTriggerArtifactHash(tc.mt); got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}

package handlers

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/kafka"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	obs "github.com/Livepeer-FrameWorks/monorepo/pkg/networkobservation"
	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	"github.com/google/uuid"
	"google.golang.org/protobuf/encoding/protojson"
)

func TestNetworkDiscoveryAdmissionDoesNotBecomeDowntime(t *testing.T) {
	for _, tc := range []struct {
		name, failure                string
		success, healthy, qualifying bool
	}{
		{name: "success", success: true, healthy: true, qualifying: true},
		{name: "network failure", failure: "transport", healthy: true, qualifying: true},
		{name: "identity mismatch", failure: "identity_mismatch", healthy: true, qualifying: true},
		{name: "unhealthy observer", failure: "transport"},
		{name: "cancelled", failure: "observer_cancelled", healthy: true},
		{name: "gateway cancelled", failure: "canceled", healthy: true},
		{name: "configuration", failure: "observer_configuration", healthy: true},
		{name: "ambiguous socket", failure: "ambiguous_attribution", healthy: true},
		{name: "authentication", failure: "authentication", healthy: true},
		{name: "sender reserve", failure: "observer_payment", healthy: true},
		{name: "capacity", failure: "capacity", healthy: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			conn := newFakeClickhouseConn()
			handler := NewAnalyticsHandler(conn, logging.NewLogger(), nil)
			raw, err := protojson.Marshal(&pb.OrchestratorDiscoveryObserved{
				OrchAddr: "0xabc", OrchUrl: "https://orch.example", TargetIp: "203.0.113.1",
				Reachable: tc.success, ObserverHealthy: tc.healthy, FailureKind: tc.failure,
			})
			if err != nil {
				t.Fatal(err)
			}
			data := map[string]any{}
			if err := json.Unmarshal(raw, &data); err != nil {
				t.Fatal(err)
			}
			data["cluster_owner_tenant_id"] = uuid.NewString()
			data["network_id"], data["environment"] = "42161", "staging"
			data["producer_kind"], data["gateway_id"], data["gateway_region"] = "prober", "test-prober", "eu"
			if err := handler.processNetworkObservation(context.Background(), kafka.AnalyticsEvent{
				EventID: uuid.NewString(), EventType: "orchestrator_discovery_observed", SchemaVersion: 3,
				Timestamp: time.Now().UTC(), Data: data,
			}); err != nil {
				t.Fatal(err)
			}
			batch := conn.batches["network_observations"]
			if batch == nil || !batch.sent || len(batch.rows) != 1 {
				t.Fatal("discovery was not retained")
			}
			var metric obs.Metric
			if err := json.Unmarshal([]byte(batch.rows[0][31].(string)), &metric); err != nil {
				t.Fatal(err)
			}
			if metric.Qualifying != tc.qualifying || metric.FailureKind != tc.failure || metric.Success != tc.success {
				t.Fatalf("persisted discovery qualifying=%v failure=%q success=%v, want %v/%q/%v", metric.Qualifying, metric.FailureKind, metric.Success, tc.qualifying, tc.failure, tc.success)
			}
			want := uint8(0)
			if tc.qualifying {
				want = 1
			}
			if batch.rows[0][16] != want {
				t.Fatalf("aggregate qualification=%v, want %v", batch.rows[0][16], want)
			}
		})
	}
}

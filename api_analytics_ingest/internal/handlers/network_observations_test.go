package handlers

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/kafka"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	obs "github.com/Livepeer-FrameWorks/monorepo/pkg/networkobservation"
	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	"github.com/google/uuid"
	"google.golang.org/protobuf/encoding/protojson"
)

func TestNetworkStatePreservesInstanceConfiguration(t *testing.T) {
	for _, producer := range []string{"prober", "gateway"} {
		for _, resolvedIP := range []string{"203.0.113.1", ""} {
			t.Run(producer+"/"+resolvedIP, func(t *testing.T) {
				conn := newFakeClickhouseConn()
				handler := NewAnalyticsHandler(conn, logging.NewLogger(), nil)
				owner := uuid.NewString()
				ts := time.Now().UTC().Truncate(time.Second)
				msg := &pb.OrchestratorStateUpdate{
					OrchAddr: "0xabc", CanonicalUrl: "https://orch.example:8935",
					AdvertisedNodeUrls: []string{"https://node.example:8935"},
					Capabilities:       []string{"caps:0:0000000000000001", "caps:1:0000000000000002"},
					Hardware:           `[{"name":"test GPU"}]`, Source: producer,
					PriceKnown: true, PricePerUnit: 7, PixelsPerUnit: 100,
					PriceCurrency: "ETH", PriceUnit: "wei/pixel",
					Vantage: &pb.OrchestratorVantageGeo{ResolvedIp: resolvedIP, Dialed: resolvedIP != ""},
				}
				identity := fullIdentity()
				identity["cluster_owner_tenant_id"] = owner
				identity["producer_kind"], identity["network_id"], identity["environment"] = producer, "42161", "staging"
				event := orchestratorEvent(t, owner, ts, msg, identity)
				event.EventID, event.EventType, event.SchemaVersion = uuid.NewString(), "orchestrator_state_update", 3
				if err := handler.processNetworkObservation(t.Context(), event); err != nil {
					t.Fatal(err)
				}
				observations := conn.batches["network_observations"]
				if observations == nil || !observations.sent || len(observations.rows) != 1 {
					t.Fatal("network state observation was not retained")
				}
				var metric obs.Metric
				if err := json.Unmarshal([]byte(observations.rows[0][31].(string)), &metric); err != nil {
					t.Fatal(err)
				}
				if metric.Price == nil || metric.Price.Numerator != "7" || metric.Price.Denominator != "100" {
					t.Fatalf("network state price lost: %+v", metric.Price)
				}
				instance := conn.batches["orchestrator_instance_state_current"]
				if resolvedIP == "" {
					if instance != nil {
						t.Fatal("instance state written without resolved IP")
					}
				} else {
					if instance == nil || !instance.sent || len(instance.rows) != 1 {
						t.Fatal("instance configuration was not retained")
					}
					row := instance.rows[0]
					if row[0] != uuid.MustParse(owner) || row[1] != msg.OrchAddr || row[2] != resolvedIP || row[3] != msg.CanonicalUrl || !reflect.DeepEqual(row[4], msg.AdvertisedNodeUrls) || !reflect.DeepEqual(row[5], msg.Capabilities) || row[6] != int64(7) || row[7] != int64(100) || row[12] != msg.Hardware || row[13] != producer || row[14] != ts || row[16] != ts {
						t.Fatalf("instance configuration mapping lost: %+v", row)
					}
				}
				for _, table := range []string{"orchestrator_vantage_current", "orchestrator_discovery_samples", "orchestrator_transcode_outcomes"} {
					if conn.batches[table] != nil {
						t.Errorf("state update unexpectedly wrote %s", table)
					}
				}
			})
		}
	}
}

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

func TestNetworkAttemptPreservesExactWorkAccounting(t *testing.T) {
	conn := newFakeClickhouseConn()
	handler := NewAnalyticsHandler(conn, logging.NewLogger(), nil)
	raw, err := protojson.Marshal(&pb.OrchestratorTranscodeOutcome{
		OrchAddr: "0xABC", OrchUrl: "https://orch.example", ResolvedIp: "203.0.113.1", RemoteIpVerified: true,
		Success: true, Pixels: 9007199254740993, SourceDurationMs: 2000, Disposition: "hedge_loser",
		Accounting: &pb.OrchestratorAttemptAccounting{Version: 1, RequestDispatched: true, ReturnedPixelsKnown: true,
			ServiceCharge:    &pb.NetworkAmount{Numerator: "2", Denominator: "3", Currency: "ETH", Unit: "wei"},
			TicketCreditSent: &pb.NetworkAmount{Numerator: "0", Denominator: "1", Currency: "ETH", Unit: "wei"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	data := map[string]any{}
	if err := json.Unmarshal(raw, &data); err != nil {
		t.Fatal(err)
	}
	data["cluster_owner_tenant_id"], data["stream_tenant_id"] = uuid.NewString(), uuid.NewString()
	data["network_id"], data["environment"] = "42161", "staging"
	data["producer_kind"], data["gateway_id"], data["gateway_region"] = "gateway", "private-gateway", "eu"
	if err := handler.processNetworkObservation(context.Background(), kafka.AnalyticsEvent{EventID: uuid.NewString(), EventType: "orchestrator_transcode_outcome", SchemaVersion: 3, Timestamp: time.Now().UTC(), Data: data}); err != nil {
		t.Fatal(err)
	}
	batch := conn.batches["network_observations"]
	if batch == nil || !batch.sent || len(batch.rows) != 1 {
		t.Fatal("attempt was not retained")
	}
	var metric obs.Metric
	if err := json.Unmarshal([]byte(batch.rows[0][31].(string)), &metric); err != nil {
		t.Fatal(err)
	}
	if metric.Work == nil || metric.Work.ReturnedPixels != 9007199254740993 || !metric.Work.RequestDispatched || metric.Work.ServiceCharge == nil || metric.Work.ServiceCharge.Numerator != "2" || metric.Work.ServiceCharge.Denominator != "3" || metric.Work.TicketCreditSent == nil {
		t.Fatalf("exact returned work/charge/known zero credit lost: %+v", metric.Work)
	}
	if metric.Qualifying || metric.ObserverID != "" {
		t.Fatal("hedged work changed qualification or exposed gateway identity")
	}
}

func TestNetworkAttemptDispositionsThatDoNotMeasureTheOrchestrator(t *testing.T) {
	for disposition, wantQualifying := range map[string]bool{
		"accepted":        true,
		"input_fault":     false,
		"hedge_loser":     false,
		"local_cancelled": false,
		"session_warmup":  false,
	} {
		t.Run(disposition, func(t *testing.T) {
			conn := newFakeClickhouseConn()
			handler := NewAnalyticsHandler(conn, logging.NewLogger(), nil)
			raw, err := protojson.Marshal(&pb.OrchestratorTranscodeOutcome{
				OrchAddr: "0xABC", OrchUrl: "https://orch.example", ResolvedIp: "203.0.113.1", RemoteIpVerified: true,
				Success: true, Pixels: 100, SourceDurationMs: 33, Disposition: disposition,
			})
			if err != nil {
				t.Fatal(err)
			}
			data := map[string]any{}
			if err := json.Unmarshal(raw, &data); err != nil {
				t.Fatal(err)
			}
			data["cluster_owner_tenant_id"], data["stream_tenant_id"] = uuid.NewString(), uuid.NewString()
			data["network_id"], data["environment"] = "42161", "staging"
			data["producer_kind"], data["gateway_id"], data["gateway_region"] = "gateway", "private-gateway", "eu"
			if err := handler.processNetworkObservation(context.Background(), kafka.AnalyticsEvent{EventID: uuid.NewString(), EventType: "orchestrator_transcode_outcome", SchemaVersion: 3, Timestamp: time.Now().UTC(), Data: data}); err != nil {
				t.Fatal(err)
			}
			batch := conn.batches["network_observations"]
			if batch == nil || len(batch.rows) != 1 {
				t.Fatal("attempt was not retained")
			}
			var metric obs.Metric
			if err := json.Unmarshal([]byte(batch.rows[0][31].(string)), &metric); err != nil {
				t.Fatal(err)
			}
			if metric.Qualifying != wantQualifying {
				t.Fatalf("disposition %s qualifying=%v, want %v", disposition, metric.Qualifying, wantQualifying)
			}
		})
	}
}

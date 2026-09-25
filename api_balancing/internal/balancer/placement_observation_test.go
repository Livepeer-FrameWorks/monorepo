package balancer

import (
	"math"
	"testing"
	"time"

	"frameworks/api_balancing/internal/state"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/placement"
)

func placementObservationFixture() PlacementObservationRequest {
	now := time.Unix(1800000000, 0)
	return PlacementObservationRequest{
		TenantID: "tenant", Verb: placement.Serve, InternalName: "stream", Protocol: "hls", Now: now,
		Clusters: map[string]PlacementClusterFacts{"private": {OwnerTenantID: "tenant", AllowedVerbs: []placement.Verb{placement.Ingest, placement.Serve}}},
		Paths:    map[string]PlacementNodePath{"node": {Presence: placement.Absent, SourceFeasible: true}},
		Snapshot: &state.BalancerSnapshot{Nodes: []state.EnhancedBalancerNodeSnapshot{{
			NodeID: "node", ClusterID: "private", TenantID: "untrusted-node-owner", Host: "https://edge.example",
			IsActive: true, CapIngest: true, CapEdge: true, MetricsObservedAt: now.Add(-time.Second), LastHeartbeat: now,
			OutputsObservedAt: now, Outputs: map[string]any{"HLS": "http://HOST:8080/hls/$/index.m3u8", "RTMP": "rtmp://HOST:1935/play/$"},
			CPU: 25, RAMMax: 10000, RAMCurrent: 1000, BWLimit: 1000, UpSpeed: 400, DownSpeed: 200, AddBandwidth: 100,
		}}},
	}
}

func TestPlacementObservationDetachesAllCommercialQuotes(t *testing.T) {
	req := placementObservationFixture()
	facts := req.Clusters["private"]
	facts.Charging, facts.ChargingRevision, facts.ChargingUntil = placement.Rated, "quotes-1", req.Now.Add(time.Minute)
	facts.Prices = []placement.Price{{Currency: "EUR", Unit: "serve:minutes=1;gib=2", AmountMicros: 10, Revision: facts.ChargingRevision, ExpiresAt: facts.ChargingUntil},
		{Currency: "EUR", Unit: "serve:minutes=60;gib=2", AmountMicros: 30, Revision: facts.ChargingRevision, ExpiresAt: facts.ChargingUntil}}
	req.Clusters["private"] = facts
	candidate := observeOne(t, req)
	wire, err := placement.CandidateToProto(candidate, req.Verb)
	if err != nil || len(wire.GetCommercialFacts().GetServePrices()) != 2 {
		t.Fatalf("lost observed quotes: %+v %v", wire, err)
	}
	candidate.Prices[0].AmountMicros = 100
	if facts.Prices[0].AmountMicros != 10 {
		t.Fatal("candidate mutated cluster quotes")
	}
	facts.Prices[1].AmountMicros = 100
	if candidate.Prices[1].AmountMicros != 30 {
		t.Fatal("cluster mutated candidate quotes")
	}
}

func observeOne(t *testing.T, req PlacementObservationRequest) placement.Candidate {
	t.Helper()
	nodes, err := ObservePlacementNodes(req)
	if err != nil || len(nodes) != 1 {
		t.Fatalf("observe: %v, %v", nodes, err)
	}
	return nodes[0]
}

func TestObservePlacementNodesUsesTenantFactsAndDirectionalCapacity(t *testing.T) {
	req := placementObservationFixture()
	serve := observeOne(t, req)
	if serve.OwnerTenantID != "tenant" || serve.BWAvailable != 500 || serve.BWLimit != 1000 || serve.Presence != placement.Absent || !serve.SourceFeasible || serve.Location != nil {
		t.Fatalf("incorrect cold edge facts: %+v", serve)
	}
	req.Verb = placement.Ingest
	req.Protocol = "rtmp"
	ingest := observeOne(t, req)
	if ingest.BWAvailable != 800 {
		t.Fatalf("ingest used viewer egress headroom: %d", ingest.BWAvailable)
	}
	serve.AllowedVerbs[0] = "corrupt"
	if req.Clusters["private"].AllowedVerbs[0] != placement.Ingest {
		t.Fatal("observation aliases authority facts")
	}
}

func TestObservePlacementNodesNeverTurnsUnknownIntoExhaustion(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*state.EnhancedBalancerNodeSnapshot)
		want   placement.Reason
	}{
		{"missing_metrics", func(n *state.EnhancedBalancerNodeSnapshot) { n.MetricsObservedAt = time.Time{} }, placement.StaleTelemetry},
		{"stale_metrics_fresh_config", func(n *state.EnhancedBalancerNodeSnapshot) {
			n.MetricsObservedAt = n.LastHeartbeat.Add(-time.Minute)
			n.LastUpdate = n.LastHeartbeat
		}, placement.StaleTelemetry},
		{"stale_heartbeat", func(n *state.EnhancedBalancerNodeSnapshot) { n.LastHeartbeat = n.MetricsObservedAt.Add(-time.Minute) }, placement.StaleTelemetry},
		{"future_metrics", func(n *state.EnhancedBalancerNodeSnapshot) { n.MetricsObservedAt = n.LastHeartbeat.Add(time.Minute) }, placement.StaleTelemetry},
		{"missing_listener_stamp", func(n *state.EnhancedBalancerNodeSnapshot) { n.OutputsObservedAt = time.Time{} }, placement.StaleTelemetry},
		{"stale_listener_fresh_metrics", func(n *state.EnhancedBalancerNodeSnapshot) {
			n.OutputsObservedAt = n.LastHeartbeat.Add(-30 * time.Second)
		}, placement.StaleTelemetry},
		{"future_listener", func(n *state.EnhancedBalancerNodeSnapshot) {
			n.OutputsObservedAt = n.LastHeartbeat.Add(time.Nanosecond)
		}, placement.StaleTelemetry},
		{"missing_listener", func(n *state.EnhancedBalancerNodeSnapshot) { n.Outputs = nil }, placement.NodeUnavailable},
		{"http_is_not_hls", func(n *state.EnhancedBalancerNodeSnapshot) {
			n.Outputs = map[string]any{"HTTP": "http://HOST:8080/$.html"}
		}, placement.NodeUnavailable},
		{"malformed_listener", func(n *state.EnhancedBalancerNodeSnapshot) {
			n.Outputs = map[string]any{"HLS": "http://user:pass@HOST:8080/hls/$/index.m3u8"}
		}, placement.NodeUnavailable},
		{"nan_bandwidth", func(n *state.EnhancedBalancerNodeSnapshot) { n.BWLimit = math.NaN() }, placement.UnknownCapacity},
		{"nan_cpu", func(n *state.EnhancedBalancerNodeSnapshot) { n.CPU = math.NaN() }, placement.UnknownCapacity},
		{"infinite_cpu", func(n *state.EnhancedBalancerNodeSnapshot) { n.CPU = math.Inf(1) }, placement.UnknownCapacity},
		{"negative_cpu", func(n *state.EnhancedBalancerNodeSnapshot) { n.CPU = -1 }, placement.UnknownCapacity},
		{"uint_overflow", func(n *state.EnhancedBalancerNodeSnapshot) { n.RAMMax = math.Exp2(64) }, placement.UnknownCapacity},
		{"negative_load", func(n *state.EnhancedBalancerNodeSnapshot) { n.UpSpeed = -1 }, placement.UnknownCapacity},
		{"bad_ram", func(n *state.EnhancedBalancerNodeSnapshot) { n.RAMCurrent = n.RAMMax + 1 }, placement.UnknownCapacity},
		{"down", func(n *state.EnhancedBalancerNodeSnapshot) { n.IsActive = false }, placement.NodeUnavailable},
		{"draining", func(n *state.EnhancedBalancerNodeSnapshot) { n.OperationalMode = state.NodeModeDraining }, placement.NodeUnavailable},
		{"maintenance", func(n *state.EnhancedBalancerNodeSnapshot) { n.OperationalMode = state.NodeModeMaintenance }, placement.NodeUnavailable},
		{"unknown_mode", func(n *state.EnhancedBalancerNodeSnapshot) { n.OperationalMode = "surprise" }, placement.NodeUnavailable},
		{"not_edge", func(n *state.EnhancedBalancerNodeSnapshot) { n.CapEdge = false }, placement.NodeUnavailable},
		{"stream_restricted", func(n *state.EnhancedBalancerNodeSnapshot) { n.ConfigStreams = []string{"other"} }, placement.NodeUnavailable},
		{"invalid_address", func(n *state.EnhancedBalancerNodeSnapshot) { n.Host = "file:///edge" }, placement.NodeUnavailable},
		{"bandwidth_full", func(n *state.EnhancedBalancerNodeSnapshot) { n.UpSpeed = n.BWLimit }, placement.CapacityFull},
		{"pending_full", func(n *state.EnhancedBalancerNodeSnapshot) { n.AddBandwidth = math.MaxUint64 }, placement.CapacityFull},
		{"cpu_full", func(n *state.EnhancedBalancerNodeSnapshot) { n.CPU = 100 }, placement.CapacityFull},
		{"ram_full", func(n *state.EnhancedBalancerNodeSnapshot) { n.RAMCurrent = n.RAMMax }, placement.CapacityFull},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := placementObservationFixture()
			tc.mutate(&req.Snapshot.Nodes[0])
			candidate := observeOne(t, req)
			wire, wireErr := placement.CandidateToProto(candidate, req.Verb)
			if wireErr != nil || !finiteMetric(wire.GetCpuPercent()) || wire.GetCpuPercent() < 0 || wire.GetCpuPercent() > 100 {
				t.Fatalf("bad node poisoned wire observation: %+v, %v", wire, wireErr)
			}
			decision, err := placement.Evaluate(placement.Request{TenantID: req.TenantID, Verb: req.Verb, Now: req.Now, Candidates: []placement.Candidate{candidate}, Complete: true})
			if err != nil || len(decision.Assessments) != 1 || decision.Assessments[0].Reason != tc.want {
				t.Fatalf("got %+v, %v; want %s", decision, err, tc.want)
			}
		})
	}
}

func TestObservePlacementNodesNamesUnavailableCondition(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*state.EnhancedBalancerNodeSnapshot)
		want   string
	}{
		{"no_hls_output", func(n *state.EnhancedBalancerNodeSnapshot) { n.Outputs = map[string]any{"RTMP": "rtmp://HOST/$"} }, "protocol_unavailable"},
		{"down", func(n *state.EnhancedBalancerNodeSnapshot) { n.IsActive = false }, "inactive"},
		{"draining", func(n *state.EnhancedBalancerNodeSnapshot) { n.OperationalMode = state.NodeModeDraining }, "mode_draining"},
		{"not_edge", func(n *state.EnhancedBalancerNodeSnapshot) { n.CapEdge = false }, "capability_missing"},
		{"invalid_address", func(n *state.EnhancedBalancerNodeSnapshot) { n.Host = "file:///edge" }, "invalid_address"},
		{"stream_restricted", func(n *state.EnhancedBalancerNodeSnapshot) { n.ConfigStreams = []string{"other"} }, "stream_not_allowed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := placementObservationFixture()
			tc.mutate(&req.Snapshot.Nodes[0])
			got := observeOne(t, req)
			if got.Capacity != placement.CapacityUnavailable || got.CapacityDetail != tc.want {
				t.Fatalf("got capacity %s detail %q, want unavailable %q", got.Capacity, got.CapacityDetail, tc.want)
			}
		})
	}
	if got := observeOne(t, placementObservationFixture()); got.CapacityDetail != "" {
		t.Fatalf("available node carries unavailable detail %q", got.CapacityDetail)
	}
}

func TestObservePlacementNodesListenerExpiryIsPerNode(t *testing.T) {
	req := placementObservationFixture()
	neighbor := req.Snapshot.Nodes[0]
	neighbor.NodeID = "neighbor"
	neighbor.OutputsObservedAt = req.Now.Add(-29 * time.Second)
	req.Snapshot.Nodes = append(req.Snapshot.Nodes, neighbor)
	req.Paths[neighbor.NodeID] = req.Paths["node"]
	nodes, err := ObservePlacementNodes(req)
	if err != nil || len(nodes) != 2 || !nodes[0].ExpiresAt.Equal(req.Now.Add(29*time.Second)) || !nodes[1].ExpiresAt.Equal(req.Now.Add(time.Second)) {
		t.Fatalf("listener expiry contaminated neighbor: %+v, %v", nodes, err)
	}
	req.Now = req.Now.Add(time.Second)
	nodes, err = ObservePlacementNodes(req)
	if err != nil {
		t.Fatal(err)
	}
	decision, err := placement.Evaluate(placement.Request{TenantID: req.TenantID, Verb: req.Verb, Now: req.Now, Candidates: nodes, Complete: true})
	if err != nil || len(decision.Assessments) != 2 || decision.Assessments[0].NodeID != "neighbor" || decision.Assessments[0].Reason != placement.StaleTelemetry || len(decision.Choices) != 1 || decision.Choices[0].NodeID != "node" {
		t.Fatalf("expired listener remained eligible: %+v, %v", decision, err)
	}
}

func TestObservePlacementNodesIngestUsesRequestedListener(t *testing.T) {
	for _, protocol := range []string{"rtmp", "whip", "srt", "hls", "RTMP"} {
		t.Run(protocol, func(t *testing.T) {
			req := placementObservationFixture()
			req.Verb, req.Protocol = placement.Ingest, protocol
			got := observeOne(t, req)
			want := placement.CapacityUnavailable
			if protocol == "rtmp" {
				want = placement.CapacityAvailable
			}
			if got.Capacity != want {
				t.Fatalf("protocol %q: got %s, want %s", protocol, got.Capacity, want)
			}
		})
	}
}

func TestObservePlacementNodesPreservesColdAndUnavailablePools(t *testing.T) {
	req := placementObservationFixture()
	second := req.Snapshot.Nodes[0]
	second.NodeID, second.ClusterID, second.IsActive = "down-node", "official", false
	req.Clusters["official"] = PlacementClusterFacts{Official: true, OwnerTenantID: "platform", AllowedVerbs: []placement.Verb{placement.Serve}}
	unentitled := second
	unentitled.NodeID, unentitled.ClusterID = "secret", "another-tenant"
	req.Snapshot.Nodes = append(req.Snapshot.Nodes, second, unentitled)
	nodes, err := ObservePlacementNodes(req)
	if err != nil || len(nodes) != 2 || nodes[0].Capacity != placement.CapacityAvailable || nodes[1].Capacity != placement.CapacityUnavailable {
		t.Fatalf("lost candidate pool evidence: %+v, %v", nodes, err)
	}
	delete(req.Paths, "node")
	if got := observeOne(t, PlacementObservationRequest{TenantID: req.TenantID, Verb: req.Verb, InternalName: req.InternalName, Protocol: req.Protocol, Now: req.Now, Clusters: map[string]PlacementClusterFacts{"private": req.Clusters["private"]}, Snapshot: req.Snapshot}); got.Capacity != placement.CapacityUnknown {
		t.Fatal("missing path evidence became known capacity or a node refusal")
	}
}

func TestObservePlacementNodesRejectsAmbiguousInventory(t *testing.T) {
	req := placementObservationFixture()
	req.Snapshot.Nodes = append(req.Snapshot.Nodes, req.Snapshot.Nodes[0])
	if _, err := ObservePlacementNodes(req); err == nil {
		t.Fatal("duplicate node identity accepted")
	}
	req.Snapshot.Nodes = make([]state.EnhancedBalancerNodeSnapshot, 4097)
	if _, err := ObservePlacementNodes(req); err == nil {
		t.Fatal("oversized inventory silently truncated")
	}
}

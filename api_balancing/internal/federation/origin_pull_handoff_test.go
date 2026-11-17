package federation

import (
	"context"
	"errors"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/streamident"
	"testing"

	federationpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn_federation"
)

func TestIsolatedPublisherPullReusesPersistedRelayRuntime(t *testing.T) {
	registry := freshRegistry(t)
	req := makeReq()
	req.RemoteCluster = "eu-cell"
	req.Remote.ClusterId = "eu-media"
	req.Remote.SourceGeneration = "84ed1d31-13d2-468d-b7b5-ef132d4e3ee9"
	req.Remote.SourceRevision = 42
	req.Remote.SourceLeaseEpoch = 2
	runtime, err := streamident.BoundRuntimeName(req.InternalName, streamident.RuntimePublisher, req.Remote.SourceGeneration)
	if err != nil {
		t.Fatal(err)
	}
	req.Remote.SourceRuntimeName = runtime
	req.DestinationFence = 7
	fed := &fakeNotifyFedClient{mutateAck: func(ack *federationpb.OriginPullAck) { ack.DtscUrl = "dtsc://peer/" + runtime }}
	deps := makeDeps(t, fed, map[string]string{"eu-cell": "peer:18009"})
	var bound []*PlacementPullBinding
	req.BindPull = func(pull *PlacementPullBinding) error { copy := *pull; bound = append(bound, &copy); return nil }
	first, err := deps.ArrangeOriginPull(t.Context(), req)
	if err != nil || first == nil {
		t.Fatalf("prepare isolated relay: %+v %v", first, err)
	}
	relay, ok := streamident.ParseRuntimeBinding(first.DestRuntimeName)
	if !ok || relay.Role != streamident.RuntimeRelay || relay.ExecutionID != first.AttemptID || first.SourceRuntimeName != runtime {
		t.Fatalf("unbound physical relay: %+v", first)
	}
	again, err := deps.ArrangeOriginPull(t.Context(), req)
	if err != nil || again == nil || !again.Reused || again.DestRuntimeName != first.DestRuntimeName || again.AttemptID != first.AttemptID || len(fed.calls) != 1 || len(bound) != 2 {
		t.Fatalf("new viewer changed relay: first=%+v next=%+v bound=%d notifications=%d err=%v", first, again, len(bound), len(fed.calls), err)
	}
	pull, found := registry.InboundPullForNode(req.InternalName, req.DestNodeID)
	if !found || pull.DestRuntimeName != first.DestRuntimeName || bound[1].DestRuntimeName != first.DestRuntimeName {
		t.Fatalf("physical binding not persisted: %+v %+v", pull, bound[1])
	}
}

func TestOriginHandoffRequiresExactOriginAcceptanceDespiteOldRelayHint(t *testing.T) {
	for _, scenario := range []string{"accepted origin", "relay refused", "wrong generation", "source unavailable"} {
		t.Run(scenario, func(t *testing.T) {
			registry := freshRegistry(t)
			fed := &fakeNotifyFedClient{}
			switch scenario {
			case "relay refused":
				fed.acks = []*federationpb.OriginPullAck{{Accepted: false, Reason: "source node is a relay"}}
			case "wrong generation":
				fed.mutateAck = func(ack *federationpb.OriginPullAck) { ack.SourceGeneration = "prior-generation" }
			case "source unavailable":
				fed.errs = []error{errors.New("origin unavailable")}
			}
			deps := makeDeps(t, fed, map[string]string{"eu-cell": "peer:18009"})
			req := makeReq()
			req.RemoteCluster = "eu-cell"
			req.Remote.ClusterId = "eu-media"
			req.Remote.SourceGeneration, req.Remote.SourceRevision = "84ed1d31-13d2-468d-b7b5-ef132d4e3ee9", 42
			req.Remote.SourceLeaseEpoch = 2
			req.DestinationFence = 9007199254740993
			bound := false
			req.BindPull = func(pull *PlacementPullBinding) error {
				if len(fed.calls) != 0 || pull.SourceGeneration != "84ed1d31-13d2-468d-b7b5-ef132d4e3ee9" || pull.SourceRevision != 42 || pull.SourceLeaseEpoch != 2 || pull.DestinationFence != req.DestinationFence {
					t.Fatal("origin was notified before its exact attempt was bound")
				}
				bound = true
				return nil
			}
			// A prior US-origin generation was replicated into EU. This advisory
			// cache format has no generation, so it cannot describe the new origin.
			if err := deps.Cache.SetRemoteReplication(t.Context(), "eu-peer", &RemoteReplicationEntry{
				StreamName: req.InternalName, ClusterID: "eu-peer", ControlCellID: "eu-cell", MediaClusterID: "eu-media", NodeID: "old-relay", Available: true,
			}); err != nil {
				t.Fatal(err)
			}
			result, err := deps.ArrangeOriginPull(context.Background(), req)
			if !bound || len(fed.calls) != 1 {
				t.Fatalf("old relay hint prevented exact origin verification: bound=%v calls=%d err=%v", bound, len(fed.calls), err)
			}
			if scenario == "accepted origin" {
				if err != nil || result == nil || result.SourceGeneration != "84ed1d31-13d2-468d-b7b5-ef132d4e3ee9" || result.SourceRevision != 42 {
					t.Fatalf("confirmed successor origin refused: %+v, %v", result, err)
				}
				return
			}
			want := ErrOriginPullNotifyFailed
			if scenario == "wrong generation" {
				want = ErrOriginPullSourceBinding
			}
			if result != nil || !errors.Is(err, want) {
				t.Fatalf("invalid origin proof must fail without fallback: result=%+v err=%v want=%v", result, err, want)
			}
			if _, found := registry.InboundPullForNode(req.InternalName, req.DestNodeID); found {
				t.Fatal("invalid origin proof installed a destination pull")
			}
		})
	}
}

func TestOriginHandoffRetainsLoopGuardWithoutPublisherProof(t *testing.T) {
	for _, scenario := range []string{"legacy", "no generation", "configured source", "no destination fence", "no source cluster", "no ownership epoch"} {
		t.Run(scenario, func(t *testing.T) {
			freshRegistry(t)
			fed := &fakeNotifyFedClient{}
			deps := makeDeps(t, fed, map[string]string{"eu-cell": "peer:18009"})
			req := makeReq()
			req.RemoteCluster, req.Remote.ClusterId = "eu-cell", "eu-media"
			req.Remote.SourceGeneration, req.Remote.SourceRevision = "84ed1d31-13d2-468d-b7b5-ef132d4e3ee9", 42
			req.Remote.SourceLeaseEpoch = 2
			req.DestinationFence = 9007199254740993
			req.BindPull = func(*PlacementPullBinding) error { t.Fatal("incomplete proof reached binding"); return nil }
			switch scenario {
			case "legacy":
				req.BindPull = nil
			case "no generation":
				req.Remote.SourceGeneration, req.Remote.SourceRevision = "", 0
				req.Remote.SourceLeaseEpoch = 0
			case "configured source":
				req.Remote.SourceGeneration = "configuration-digest"
				req.Remote.SourceLeaseEpoch = 0
			case "no ownership epoch":
				req.Remote.SourceLeaseEpoch = 0
			case "no destination fence":
				req.DestinationFence = 0
			case "no source cluster":
				req.Remote.ClusterId = ""
			}
			if err := deps.Cache.SetRemoteReplication(t.Context(), "eu-peer", &RemoteReplicationEntry{
				StreamName: req.InternalName, ClusterID: "eu-peer", ControlCellID: "eu-cell", MediaClusterID: "eu-media", Available: true,
			}); err != nil {
				t.Fatal(err)
			}
			result, err := deps.ArrangeOriginPull(t.Context(), req)
			if result != nil || !errors.Is(err, ErrOriginPullLoop) || len(fed.calls) != 0 {
				t.Fatalf("incomplete proof bypassed conservative loop guard: result=%+v err=%v calls=%d", result, err, len(fed.calls))
			}
		})
	}
}

package federation

import (
	"context"
	"errors"
	"testing"

	"frameworks/api_balancing/internal/control"
	foghornfederationpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn_federation"
	"google.golang.org/protobuf/proto"
)

func TestArrangeRequiresExactSourceGenerationAcknowledgement(t *testing.T) {
	for name, mutate := range map[string]func(*foghornfederationpb.OriginPullAck){
		"legacy": func(a *foghornfederationpb.OriginPullAck) {
			a.SourceGeneration = ""
			a.SourceRevision = 0
			a.AttemptId = ""
		},
		"tenant":              func(a *foghornfederationpb.OriginPullAck) { a.TenantId = "another" },
		"generation":          func(a *foghornfederationpb.OriginPullAck) { a.SourceGeneration = "old" },
		"revision":            func(a *foghornfederationpb.OriginPullAck) { a.SourceRevision-- },
		"attempt":             func(a *foghornfederationpb.OriginPullAck) { a.AttemptId = "different" },
		"source_node":         func(a *foghornfederationpb.OriginPullAck) { a.SourceNodeId = "another" },
		"destination_cluster": func(a *foghornfederationpb.OriginPullAck) { a.DestClusterId = "another" },
		"destination_node":    func(a *foghornfederationpb.OriginPullAck) { a.DestNodeId = "another" },
	} {
		t.Run(name, func(t *testing.T) {
			registry := freshRegistry(t)
			fed := &fakeNotifyFedClient{mutateAck: mutate}
			deps := makeDeps(t, fed, map[string]string{"cluster-peer": "peer:443"})
			req := makeReq()
			req.Remote.SourceGeneration, req.Remote.SourceRevision = "generation", 7
			if _, err := deps.ArrangeOriginPull(context.Background(), req); !errors.Is(err, ErrOriginPullSourceBinding) || !IsArrangeInfraError(err) {
				t.Fatalf("mismatched acknowledgement accepted: %v", err)
			}
			if _, found := registry.InboundPullForNode(req.InternalName, req.DestNodeID); found {
				t.Fatal("unbound acknowledgement became a destination record")
			}
		})
	}
}

func TestArrangeCarriesSourceBindingAndReplacesOnlyNewerGeneration(t *testing.T) {
	registry := freshRegistry(t)
	fed := &fakeNotifyFedClient{}
	deps := makeDeps(t, fed, map[string]string{"cluster-peer": "peer:443"})
	req := makeReq()
	req.Remote.SourceGeneration, req.Remote.SourceRevision = "generation-1", 7
	first, err := deps.ArrangeOriginPull(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if first.SourceGeneration != "generation-1" || first.SourceRevision != 7 || !canonicalPullAttempt(first.AttemptID) || fed.calls[0].GetAttemptId() != first.AttemptID {
		t.Fatalf("source binding was not persisted: %+v", first)
	}
	reused, err := deps.ArrangeOriginPull(context.Background(), req)
	if err != nil || !reused.Reused || reused.AttemptID != first.AttemptID || reused.SourceRevision != 7 || len(fed.calls) != 1 {
		t.Fatalf("source-bound reuse: %+v %v", reused, err)
	}
	req.Remote.SourceGeneration, req.Remote.SourceRevision = "generation-2", 8
	second, err := deps.ArrangeOriginPull(context.Background(), req)
	if err != nil || second.AttemptID == first.AttemptID || second.SourceGeneration != "generation-2" || second.SourceRevision != 8 {
		t.Fatalf("confirmed newer source could not replace preparation: %+v %v", second, err)
	}
	if _, err := registry.ClearInboundPull(context.Background(), req.InternalName, req.DestNodeID, first.AttemptID); !errors.Is(err, control.ErrReplicationConflict) {
		t.Fatalf("old completion erased newer source: %v", err)
	}
	req.Remote.SourceGeneration, req.Remote.SourceRevision = "generation-1", 7
	if _, err := deps.ArrangeOriginPull(context.Background(), req); err == nil {
		t.Fatal("older source restored a superseded preparation")
	}
}

func TestOriginSourceBindingRejectsIncompleteOrContradictoryFacts(t *testing.T) {
	for _, mutate := range []func(*ArrangeOriginPullRequest){
		func(r *ArrangeOriginPullRequest) { r.SourceGeneration = "generation" },
		func(r *ArrangeOriginPullRequest) { r.SourceRevision = 1 },
		func(r *ArrangeOriginPullRequest) { r.Remote.SourceGeneration = "generation" },
		func(r *ArrangeOriginPullRequest) { r.Remote.SourceRevision = 1 },
		func(r *ArrangeOriginPullRequest) { r.AttemptID = "not-an-attempt" },
		func(r *ArrangeOriginPullRequest) {
			r.SourceGeneration = "wanted"
			r.SourceRevision = 1
			r.Remote.SourceGeneration = "another"
			r.Remote.SourceRevision = 1
		},
	} {
		req := makeReq()
		mutate(&req)
		if err := bindOriginPullRequest(&req); !errors.Is(err, ErrOriginPullSourceBinding) {
			t.Fatalf("invalid source request accepted: %v", err)
		}
	}
}

func TestConfirmedPublisherBindingCannotDescribeOtherNodesOrTenants(t *testing.T) {
	entry := control.StreamEntry{TenantID: "tenant", IngestMode: control.IngestPush, Locations: map[string]control.Location{
		"source": {ClusterID: "source", SourceActive: true, OwnerNodeID: "node", SourceGeneration: "generation", SourceRevision: 7},
	}}
	if generation, revision := confirmedPublisherBinding(entry, "node", "tenant", "source"); generation != "generation" || revision != 7 {
		t.Fatal("confirmed publisher was not advertised")
	}
	for _, identity := range [][3]string{{"other-node", "tenant", "source"}, {"node", "other-tenant", "source"}, {"node", "tenant", "other-cell"}} {
		if generation, revision := confirmedPublisherBinding(entry, identity[0], identity[1], identity[2]); generation != "" || revision != 0 {
			t.Fatal("publisher proof escaped its identity")
		}
	}
	entry.IngestMode = control.IngestPull
	if generation, _ := confirmedPublisherBinding(entry, "node", "tenant", "source"); generation != "" {
		t.Fatal("managed input inferred a push generation")
	}
}

func TestArrangeVirtualSourceBindingRevalidatesExistingPhysicalPull(t *testing.T) {
	registry := freshRegistry(t)
	fed := &fakeNotifyFedClient{}
	deps := makeDeps(t, fed, map[string]string{"cluster-peer": "peer:443"})
	req := makeReq()
	req.Remote.SourceGeneration, req.Remote.SourceRevision = "generation", 7
	original, err := deps.ArrangeOriginPull(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	req.Remote.ClusterId = "virtual-source"
	if reused, reuseErr := lookupExistingReplication(context.Background(), control.StreamRegistryInstance, ArrangeOriginPullRequest{
		InternalName: req.InternalName, TenantID: req.TenantID, Remote: req.Remote, RemoteCluster: req.RemoteCluster,
		SourceGeneration: "generation", SourceRevision: 7,
	}, req.DestNodeID, req.DestClusterID); reused != nil || reuseErr != nil {
		t.Fatal("unbound record was promoted from a new candidate advertisement")
	}
	bound, err := deps.ArrangeOriginPull(context.Background(), req)
	if err != nil || !bound.Reused || bound.AttemptID != original.AttemptID || len(fed.calls) != 2 ||
		bound.SourceCellID != "cluster-peer" || bound.SourceMediaClusterID != "virtual-source" || bound.SourceNodeID != req.Remote.NodeId {
		t.Fatalf("revalidation relabeled or lost physical identity: %+v, %v", bound, err)
	}
	notification := fed.calls[1]
	if notification.GetSourceCellId() != "cluster-peer" || notification.GetSourceClusterId() != "virtual-source" || notification.GetAttemptId() != original.AttemptID {
		t.Fatalf("source was not asked to revalidate the original pull: %+v", notification)
	}
	stored, found := registry.InboundPullForNode(req.InternalName, req.DestNodeID)
	if !found || stored.SourceMediaClusterID != "virtual-source" || stored.SourceClusterID != "cluster-peer" || stored.AttemptID != original.AttemptID {
		t.Fatalf("source cell and virtual cluster conflated: %+v", stored)
	}
	again, err := deps.ArrangeOriginPull(context.Background(), req)
	if err != nil || !again.Reused || len(fed.calls) != 2 || again.AttemptID != original.AttemptID {
		t.Fatalf("exact bound reuse re-notified source: %+v, %v", again, err)
	}
	req.Remote.ClusterId = "another-virtual-source"
	if _, err := deps.ArrangeOriginPull(context.Background(), req); err == nil {
		t.Fatal("same generation/pull silently moved virtual clusters")
	}
	if len(fed.calls) != 2 {
		t.Fatal("conflicting virtual source mutated remote tracking before local refusal")
	}
}

func TestArrangeRejectsUnboundVirtualSourceAcknowledgement(t *testing.T) {
	for name, mutate := range map[string]func(*foghornfederationpb.OriginPullAck){
		"missing_cell":    func(a *foghornfederationpb.OriginPullAck) { a.SourceCellId = "" },
		"missing_cluster": func(a *foghornfederationpb.OriginPullAck) { a.SourceClusterId = "" },
		"wrong_cell":      func(a *foghornfederationpb.OriginPullAck) { a.SourceCellId = "another" },
		"wrong_cluster":   func(a *foghornfederationpb.OriginPullAck) { a.SourceClusterId = "another" },
		"wrong_attempt":   func(a *foghornfederationpb.OriginPullAck) { a.AttemptId = "another" },
	} {
		t.Run(name, func(t *testing.T) {
			registry := freshRegistry(t)
			fed := &fakeNotifyFedClient{mutateAck: mutate}
			deps := makeDeps(t, fed, map[string]string{"cluster-peer": "peer:443"})
			req := makeReq()
			req.Remote.ClusterId = "virtual-source"
			if _, err := deps.ArrangeOriginPull(context.Background(), req); !errors.Is(err, ErrOriginPullSourceBinding) {
				t.Fatalf("unbound virtual source accepted without a push generation: %v", err)
			}
			if _, found := registry.InboundPullForNode(req.InternalName, req.DestNodeID); found {
				t.Fatal("bad acknowledgement created a destination")
			}
		})
	}
}

func TestVirtualSourceNotificationRequiresExactDestination(t *testing.T) {
	req := &foghornfederationpb.OriginPullNotification{StreamName: "stream", TenantId: "tenant",
		SourceCellId: "cell", SourceClusterId: "source-media", SourceNodeId: "source",
		DestClusterId: "destination-media", DestNodeId: "edge", AttemptId: "10000000-0000-4000-8000-000000000001"}
	if !validOriginPullNotification(req) {
		t.Fatal("exact source/destination without a push generation rejected")
	}
	for _, field := range []string{"cluster", "node"} {
		for _, invalid := range []string{"", " padded", "with\u0000control"} {
			changed := proto.CloneOf(req)
			if field == "cluster" {
				changed.DestClusterId = invalid
			} else {
				changed.DestNodeId = invalid
			}
			if validOriginPullNotification(changed) {
				t.Fatalf("invalid exact destination %s accepted: %q", field, invalid)
			}
		}
	}
}

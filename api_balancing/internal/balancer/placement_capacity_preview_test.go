package balancer

import (
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/placement"
)

func TestCapacityObservationNeverInventsSourceOrChangesLiveObservation(t *testing.T) {
	req := placementObservationFixture()
	req.InternalName, req.Paths = "", nil
	nodes, err := ObservePlacementCapacity(req)
	if err != nil || len(nodes) != 1 || nodes[0].Capacity != placement.CapacityAvailable || nodes[0].SourceFeasible || nodes[0].Presence != "" || nodes[0].BWAvailable != 500 {
		t.Fatalf("source-free capacity observation: %+v %v", nodes, err)
	}
	preview, err := placement.EvaluateCapacity(placement.Request{TenantID: req.TenantID, Verb: req.Verb, Now: req.Now, Candidates: nodes, Complete: true})
	if err != nil || len(preview.Choices) != 1 {
		t.Fatalf("capacity preview could not use actual telemetry: %+v %v", preview, err)
	}
	if _, liveErr := ObservePlacementNodes(req); liveErr == nil {
		t.Fatal("live observation accepted a missing stream")
	}
	req.InternalName = "stream"
	live, err := ObservePlacementNodes(req)
	if err != nil || live[0].Capacity != placement.CapacityUnknown {
		t.Fatalf("live source uncertainty was weakened: %+v %v", live, err)
	}
}

func TestCapacityObservationPreservesListenerAndStreamUncertainty(t *testing.T) {
	for _, scenario := range []string{"missing listener", "stale listener", "restricted stream", "unknown stream", "missing metrics"} {
		t.Run(scenario, func(t *testing.T) {
			req := placementObservationFixture()
			req.Paths = nil
			node := &req.Snapshot.Nodes[0]
			switch scenario {
			case "missing listener":
				node.Outputs = nil
			case "stale listener":
				node.OutputsObservedAt = req.Now.Add(-time.Minute)
			case "restricted stream":
				node.ConfigStreams = []string{"other-stream"}
			case "unknown stream":
				req.InternalName = ""
				node.ConfigStreams = []string{"other-stream"}
			case "missing metrics":
				node.MetricsObservedAt = time.Time{}
			}
			nodes, err := ObservePlacementCapacity(req)
			if err != nil {
				t.Fatal(err)
			}
			out, err := placement.EvaluateCapacity(placement.Request{TenantID: req.TenantID, Verb: req.Verb, Now: req.Now, Candidates: nodes, Complete: true})
			if err != nil || len(out.Choices) != 0 {
				t.Fatalf("capacity preview bypassed %s: %+v %v", scenario, out, err)
			}
			if scenario == "unknown stream" && out.Assessments[0].Reason != placement.UnknownCapacity {
				t.Fatalf("missing stream identity became known exhaustion: %+v", out)
			}
		})
	}
}

func TestCapacityObservationDiscardsProvidedSourceFacts(t *testing.T) {
	req := placementObservationFixture()
	req.Paths["node"] = PlacementNodePath{Presence: placement.Present, SourceFeasible: true}
	nodes, err := ObservePlacementCapacity(req)
	if err != nil || len(nodes) != 1 || nodes[0].Presence != "" || nodes[0].SourceFeasible {
		t.Fatalf("capacity-only observation retained unrelated source facts: %+v %v", nodes, err)
	}
	if req.Paths["node"].Presence != placement.Present || !req.Paths["node"].SourceFeasible {
		t.Fatal("preview mutated caller's source evidence")
	}
}

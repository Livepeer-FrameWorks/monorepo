package placement

import (
	"testing"
	"time"

	placementpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_placement"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func capacityWireFixture() (*placementpb.CapacityPreviewQuery, *placementpb.CapacityPreviewObservation) {
	query := &placementpb.CapacityPreviewQuery{TenantId: "tenant", ControlCellId: "cell", ClusterIds: []string{"cluster"}, Verb: placementpb.Verb_VERB_SERVE, Protocol: "hls"}
	response := &placementpb.CapacityPreviewObservation{TenantId: query.TenantId, ControlCellId: query.ControlCellId, ClusterIds: []string{"cluster"}, Verb: query.Verb, Protocol: query.Protocol, Complete: true,
		ClusterConsents: map[string]*placementpb.CapacityConsent{"cluster": {}},
		ObservedAt:      timestamppb.New(testNow), ExpiresAt: timestamppb.New(testNow.Add(20 * time.Second)),
		Candidates: []*placementpb.ObservedCandidate{{TenantId: "tenant", ClusterId: "cluster", NodeId: "node", ExpiresAt: timestamppb.New(testNow.Add(30 * time.Second))}},
	}
	return query, response
}

func TestCapacityPreviewWireBindsScopeAndRejectsSourceClaims(t *testing.T) {
	for _, scenario := range []string{"tenant", "cell", "clusters", "verb", "protocol", "stream", "expired", "future", "long lease", "source", "presence", "price", "foreign candidate", "duplicate", "unknown field", "missing consent", "extra consent", "unknown consent"} {
		t.Run(scenario, func(t *testing.T) {
			query, response := capacityWireFixture()
			switch scenario {
			case "missing consent":
				response.ClusterConsents = nil
			case "extra consent":
				response.ClusterConsents["other"] = &placementpb.CapacityConsent{}
			case "unknown consent":
				response.ClusterConsents["cluster"].ProtoReflect().SetUnknown([]byte{0xa0, 6, 1})
			case "tenant":
				response.TenantId = "foreign"
			case "cell":
				response.ControlCellId = "foreign"
			case "clusters":
				response.ClusterIds = nil
			case "verb":
				response.Verb = placementpb.Verb_VERB_INGEST
			case "protocol":
				response.Protocol = "whep"
			case "stream":
				response.InternalName = "other"
			case "expired":
				response.ExpiresAt = timestamppb.New(testNow)
			case "future":
				response.ObservedAt = timestamppb.New(testNow.Add(time.Second))
			case "long lease":
				response.ExpiresAt = timestamppb.New(testNow.Add(time.Minute))
			case "source":
				response.Candidates[0].SourceFeasible = true
			case "presence":
				response.Candidates[0].Presence = placementpb.Presence_PRESENCE_PRESENT
			case "price":
				response.Candidates[0].CommercialFacts = &placementpb.CommercialFacts{}
			case "foreign candidate":
				response.Candidates[0].TenantId = "foreign"
			case "duplicate":
				response.Candidates = append(response.Candidates, response.Candidates[0])
			case "unknown field":
				response.ProtoReflect().SetUnknown([]byte{0xa0, 0x06, 0x01})
			}
			if _, err := DecodeCapacityPreviewObservation(query, response, testNow); err == nil {
				t.Fatalf("accepted %s", scenario)
			}
		})
	}
}

func TestCapacityPreviewWirePreservesUnknownNodesAndCapsExpiry(t *testing.T) {
	query, response := capacityWireFixture()
	out, err := DecodeCapacityPreviewObservation(query, response, testNow)
	if err != nil || !out.Complete || len(out.Candidates) != 1 || out.Candidates[0].Capacity != CapacityUnknown || !out.Candidates[0].ObservedAt.IsZero() || !out.Candidates[0].ExpiresAt.Equal(out.ExpiresAt) {
		t.Fatalf("unknown node or aggregate expiry was lost: %+v %v", out, err)
	}
	if !response.Candidates[0].ExpiresAt.AsTime().Equal(testNow.Add(30 * time.Second)) {
		t.Fatal("decoder mutated source response")
	}
}

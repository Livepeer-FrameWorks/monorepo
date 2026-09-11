package placement

import (
	"math"
	"testing"

	placementpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_placement"
	"google.golang.org/protobuf/proto"
)

func TestPreviewRequestRejectsAmbiguousDraftsAndScope(t *testing.T) {
	for _, scenario := range []string{"nil", "scope", "tenant parent", "tenant stream", "foreign stream", "verb", "protocol", "latitude", "longitude", "nan", "revision", "missing revision", "missing parent", "different draft verb", "clear rules", "unknown wire"} {
		t.Run(scenario, func(t *testing.T) {
			req := &placementpb.PreviewRequest{Scope: &placementpb.Scope{Kind: placementpb.ScopeKind_SCOPE_KIND_TENANT}, Verb: placementpb.Verb_VERB_SERVE, ExpectedRevision: proto.Uint64(0), ExpectedParentRevision: proto.Uint64(0), DraftUpdate: &placementpb.VerbUpdate{Verb: placementpb.Verb_VERB_SERVE, Kind: placementpb.UpdateKind_UPDATE_KIND_CLEAR}}
			if err := ValidatePreviewRequest(req); err != nil {
				t.Fatal(err)
			}
			switch scenario {
			case "nil":
				req = nil
			case "scope":
				req.Scope = nil
			case "tenant parent":
				req.ExpectedParentRevision = proto.Uint64(1)
			case "tenant stream":
				req.Scope.StreamId = "stream"
			case "foreign stream":
				req.Scope = &placementpb.Scope{Kind: placementpb.ScopeKind_SCOPE_KIND_STREAM, StreamId: "stream"}
				req.StreamId = "other"
			case "verb":
				req.Verb = 99
			case "protocol":
				req.Protocol = "HLS"
			case "latitude":
				req.Coordinates = &placementpb.Coordinates{Latitude: 91}
			case "longitude":
				req.Coordinates = &placementpb.Coordinates{Longitude: -181}
			case "nan":
				req.Coordinates = &placementpb.Coordinates{Latitude: math.NaN()}
			case "revision":
				req.ExpectedRevision = proto.Uint64(math.MaxUint64)
			case "missing revision":
				req.ExpectedRevision = nil
			case "missing parent":
				req.ExpectedParentRevision = nil
			case "different draft verb":
				req.DraftUpdate.Verb = placementpb.Verb_VERB_INGEST
			case "clear rules":
				req.DraftUpdate.Rules = &placementpb.Rules{SchemaVersion: 1}
			case "unknown wire":
				req.DraftUpdate.ProtoReflect().SetUnknown([]byte{0xa0, 6, 1})
			}
			if ValidatePreviewRequest(req) == nil {
				t.Fatal("invalid preview accepted")
			}
		})
	}
	for _, scope := range []*placementpb.Scope{{Kind: placementpb.ScopeKind_SCOPE_KIND_TENANT}, {Kind: placementpb.ScopeKind_SCOPE_KIND_STREAM, StreamId: "stream"}} {
		if err := ValidatePreviewRequest(&placementpb.PreviewRequest{Scope: scope, Verb: placementpb.Verb_VERB_INGEST}); err != nil {
			t.Fatal(err)
		}
	}
}

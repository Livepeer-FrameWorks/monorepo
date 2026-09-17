package balancer

import (
	"errors"
	"testing"

	sharedauthority "github.com/Livepeer-FrameWorks/monorepo/pkg/mediaauthority"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/placement"
	placementpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_placement"
	"google.golang.org/protobuf/proto"
)

func TestSchemaThreeIngestAuthorityChecksExactNodes(t *testing.T) {
	pair, now := placementAuthorityFixture()
	pair.Tenant.Authority.SchemaVersion = sharedauthority.NodePlacementSchemaVersion
	pair.Object.Authority.SchemaVersion = sharedauthority.NodePlacementSchemaVersion
	pair.Tenant.Authority.MediaPlacement.Ingest = &placementpb.Rules{SchemaVersion: 1, Constraints: &placementpb.Constraints{
		Allow: &placementpb.SelectorSet{Any: []*placementpb.Selector{{ClusterIds: []string{"us"}, NodeIds: []string{"edge-1"}}}},
	}}
	authority, err := CompilePlacementAuthority(pair, placement.Ingest, now)
	if err != nil {
		t.Fatalf("schema-3 pair did not compile: %v", err)
	}
	for _, test := range []struct {
		cluster, node string
		want          placement.Reason
	}{
		{"us", "edge-1", placement.Eligible},
		{"us", "edge-2", placement.PolicyDenied},
		{"us", "", placement.PolicyFactsUnavailable},
		{"missing", "edge-1", placement.NotEntitled},
	} {
		got, reasonErr := authority.IngestNodeReason(test.cluster, test.node, now)
		if reasonErr != nil || got != test.want {
			t.Fatalf("IngestNodeReason(%q, %q) = %s, %v; want %s", test.cluster, test.node, got, reasonErr, test.want)
		}
	}
	serve, err := CompilePlacementAuthority(pair, placement.Serve, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, reasonErr := serve.IngestNodeReason("us", "edge-1", now); !errors.Is(reasonErr, ErrPlacementAuthorityInvalid) {
		t.Fatalf("serve authority answered an ingest node check: %v", reasonErr)
	}

	dial, err := CompileSourceDialAuthority(pair, now)
	if err != nil {
		t.Fatalf("source-ready pair did not compile for dialing: %v", err)
	}
	if got, _ := dial.IngestNodeReason("us", "edge-2", now); got != placement.PolicyDenied {
		t.Fatalf("dial authority admitted an unlisted node: %s", got)
	}
	unready := pair
	unready.Object.SourceReady = false
	if _, err := CompileSourceDialAuthority(unready, now); !errors.Is(err, ErrPlacementAuthorityNotReady) {
		t.Fatalf("source-unready pair compiled for dialing: %v", err)
	}

	mixed := pair
	mixed.Object.Authority = proto.CloneOf(pair.Object.Authority)
	mixed.Object.Authority.SchemaVersion = sharedauthority.PlacementSchemaVersion
	if _, err := CompilePlacementAuthority(mixed, placement.Ingest, now); !errors.Is(err, ErrPlacementAuthorityNotReady) {
		t.Fatalf("mixed schema-2/3 pair compiled: %v", err)
	}
}

package graph

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"frameworks/api_gateway/graph/model"
	"frameworks/api_gateway/internal/clients/clientstest"
	"frameworks/api_gateway/internal/resolvers"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/mist"
	sharedpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/shared"
)

func TestPlacementAPIViewerGraphFieldPreservesEveryProtocol(t *testing.T) {
	for _, protocol := range model.AllMediaViewerProtocol {
		t.Run(string(protocol), func(t *testing.T) {
			fake := &clientstest.FakeCommodore{ResolveViewerEndpointWithProtocolFn: func(_ context.Context, content, _ string, _ string, got string) (*sharedpb.ViewerEndpointResponse, error) {
				if content != "public" || got == "" || got != mist.PlaybackProtocol(string(protocol)) {
					t.Fatalf("GraphQL dropped requested format %s: %s", protocol, got)
				}
				return &sharedpb.ViewerEndpointResponse{}, nil
			}}
			r := &queryResolver{&Resolver{Resolver: &resolvers.Resolver{Clients: clientstest.Clients(clientstest.WithCommodore(fake))}}}
			if _, err := r.ResolveViewerEndpoint(t.Context(), "public", &protocol); err != nil || fake.Calls != 1 {
				t.Fatalf("format forwarding failed: %v, calls=%d", err, fake.Calls)
			}
		})
	}
}

func TestPlacementAPIViewerCanonicalOperationPreservesDemoFormat(t *testing.T) {
	server := newPlaygroundTestServer()
	query := readFile(t, filepath.Join(findRepoRoot(t), "pkg/graphql/operations/queries/ResolveViewerDestination.gql"))
	for _, protocol := range []string{"HLS", "WHEP"} {
		response := executeGraphQL(t, server, query, map[string]any{"contentId": "demo_live_stream_001", "protocol": protocol})
		if len(response.Errors) != 0 {
			t.Fatalf("typed operation failed: %+v", response.Errors)
		}
		var data struct {
			ResolveViewerEndpoint struct {
				Primary struct {
					Protocol string
					URL      string
					Outputs  map[string]json.RawMessage
				}
			}
		}
		if err := json.Unmarshal(response.Data, &data); err != nil {
			t.Fatal(err)
		}
		primary := data.ResolveViewerEndpoint.Primary
		if primary.Protocol != strings.ToLower(protocol) || primary.URL == "" || len(primary.Outputs) != 1 || primary.Outputs[protocol] == nil {
			t.Fatalf("typed operation changed requested format: %s", response.Data)
		}
	}
}

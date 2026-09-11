package graph

import (
	"context"
	"encoding/json"
	"testing"

	"frameworks/api_gateway/internal/clients/clientstest"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	placementpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_placement"
)

func TestPlacementAPIOptionsGraphQLPreservesFilterAndPagination(t *testing.T) {
	fake := &clientstest.FakeCommodore{GetMediaPlacementOptionsFn: func(ctx context.Context, req *placementpb.GetOptionsRequest) (*placementpb.Options, error) {
		if ctxkeys.GetTenantID(ctx) != "tenant-realpath-1" || req.GetScope().GetKind() != placementpb.ScopeKind_SCOPE_KIND_TENANT || req.GetFirst() != 2 || req.GetAfter() != "cursor" || req.GetFilter().GetQuery() != "EU" || req.GetFilter().GetKind() != placementpb.OptionKind_OPTION_KIND_REGION || len(req.GetFilter().GetClasses()) != 1 || req.GetFilter().GetClasses()[0] != placementpb.ClusterClass_CLUSTER_CLASS_TENANT_PRIVATE {
			t.Fatal("GraphQL changed authorized selector query")
		}
		return &placementpb.Options{Scope: req.GetScope(), HasNextPage: true, HasPreviousPage: true, StartCursor: "start", EndCursor: "end", Nodes: []*placementpb.Option{{Id: "eu", Name: "EU", Kind: placementpb.OptionKind_OPTION_KIND_REGION, Region: "eu", Eligible: false, Reason: "owner_consent_unknown"}}}, nil
	}}
	srv := newRealPathTestServer(clientstest.Clients(clientstest.WithCommodore(fake)))
	response, code := tryExecuteRealPath(srv, `{ mediaPlacementOptions(scope: {kind: TENANT}, filter: {query: "EU", kind: REGION, classes: [TENANT_PRIVATE]}, after: "cursor", first: 2) { __typename ... on MediaPlacementOptionsConnection { nodes { id name kind region clusterClass ownerId eligible reason } pageInfo { hasNextPage hasPreviousPage startCursor endCursor } } } }`, nil)
	if code != 200 || len(response.Errors) != 0 {
		t.Fatalf("GraphQL options failed: %d %s", code, formatGraphQLErrors(response.Errors))
	}
	var data struct {
		MediaPlacementOptions struct {
			Typename string `json:"__typename"`
			Nodes    []struct {
				ID, Kind, Reason      string
				Eligible              bool
				ClusterClass, OwnerID *string
			}
			PageInfo struct {
				HasNextPage, HasPreviousPage bool
				StartCursor, EndCursor       string
			}
		}
	}
	if err := json.Unmarshal(response.Data, &data); err != nil {
		t.Fatal(err)
	}
	out := data.MediaPlacementOptions
	if out.Typename != "MediaPlacementOptionsConnection" || len(out.Nodes) != 1 || out.Nodes[0].ID != "eu" || out.Nodes[0].Kind != "REGION" || out.Nodes[0].Eligible || out.Nodes[0].Reason != "owner_consent_unknown" || out.Nodes[0].ClusterClass != nil || out.Nodes[0].OwnerID != nil || !out.PageInfo.HasNextPage || !out.PageInfo.HasPreviousPage || out.PageInfo.StartCursor != "start" || out.PageInfo.EndCursor != "end" || fake.Calls != 1 {
		t.Fatalf("GraphQL options lost safe metadata or pagination: %s", response.Data)
	}
}

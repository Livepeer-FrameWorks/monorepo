package quartermaster

import (
	"context"
	"testing"

	"google.golang.org/grpc"

	commonpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/common"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
)

// fakeClusterClient overrides only ListClusters; any other method (unused here) would panic on the embedded
// nil interface, which is the desired "not expected in this test" behavior.
type fakeClusterClient struct {
	quartermasterpb.ClusterServiceClient
	pages []*quartermasterpb.ListClustersResponse
	idx   int
}

func (f *fakeClusterClient) ListClusters(_ context.Context, _ *quartermasterpb.ListClustersRequest, _ ...grpc.CallOption) (*quartermasterpb.ListClustersResponse, error) {
	if f.idx >= len(f.pages) {
		return &quartermasterpb.ListClustersResponse{}, nil
	}
	p := f.pages[f.idx]
	f.idx++
	return p, nil
}

func page(hasNext bool, endCursor string, ids ...string) *quartermasterpb.ListClustersResponse {
	var cs []*quartermasterpb.InfrastructureCluster
	for _, id := range ids {
		cs = append(cs, &quartermasterpb.InfrastructureCluster{ClusterId: id})
	}
	ec := endCursor
	return &quartermasterpb.ListClustersResponse{
		Clusters:   cs,
		Pagination: &commonpb.CursorPaginationResponse{HasNextPage: hasNext, EndCursor: &ec},
	}
}

// ListOfficialClusters must follow pagination to completion (not stop at the first page).
func TestListOfficialClusters_FollowsAllPages(t *testing.T) {
	c := &GRPCClient{cluster: &fakeClusterClient{pages: []*quartermasterpb.ListClustersResponse{
		page(true, "c1", "a", "b"),
		page(true, "c2", "c", "d"),
		page(false, "", "e"),
	}}}
	resp, err := c.ListOfficialClusters(context.Background())
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	got := map[string]bool{}
	for _, cl := range resp.GetClusters() {
		got[cl.GetClusterId()] = true
	}
	for _, want := range []string{"a", "b", "c", "d", "e"} {
		if !got[want] {
			t.Fatalf("missing cluster %q — pagination was truncated: %v", want, got)
		}
	}
}

// An inconsistent page (has_next_page=true but no advancing cursor) must FAIL the refresh, never silently
// publish a truncated set.
func TestListOfficialClusters_FailsOnInconsistentPagination(t *testing.T) {
	// hasNextPage=true but empty cursor.
	c := &GRPCClient{cluster: &fakeClusterClient{pages: []*quartermasterpb.ListClustersResponse{
		page(true, "", "a"),
	}}}
	if _, err := c.ListOfficialClusters(context.Background()); err == nil {
		t.Fatal("expected an error on has_next_page with an empty cursor")
	}

	// hasNextPage=true but the cursor does not advance (same as the request's After).
	c2 := &GRPCClient{cluster: &fakeClusterClient{pages: []*quartermasterpb.ListClustersResponse{
		page(true, "c1", "a"),
		page(true, "c1", "b"), // cursor did not advance from c1
	}}}
	if _, err := c2.ListOfficialClusters(context.Background()); err == nil {
		t.Fatal("expected an error on a non-advancing cursor")
	}
}

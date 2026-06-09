package graph

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"frameworks/api_gateway/internal/clients/clientstest"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	commonpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/common"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type streamsConnResp struct {
	StreamsConnection struct {
		TotalCount int `json:"totalCount"`
		PageInfo   struct {
			HasNextPage bool    `json:"hasNextPage"`
			EndCursor   *string `json:"endCursor"`
		} `json:"pageInfo"`
		Edges []struct {
			Node struct {
				ID string `json:"id"`
			} `json:"node"`
		} `json:"edges"`
		Nodes []struct {
			ID string `json:"id"`
		} `json:"nodes"`
	} `json:"streamsConnection"`
}

func decodeStreamsConnection(t *testing.T, data json.RawMessage) streamsConnResp {
	t.Helper()
	var out streamsConnResp
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("unmarshal streamsConnection: %v\n%s", err, string(data))
	}
	return out
}

// Behavioral teeth for the real (non-demo) path. Unlike the smoke sweep (which
// returns empty protos and only proves "no panic"), these feed sentinel data and
// assert the resolver maps it onto the right GraphQL fields. A wrong-field-mapping
// regression — e.g. totalCount built from len(edges) instead of the pagination
// field — fails here.

func TestRealPathStreamsConnectionMapsPaginationFields(t *testing.T) {
	ts := timestamppb.New(time.Unix(1_700_000_000, 0))
	endCursor := "ec-sentinel"
	commo := &clientstest.FakeCommodore{
		ListStreamsFn: func(_ context.Context, _ *commonpb.CursorPaginationRequest) (*commodorepb.ListStreamsResponse, error) {
			return &commodorepb.ListStreamsResponse{
				Streams: []*commodorepb.Stream{
					{StreamId: "s1", CreatedAt: ts},
					{StreamId: "s2", CreatedAt: ts},
					{StreamId: "s3", CreatedAt: ts},
				},
				// TotalCount is deliberately NOT 3: it must come from the
				// pagination field, not the slice length.
				Pagination: &commonpb.CursorPaginationResponse{
					TotalCount:  42,
					HasNextPage: true,
					EndCursor:   &endCursor,
				},
			}, nil
		},
	}
	srv := newRealPathTestServer(clientstest.Clients(clientstest.WithCommodore(commo)))

	query := `{ streamsConnection {
		totalCount
		pageInfo { hasNextPage endCursor }
		edges { node { id } }
		nodes { id }
	} }`
	resp, status := tryExecuteRealPath(srv, query, nil)
	if status != 200 {
		t.Fatalf("http status %d", status)
	}
	if len(resp.Errors) > 0 {
		t.Fatalf("errors: %s", formatGraphQLErrors(resp.Errors))
	}

	got := decodeStreamsConnection(t, resp.Data)
	// The distinguishing assertion: totalCount is the backend pagination value
	// (42), NOT the page size (3).
	if got.StreamsConnection.TotalCount != 42 {
		t.Errorf("totalCount = %d, want 42 (must read pagination.TotalCount, not len(edges))", got.StreamsConnection.TotalCount)
	}
	if n := len(got.StreamsConnection.Edges); n != 3 {
		t.Errorf("edges = %d, want 3 (one per returned stream)", n)
	}
	if n := len(got.StreamsConnection.Nodes); n != 3 {
		t.Errorf("nodes = %d, want 3", n)
	}
	if !got.StreamsConnection.PageInfo.HasNextPage {
		t.Error("hasNextPage = false, want true (from pagination.HasNextPage)")
	}
	if got.StreamsConnection.PageInfo.EndCursor == nil || *got.StreamsConnection.PageInfo.EndCursor != "ec-sentinel" {
		t.Errorf("endCursor = %v, want ec-sentinel", got.StreamsConnection.PageInfo.EndCursor)
	}
}

// Same pattern, a different resolver family + backend (Quartermaster). Also
// exercises the cluster-operator auth gate, which calls ListClustersByOwner once
// for the ownership check before the page fetch — the stub satisfies both.
func TestRealPathClustersConnectionMapsPaginationFields(t *testing.T) {
	ts := timestamppb.New(time.Unix(1_700_000_000, 0))
	qm := &clientstest.FakeQuartermaster{
		ListClustersByOwnerFn: func(_ context.Context, _ string, _ *commonpb.CursorPaginationRequest) (*quartermasterpb.ListClustersResponse, error) {
			return &quartermasterpb.ListClustersResponse{
				Clusters: []*quartermasterpb.InfrastructureCluster{
					{Id: "c1", ClusterId: "c1", CreatedAt: ts},
					{Id: "c2", ClusterId: "c2", CreatedAt: ts},
				},
				Pagination: &commonpb.CursorPaginationResponse{TotalCount: 99},
			}, nil
		},
	}
	srv := newRealPathTestServer(clientstest.Clients(clientstest.WithQuartermaster(qm)))

	resp, status := tryExecuteRealPath(srv, `{ clustersConnection { totalCount edges { node { id } } nodes { id } } }`, nil)
	if status != 200 {
		t.Fatalf("http status %d", status)
	}
	if len(resp.Errors) > 0 {
		t.Fatalf("errors: %s", formatGraphQLErrors(resp.Errors))
	}

	var got struct {
		ClustersConnection struct {
			TotalCount int               `json:"totalCount"`
			Edges      []json.RawMessage `json:"edges"`
			Nodes      []json.RawMessage `json:"nodes"`
		} `json:"clustersConnection"`
	}
	if err := json.Unmarshal(resp.Data, &got); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, string(resp.Data))
	}
	if got.ClustersConnection.TotalCount != 99 {
		t.Errorf("totalCount = %d, want 99 (must read pagination.TotalCount, not len(edges))", got.ClustersConnection.TotalCount)
	}
	if n := len(got.ClustersConnection.Edges); n != 2 {
		t.Errorf("edges = %d, want 2", n)
	}
	if n := len(got.ClustersConnection.Nodes); n != 2 {
		t.Errorf("nodes = %d, want 2", n)
	}
}

package resolvers

import (
	"context"
	"errors"
	"testing"

	"frameworks/api_gateway/graph/model"
	"frameworks/api_gateway/internal/clients/clientstest"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
)

func TestDoStorageArtifactsConnection(t *testing.T) {
	// Nil SizeBytes keeps storageArtifactFromProto off the Purser cost path.
	var gotReq *commodorepb.ListStorageArtifactsRequest
	commo := &clientstest.FakeCommodore{
		ListStorageArtifactsFn: func(_ context.Context, req *commodorepb.ListStorageArtifactsRequest) (*commodorepb.ListStorageArtifactsResponse, error) {
			gotReq = req
			return &commodorepb.ListStorageArtifactsResponse{
				Artifacts: []*commodorepb.StorageArtifactInfo{
					{Kind: "vod", Id: "a1", ArtifactHash: "h1", Title: "Clip"},
				},
				TotalCount:  1,
				HasNextPage: true,
			}, nil
		},
	}

	first := 10
	offset := 5
	sort := model.StorageArtifactSortFieldTitle
	dir := model.SortDirectionAsc
	in := &model.StorageArtifactsInput{
		First:     &first,
		Offset:    &offset,
		Kinds:     []model.StorageArtifactKind{model.StorageArtifactKindVod, model.StorageArtifactKindClip},
		Sort:      &sort,
		Direction: &dir,
	}
	conn, err := commoB3(commo).DoStorageArtifactsConnection(clientstest.AuthedCtx("t1"), in)
	if err != nil {
		t.Fatalf("DoStorageArtifactsConnection err: %v", err)
	}
	// Inputs map into the outbound request.
	if gotReq.Limit != 10 || gotReq.Offset != 5 {
		t.Fatalf("req limit/offset = %d/%d", gotReq.Limit, gotReq.Offset)
	}
	if gotReq.SortField != "title" || gotReq.SortDirection != "asc" {
		t.Fatalf("req sort = %q/%q", gotReq.SortField, gotReq.SortDirection)
	}
	if len(gotReq.Kinds) != 2 || gotReq.Kinds[0] != "vod" || gotReq.Kinds[1] != "clip" {
		t.Fatalf("req kinds = %v", gotReq.Kinds)
	}
	// Output reflects backend metadata + echoed pagination window.
	if len(conn.Nodes) != 1 || conn.Nodes[0].ID != "a1" || conn.Nodes[0].Hash != "h1" {
		t.Fatalf("nodes = %+v", conn.Nodes)
	}
	if conn.Nodes[0].Kind != model.StorageArtifactKindVod {
		t.Fatalf("kind = %v", conn.Nodes[0].Kind)
	}
	if conn.TotalCount != 1 || !conn.HasNextPage || conn.Limit != 10 || conn.Offset != 5 {
		t.Fatalf("conn = %+v", conn)
	}

	// Nil input → defaults (limit 25, offset 0).
	commo2 := &clientstest.FakeCommodore{
		ListStorageArtifactsFn: func(_ context.Context, req *commodorepb.ListStorageArtifactsRequest) (*commodorepb.ListStorageArtifactsResponse, error) {
			gotReq = req
			return &commodorepb.ListStorageArtifactsResponse{}, nil
		},
	}
	conn, err = commoB3(commo2).DoStorageArtifactsConnection(clientstest.AuthedCtx("t1"), nil)
	if err != nil {
		t.Fatalf("nil-input err: %v", err)
	}
	if gotReq.Limit != 25 || conn.Limit != 25 || conn.Offset != 0 {
		t.Fatalf("defaults req.Limit=%d conn=%+v", gotReq.Limit, conn)
	}

	// Permission guard: unauthenticated → error, backend untouched.
	guard := &clientstest.FakeCommodore{}
	if _, derr := commoB3(guard).DoStorageArtifactsConnection(context.Background(), nil); derr == nil {
		t.Fatal("unauthenticated should error")
	}
	if guard.Calls != 0 {
		t.Fatalf("guard hit backend %d times", guard.Calls)
	}

	// Backend error is wrapped.
	failing := commoB3(&clientstest.FakeCommodore{
		ListStorageArtifactsFn: func(context.Context, *commodorepb.ListStorageArtifactsRequest) (*commodorepb.ListStorageArtifactsResponse, error) {
			return nil, errors.New("commodore down")
		},
	})
	if _, derr := failing.DoStorageArtifactsConnection(clientstest.AuthedCtx("t1"), nil); derr == nil {
		t.Fatal("backend error should surface")
	}
}

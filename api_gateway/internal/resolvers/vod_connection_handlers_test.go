package resolvers

import (
	"context"
	"errors"
	"testing"

	"frameworks/api_gateway/internal/clients/clientstest"
	commodore "github.com/Livepeer-FrameWorks/monorepo/pkg/clients/commodore"
	commonpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/common"
	sharedpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/shared"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestDoGetVodAssetsConnectionFiltered(t *testing.T) {
	var gotTenant string
	var gotStreamID *string
	var gotPag *commonpb.CursorPaginationRequest
	commo := &clientstest.FakeCommodore{
		ListVodAssetsFn: func(_ context.Context, tenantID string, pag *commonpb.CursorPaginationRequest, streamID *string, _ ...commodore.MediaListOptions) (*sharedpb.ListVodAssetsResponse, error) {
			gotTenant, gotStreamID, gotPag = tenantID, streamID, pag
			return &sharedpb.ListVodAssetsResponse{
				Assets: []*sharedpb.VodAssetInfo{
					{ArtifactHash: "vh1", CreatedAt: timestamppb.Now(), UpdatedAt: timestamppb.Now()},
				},
				Pagination: &commonpb.CursorPaginationResponse{TotalCount: 1, HasNextPage: true},
			}, nil
		},
	}
	sid := "stream-3"
	first := 4
	conn, err := commoB3(commo).DoGetVodAssetsConnectionFiltered(clientstest.AuthedCtx("t1"), &sid, &first, nil, nil, nil)
	if err != nil {
		t.Fatalf("DoGetVodAssetsConnectionFiltered err: %v", err)
	}
	// Tenant from ctx; stream filter + first forwarded.
	if gotTenant != "t1" || gotStreamID == nil || *gotStreamID != "stream-3" || gotPag.First != 4 {
		t.Fatalf("forwarded tenant=%q stream=%v first=%d", gotTenant, gotStreamID, gotPag.First)
	}
	// ArtifactHash surfaces on the node; total/hasNext from backend pagination.
	if len(conn.Nodes) != 1 || conn.Nodes[0].ArtifactHash != "vh1" || conn.TotalCount != 1 {
		t.Fatalf("conn = %+v", conn)
	}
	if !conn.PageInfo.HasNextPage {
		t.Fatalf("expected HasNextPage")
	}

	// Missing tenant context → error before backend.
	guard := &clientstest.FakeCommodore{}
	if _, err := commoB3(guard).DoGetVodAssetsConnectionFiltered(context.Background(), nil, nil, nil, nil, nil); err == nil {
		t.Fatal("missing tenant should error")
	}
	if guard.Calls != 0 {
		t.Fatalf("guard hit backend %d times", guard.Calls)
	}

	failing := commoB3(&clientstest.FakeCommodore{
		ListVodAssetsFn: func(context.Context, string, *commonpb.CursorPaginationRequest, *string, ...commodore.MediaListOptions) (*sharedpb.ListVodAssetsResponse, error) {
			return nil, errors.New("down")
		},
	})
	if _, err := failing.DoGetVodAssetsConnectionFiltered(clientstest.AuthedCtx("t1"), nil, nil, nil, nil, nil); err == nil {
		t.Fatal("backend error should surface")
	}
}

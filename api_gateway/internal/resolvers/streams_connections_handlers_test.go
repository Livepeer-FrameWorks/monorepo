package resolvers

import (
	"context"
	"errors"
	"testing"

	"frameworks/api_gateway/internal/clients/clientstest"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	commonpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/common"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestDoGetStreamsConnection(t *testing.T) {
	var gotPag *commonpb.CursorPaginationRequest
	commo := &clientstest.FakeCommodore{
		ListStreamsFn: func(_ context.Context, pag *commonpb.CursorPaginationRequest) (*commodorepb.ListStreamsResponse, error) {
			gotPag = pag
			return &commodorepb.ListStreamsResponse{
				Streams:    []*commodorepb.Stream{{StreamId: "s1"}, {StreamId: "s2"}},
				Pagination: &commonpb.CursorPaginationResponse{TotalCount: 2, HasNextPage: true},
			}, nil
		},
	}
	first := 7
	conn, err := commoB3(commo).DoGetStreamsConnection(clientstest.AuthedCtx("t1"), &first, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("DoGetStreamsConnection err: %v", err)
	}
	// first maps to pagination.First; nodes + total surfaced.
	if gotPag.First != 7 {
		t.Fatalf("pagination first = %d", gotPag.First)
	}
	if len(conn.Nodes) != 2 || conn.Nodes[0].StreamId != "s1" || conn.TotalCount != 2 {
		t.Fatalf("conn = %+v", conn)
	}
	if !conn.PageInfo.HasNextPage {
		t.Fatalf("expected HasNextPage")
	}

	failing := commoB3(&clientstest.FakeCommodore{
		ListStreamsFn: func(context.Context, *commonpb.CursorPaginationRequest) (*commodorepb.ListStreamsResponse, error) {
			return nil, errors.New("down")
		},
	})
	if _, err := failing.DoGetStreamsConnection(clientstest.AuthedCtx("t1"), nil, nil, nil, nil, nil); err == nil {
		t.Fatal("backend error should surface")
	}
}

func TestDoGetStreamKeysConnection(t *testing.T) {
	var gotStreamID string
	commo := &clientstest.FakeCommodore{
		ListStreamKeysFn: func(_ context.Context, streamID string, _ *commonpb.CursorPaginationRequest) (*commodorepb.ListStreamKeysResponse, error) {
			gotStreamID = streamID
			return &commodorepb.ListStreamKeysResponse{
				StreamKeys: []*commodorepb.StreamKey{
					{Id: "k1", CreatedAt: timestamppb.Now()},
				},
				Pagination: &commonpb.CursorPaginationResponse{TotalCount: 1, HasNextPage: false},
			}, nil
		},
	}
	conn, err := commoB3(commo).DoGetStreamKeysConnection(clientstest.AuthedCtx("t1"), "stream-7", nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("DoGetStreamKeysConnection err: %v", err)
	}
	if gotStreamID != "stream-7" {
		t.Fatalf("forwarded streamID = %q", gotStreamID)
	}
	if len(conn.Nodes) != 1 || conn.Nodes[0].Id != "k1" || conn.TotalCount != 1 {
		t.Fatalf("conn = %+v", conn)
	}

	failing := commoB3(&clientstest.FakeCommodore{
		ListStreamKeysFn: func(context.Context, string, *commonpb.CursorPaginationRequest) (*commodorepb.ListStreamKeysResponse, error) {
			return nil, errors.New("down")
		},
	})
	if _, err := failing.DoGetStreamKeysConnection(clientstest.AuthedCtx("t1"), "stream-7", nil, nil, nil, nil); err == nil {
		t.Fatal("backend error should surface")
	}
}

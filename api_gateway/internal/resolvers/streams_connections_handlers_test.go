package resolvers

import (
	"context"
	"errors"
	"testing"

	"frameworks/api_gateway/internal/clients/clientstest"
	commodore "github.com/Livepeer-FrameWorks/monorepo/pkg/clients/commodore"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	commonpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/common"
	sharedpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/shared"
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
	conn, err := commoB3(commo).DoGetStreamsConnection(clientstest.AuthedCtx("t1"), &first, nil, nil, nil)
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
	if _, err := failing.DoGetStreamsConnection(clientstest.AuthedCtx("t1"), nil, nil, nil, nil); err == nil {
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

func TestDoGetClipsConnection(t *testing.T) {
	var gotTenant string
	var gotStreamID *string
	commo := &clientstest.FakeCommodore{
		GetClipsFn: func(_ context.Context, tenantID string, streamID *string, _ *commonpb.CursorPaginationRequest, _ ...commodore.MediaListOptions) (*sharedpb.GetClipsResponse, error) {
			gotTenant, gotStreamID = tenantID, streamID
			return &sharedpb.GetClipsResponse{
				Clips:      []*sharedpb.ClipInfo{{ClipHash: "c1", CreatedAt: timestamppb.Now()}},
				Pagination: &commonpb.CursorPaginationResponse{TotalCount: 1},
			}, nil
		},
	}
	sid := "stream-9"
	conn, err := commoB3(commo).DoGetClipsConnection(clientstest.AuthedCtx("t1"), &sid, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("DoGetClipsConnection err: %v", err)
	}
	// Tenant pulled from ctx; stream filter forwarded.
	if gotTenant != "t1" || gotStreamID == nil || *gotStreamID != "stream-9" {
		t.Fatalf("forwarded tenant=%q stream=%v", gotTenant, gotStreamID)
	}
	if len(conn.Nodes) != 1 || conn.Nodes[0].ClipHash != "c1" {
		t.Fatalf("conn = %+v", conn)
	}

	// Missing tenant context → error before backend.
	guard := &clientstest.FakeCommodore{}
	if _, err := commoB3(guard).DoGetClipsConnection(context.Background(), nil, nil, nil, nil, nil); err == nil {
		t.Fatal("missing tenant should error")
	}
	if guard.Calls != 0 {
		t.Fatalf("guard hit backend %d times", guard.Calls)
	}

	failing := commoB3(&clientstest.FakeCommodore{
		GetClipsFn: func(context.Context, string, *string, *commonpb.CursorPaginationRequest, ...commodore.MediaListOptions) (*sharedpb.GetClipsResponse, error) {
			return nil, errors.New("down")
		},
	})
	if _, err := failing.DoGetClipsConnection(clientstest.AuthedCtx("t1"), nil, nil, nil, nil, nil); err == nil {
		t.Fatal("backend error should surface")
	}
}

func TestDoGetDVRRecordingsConnection(t *testing.T) {
	var gotTenant string
	commo := &clientstest.FakeCommodore{
		ListDVRRequestsFn: func(_ context.Context, tenantID string, _ *string, _ *commonpb.CursorPaginationRequest, _ ...commodore.MediaListOptions) (*sharedpb.ListDVRRecordingsResponse, error) {
			gotTenant = tenantID
			return &sharedpb.ListDVRRecordingsResponse{
				DvrRecordings: []*sharedpb.DVRInfo{{DvrHash: "d1", CreatedAt: timestamppb.Now()}},
				Pagination:    &commonpb.CursorPaginationResponse{TotalCount: 1},
			}, nil
		},
	}
	conn, err := commoB3(commo).DoGetDVRRecordingsConnection(clientstest.AuthedCtx("t1"), nil, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("DoGetDVRRecordingsConnection err: %v", err)
	}
	if gotTenant != "t1" {
		t.Fatalf("forwarded tenant = %q", gotTenant)
	}
	if len(conn.Nodes) != 1 || conn.Nodes[0].DvrHash != "d1" {
		t.Fatalf("conn = %+v", conn)
	}

	failing := commoB3(&clientstest.FakeCommodore{
		ListDVRRequestsFn: func(context.Context, string, *string, *commonpb.CursorPaginationRequest, ...commodore.MediaListOptions) (*sharedpb.ListDVRRecordingsResponse, error) {
			return nil, errors.New("down")
		},
	})
	if _, err := failing.DoGetDVRRecordingsConnection(clientstest.AuthedCtx("t1"), nil, nil, nil, nil, nil); err == nil {
		t.Fatal("backend error should surface")
	}
}

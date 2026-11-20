package resolvers

import (
	"context"
	"math"

	"frameworks/api_gateway/graph/model"
	"frameworks/api_gateway/internal/demo"
	"frameworks/api_gateway/internal/middleware"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/pagination"
	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	commonpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/common"
	foghornpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn_control"
	sharedpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/shared"
)

func mediaPagination(page *model.ConnectionInput) (*commonpb.CursorPaginationRequest, error) {
	request := &commonpb.CursorPaginationRequest{First: 50}
	if page == nil {
		return request, nil
	}
	if (page.First != nil && page.Last != nil) || (page.After != nil && page.Before != nil) || (page.Before != nil && page.Last == nil) || (page.After != nil && page.Last != nil) {
		return nil, middleware.InvalidInput("use first/after or last/before for media pagination")
	}
	if page.First != nil {
		if *page.First < 1 {
			return nil, middleware.InvalidInput("first must be positive")
		}
		request.First = int32(min(*page.First, 100))
	}
	if page.Last != nil {
		if *page.Last < 1 {
			return nil, middleware.InvalidInput("last must be positive")
		}
		request.First = 0
		request.Last = int32(min(*page.Last, 100))
	}
	request.After, request.Before = page.After, page.Before
	if _, err := pagination.Parse(request); err != nil {
		return nil, middleware.InvalidInput("%s", err)
	}
	return request, nil
}

func mediaPageInfo(page *commonpb.CursorPaginationResponse) *model.PageInfo {
	if page == nil {
		return &model.PageInfo{}
	}
	return &model.PageInfo{HasNextPage: page.GetHasNextPage(), HasPreviousPage: page.GetHasPreviousPage(), StartCursor: page.StartCursor, EndCursor: page.EndCursor}
}

func (r *Resolver) DoStreamArtifacts(ctx context.Context, stream *pb.Stream, kind string, page *model.ConnectionInput) (*model.StreamArtifactsConnection, error) {
	if err := middleware.RequirePermission(ctx, "streams:read"); err != nil {
		return nil, err
	}
	request, err := mediaPagination(page)
	if err != nil {
		return nil, err
	}
	if middleware.IsDemoMode(ctx) {
		return demo.GenerateStreamArtifactsConnection(kind), nil
	}
	response, err := r.Clients.Commodore.ListStreamArtifacts(ctx, &pb.ListStreamArtifactsRequest{StreamId: stream.GetStreamId(), Kind: kind, Pagination: request})
	if err != nil {
		return nil, err
	}
	connection := &model.StreamArtifactsConnection{Edges: []*model.StreamArtifactEdge{}, Nodes: []*model.StorageArtifact{}, PageInfo: mediaPageInfo(response.GetPagination()), TotalCount: int(response.GetPagination().GetTotalCount())}
	for _, artifact := range response.GetArtifacts() {
		node, mapErr := r.storageArtifactFromProto(ctx, artifact)
		if mapErr != nil {
			return nil, mapErr
		}
		connection.Nodes = append(connection.Nodes, node)
		connection.Edges = append(connection.Edges, &model.StreamArtifactEdge{Cursor: pagination.EncodeCursor(artifact.GetCreatedAt().AsTime(), artifact.GetId()), Node: node})
	}
	return connection, nil
}

func (r *Resolver) DoGetDVRRecording(ctx context.Context, id string) (*sharedpb.DVRInfo, error) {
	if err := middleware.RequirePermission(ctx, "streams:read"); err != nil {
		return nil, err
	}
	if _, err := NormalizeDvrID(id); err != nil {
		return nil, err
	}
	if middleware.IsDemoMode(ctx) {
		return demo.GenerateDVRRecording(), nil
	}
	return r.Clients.Commodore.GetDVRRecording(ctx, id)
}

func (r *Resolver) DoSavedDVRChapters(ctx context.Context, recording *sharedpb.DVRInfo, page *model.ConnectionInput, rangeStart, rangeEnd *float64) (*model.DVRChapterConnection, error) {
	if err := middleware.RequirePermission(ctx, "streams:read"); err != nil {
		return nil, err
	}
	request, err := mediaPagination(page)
	if err != nil {
		return nil, err
	}
	epochMS := func(value *float64) (int64, error) {
		if value == nil {
			return 0, nil
		}
		if math.IsNaN(*value) || math.IsInf(*value, 0) || *value < 0 || *value >= math.Exp2(63) || math.Trunc(*value) != *value {
			return 0, middleware.InvalidInput("chapter bounds must be non-negative integer epoch milliseconds")
		}
		return int64(*value), nil
	}
	start, err := epochMS(rangeStart)
	if err != nil {
		return nil, err
	}
	end, err := epochMS(rangeEnd)
	if err != nil {
		return nil, err
	}
	if end > 0 && end <= start {
		return nil, middleware.InvalidInput("chapter range end must follow start")
	}
	if middleware.IsDemoMode(ctx) {
		return demo.GenerateDVRChapterConnection(), nil
	}
	response, err := r.Clients.Commodore.ListDVRChapters(ctx, &foghornpb.ListDVRChaptersRequest{DvrArtifactId: recording.GetDvrHash(), Pagination: request, RangeStartMs: start, RangeEndMs: end})
	if err != nil {
		return nil, err
	}
	connection := &model.DVRChapterConnection{Edges: []*model.DVRChapterEdge{}, Nodes: []*model.DVRChapterRef{}, PageInfo: mediaPageInfo(response.GetPagination()), TotalCount: int(response.GetPagination().GetTotalCount())}
	for _, chapter := range response.GetChapters() {
		node := chapterRefFromProto(chapter)
		connection.Nodes = append(connection.Nodes, node)
		connection.Edges = append(connection.Edges, &model.DVRChapterEdge{Cursor: pagination.EncodeCursorWithSortKey(chapter.GetStartMs(), chapter.GetChapterId()), Node: node})
	}
	return connection, nil
}

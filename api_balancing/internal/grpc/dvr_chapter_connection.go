package grpc

import (
	"context"
	"slices"
	"strings"

	"frameworks/api_balancing/internal/database/foghorndb"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/pagination"
	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn_control"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (s *FoghornGRPCServer) listSavedDVRChapters(ctx context.Context, req *pb.ListDVRChaptersRequest) (*pb.ListDVRChaptersResponse, error) {
	if req.GetTenantId() == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id is required for saved chapters")
	}
	if req.GetRangeStartMs() < 0 || req.GetRangeEndMs() < 0 || (req.GetRangeEndMs() > 0 && req.GetRangeEndMs() <= req.GetRangeStartMs()) {
		return nil, status.Error(codes.InvalidArgument, "invalid chapter range")
	}
	page, err := pagination.Parse(req.GetPagination())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	if page.Limit > 100 {
		page.Limit = 100
	}
	var start int64
	var id string
	if page.Cursor != nil {
		if !page.Cursor.IsSortKey || page.Cursor.SortKey < 0 || page.Cursor.ID == "" || len(page.Cursor.ID) > 32 || strings.ContainsAny(page.Cursor.ID, "\x00\r\n") {
			return nil, status.Error(codes.InvalidArgument, "invalid chapter cursor")
		}
		start, id = page.Cursor.SortKey, page.Cursor.ID
	}
	queries := foghorndb.New(s.db)
	count, err := queries.CountSavedDVRChapters(ctx, foghorndb.CountSavedDVRChaptersParams{TenantID: req.GetTenantId(), ArtifactHash: req.GetDvrArtifactId(), RangeStartMs: req.GetRangeStartMs(), RangeEndMs: req.GetRangeEndMs()})
	if err != nil {
		return nil, status.Error(codes.Unavailable, "chapter catalog unavailable")
	}
	var chapters []foghorndb.FoghornDvrChapter
	if page.Direction == pagination.Backward {
		rows, queryErr := queries.ListSavedDVRChaptersBackward(ctx, foghorndb.ListSavedDVRChaptersBackwardParams{TenantID: req.GetTenantId(), ArtifactHash: req.GetDvrArtifactId(), RangeStartMs: req.GetRangeStartMs(), RangeEndMs: req.GetRangeEndMs(), HasCursor: page.Cursor != nil, CursorStartMs: start, CursorID: id, PageLimit: int32(page.Limit + 1)})
		if queryErr != nil {
			return nil, status.Error(codes.Unavailable, "chapter catalog unavailable")
		}
		for _, row := range rows {
			chapters = append(chapters, row.FoghornDvrChapter)
		}
	} else {
		rows, queryErr := queries.ListSavedDVRChaptersForward(ctx, foghorndb.ListSavedDVRChaptersForwardParams{TenantID: req.GetTenantId(), ArtifactHash: req.GetDvrArtifactId(), RangeStartMs: req.GetRangeStartMs(), RangeEndMs: req.GetRangeEndMs(), HasCursor: page.Cursor != nil, CursorStartMs: start, CursorID: id, PageLimit: int32(page.Limit + 1)})
		if queryErr != nil {
			return nil, status.Error(codes.Unavailable, "chapter catalog unavailable")
		}
		for _, row := range rows {
			chapters = append(chapters, row.FoghornDvrChapter)
		}
	}
	fetched := len(chapters)
	if len(chapters) > page.Limit {
		chapters = chapters[:page.Limit]
	}
	if page.Direction == pagination.Backward {
		slices.Reverse(chapters)
	}
	response := &pb.ListDVRChaptersResponse{Chapters: make([]*pb.ChapterRef, 0, len(chapters))}
	for _, row := range chapters {
		response.Chapters = append(response.Chapters, &pb.ChapterRef{ChapterId: row.ChapterID, Mode: row.Mode, IntervalSeconds: row.IntervalSeconds.Int32, StartMs: row.StartMs, EndMs: row.EndMs, IsCurrent: row.IsCurrent, State: row.State, HasGaps: row.HasGaps, SegmentCount: row.SegmentCount, PlaybackArtifactHash: row.PlaybackArtifactHash.String, PlaybackId: row.PlaybackID.String, LastFailureReason: row.LastFailureReason.String, ActualMediaStartMs: row.ActualMediaStartMs.Int64, ActualMediaEndMs: row.ActualMediaEndMs.Int64})
	}
	var first, last string
	if len(chapters) > 0 {
		first = pagination.EncodeCursorWithSortKey(chapters[0].StartMs, chapters[0].ChapterID)
		last = pagination.EncodeCursorWithSortKey(chapters[len(chapters)-1].StartMs, chapters[len(chapters)-1].ChapterID)
	}
	response.Pagination = pagination.BuildResponse(fetched, page.Limit, page.Direction, count, first, last)
	if page.Direction == pagination.Forward {
		response.Pagination.HasPreviousPage = page.Cursor != nil
	} else {
		response.Pagination.HasNextPage = page.Cursor != nil
	}
	return response, nil
}

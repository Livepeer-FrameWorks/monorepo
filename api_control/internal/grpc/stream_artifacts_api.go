package grpc

import (
	"context"
	"database/sql"
	"errors"
	"slices"

	"frameworks/api_control/internal/database/commodoredb"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/pagination"
	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	sharedpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/shared"
	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func (s *CommodoreServer) ListStreamArtifacts(ctx context.Context, req *pb.ListStreamArtifactsRequest) (*pb.ListStreamArtifactsResponse, error) {
	_, tenant, err := extractUserContext(ctx)
	if err != nil {
		return nil, err
	}
	if err = requireTenantStreamPermission(ctx, "streams:read"); err != nil {
		return nil, err
	}
	if _, err = uuid.Parse(req.GetStreamId()); err != nil {
		return nil, status.Error(codes.InvalidArgument, "stream_id must be a UUID")
	}
	if req.GetKind() != "dvr" && req.GetKind() != "clip" {
		return nil, status.Error(codes.InvalidArgument, "kind must be dvr or clip")
	}
	page, err := pagination.Parse(req.GetPagination())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	if page.Limit > 100 {
		page.Limit = 100
	}
	if page.Cursor != nil {
		if _, err = uuid.Parse(page.Cursor.ID); err != nil || page.Cursor.IsSortKey {
			return nil, status.Error(codes.InvalidArgument, "invalid media cursor")
		}
	}
	queries := commodoredb.New(s.db)
	exists, err := queries.StreamMediaCatalogExists(ctx, commodoredb.StreamMediaCatalogExistsParams{TenantID: tenant, StreamID: req.GetStreamId()})
	if err != nil {
		return nil, status.Error(codes.Unavailable, "stream catalog unavailable")
	}
	if !exists {
		return nil, status.Error(codes.NotFound, "stream not found")
	}
	filter := commodoredb.StorageArtifactFilter{TenantID: tenant, StreamID: req.GetStreamId(), Kinds: []string{req.GetKind()}, Limit: page.Limit, Keyset: true, Reverse: page.Direction == pagination.Backward}
	if page.Cursor != nil {
		filter.CursorTime, filter.CursorID = page.Cursor.Timestamp, page.Cursor.ID
	}
	catalog, err := queries.ListStorageArtifactCatalog(ctx, filter)
	if err != nil {
		return nil, status.Error(codes.Unavailable, "stream media catalog unavailable")
	}
	fetched := len(catalog.Rows)
	if len(catalog.Rows) > page.Limit {
		catalog.Rows = catalog.Rows[:page.Limit]
	}
	if page.Direction == pagination.Backward {
		slices.Reverse(catalog.Rows)
	}
	response := &pb.ListStreamArtifactsResponse{Artifacts: make([]*pb.StorageArtifactInfo, 0, len(catalog.Rows))}
	for _, row := range catalog.Rows {
		artifact, mapErr := s.storageArtifactFromCatalogRow(row)
		if mapErr != nil {
			return nil, mapErr
		}
		response.Artifacts = append(response.Artifacts, artifact)
	}
	var start, end string
	if len(catalog.Rows) > 0 {
		first, last := catalog.Rows[0], catalog.Rows[len(catalog.Rows)-1]
		start, end = pagination.EncodeCursor(first.CreatedAt, first.ID), pagination.EncodeCursor(last.CreatedAt, last.ID)
	}
	response.Pagination = pagination.BuildResponse(fetched, page.Limit, page.Direction, catalog.Total, start, end)
	// Opposite-direction availability comes from the request cursor, not the returned edges.
	if page.Direction == pagination.Forward {
		response.Pagination.HasPreviousPage = page.Cursor != nil
	} else {
		response.Pagination.HasNextPage = page.Cursor != nil
	}
	return response, nil
}

func (s *CommodoreServer) GetDVRRecording(ctx context.Context, req *pb.GetDVRRecordingRequest) (*sharedpb.DVRInfo, error) {
	_, tenant, err := extractUserContext(ctx)
	if err != nil {
		return nil, err
	}
	if err = requireTenantStreamPermission(ctx, "streams:read"); err != nil {
		return nil, err
	}
	if req.GetId() == "" {
		return nil, status.Error(codes.InvalidArgument, "recording id is required")
	}
	row, err := commodoredb.New(s.db).GetDVRRecordingForTenant(ctx, commodoredb.GetDVRRecordingForTenantParams{TenantID: tenant, RecordingID: req.GetId()})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, status.Error(codes.NotFound, "recording not found")
	}
	if err != nil {
		return nil, status.Error(codes.Unavailable, "recording catalog unavailable")
	}
	recording := &sharedpb.DVRInfo{Id: &row.ID, DvrHash: row.DvrHash, InternalName: row.InternalName, Title: &row.Title, PlaybackId: &row.PlaybackID, Status: row.LifecycleStatus.String, ErrorMessage: row.ErrorMessage.String, StorageClusterId: row.StorageClusterID.String}
	if recording.StorageClusterId == "" {
		recording.StorageClusterId = row.OriginClusterID.String
	}
	if row.StreamID != "" {
		recording.StreamId = &row.StreamID
	}
	if row.CreatedAt.Valid {
		recording.CreatedAt = timestamppb.New(row.CreatedAt.Time)
	}
	if row.UpdatedAt.Valid {
		recording.UpdatedAt = timestamppb.New(row.UpdatedAt.Time)
	}
	if row.StartedAt.Valid {
		recording.StartedAt = timestamppb.New(row.StartedAt.Time)
	}
	if row.EndedAt.Valid {
		recording.EndedAt = timestamppb.New(row.EndedAt.Time)
	}
	if row.RetentionUntil.Valid {
		recording.ExpiresAt = timestamppb.New(row.RetentionUntil.Time)
	}
	if row.RetentionSource.Valid {
		recording.RetentionSource = &row.RetentionSource.String
	}
	if row.SizeBytes.Valid {
		recording.SizeBytes = &row.SizeBytes.Int64
	}
	if row.Duration.Valid {
		recording.DurationSeconds = proto.Int32(int32(row.Duration.Int64 / 1000))
	}
	if row.StorageLocation.Valid {
		recording.StorageLocation = &row.StorageLocation.String
	}
	if row.SyncStatus.Valid {
		recording.SyncStatus = &row.SyncStatus.String
	}
	if row.IsSynced.Valid {
		recording.IsSynced = &row.IsSynced.Bool
	}
	thumbnailCluster := row.ThumbnailServingClusterID
	if !thumbnailCluster.Valid {
		thumbnailCluster = sql.NullString{String: recording.StorageClusterId, Valid: recording.StorageClusterId != ""}
	}
	recording.ThumbnailAssets = s.buildArtifactThumbnailAssets(row.HasThumbnails, thumbnailCluster, row.DvrHash)
	return recording, nil
}

func populateChapterParentRecordings(ctx context.Context, queries *commodoredb.Queries, tenant string, artifacts []*pb.StorageArtifactInfo) error {
	var hashes []string
	for _, artifact := range artifacts {
		if artifact.GetKind() == "chapter" {
			hashes = append(hashes, artifact.GetArtifactHash())
		}
	}
	if len(hashes) == 0 {
		return nil
	}
	rows, err := queries.ListChapterParentRecordings(ctx, commodoredb.ListChapterParentRecordingsParams{TenantID: tenant, ArtifactHashes: hashes})
	if err != nil {
		return status.Error(codes.Unavailable, "chapter parent catalog unavailable")
	}
	parents := make(map[string]string, len(rows))
	for _, row := range rows {
		parents[row.ArtifactHash] = row.RecordingID
	}
	for _, artifact := range artifacts {
		if parent := parents[artifact.GetArtifactHash()]; parent != "" {
			artifact.ParentRecordingId = proto.String(parent)
		}
	}
	return nil
}

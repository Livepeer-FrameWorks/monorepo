package grpc

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"frameworks/api_balancing/internal/database/foghorndb"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/middleware"
	foghornpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const dvrDiagnosisGapLimit = 100

// DiagnoseDVR reads one recording's state for a platform operator. The reads
// are keyed by dvr_hash alone, with no tenant filter: the operator is
// diagnosing another tenant's recording on purpose, and the platform-operator
// session check below is the authorization boundary instead of tenant scope.
func (s *FoghornGRPCServer) DiagnoseDVR(ctx context.Context, req *foghornpb.DiagnoseDVRRequest) (*foghornpb.DiagnoseDVRResponse, error) {
	if err := middleware.RequirePlatformOperatorJWT(ctx); err != nil {
		return nil, err
	}
	dvrHash := strings.TrimSpace(req.GetDvrHash())
	if dvrHash == "" {
		return nil, status.Error(codes.InvalidArgument, "dvr_hash is required")
	}
	if s.db == nil {
		return nil, status.Error(codes.Unavailable, "database not configured")
	}
	q := foghorndb.New(s.db)

	rec, err := q.GetDVRDiagnosisRecording(ctx, dvrHash)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, status.Error(codes.NotFound, "DVR recording not found on this cell")
	}
	if err != nil {
		return nil, s.dvrDiagnosisError(err, dvrHash, "recording")
	}
	summary, err := q.GetDVRDiagnosisSegmentSummary(ctx, dvrHash)
	if err != nil {
		return nil, s.dvrDiagnosisError(err, dvrHash, "segment summary")
	}
	byStatus, err := q.ListDVRDiagnosisSegmentStatusCounts(ctx, dvrHash)
	if err != nil {
		return nil, s.dvrDiagnosisError(err, dvrHash, "segment status counts")
	}
	gaps, gapCount, err := q.ListDVRDiagnosisSegmentGaps(ctx, dvrHash, dvrDiagnosisGapLimit)
	if err != nil {
		return nil, s.dvrDiagnosisError(err, dvrHash, "segment gaps")
	}
	chapters, err := q.ListDVRDiagnosisChapters(ctx, dvrHash, foghorndb.DVRDiagnosisChapterLimit)
	if err != nil {
		return nil, s.dvrDiagnosisError(err, dvrHash, "chapters")
	}
	pending, err := q.ListDVRDiagnosisPendingFinalize(ctx, dvrHash, foghorndb.DVRDiagnosisChapterLimit)
	if err != nil {
		return nil, s.dvrDiagnosisError(err, dvrHash, "finalize queue")
	}

	resp := &foghornpb.DiagnoseDVRResponse{
		Recording: &foghornpb.DVRRecordingDiagnosis{
			DvrHash:                 rec.ArtifactHash,
			TenantId:                rec.TenantID,
			StreamInternalName:      rec.StreamInternalName,
			InternalName:            rec.InternalName,
			Status:                  rec.Status,
			ErrorMessage:            rec.ErrorMessage,
			OriginClusterId:         rec.OriginClusterID,
			StorageClusterId:        rec.StorageClusterID,
			FederatedPointer:        rec.FederatedPointer,
			StorageLocation:         rec.StorageLocation,
			SyncStatus:              rec.SyncStatus,
			SyncError:               rec.SyncError,
			LastSyncAttempt:         optionalTimestamp(rec.LastSyncAttempt),
			SyncNodeId:              rec.SyncNodeID,
			SyncFailureCount:        rec.FailureCount,
			DtshSynced:              rec.DtshSynced,
			DtshStatus:              rec.DtshStatus,
			DtshFailureCount:        rec.DtshFailureCount,
			StartedAt:               optionalTimestamp(rec.StartedAt),
			EndedAt:                 optionalTimestamp(rec.EndedAt),
			DurationSeconds:         rec.DurationSeconds,
			SizeBytes:               rec.SizeBytes,
			RetentionUntil:          optionalTimestamp(rec.RetentionUntil),
			FrozenAt:                optionalTimestamp(rec.FrozenAt),
			ChapterMode:             rec.ChapterMode,
			ChapterIntervalSeconds:  rec.ChapterIntervalSeconds,
			ChapterBackfillComplete: rec.ChapterBackfillComplete,
			IngestGeneration:        rec.IngestGeneration,
			StartDispatchState:      rec.StartDispatchState,
		},
		Segments: &foghornpb.DVRSegmentSummary{
			Count:             summary.Count,
			FirstMediaStartMs: summary.FirstMediaStartMs,
			LastMediaEndMs:    summary.LastMediaEndMs,
			TotalDurationMs:   summary.TotalDurationMs,
			TotalSizeBytes:    summary.TotalSizeBytes,
			GapCount:          gapCount,
		},
	}
	for _, c := range byStatus {
		resp.Segments.ByStatus = append(resp.Segments.ByStatus, &foghornpb.DVRSegmentStatusCount{Status: c.Status, Count: c.Count})
	}
	for _, g := range gaps {
		resp.Segments.Gaps = append(resp.Segments.Gaps, &foghornpb.DVRSegmentGap{StartMs: g.StartMs, EndMs: g.EndMs, NextSequence: g.NextSequence})
	}
	for _, c := range chapters {
		ch := &foghornpb.DVRChapterDiagnosis{
			ChapterId:            c.ChapterID,
			Mode:                 c.Mode,
			State:                c.State,
			StartMs:              c.StartMs,
			EndMs:                c.EndMs,
			IsCurrent:            c.IsCurrent,
			FinalizeAttempts:     c.FinalizeAttempts,
			LastFailureReason:    c.LastFailureReason,
			FinalizeJobId:        chapterFinalizeJobID(c),
			FinalizeNodeId:       c.FinalizeNodeID,
			FinalizeStartedAt:    optionalTimestamp(c.FinalizeStartedAt),
			FrozenAt:             optionalTimestamp(c.FrozenAt),
			ReclaimStartedAt:     optionalTimestamp(c.ReclaimStartedAt),
			CreatedAt:            optionalTimestamp(c.CreatedAt),
			SegmentCount:         c.SegmentCount,
			HasGaps:              c.HasGaps,
			PlaybackArtifactHash: c.PlaybackArtifactHash,
		}
		if c.ActualMediaStartMs.Valid {
			ch.ActualMediaStartMs = &c.ActualMediaStartMs.Int64
		}
		if c.ActualMediaEndMs.Valid {
			ch.ActualMediaEndMs = &c.ActualMediaEndMs.Int64
		}
		resp.Chapters = append(resp.Chapters, ch)
	}
	for _, c := range pending {
		resp.PendingFinalize = append(resp.PendingFinalize, &foghornpb.DVRPendingFinalize{
			ChapterId:         c.ChapterID,
			State:             c.State,
			FinalizeAttempts:  c.FinalizeAttempts,
			FinalizeJobId:     chapterFinalizeJobID(c),
			FinalizeNodeId:    c.FinalizeNodeID,
			FinalizeStartedAt: optionalTimestamp(c.FinalizeStartedAt),
			QueuedAt:          optionalTimestamp(c.CreatedAt),
			LastFailureReason: c.LastFailureReason,
		})
	}
	return resp, nil
}

// chapterFinalizeJobID names the chapter's latest finalize attempt; a chapter
// never dispatched has none.
func chapterFinalizeJobID(c foghorndb.DVRDiagnosisChapter) string {
	if c.FinalizeAttempts <= 0 {
		return ""
	}
	return foghorndb.ChapterFinalizeJobID(c.FinalizeAttempts, c.ChapterID)
}

func optionalTimestamp(t sql.NullTime) *timestamppb.Timestamp {
	if !t.Valid {
		return nil
	}
	return timestamppb.New(t.Time)
}

func (s *FoghornGRPCServer) dvrDiagnosisError(err error, dvrHash, part string) error {
	s.logger.WithError(err).WithField("dvr_hash", dvrHash).Error("DVR diagnosis read failed: " + part)
	return status.Errorf(codes.Internal, "read DVR %s", part)
}

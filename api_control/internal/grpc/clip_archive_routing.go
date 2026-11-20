package grpc

import (
	"context"
	"time"

	foghornclient "github.com/Livepeer-FrameWorks/monorepo/pkg/clients/foghorn"
	foghorncontrolpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn_control"
	sharedpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/shared"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// The parent DVR's origin owns the chapter ledger. The VOD's storage
// cluster is a placement destination and cannot substitute for that origin.
const findArchivedClipDVRs = `-- name: FindArchivedClipDVRs
SELECT d.dvr_hash, d.origin_cluster_id, cp.artifact_hash
FROM commodore.dvr_recordings d
JOIN commodore.dvr_chapter_playback cp
  ON cp.tenant_id = d.tenant_id AND cp.dvr_hash = d.dvr_hash
JOIN commodore.vod_assets v
  ON v.tenant_id = cp.tenant_id AND v.vod_hash = cp.artifact_hash
  AND v.origin_type = 'dvr_chapter' AND v.origin_id = cp.chapter_id
  AND v.stream_id = d.stream_id
WHERE d.tenant_id = $1 AND d.stream_id = $2 AND d.stream_internal_name = $3
  AND COALESCE(d.origin_cluster_id, '') <> ''
ORDER BY d.dvr_hash, cp.artifact_hash`

type archivedClipDVR struct {
	hash, origin string
	chapters     map[string]struct{}
}

// Mirrors Foghorn's explicit Unix-time modes. Media-relative and clip-now
// requests still depend on the live stream clock and use the existing route.
func archivedClipRange(req *sharedpb.CreateClipRequest, now time.Time) (startMs, endMs int64, ok bool) {
	start, latest := req.GetStartUnix(), now.Unix()
	if start <= 0 || start >= latest {
		return 0, 0, false
	}
	var end int64
	switch req.GetMode() {
	case sharedpb.ClipMode_CLIP_MODE_ABSOLUTE, sharedpb.ClipMode_CLIP_MODE_UNSPECIFIED:
		if req.StopUnix != nil {
			end = req.GetStopUnix()
		}
	case sharedpb.ClipMode_CLIP_MODE_DURATION:
		// Foghorn ignores stop_unix in this mode.
	default:
		return 0, 0, false
	}
	if req.StopUnix == nil || req.GetMode() == sharedpb.ClipMode_CLIP_MODE_DURATION {
		duration := req.GetDurationSec()
		if duration <= 0 || duration > latest-start {
			return 0, 0, false
		}
		end = start + duration
	}
	if end <= start || end > latest {
		return 0, 0, false
	}
	return start * 1000, end * 1000, true
}

func (s *CommodoreServer) archivedClipDVRs(ctx context.Context, tenantID, streamID, streamName string) ([]archivedClipDVR, error) {
	rows, err := s.db.QueryContext(ctx, findArchivedClipDVRs, tenantID, streamID, streamName)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var recordings []archivedClipDVR
	for rows.Next() {
		var hash, origin, chapterHash string
		if err := rows.Scan(&hash, &origin, &chapterHash); err != nil {
			return nil, err
		}
		if len(recordings) == 0 || recordings[len(recordings)-1].hash != hash {
			recordings = append(recordings, archivedClipDVR{hash: hash, origin: origin, chapters: make(map[string]struct{})})
		}
		recordings[len(recordings)-1].chapters[chapterHash] = struct{}{}
	}
	return recordings, rows.Err()
}

// Resolve the origin only from a registered, materialized chapter whose
// actual media covers the full request. The selected Foghorn still runs its
// existing coverage and source checks before accepting the single create.
func (s *CommodoreServer) resolveArchivedClipOrigin(ctx context.Context, tenantID, streamID, streamName string, startMs, endMs int64) (*foghornclient.GRPCClient, string, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	recordings, err := s.archivedClipDVRs(ctx, tenantID, streamID, streamName)
	if err != nil {
		s.logger.WithError(err).Warn("Archived clip catalog lookup failed")
		return nil, "", status.Error(codes.Unavailable, "archived clip catalog lookup failed")
	}
	var selected *foghornclient.GRPCClient
	var selectedOrigin, selectedHash string
	for _, dvr := range recordings {
		client, err := s.resolveFoghornForCluster(ctx, dvr.origin, tenantID)
		if err != nil {
			return nil, "", err
		}
		req := &foghorncontrolpb.ListDVRChaptersRequest{
			DvrArtifactId: dvr.hash,
			TenantId:      tenantID,
			Mode:          foghorncontrolpb.ArchivedClipCoverageMode,
			RangeStartMs:  startMs,
			RangeEndMs:    endMs,
		}
		resp, trailers, err := client.ListDVRChapters(ctx, req)
		if err != nil {
			s.logger.WithError(err).Warn("Archived clip chapter lookup failed")
			return nil, "", status.Error(codes.Unavailable, "archived clip chapter lookup failed")
		}
		capability := trailers.Get(foghorncontrolpb.ArchivedClipCoverageTrailer)
		if len(capability) != 1 || capability[0] != "1" {
			return nil, "", status.Error(codes.Unavailable, "archive origin does not support clip coverage lookup")
		}
		for _, chapter := range resp.GetChapters() {
			if !dvr.covers(chapter, startMs, endMs) {
				continue
			}
			hash := chapter.GetPlaybackArtifactHash()
			if selected != nil && (selectedHash != hash || selectedOrigin != dvr.origin) {
				return nil, "", status.Error(codes.FailedPrecondition, "multiple archived chapters cover the clip range")
			}
			selected, selectedOrigin, selectedHash = client, dvr.origin, hash
		}
	}
	return selected, selectedOrigin, nil
}

func (dvr archivedClipDVR) covers(chapter *foghorncontrolpb.ChapterRef, startMs, endMs int64) bool {
	if chapter.GetIsCurrent() || chapter.GetPlaybackArtifactHash() == "" {
		return false
	}
	switch chapter.GetState() {
	case "finalized", "frozen", "reclaimed":
	default:
		return false
	}
	_, registered := dvr.chapters[chapter.GetPlaybackArtifactHash()]
	actualStart, actualEnd := chapter.GetActualMediaStartMs(), chapter.GetActualMediaEndMs()
	return registered && actualStart > 0 && actualStart <= startMs && actualEnd >= endMs
}

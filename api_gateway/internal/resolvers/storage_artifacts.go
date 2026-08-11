package resolvers

import (
	"context"
	"fmt"
	"strings"
	"time"

	"frameworks/api_gateway/graph/model"
	"frameworks/api_gateway/internal/demo"
	"frameworks/api_gateway/internal/loaders"
	"frameworks/api_gateway/internal/middleware"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	periscopepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/periscope"
)

// DoStorageArtifactsConnection returns one account-level media index across
// VOD uploads, DVR recordings, finalized DVR chapters, and clips. Search,
// filtering, sorting, and pagination are handled by Commodore so the UI is
// not joining one local page per artifact type.
func (r *Resolver) DoStorageArtifactsConnection(ctx context.Context, input *model.StorageArtifactsInput) (*model.StorageArtifactsConnection, error) {
	if err := middleware.RequirePermission(ctx, "streams:read"); err != nil {
		return nil, err
	}
	if middleware.IsDemoMode(ctx) {
		now := time.Now()
		size := float64(1048576)
		playbackID := demo.DemoVodPlaybackID
		streamID := demo.DemoStreamID
		isTrue := true
		isFalse := false
		location := "cold"
		syncStatus := "synced"
		artifact := &model.StorageArtifact{
			Key:             "vod:" + demo.DemoVodHash,
			Kind:            model.StorageArtifactKindVod,
			ID:              demo.DemoVodHash,
			Hash:            demo.DemoVodHash,
			PlaybackID:      &playbackID,
			StreamID:        &streamID,
			StreamTitle:     "Live: FrameWorks Demo Stream",
			Title:           "Example VOD",
			SecondaryLabel:  "video/mp4",
			SizeBytes:       &size,
			Status:          "ready",
			StorageLocation: &location,
			SyncStatus:      &syncStatus,
			// Cold/synced demo asset has no local node copy: hasLocalCopy is false.
			IsSynced:     &isTrue,
			IsFinalized:  &isTrue,
			HasLocalCopy: &isFalse,
			CreatedAt:    now.Add(-24 * time.Hour),
			UpdatedAt:    now.Add(-1 * time.Hour),
			DeleteID:     demo.DemoVodHash,
			RetentionID:  demo.DemoVodHash,
		}
		return &model.StorageArtifactsConnection{
			Nodes:              []*model.StorageArtifact{artifact},
			TotalCount:         1,
			HasNextPage:        false,
			Limit:              25,
			Offset:             0,
			LifecycleAvailable: true, // demo artifact carries real lifecycle flags
			KindCounts:         &model.StorageArtifactKindCounts{Total: 1, Vod: 1},
		}, nil
	}

	limit := int32(25)
	offset := int32(0)
	req := &commodorepb.ListStorageArtifactsRequest{Limit: limit}
	if input != nil {
		if input.First != nil {
			limit = int32(*input.First)
			req.Limit = limit
		}
		if input.Offset != nil {
			offset = int32(*input.Offset)
			req.Offset = offset
		}
		if input.StreamID != nil {
			streamID, err := normalizeStreamIDPtr(input.StreamID)
			if err != nil {
				return nil, err
			}
			req.StreamId = streamID
		}
		req.Search = strings.TrimSpace(strValue(input.Search))
		req.ArtifactHash = strings.TrimSpace(strValue(input.ArtifactHash))
		req.Status = strings.TrimSpace(strValue(input.Status))
		for _, kind := range input.Kinds {
			req.Kinds = append(req.Kinds, strings.ToLower(kind.String()))
		}
		if input.Sort != nil {
			req.SortField = storageArtifactSortField(*input.Sort)
		}
		if input.Direction != nil {
			req.SortDirection = strings.ToLower(input.Direction.String())
		}
	}
	if req.GetLimit() <= 0 {
		req.Limit = 25
		limit = 25
	}
	if req.GetOffset() < 0 {
		req.Offset = 0
		offset = 0
	}

	resp, err := r.Clients.Commodore.ListStorageArtifacts(ctx, req)
	if err != nil {
		r.Logger.WithError(err).Error("ListStorageArtifacts failed")
		return nil, fmt.Errorf("list storage artifacts: %w", err)
	}

	// has_local_copy is a PLACEMENT fact and comes SOLELY from the Periscope overlay below (full-local-
	// node-copy presence, origin or cache). Commodore does not populate it — the catalog is authoritative
	// only for durable S3 lifecycle (syncStatus/isSynced/isFinalized) — so a row with no placement
	// telemetry keeps the proto3 default (null = unknown) rather than a lifecycle fact masquerading as
	// placement.
	if l := loaders.FromContext(ctx); l != nil && l.ArtifactLifecycle != nil && len(resp.GetArtifacts()) > 0 {
		tenantID := ctxkeys.GetTenantID(ctx)
		hashes := make([]string, 0, len(resp.GetArtifacts()))
		for _, artifact := range resp.GetArtifacts() {
			if hash := artifact.GetArtifactHash(); hash != "" {
				hashes = append(hashes, hash)
			}
		}
		if tenantID != "" && len(hashes) > 0 {
			states, stateErr := l.ArtifactLifecycle.LoadMany(ctx, tenantID, hashes)
			if stateErr != nil {
				r.Logger.WithError(stateErr).Warn("Failed to load storage artifact lifecycle overlay; using durable catalog lifecycle")
			} else {
				for _, artifact := range resp.GetArtifacts() {
					if state, ok := states[artifact.GetArtifactHash()]; ok && state != nil {
						applyArtifactStorageStateToStorageArtifact(artifact, state)
					}
				}
			}
		}
	}
	// lifecycleAvailable is honest per page: true only when every returned row's lifecycle is
	// actually known (sync_status resolved from the durable catalog or the overlay). An
	// unprojected/not-yet-backfilled row leaves it false so the UI shows "unknown", not "not
	// synced". Per-row availability is derivable client-side from whether syncStatus is set.
	lifecycleAvailable := true
	for _, artifact := range resp.GetArtifacts() {
		if artifact.SyncStatus == nil {
			lifecycleAvailable = false
			break
		}
	}

	// Fail closed on a projection error: dropping the offending row while keeping totalCount and
	// kindCounts would silently hand back a short page (fewer nodes than the count claims) with no
	// signal to the client. Surface the error so the page reflects a real, consistent state.
	nodes := make([]*model.StorageArtifact, 0, len(resp.GetArtifacts()))
	for _, artifact := range resp.GetArtifacts() {
		node, nodeErr := r.storageArtifactFromProto(ctx, artifact)
		if nodeErr != nil {
			r.Logger.WithError(nodeErr).WithField("artifact_hash", artifact.GetArtifactHash()).Error("storage artifact projection failed")
			return nil, fmt.Errorf("project storage artifact %s: %w", artifact.GetArtifactHash(), nodeErr)
		}
		nodes = append(nodes, node)
	}

	kc := resp.GetKindCounts()
	kindCounts := &model.StorageArtifactKindCounts{
		Vod:     int(kc["vod"]),
		Dvr:     int(kc["dvr"]),
		Chapter: int(kc["chapter"]),
		Clip:    int(kc["clip"]),
	}
	kindCounts.Total = kindCounts.Vod + kindCounts.Dvr + kindCounts.Chapter + kindCounts.Clip

	return &model.StorageArtifactsConnection{
		Nodes:              nodes,
		TotalCount:         int(resp.GetTotalCount()),
		HasNextPage:        resp.GetHasNextPage(),
		Limit:              int(limit),
		Offset:             int(offset),
		LifecycleAvailable: lifecycleAvailable,
		KindCounts:         kindCounts,
	}, nil
}

func (r *Resolver) storageArtifactFromProto(ctx context.Context, artifact *commodorepb.StorageArtifactInfo) (*model.StorageArtifact, error) {
	if artifact == nil {
		return nil, fmt.Errorf("nil storage artifact")
	}

	kind, kindOK := storageArtifactKind(artifact.GetKind())
	if !kindOK {
		return nil, fmt.Errorf("unknown storage artifact kind %q for %s", artifact.GetKind(), artifact.GetArtifactHash())
	}
	hash := artifact.GetArtifactHash()
	createdAt := timestampAsTime(artifact.GetCreatedAt())
	updatedAt := timestampAsTime(artifact.GetUpdatedAt())

	var sizeBytes *float64
	var storageCost *model.StorageCostProjection
	if artifact.SizeBytes != nil {
		size := artifact.GetSizeBytes()
		value := float64(size)
		sizeBytes = &value
		projected, err := r.ProjectStorageCostForCaller(ctx, size)
		if err != nil {
			return nil, err
		}
		storageCost = projected
	}

	var expiresAt *time.Time
	var effectiveRetention *model.EffectiveRetention
	if ts := artifact.GetExpiresAt(); ts != nil {
		t := ts.AsTime()
		expiresAt = &t
		effectiveRetention = &model.EffectiveRetention{
			RetentionDays:  storageDaysUntil(t),
			RetentionUntil: &t,
			Source:         RetentionSourceFromString(artifact.GetRetentionSource()),
		}
	}

	var thumbnailURL *string
	if thumbs := artifact.GetThumbnailAssets(); thumbs != nil && thumbs.GetPosterUrl() != "" {
		v := thumbs.GetPosterUrl()
		thumbnailURL = &v
	}

	var storageClusterID *string
	if v := artifact.GetStorageClusterId(); v != "" {
		storageClusterID = &v
	}

	// Duration is milliseconds on the wire (all kinds; null until finalized) → expose seconds.
	var durationSeconds *float64
	if artifact.DurationMs != nil {
		secs := float64(artifact.GetDurationMs()) / 1000.0
		durationSeconds = &secs
	}

	return &model.StorageArtifact{
		Key:              fmt.Sprintf("%s:%s", strings.ToLower(kind.String()), hash),
		Kind:             kind,
		ID:               artifact.GetId(),
		Hash:             hash,
		PlaybackID:       artifact.PlaybackId,
		StreamID:         artifact.StreamId,
		StreamTitle:      artifact.GetStreamTitle(),
		Title:            artifact.GetTitle(),
		SecondaryLabel:   artifact.GetSecondaryLabel(),
		Description:      artifact.Description,
		ErrorMessage:     artifact.ErrorMessage,
		SizeBytes:        sizeBytes,
		Status:           artifact.GetStatus(),
		StorageLocation:  artifact.StorageLocation,
		StorageClusterID: storageClusterID,
		SyncStatus:       artifact.SyncStatus,
		IsSynced:         artifact.IsSynced,
		IsFinalized:      artifact.IsFinalized,
		// hasLocalCopy is the present-full-local-node-copy overlay (origin or cache);
		// null = placement overlay unavailable/unknown.
		HasLocalCopy:       artifact.HasLocalCopy,
		CreatedAt:          createdAt,
		UpdatedAt:          updatedAt,
		ExpiresAt:          expiresAt,
		EffectiveRetention: effectiveRetention,
		StorageCost:        storageCost,
		DeleteID:           hash,
		RetentionID:        hash,
		ThumbnailURL:       thumbnailURL,
		ThumbnailAssets:    artifact.GetThumbnailAssets(),
		DurationSeconds:    durationSeconds,
		Tracks:             artifactTracksFromProto(artifact.GetTracks()),
	}, nil
}

// artifactTracksFromProto maps the Commodore MediaTrack projection to the GraphQL
// ArtifactTrack model. GraphQL Int is Go *int, so the int32 track fields widen.
func artifactTracksFromProto(tracks []*commodorepb.MediaTrack) []*model.ArtifactTrack {
	out := make([]*model.ArtifactTrack, 0, len(tracks))
	for _, t := range tracks {
		if t == nil {
			continue
		}
		out = append(out, &model.ArtifactTrack{
			Type:        t.GetType(),
			Codec:       t.GetCodec(),
			Width:       int32PtrToInt(t.Width),
			Height:      int32PtrToInt(t.Height),
			Fps:         t.Fps,
			Resolution:  t.Resolution,
			BitrateKbps: int32PtrToInt(t.BitrateKbps),
			Channels:    int32PtrToInt(t.Channels),
			SampleRate:  int32PtrToInt(t.SampleRate),
		})
	}
	return out
}

func int32PtrToInt(v *int32) *int {
	if v == nil {
		return nil
	}
	i := int(*v)
	return &i
}

// applyArtifactStorageStateToStorageArtifact overlays the live Periscope projection onto the durable
// Commodore catalog lifecycle. Two DISTINCT kinds of fact, never conflated:
//   - PLACEMENT (has_local_copy): a node holds a present full local copy. Periscope is the SOLE writer;
//     Commodore never populates it, so when the projection has no value the field stays null (placement
//     unknown), never a durable storage-lifecycle fact masquerading as placement.
//   - DURABLE lifecycle (sync_status/is_synced/is_finalized/storage_location): catalog-authoritative
//     (reconciler-repaired), filled from Periscope only when the catalog hasn't projected it yet — so
//     a fresh sync is visible before the projection lands without overriding the durable value.
func applyArtifactStorageStateToStorageArtifact(artifact *commodorepb.StorageArtifactInfo, state *periscopepb.ArtifactState) {
	if artifact == nil || state == nil {
		return
	}
	// Placement: Periscope is the only source; a null projection value leaves the field null.
	if state.HasLocalCopy != nil {
		artifact.HasLocalCopy = state.HasLocalCopy
	}
	if artifact.SyncStatus == nil {
		artifact.SyncStatus = state.SyncStatus
	}
	if artifact.IsSynced == nil {
		artifact.IsSynced = state.IsSynced
	}
	if artifact.IsFinalized == nil {
		artifact.IsFinalized = state.IsFinalized
	}
	if artifact.StorageLocation == nil || *artifact.StorageLocation == "" {
		if state.StorageLocation != nil && *state.StorageLocation != "" {
			artifact.StorageLocation = state.StorageLocation
		} else if state.FilePath != nil && *state.FilePath != "" {
			loc := "local"
			artifact.StorageLocation = &loc
		}
	}
}

// storageArtifactKind maps the Commodore kind token to the GraphQL enum. ok is false for an
// unrecognized (or empty) kind: the caller FAILS CLOSED rather than silently mislabeling the row as
// VOD, which would misreport its kind, kind-count, retention semantics, and playback surface.
func storageArtifactKind(kind string) (model.StorageArtifactKind, bool) {
	switch strings.ToLower(kind) {
	case "dvr":
		return model.StorageArtifactKindDvr, true
	case "chapter":
		return model.StorageArtifactKindChapter, true
	case "clip":
		return model.StorageArtifactKindClip, true
	case "vod":
		return model.StorageArtifactKindVod, true
	default:
		return "", false
	}
}

func storageArtifactSortField(field model.StorageArtifactSortField) string {
	switch field {
	case model.StorageArtifactSortFieldTitle:
		return "title"
	case model.StorageArtifactSortFieldKind:
		return "kind"
	case model.StorageArtifactSortFieldSizeBytes:
		return "size_bytes"
	case model.StorageArtifactSortFieldExpiresAt:
		return "expires_at"
	default:
		return "created_at"
	}
}

func timestampAsTime(ts interface{ AsTime() time.Time }) time.Time {
	if ts == nil {
		return time.Time{}
	}
	return ts.AsTime()
}

func strValue(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func storageDaysUntil(t time.Time) int {
	days := int((time.Until(t) + 24*time.Hour - 1) / (24 * time.Hour))
	if days < 0 {
		return 0
	}
	return days
}

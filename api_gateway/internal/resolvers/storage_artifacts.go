package resolvers

import (
	"context"
	"fmt"
	"strings"
	"time"

	"frameworks/api_gateway/graph/model"
	"frameworks/api_gateway/internal/middleware"
	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto"
)

// DoStorageArtifactsConnection returns one account-level media index across
// VOD uploads, DVR recordings, finalized DVR chapters, and clips. Search,
// filtering, sorting, and pagination are handled by Commodore so the UI is
// not joining one local page per artifact type.
func (r *Resolver) DoStorageArtifactsConnection(ctx context.Context, input *model.StorageArtifactsInput) (*model.StorageArtifactsConnection, error) {
	if err := middleware.RequirePermission(ctx, "streams:read"); err != nil {
		return nil, err
	}

	limit := int32(25)
	offset := int32(0)
	req := &pb.ListStorageArtifactsRequest{Limit: limit}
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

	nodes := make([]*model.StorageArtifact, 0, len(resp.GetArtifacts()))
	for _, artifact := range resp.GetArtifacts() {
		node, nodeErr := r.storageArtifactFromProto(ctx, artifact)
		if nodeErr != nil {
			r.Logger.WithError(nodeErr).WithField("artifact_hash", artifact.GetArtifactHash()).Warn("storage artifact projection failed")
			continue
		}
		nodes = append(nodes, node)
	}

	return &model.StorageArtifactsConnection{
		Nodes:       nodes,
		TotalCount:  int(resp.GetTotalCount()),
		HasNextPage: resp.GetHasNextPage(),
		Limit:       int(limit),
		Offset:      int(offset),
	}, nil
}

func (r *Resolver) storageArtifactFromProto(ctx context.Context, artifact *pb.StorageArtifactInfo) (*model.StorageArtifact, error) {
	if artifact == nil {
		return nil, fmt.Errorf("nil storage artifact")
	}

	kind := storageArtifactKind(artifact.GetKind())
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

	return &model.StorageArtifact{
		Key:                fmt.Sprintf("%s:%s", strings.ToLower(kind.String()), hash),
		Kind:               kind,
		ID:                 artifact.GetId(),
		Hash:               hash,
		PlaybackID:         artifact.PlaybackId,
		StreamID:           artifact.StreamId,
		StreamTitle:        artifact.GetStreamTitle(),
		Title:              artifact.GetTitle(),
		SecondaryLabel:     artifact.GetSecondaryLabel(),
		SizeBytes:          sizeBytes,
		Status:             artifact.GetStatus(),
		StorageLocation:    artifact.StorageLocation,
		IsFrozen:           artifact.IsFrozen,
		CreatedAt:          createdAt,
		UpdatedAt:          updatedAt,
		ExpiresAt:          expiresAt,
		EffectiveRetention: effectiveRetention,
		StorageCost:        storageCost,
		DeleteID:           hash,
		RetentionID:        hash,
		ThumbnailURL:       thumbnailURL,
	}, nil
}

func storageArtifactKind(kind string) model.StorageArtifactKind {
	switch strings.ToLower(kind) {
	case "dvr":
		return model.StorageArtifactKindDvr
	case "chapter":
		return model.StorageArtifactKindChapter
	case "clip":
		return model.StorageArtifactKindClip
	default:
		return model.StorageArtifactKindVod
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

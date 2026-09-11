package federation

import (
	"context"
	"errors"
	"time"

	"frameworks/api_balancing/internal/balancer"
	"frameworks/api_balancing/internal/state"
	mediaauthoritypb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_authority"
	placementpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_placement"
)

// MediaSourcePathReader observes one media kind: destination path evidence for
// discovery and the owner-resolved generation front doors and final admission
// bind to. Both answers must come from the same reader, so a kind whose source
// this cell cannot resolve is refused rather than served from another kind's facts.
type MediaSourcePathReader interface {
	PlacementPathReader
	ResolveSourceGeneration(context.Context, balancer.PlacementAuthority) (string, time.Time, error)
}

// MediaPlacementPaths routes source observation by signed object kind and, for
// live streams, by signed ingest mode. Dispatch is never inferred from a runtime
// stream name or a viewer's request: an object whose kind has no configured
// reader is unplaceable here, which refuses viewers instead of quietly serving
// them from an unenforced path.
type MediaPlacementPaths struct {
	Push       *LivePushPlacementPaths
	Configured *ConfiguredSourcePlacementPaths
	Artifact   *ArtifactPlacementPaths
}

var (
	_ PlacementPathReader   = (*MediaPlacementPaths)(nil)
	_ MediaSourcePathReader = (*MediaPlacementPaths)(nil)
)

func (paths *MediaPlacementPaths) reader(authority balancer.PlacementAuthority) (MediaSourcePathReader, error) {
	if paths == nil {
		return nil, errors.New("media placement paths are unavailable")
	}
	switch authority.ObjectKind {
	case mediaauthoritypb.MediaObjectKind_MEDIA_OBJECT_KIND_LIVE_STREAM:
		if authority.IngestMode == "push" {
			if paths.Push == nil {
				return nil, errors.New("push source paths are unavailable")
			}
			return paths.Push, nil
		}
		if ConfiguredIngestMode(authority.IngestMode) {
			if paths.Configured == nil {
				return nil, errors.New("configured source paths are unavailable")
			}
			return paths.Configured, nil
		}
		return nil, errors.New("live source ingest mode is unsupported")
	case mediaauthoritypb.MediaObjectKind_MEDIA_OBJECT_KIND_ARTIFACT:
		if paths.Artifact == nil {
			return nil, errors.New("artifact source paths are unavailable")
		}
		return paths.Artifact, nil
	default:
		return nil, errors.New("media object kind is unsupported")
	}
}

func (paths *MediaPlacementPaths) ObservePlacementPaths(ctx context.Context, authority balancer.PlacementAuthority, req *placementpb.CandidateQuery, destinations *state.BalancerSnapshot) (PlacementPathObservation, error) {
	reader, err := paths.reader(authority)
	if err != nil {
		return PlacementPathObservation{}, err
	}
	return reader.ObservePlacementPaths(ctx, authority, req, destinations)
}

func (paths *MediaPlacementPaths) ResolveSourceGeneration(ctx context.Context, authority balancer.PlacementAuthority) (string, time.Time, error) {
	reader, err := paths.reader(authority)
	if err != nil {
		return "", time.Time{}, err
	}
	return reader.ResolveSourceGeneration(ctx, authority)
}

// CellID reports the control cell every configured reader is bound to. Assembly
// refuses a mixed set, so one identity is authoritative for the whole dispatcher.
func (paths *MediaPlacementPaths) CellID() (string, error) {
	if paths == nil || paths.Push == nil || paths.Push.CellID == "" {
		return "", errors.New("media placement paths require a cell-bound push reader")
	}
	cellID := paths.Push.CellID
	if paths.Configured != nil && paths.Configured.CellID != cellID {
		return "", errors.New("configured source paths belong to another cell")
	}
	if paths.Artifact != nil && paths.Artifact.CellID != cellID {
		return "", errors.New("artifact source paths belong to another cell")
	}
	return cellID, nil
}

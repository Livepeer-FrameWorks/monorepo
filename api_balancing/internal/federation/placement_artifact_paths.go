package federation

import (
	"context"
	"errors"
	"time"

	"frameworks/api_balancing/internal/balancer"
	"frameworks/api_balancing/internal/control"
	localauthority "frameworks/api_balancing/internal/mediaauthority"
	"frameworks/api_balancing/internal/state"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/placement"
	mediaauthoritypb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_authority"
	placementpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_placement"
)

// ArtifactPlacementPaths observes stored media. Its generation is the signed
// artifact identity, so it never changes with a copy appearing or being reaped.
// Presence is a ready warm copy on that exact node; feasibility is the node's
// storage capability, because the read-through relay materializes bytes on first
// request. Deciding which durable backend holds those bytes stays outside
// placement: this reader only says which destinations may serve them.
type ArtifactPlacementPaths struct {
	CellID    string
	Authority PlacementAuthorityReader
	Secrets   control.MediaSourceSecretReader
	// WarmNodes reports nodes with a ready, complete copy. The inventory cordon
	// already excludes unreadable copies, so absence here is not an error.
	WarmNodes func(string) []state.ArtifactNodeInfo
	Snapshot  func() *state.BalancerSnapshot
	Now       func() time.Time
}

func (reader *ArtifactPlacementPaths) now() time.Time {
	if reader != nil && reader.Now != nil {
		return reader.Now().UTC()
	}
	return time.Now().UTC()
}

func (reader *ArtifactPlacementPaths) describe(ctx context.Context, authority balancer.PlacementAuthority) (control.MediaSourceDescriptor, error) {
	if reader == nil || reader.CellID == "" || reader.Authority == nil || reader.WarmNodes == nil || reader.Snapshot == nil {
		return control.MediaSourceDescriptor{}, errors.New("artifact source reader is unavailable")
	}
	if authority.ObjectKind != mediaauthoritypb.MediaObjectKind_MEDIA_OBJECT_KIND_ARTIFACT ||
		authority.TenantAuthorityVersion <= 0 || authority.ObjectAuthorityVersion <= 0 {
		return control.MediaSourceDescriptor{}, errors.New("artifact source authority is not stored media")
	}
	readCtx, cancel := context.WithTimeout(ctx, localauthority.PlacementReadTimeout)
	defer cancel()
	pair, err := reader.Authority.Placement(readCtx, authority.TenantID, authority.ObjectID, authority.InternalName)
	if err != nil {
		return control.MediaSourceDescriptor{}, err
	}
	if err = readCtx.Err(); err != nil {
		return control.MediaSourceDescriptor{}, err
	}
	if pair.Tenant.Version != authority.TenantAuthorityVersion || pair.Object.Version != authority.ObjectAuthorityVersion {
		return control.MediaSourceDescriptor{}, errors.New("artifact source authority changed during observation")
	}
	descriptor, err := control.DescribeMediaSource(pair, reader.Secrets)
	if err != nil {
		return control.MediaSourceDescriptor{}, err
	}
	if descriptor.TenantID != authority.TenantID || descriptor.ObjectID != authority.ObjectID || descriptor.InternalName != authority.InternalName ||
		descriptor.ArtifactHash == "" || descriptor.Generation == "" {
		return control.MediaSourceDescriptor{}, errors.New("artifact source identity differs from authority")
	}
	return descriptor, nil
}

// ResolveSourceGeneration returns the stored-media generation. A stored artifact
// does not need a live copy to be placeable, so this reports configuration, and
// presence or storage capability decides whether a destination can serve it.
func (reader *ArtifactPlacementPaths) ResolveSourceGeneration(ctx context.Context, authority balancer.PlacementAuthority) (string, time.Time, error) {
	if authority.Verb != placement.Serve {
		return "", time.Time{}, errors.New("artifact source generation resolver is unavailable")
	}
	now := reader.now()
	if now.IsZero() || !now.Before(authority.ExpiresAt) {
		return "", time.Time{}, errors.New("artifact source authority is expired")
	}
	descriptor, err := reader.describe(ctx, authority)
	if err != nil {
		return "", time.Time{}, err
	}
	return descriptor.Generation, authority.ExpiresAt, nil
}

func (reader *ArtifactPlacementPaths) ObservePlacementPaths(ctx context.Context, authority balancer.PlacementAuthority, req *placementpb.CandidateQuery, destinations *state.BalancerSnapshot) (PlacementPathObservation, error) {
	if err := ctx.Err(); err != nil {
		return PlacementPathObservation{}, err
	}
	if reader == nil || reader.CellID == "" || destinations == nil || len(destinations.Nodes) > 4096 || !authority.MatchesQuery(req, reader.CellID) {
		return PlacementPathObservation{}, errors.New("artifact source authority or inventory is unavailable")
	}
	// Stored media has no publisher: recording admission belongs to the parent
	// live stream, which carries its own signed authority.
	if authority.Verb == placement.Ingest {
		return PlacementPathObservation{}, errors.New("stored artifacts have no publisher admission")
	}
	descriptor, err := reader.describe(ctx, authority)
	if err != nil {
		return PlacementPathObservation{}, err
	}
	now := reader.now()
	if now.IsZero() || !now.Before(authority.ExpiresAt) {
		return PlacementPathObservation{}, errors.New("artifact source authority is expired")
	}
	result := PlacementPathObservation{SourceGeneration: req.GetSourceGeneration(), ObservedAt: now,
		ExpiresAt: now.Add(30 * time.Second), Paths: make(map[string]balancer.PlacementNodePath, len(destinations.Nodes))}
	if authority.ExpiresAt.Before(result.ExpiresAt) {
		result.ExpiresAt = authority.ExpiresAt
	}
	for _, node := range destinations.Nodes {
		if _, duplicate := result.Paths[node.NodeID]; duplicate || node.NodeID == "" {
			return PlacementPathObservation{}, errors.New("artifact destination identity is ambiguous")
		}
		grant, found := authority.SourceGrants[node.ClusterID]
		_, destinationAllowed := authority.Clusters[node.ClusterID]
		if !found || !destinationAllowed || grant.CellID != reader.CellID {
			return PlacementPathObservation{}, errors.New("artifact destination is outside authority")
		}
		result.Paths[node.NodeID] = balancer.PlacementNodePath{Presence: placement.Absent}
	}
	if req.GetSourceGeneration() == "" {
		return PlacementPathObservation{}, errors.New("artifact serving requires a source generation")
	}
	if req.GetSourceGeneration() != descriptor.Generation {
		return result, nil
	}
	warm := reader.warmNodeSet(descriptor.ArtifactHash)
	for _, node := range destinations.Nodes {
		result.Paths[node.NodeID] = reader.nodePath(node, warm, now)
	}
	return result, nil
}

func (reader *ArtifactPlacementPaths) warmNodeSet(artifactHash string) map[string]bool {
	holders := reader.WarmNodes(artifactHash)
	if len(holders) > 4096 {
		holders = holders[:4096]
	}
	warm := make(map[string]bool, len(holders))
	for _, holder := range holders {
		if holder.NodeID != "" {
			warm[holder.NodeID] = true
		}
	}
	return warm
}

func (reader *ArtifactPlacementPaths) nodePath(node state.EnhancedBalancerNodeSnapshot, warm map[string]bool, now time.Time) balancer.PlacementNodePath {
	path := balancer.PlacementNodePath{Presence: placement.Absent}
	if !node.IsActive || !freshPlacementEvidence(node.LastHeartbeat, now) {
		return path
	}
	if warm[node.NodeID] {
		path.Presence = placement.Present
		return path
	}
	// A storage-capable edge materializes the bytes through the read-through
	// relay on the first request; a node without storage cannot.
	path.SourceFeasible = node.CapStorage
	return path
}

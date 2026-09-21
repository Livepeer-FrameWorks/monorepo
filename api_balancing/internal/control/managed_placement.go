package control

import (
	"context"
	"errors"
	"slices"
	"time"

	"frameworks/api_balancing/internal/balancer"
	localauthority "frameworks/api_balancing/internal/mediaauthority"
	sharedauthority "github.com/Livepeer-FrameWorks/monorepo/pkg/mediaauthority"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/placement"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	mediapb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_authority"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type managedPlacementReader interface {
	Placement(context.Context, string, string, string) (localauthority.PlacementPair, error)
	OpenLiveStreamSecret(localauthority.MediaObjectSnapshot) (*mediapb.LiveStreamSecret, error)
}

// Managed sources retain their explicit source-cluster election. Placement hard
// constraints restrict materialization there, per elected node; they cannot
// initiate migration or infer publisher geography from a Foghorn or source URL.
func checkManagedPlacement(ctx context.Context, reader managedPlacementReader, clusterID, nodeID string, row *commodorepb.ManagedStreamRow, streamCtx *commodorepb.ResolveStreamContextResponse) materializeStatus {
	return checkManagedPlacementAdmission(ctx, reader, clusterID, nodeID, row, streamCtx, nil)
}

// filterManagedNodesByPlacement drops candidates the signed ingest policy denies
// before the deterministic election, so a stream is never elected onto a node
// its admission would refuse while a permitted node exists. Every Foghorn reads
// the same signed pair, so the filtered election stays identical across peers.
// A schema-1 pair keeps the input; missing or unreadable evidence is transient.
func filterManagedNodesByPlacement(ctx context.Context, reader managedPlacementReader, row *commodorepb.ManagedStreamRow, nodes []eligibleNode, now time.Time) ([]eligibleNode, placementStatus) {
	if reader == nil || len(nodes) == 0 {
		return nodes, placementOK
	}
	if row.GetTenantId() == "" || row.GetStreamId() == "" || row.GetInternalName() == "" {
		return nil, placementTransient
	}
	readCtx, cancel := context.WithTimeout(ctx, localauthority.PlacementReadTimeout)
	defer cancel()
	if readCtx.Err() != nil {
		return nil, placementTransient
	}
	pair, err := reader.Placement(readCtx, row.GetTenantId(), sharedauthority.LiveStreamAuthorityID(row.GetStreamId()), row.GetInternalName())
	if err != nil || readCtx.Err() != nil {
		return nil, placementTransient
	}
	if pair.Tenant.Authority.GetSchemaVersion() == sharedauthority.SchemaVersion && pair.Object.Authority.GetSchemaVersion() == sharedauthority.SchemaVersion {
		return nodes, placementOK
	}
	authority, err := balancer.CompilePlacementAuthority(pair, placement.Ingest, now)
	if errors.Is(err, balancer.ErrPlacementAuthorityDenied) {
		return nil, placementOK
	}
	if err != nil {
		return nil, placementTransient
	}
	permitted := make([]eligibleNode, 0, len(nodes))
	for _, node := range nodes {
		switch sourceDialVerdict(authority.IngestNodeReason(node.clusterID, node.nodeID, now)) {
		case SourceDialPermitted:
			permitted = append(permitted, node)
		case SourceDialUnavailable:
			return nil, placementTransient
		}
	}
	return permitted, placementOK
}

func checkManagedPlacementAdmission(ctx context.Context, reader managedPlacementReader, clusterID, nodeID string, row *commodorepb.ManagedStreamRow, streamCtx *commodorepb.ResolveStreamContextResponse, admission **ipcpb.ManagedStreamAdmission) materializeStatus {
	if admission != nil {
		*admission = nil
	}
	if reader == nil || row == nil || streamCtx == nil || row.GetTenantId() == "" || row.GetStreamId() == "" || row.GetInternalName() == "" {
		return materializeTransient
	}
	readCtx, cancel := context.WithTimeout(ctx, localauthority.PlacementReadTimeout)
	defer cancel()
	if readCtx.Err() != nil {
		return materializeTransient
	}
	pair, err := reader.Placement(readCtx, row.GetTenantId(), sharedauthority.LiveStreamAuthorityID(row.GetStreamId()), row.GetInternalName())
	if err != nil || readCtx.Err() != nil {
		return materializeTransient
	}
	if pair.Tenant.Authority.GetSchemaVersion() == sharedauthority.SchemaVersion && pair.Object.Authority.GetSchemaVersion() == sharedauthority.SchemaVersion {
		return materializeOK
	}
	if streamCtx.GetTenantId() != row.GetTenantId() || streamCtx.GetStreamId() != row.GetStreamId() || streamCtx.GetInternalName() != row.GetInternalName() ||
		row.GetIngestMode() != "mist_native" || streamCtx.GetIngestMode() != row.GetIngestMode() {
		return materializeTransient
	}
	authority, err := balancer.CompilePlacementAuthority(pair, placement.Ingest, time.Now())
	if errors.Is(err, balancer.ErrPlacementAuthorityDenied) {
		return materializeDenied
	}
	if err != nil {
		return materializeTransient
	}
	if authority.TenantID != row.GetTenantId() || authority.ObjectID != sharedauthority.LiveStreamAuthorityID(row.GetStreamId()) || authority.InternalName != row.GetInternalName() || authority.IngestMode != row.GetIngestMode() {
		return materializeTransient
	}
	switch sourceDialVerdict(authority.IngestNodeReason(clusterID, nodeID, time.Now())) {
	case SourceDialUnavailable:
		return materializeTransient
	case SourceDialDenied:
		return materializeDenied
	}
	secret, err := reader.OpenLiveStreamSecret(pair.Object)
	if err != nil || readCtx.Err() != nil || !time.Now().Before(authority.ExpiresAt) {
		return materializeTransient
	}
	if len(secret.GetNativeAllowedClusterIds()) != 1 {
		return materializeTransient
	}
	if !slices.Contains(secret.GetNativeAllowedClusterIds(), clusterID) {
		return materializeDenied
	}
	if secret.GetNativeSourceSpec() == "" || secret.GetNativeSourceSpec() != row.GetSourceSpec() || secret.GetNativeSourceKind() != row.GetSourceKind() ||
		secret.GetNativeAlwaysOn() != row.GetAlwaysOn() || secret.GetNativePlacementCount() != row.GetPlacementCount() ||
		!slices.Equal(secret.GetNativeAllowedClusterIds(), row.GetAllowedClusterIds()) {
		return materializeTransient
	}
	if admission != nil {
		now := time.Now()
		until := now.Add(placement.PreparationLifetime)
		if authority.ExpiresAt.Before(until) {
			until = authority.ExpiresAt
		}
		if !now.Before(until) || readCtx.Err() != nil {
			return materializeTransient
		}
		*admission = &ipcpb.ManagedStreamAdmission{SchemaVersion: 1, TargetClusterId: clusterID,
			ObjectAuthorityVersion: pair.Object.Version, TenantAuthorityVersion: pair.Tenant.Version,
			PolicyRevision: authority.PolicyRevision, ParentRevision: authority.ParentRevision, PolicyDigest: authority.PolicyDigest,
			IssuedAt: timestamppb.New(now), ExpiresAt: timestamppb.New(until)}
	}
	return materializeOK
}

func checkConfiguredManagedPlacement(ctx context.Context, clusterID, nodeID string, row *commodorepb.ManagedStreamRow, streamCtx *commodorepb.ResolveStreamContextResponse) materializeStatus {
	if store := LocalMediaAuthorityStore(); store != nil {
		return checkManagedPlacement(ctx, store, clusterID, nodeID, row, streamCtx)
	}
	return materializeOK
}

func filterConfiguredManagedNodes(ctx context.Context, row *commodorepb.ManagedStreamRow, nodes []eligibleNode) ([]eligibleNode, placementStatus) {
	if store := LocalMediaAuthorityStore(); store != nil {
		return filterManagedNodesByPlacement(ctx, store, row, nodes, time.Now())
	}
	return nodes, placementOK
}

package grpc

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"frameworks/api_control/internal/database/commodoredb"
	"frameworks/api_control/internal/placementpolicy"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/placement"
	placementpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_placement"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type mediaPlacementPreviewContext struct {
	snapshot                                             placementpolicy.Snapshot
	content                                              *placementpolicy.Snapshot
	policy                                               *placement.Policy
	digest                                               string
	verb                                                 placement.Verb
	protocol, internalName, activeCluster, sourceCluster string
	activeUntil                                          time.Time
}

func (s *CommodoreServer) prepareMediaPlacementPreview(ctx context.Context, scope placementpolicy.Scope, req *placementpb.PreviewRequest) (*mediaPlacementPreviewContext, error) {
	store := placementpolicy.NewStore(s.db)
	snapshot, err := store.Read(ctx, scope)
	if err != nil {
		return nil, mediaPlacementError(err)
	}
	if req.ExpectedRevision != nil && req.GetExpectedRevision() != snapshot.Own.GetRevision() || req.ExpectedParentRevision != nil && req.GetExpectedParentRevision() != snapshot.Parent.GetRevision() {
		return nil, mediaPlacementError(placementpolicy.ErrRevisionConflict)
	}
	out := &mediaPlacementPreviewContext{snapshot: snapshot, verb: placement.Serve, protocol: req.GetProtocol()}
	if req.GetVerb() == placementpb.Verb_VERB_INGEST {
		out.verb = placement.Ingest
	}
	if out.protocol == "" {
		out.protocol = "hls"
		if out.verb == placement.Ingest {
			out.protocol = "whip"
		}
	}
	own := snapshot.Own
	if req.GetDraftUpdate() != nil {
		own, err = placement.ApplyUpdates(own, []*placementpb.VerbUpdate{req.GetDraftUpdate()})
		if err != nil {
			return nil, status.Error(codes.InvalidArgument, "invalid preview draft")
		}
	}
	tenant, stream := own, (*placementpb.PolicySet)(nil)
	streamID := req.GetStreamId()
	if scope.Kind == "stream" {
		tenant, stream, streamID = snapshot.Parent, own, scope.ID
	} else if streamID != "" {
		contentScope := placementpolicy.Scope{TenantID: scope.TenantID, Kind: "stream", ID: streamID}
		if scopeErr := contentScope.Validate(); scopeErr != nil {
			return nil, status.Error(codes.InvalidArgument, "invalid preview stream")
		}
		content, contentErr := store.Read(ctx, contentScope)
		if contentErr != nil {
			return nil, mediaPlacementError(contentErr)
		}
		if content.Parent.GetRevision() != snapshot.Own.GetRevision() {
			return nil, mediaPlacementError(placementpolicy.ErrRevisionConflict)
		}
		out.content, stream = &content, content.Own
	}
	if streamID != "" {
		metadata, metadataErr := commodoredb.New(s.db).GetPlacementPreviewStream(ctx, commodoredb.GetPlacementPreviewStreamParams{StreamID: streamID, TenantID: scope.TenantID})
		if errors.Is(metadataErr, sql.ErrNoRows) {
			return nil, mediaPlacementError(placementpolicy.ErrNotFound)
		}
		if metadataErr != nil {
			return nil, mediaPlacementError(metadataErr)
		}
		out.internalName = metadata.InternalName
		if metadata.IngestMode != "push" {
			return nil, status.Error(codes.Unimplemented, "managed source preview requires source-placement observations")
		}
		claimed := metadata.ActiveIngestClusterID.Valid && metadata.ActiveIngestClusterID.String != ""
		if claimed != metadata.ActiveIngestClusterUpdatedAt.Valid {
			return nil, status.Error(codes.Unavailable, "ingest ownership observation is incomplete")
		}
		if claimed {
			until := metadata.ActiveIngestClusterUpdatedAt.Time.Add(activeIngestLease)
			if metadata.ActiveIngestClusterUpdatedAt.Time.After(metadata.ObservedAt) {
				return nil, status.Error(codes.Unavailable, "ingest ownership observation is inconsistent")
			}
			if metadata.ObservedAt.Before(until) {
				out.sourceCluster, out.activeUntil = metadata.ActiveIngestClusterID.String, until
				if out.verb == placement.Ingest {
					out.activeCluster = out.sourceCluster
				}
			}
		}
	}
	out.policy, err = placement.CompilePolicySets(tenant, stream, out.verb)
	if err != nil {
		return nil, status.Error(codes.Unavailable, "preview policy is inconsistent")
	}
	out.digest, err = placement.Digest(out.policy)
	if err != nil {
		return nil, status.Error(codes.Unavailable, "preview policy cannot be bound")
	}
	return out, nil
}

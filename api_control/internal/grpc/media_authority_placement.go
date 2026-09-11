package grpc

import (
	"context"
	"errors"

	"frameworks/api_control/internal/placementpolicy"
	sharedauthority "github.com/Livepeer-FrameWorks/monorepo/pkg/mediaauthority"
	mediapb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_authority"
	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_placement"
	"google.golang.org/protobuf/proto"
)

// An object follows the tenant's activated wire schema. Its requested policy is
// read with its parent in one snapshot; a different signed parent must be refreshed
// before this object can be issued, never flattened into legacy defaults.
func (s *CommodoreServer) compileObjectPlacement(ctx context.Context, tenant *mediapb.TenantAuthority, object *mediapb.MediaObjectAuthority) error {
	if tenant.GetSchemaVersion() == sharedauthority.SchemaVersion {
		return nil
	}
	if tenant.GetSchemaVersion() != sharedauthority.PlacementSchemaVersion || tenant.GetMediaPlacement() == nil || tenant.GetTenantId() != object.GetTenantId() {
		return errors.New("unsupported object placement parent")
	}
	if object.GetLifecycle() != mediapb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_ACTIVE {
		object.SchemaVersion = sharedauthority.PlacementSchemaVersion
		object.PlacementTenantRevision = tenant.GetMediaPlacement().GetRevision()
		object.MediaPlacement = &pb.PolicySet{}
		return nil
	}
	scope := placementpolicy.Scope{TenantID: object.GetTenantId(), Kind: "stream"}
	switch object.GetObjectKind() {
	case mediapb.MediaObjectKind_MEDIA_OBJECT_KIND_LIVE_STREAM:
		scope.ID = object.GetLiveStream().GetStreamId()
	case mediapb.MediaObjectKind_MEDIA_OBJECT_KIND_ARTIFACT:
		scope.ID = object.GetArtifact().GetParentStreamId()
		if scope.ID == "" {
			scope.Kind, scope.ID = "tenant", scope.TenantID
		}
	default:
		return errors.New("unsupported placement object kind")
	}
	store := placementpolicy.NewStore(s.db)
	var snapshot placementpolicy.Snapshot
	var err error
	if object.GetObjectKind() == mediapb.MediaObjectKind_MEDIA_OBJECT_KIND_ARTIFACT && scope.Kind == "stream" {
		snapshot, err = store.ReadArtifactParent(ctx, scope)
	} else {
		snapshot, err = store.Read(ctx, scope)
	}
	if err != nil {
		return err
	}
	parent := snapshot.Parent
	if scope.Kind == "tenant" {
		parent = snapshot.Own
	}
	if !proto.Equal(parent, tenant.GetMediaPlacement()) {
		return errors.New("object placement parent differs from current signed tenant policy")
	}
	object.SchemaVersion = sharedauthority.PlacementSchemaVersion
	object.PlacementTenantRevision = tenant.GetMediaPlacement().GetRevision()
	object.MediaPlacement = &pb.PolicySet{}
	if scope.Kind == "stream" {
		object.MediaPlacement = proto.CloneOf(snapshot.Own)
	}
	return nil
}

package graph

import (
	"context"
	"errors"
	"frameworks/api_gateway/internal/clients/clientstest"
	"frameworks/api_gateway/internal/loaders"
	"frameworks/api_gateway/internal/middleware"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	periscopepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/periscope"
	sharedpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/shared"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"testing"
)

func artifactScopeContext(scopes ...string) context.Context {
	ctx := context.WithValue(context.Background(), ctxkeys.KeyTenantID, "tenant-1")
	ctx = context.WithValue(ctx, ctxkeys.KeyAuthType, "api_token")
	return context.WithValue(ctx, ctxkeys.KeyPermissions, scopes)
}

func TestArtifactCatalogLifecycleDoesNotReadAnalytics(t *testing.T) {
	fake := &clientstest.FakePeriscope{GetArtifactStatesByIDsFn: func(context.Context, string, []string, *string) (*periscopepb.GetArtifactStatesResponse, error) {
		return &periscopepb.GetArtifactStatesResponse{Artifacts: []*periscopepb.ArtifactState{{RequestId: "clip-1", Stage: "processing"}, {RequestId: "dvr-1", Stage: "processing"}}}, nil
	}}
	ctx := loaders.ContextWithLoaders(artifactScopeContext("streams:read"), &loaders.Loaders{ArtifactLifecycle: loaders.NewArtifactLifecycleLoader(fake)})
	got, err := (&clipResolver{}).Status(ctx, &sharedpb.ClipInfo{ClipHash: "clip-1", Status: "ready"})
	if err != nil || got != "ready" {
		t.Errorf("catalog clip ready became %q, %v", got, err)
	}
	dvr, err := (&dVRRequestResolver{}).Status(ctx, &sharedpb.DVRInfo{DvrHash: "dvr-1", Status: "failed"})
	if err != nil || dvr == nil || *dvr != "failed" {
		t.Errorf("catalog DVR failed became %v, %v", dvr, err)
	}
	if fake.Calls != 0 {
		t.Errorf("catalog fields made %d analytics calls", fake.Calls)
	}
}

func TestArtifactPlacementRequiresAnalytics(t *testing.T) {
	ctx := artifactScopeContext("streams:read")
	_, err := (&clipResolver{}).NodeID(ctx, &sharedpb.ClipInfo{ClipHash: "clip-1", NodeId: "catalog-node"})
	if !errors.Is(err, middleware.ErrForbidden) {
		t.Errorf("clip placement error = %v, want forbidden", err)
	}
	_, err = (&dVRRequestResolver{}).StorageNodeID(ctx, &sharedpb.DVRInfo{DvrHash: "dvr-1"})
	if !errors.Is(err, middleware.ErrForbidden) {
		t.Errorf("DVR placement error = %v, want forbidden", err)
	}
}

func TestArtifactPlacementPropagatesAnalyticsOutage(t *testing.T) {
	fake := &clientstest.FakePeriscope{GetArtifactStatesByIDsFn: func(context.Context, string, []string, *string) (*periscopepb.GetArtifactStatesResponse, error) {
		return nil, status.Error(codes.Unavailable, "analytics down")
	}}
	ctx := loaders.ContextWithLoaders(artifactScopeContext("streams:read", "analytics:read"), &loaders.Loaders{ArtifactLifecycle: loaders.NewArtifactLifecycleLoader(fake)})
	_, err := (&dVRRequestResolver{}).StorageNodeID(ctx, &sharedpb.DVRInfo{DvrHash: "dvr-1"})
	if status.Code(err) != codes.Unavailable {
		t.Fatalf("placement outage = %v, want unavailable", err)
	}
}

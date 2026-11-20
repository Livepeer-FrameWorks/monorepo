package resolvers

import (
	"context"
	"errors"
	"testing"

	"frameworks/api_gateway/internal/clients/clientstest"
	"frameworks/api_gateway/internal/middleware"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	sharedpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/shared"
)

func TestTenantMediaSubscriptionsRequireScope(t *testing.T) {
	r := &Resolver{Logger: logging.NewLogger(), SubManager: &SubscriptionManager{closed: true}}
	scopedContext := func(scopes ...string) context.Context {
		return context.WithValue(streamScopeAPITokenCtx(scopes...), ctxkeys.KeyUser,
			&middleware.UserContext{UserID: "user-1", TenantID: "tenant-1", Role: "owner", Permissions: scopes})
	}
	cases := []struct {
		name, scope string
		call        func(context.Context) error
	}{
		{"liveStreamEvents", "streams:read", func(ctx context.Context) error { _, err := r.DoStreamUpdates(ctx, nil); return err }},
		{"liveViewerMetrics", "analytics:read", func(ctx context.Context) error { _, err := r.DoAnalyticsUpdates(ctx, "stream-1"); return err }},
		{"liveConnectionEvents", "analytics:read", func(ctx context.Context) error { _, err := r.DoConnectionEvents(ctx, nil); return err }},
		{"liveStorageEvents", "streams:read", func(ctx context.Context) error { _, err := r.DoStorageEvents(ctx, nil); return err }},
		{"liveProcessingEvents", "streams:read", func(ctx context.Context) error { _, err := r.DoProcessingEvents(ctx, nil); return err }},
		{"liveTrackListUpdates", "streams:read", func(ctx context.Context) error { _, err := r.DoTrackListUpdates(ctx, "stream-1"); return err }},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			wantScopeError(t, test.call(scopedContext("billing:read")), test.scope)
			if test.scope == "analytics:read" {
				wantScopeError(t, test.call(scopedContext("streams:read")), test.scope)
			}
			if err := test.call(scopedContext(test.scope)); !errors.Is(err, errSubscriptionManagerClosed) {
				t.Fatalf("scoped subscription did not reach the upstream manager: %v", err)
			}
		})
	}
}

func TestIndividualMediaReadsRequireStreamScope(t *testing.T) {
	fake := &clientstest.FakeCommodore{
		GetClipFn: func(context.Context, string) (*sharedpb.ClipInfo, error) { return &sharedpb.ClipInfo{}, nil },
		ListStorageArtifactsFn: func(context.Context, *commodorepb.ListStorageArtifactsRequest) (*commodorepb.ListStorageArtifactsResponse, error) {
			return &commodorepb.ListStorageArtifactsResponse{}, nil
		},
	}
	r := commoW2(fake)
	ctx := streamScopeAPITokenCtx("billing:read")
	_, err := r.DoGetClip(ctx, "clip-1")
	wantScopeError(t, err, "streams:read")
	_, err = r.DoGetVodAsset(ctx, "vod-1")
	wantScopeError(t, err, "streams:read")
	if fake.Calls != 0 {
		t.Fatalf("under-scoped reads reached Commodore %d times", fake.Calls)
	}
}

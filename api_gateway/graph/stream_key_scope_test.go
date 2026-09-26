package graph

import (
	"context"
	"errors"
	"strings"
	"testing"

	"frameworks/api_gateway/internal/middleware"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
)

func streamKeyScopeCtx(authType string, permissions ...string) context.Context {
	ctx := context.WithValue(context.Background(), ctxkeys.KeyTenantID, "tenant-1")
	ctx = context.WithValue(ctx, ctxkeys.KeyAuthType, authType)
	if len(permissions) > 0 {
		ctx = context.WithValue(ctx, ctxkeys.KeyPermissions, permissions)
	}
	return ctx
}

// Stream.streamKey is the one field every path to a Stream shares (stream,
// streamsConnection, and nested stream fields), so an API token without
// streams:write gets null plus a FORBIDDEN error naming the scope wherever
// it selects the key.
func TestStreamKeyFieldRequiresStreamsWriteForAPITokens(t *testing.T) {
	stream := &commodorepb.Stream{StreamId: "stream-1", IngestMode: "push", StreamKey: "sk_live_secret"}

	cases := []struct {
		name      string
		ctx       context.Context
		wantKey   bool
		wantScope bool
	}{
		{name: "api_token streams:read", ctx: streamKeyScopeCtx("api_token", "streams:read"), wantScope: true},
		{name: "api_token analytics:read", ctx: streamKeyScopeCtx("api_token", "analytics:read"), wantScope: true},
		{name: "api_token streams:write", ctx: streamKeyScopeCtx("api_token", "streams:write"), wantKey: true},
		{name: "jwt session", ctx: streamKeyScopeCtx("jwt"), wantKey: true},
		{name: "wallet session", ctx: streamKeyScopeCtx("wallet"), wantKey: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			key, err := (&streamResolver{}).StreamKey(tc.ctx, stream)
			if tc.wantKey {
				if err != nil || key == nil || *key != "sk_live_secret" {
					t.Fatalf("StreamKey() = (%v, %v), want the key", key, err)
				}
				return
			}
			if key != nil || !errors.Is(err, middleware.ErrForbidden) || !strings.Contains(err.Error(), "streams:write") {
				t.Fatalf("StreamKey() = (%v, %v), want (nil, ErrForbidden naming streams:write)", key, err)
			}
		})
	}
}

// A pull stream has no publishing key; the field stays null without an error
// whatever the caller's scopes.
func TestStreamKeyFieldNullForPullStreams(t *testing.T) {
	key, err := (&streamResolver{}).StreamKey(streamKeyScopeCtx("api_token", "streams:read"), &commodorepb.Stream{IngestMode: "pull"})
	if key != nil || err != nil {
		t.Fatalf("StreamKey() = (%v, %v), want (nil, nil)", key, err)
	}
}

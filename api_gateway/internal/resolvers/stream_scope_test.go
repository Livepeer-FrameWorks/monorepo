package resolvers

import (
	"context"
	"errors"
	"strings"
	"testing"

	"frameworks/api_gateway/internal/clients/clientstest"
	"frameworks/api_gateway/internal/middleware"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	commonpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/common"
)

func streamScopeAPITokenCtx(permissions ...string) context.Context {
	ctx := context.WithValue(clientstest.AuthedCtx("tenant-1"), ctxkeys.KeyAuthType, "api_token")
	return context.WithValue(ctx, ctxkeys.KeyPermissions, permissions)
}

func streamScopeFakeCommodore() *clientstest.FakeCommodore {
	return &clientstest.FakeCommodore{
		GetStreamFn: func(context.Context, string) (*commodorepb.Stream, error) {
			return &commodorepb.Stream{StreamId: "stream-1"}, nil
		},
		ListStreamsFn: func(context.Context, *commonpb.CursorPaginationRequest) (*commodorepb.ListStreamsResponse, error) {
			return &commodorepb.ListStreamsResponse{Pagination: &commonpb.CursorPaginationResponse{}}, nil
		},
		ListStreamKeysFn: func(context.Context, string, *commonpb.CursorPaginationRequest) (*commodorepb.ListStreamKeysResponse, error) {
			return &commodorepb.ListStreamKeysResponse{Pagination: &commonpb.CursorPaginationResponse{}}, nil
		},
	}
}

func wantScopeError(t *testing.T, err error, scope string) {
	t.Helper()
	if !errors.Is(err, middleware.ErrForbidden) || !strings.Contains(err.Error(), scope) {
		t.Fatalf("err = %v, want ErrForbidden naming %s", err, scope)
	}
}

func TestStreamReadsRequireStreamScopeForAPITokens(t *testing.T) {
	fake := streamScopeFakeCommodore()
	r := commoW2(fake)
	ctx := streamScopeAPITokenCtx("analytics:read")

	if _, err := r.DoGetStream(ctx, "stream-1"); err == nil {
		t.Fatal("DoGetStream accepted a token without streams:read")
	} else {
		wantScopeError(t, err, "streams:read")
	}
	if _, err := r.DoGetStreamsConnection(ctx, nil, nil, nil, nil, nil); err == nil {
		t.Fatal("DoGetStreamsConnection accepted a token without streams:read")
	} else {
		wantScopeError(t, err, "streams:read")
	}
	if _, err := r.DoGetStreams(ctx); err == nil {
		t.Fatal("DoGetStreams accepted a token without streams:read")
	} else {
		wantScopeError(t, err, "streams:read")
	}
	if fake.Calls != 0 {
		t.Fatalf("under-scoped reads reached Commodore %d times", fake.Calls)
	}
}

// streams:write admits stream reads: the write resolvers refetch the stream
// they changed, and a token that may change a stream may read it.
func TestStreamReadsAcceptReadOrWriteScope(t *testing.T) {
	for _, scope := range []string{"streams:read", "streams:write"} {
		t.Run(scope, func(t *testing.T) {
			r := commoW2(streamScopeFakeCommodore())
			ctx := streamScopeAPITokenCtx(scope)
			if _, err := r.DoGetStream(ctx, "stream-1"); err != nil {
				t.Fatalf("DoGetStream: %v", err)
			}
			if _, err := r.DoGetStreamsConnection(ctx, nil, nil, nil, nil, nil); err != nil {
				t.Fatalf("DoGetStreamsConnection: %v", err)
			}
		})
	}
}

func TestStreamKeyListingRequiresStreamsWrite(t *testing.T) {
	fake := streamScopeFakeCommodore()
	r := commoW2(fake)
	ctx := streamScopeAPITokenCtx("streams:read")
	streamID := "stream-1"

	if _, err := r.DoGetStreamKeysConnection(ctx, streamID, nil, nil, nil, nil); err == nil {
		t.Fatal("DoGetStreamKeysConnection accepted a streams:read token")
	} else {
		wantScopeError(t, err, "streams:write")
	}
	if _, err := r.DoGetStreamKeys(ctx, streamID); err == nil {
		t.Fatal("DoGetStreamKeys accepted a streams:read token")
	} else {
		wantScopeError(t, err, "streams:write")
	}
	if fake.Calls != 0 {
		t.Fatalf("streams:read token reached Commodore %d times", fake.Calls)
	}

	if _, err := r.DoGetStreamKeysConnection(streamScopeAPITokenCtx("streams:write"), streamID, nil, nil, nil, nil); err != nil {
		t.Fatalf("DoGetStreamKeysConnection with streams:write: %v", err)
	}
	if _, err := r.DoGetStreamKeysConnection(clientstest.AuthedCtx("tenant-1"), streamID, nil, nil, nil, nil); err != nil {
		t.Fatalf("DoGetStreamKeysConnection for a session: %v", err)
	}
}

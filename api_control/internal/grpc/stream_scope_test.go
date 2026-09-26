package grpc

import (
	"context"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const scopeTestStreamKey = "sk_scope_test_secret"

// streamScopeCtx is a tenant owner's request carrying authType; permissions
// apply to API tokens, the only auth type Commodore restricts by scope.
func streamScopeCtx(authType string, permissions ...string) context.Context {
	ctx := ctxAs("u1", testTenantID, "owner")
	ctx = context.WithValue(ctx, ctxkeys.KeyAuthType, authType)
	if len(permissions) > 0 {
		ctx = context.WithValue(ctx, ctxkeys.KeyPermissions, permissions)
	}
	return ctx
}

func scopeTestStreamRow() *sqlmock.Rows {
	return pushFullRow().AddRow(
		"s1", "live+abc", scopeTestStreamKey, "pb-1", "Title", nil,
		false, fixedTS, fixedTS, "push",
		nil, nil, "{}", nil, false, nil, "{}", nil,
		nil, nil, nil, nil, nil)
}

func TestStreamReadRPCsRejectAPITokenWithoutStreamScope(t *testing.T) {
	s, mock, done := newMockServer(t)
	defer done()
	ctx := streamScopeCtx("api_token", "analytics:read")

	calls := map[string]func() error{
		"GetStream": func() error {
			_, err := s.GetStream(ctx, &commodorepb.GetStreamRequest{StreamId: "s1"})
			return err
		},
		"ListStreams": func() error {
			_, err := s.ListStreams(ctx, &commodorepb.ListStreamsRequest{})
			return err
		},
		"GetStreamsBatch": func() error {
			_, err := s.GetStreamsBatch(ctx, &commodorepb.GetStreamsBatchRequest{StreamIds: []string{"s1"}})
			return err
		},
	}
	for name, call := range calls {
		t.Run(name, func(t *testing.T) {
			err := call()
			if status.Code(err) != codes.PermissionDenied || !strings.Contains(err.Error(), "streams:read") {
				t.Fatalf("err = %v, want PermissionDenied naming streams:read", err)
			}
		})
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("under-scoped read reached the database: %v", err)
	}
}

func TestStreamWriteRPCsRejectReadOnlyAPIToken(t *testing.T) {
	s, mock, done := newMockServer(t)
	defer done()
	ctx := streamScopeCtx("api_token", "streams:read")

	calls := map[string]func() error{
		"CreateStream": func() error {
			_, err := s.CreateStream(ctx, &commodorepb.CreateStreamRequest{Title: "t"})
			return err
		},
		"UpdateStream": func() error {
			_, err := s.UpdateStream(ctx, &commodorepb.UpdateStreamRequest{StreamId: "s1"})
			return err
		},
		"DeleteStream": func() error {
			_, err := s.DeleteStream(ctx, &commodorepb.DeleteStreamRequest{StreamId: "s1"})
			return err
		},
		"RefreshStreamKey": func() error {
			_, err := s.RefreshStreamKey(ctx, &commodorepb.RefreshStreamKeyRequest{StreamId: "s1"})
			return err
		},
		"CreateStreamKey": func() error {
			_, err := s.CreateStreamKey(ctx, &commodorepb.CreateStreamKeyRequest{StreamId: "s1"})
			return err
		},
		"ListStreamKeys": func() error {
			_, err := s.ListStreamKeys(ctx, &commodorepb.ListStreamKeysRequest{StreamId: "s1"})
			return err
		},
		"DeactivateStreamKey": func() error {
			_, err := s.DeactivateStreamKey(ctx, &commodorepb.DeactivateStreamKeyRequest{StreamId: "s1", KeyId: "k1"})
			return err
		},
	}
	for name, call := range calls {
		t.Run(name, func(t *testing.T) {
			err := call()
			if status.Code(err) != codes.PermissionDenied || !strings.Contains(err.Error(), "streams:write") {
				t.Fatalf("err = %v, want PermissionDenied naming streams:write", err)
			}
		})
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("read-only token reached the database: %v", err)
	}
}

// TestStreamReadsReturnKeyOnlyToWriteScope covers every tenant stream read:
// an API token needs streams:write to receive the publishing key, while
// session and service callers keep receiving it.
func TestStreamReadsReturnKeyOnlyToWriteScope(t *testing.T) {
	cases := []struct {
		name    string
		ctx     context.Context
		wantKey string
	}{
		{name: "api_token streams:read", ctx: streamScopeCtx("api_token", "streams:read"), wantKey: ""},
		{name: "api_token streams:write", ctx: streamScopeCtx("api_token", "streams:write"), wantKey: scopeTestStreamKey},
		{name: "api_token streams:read+write", ctx: streamScopeCtx("api_token", "streams:read", "streams:write"), wantKey: scopeTestStreamKey},
		{name: "jwt session", ctx: streamScopeCtx("jwt"), wantKey: scopeTestStreamKey},
		{name: "service", ctx: streamScopeCtx("service"), wantKey: scopeTestStreamKey},
	}
	for _, tc := range cases {
		t.Run(tc.name+"/GetStream", func(t *testing.T) {
			s, mock, done := newMockServer(t)
			defer done()
			mock.ExpectQuery("LEFT JOIN commodore.stream_pull_sources").
				WithArgs("s1", "u1", testTenantID).
				WillReturnRows(scopeTestStreamRow())
			expectStreamPlacementRead(mock, nil)

			stream, err := s.GetStream(tc.ctx, &commodorepb.GetStreamRequest{StreamId: "s1"})
			if err != nil {
				t.Fatalf("GetStream: %v", err)
			}
			if stream.GetStreamKey() != tc.wantKey {
				t.Fatalf("stream_key = %q, want %q", stream.GetStreamKey(), tc.wantKey)
			}
			if stream.GetPlaybackId() != "pb-1" {
				t.Fatalf("playback_id = %q, want pb-1", stream.GetPlaybackId())
			}
		})

		t.Run(tc.name+"/ListStreams", func(t *testing.T) {
			s, mock, done := newMockServer(t)
			defer done()
			mock.ExpectQuery("COUNT").
				WithArgs("u1", testTenantID).
				WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(int32(1)))
			mock.ExpectQuery("LEFT JOIN commodore.stream_pull_sources").
				WithArgs("u1", testTenantID, false, "", int32(51)).
				WillReturnRows(scopeTestStreamRow())
			expectStreamPlacementRead(mock, nil)

			resp, err := s.ListStreams(tc.ctx, &commodorepb.ListStreamsRequest{})
			if err != nil {
				t.Fatalf("ListStreams: %v", err)
			}
			if len(resp.GetStreams()) != 1 || resp.GetStreams()[0].GetStreamKey() != tc.wantKey {
				t.Fatalf("streams = %+v, want one with stream_key %q", resp.GetStreams(), tc.wantKey)
			}
		})

		t.Run(tc.name+"/GetStreamsBatch", func(t *testing.T) {
			s, mock, done := newMockServer(t)
			defer done()
			mock.ExpectQuery("LEFT JOIN commodore.stream_pull_sources").
				WithArgs(sqlmock.AnyArg(), "u1", testTenantID).
				WillReturnRows(scopeTestStreamRow())
			expectStreamPlacementRead(mock, nil)

			resp, err := s.GetStreamsBatch(tc.ctx, &commodorepb.GetStreamsBatchRequest{StreamIds: []string{"s1"}})
			if err != nil {
				t.Fatalf("GetStreamsBatch: %v", err)
			}
			if len(resp.GetStreams()) != 1 || resp.GetStreams()[0].GetStreamKey() != tc.wantKey {
				t.Fatalf("streams = %+v, want one with stream_key %q", resp.GetStreams(), tc.wantKey)
			}
		})
	}
}

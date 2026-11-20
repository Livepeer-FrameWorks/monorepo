package tools

import (
	"context"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"sync/atomic"
	"testing"

	"frameworks/api_gateway/internal/clients/clientstest"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
)

func keyToolCtx() context.Context {
	return context.WithValue(context.Background(), ctxkeys.KeyTenantID, "t1")
}

// Destructive key tools require an exact confirmation phrase before touching
// Commodore. The wrong phrase must short-circuit to a tool error and never
// reach the backend (an unstubbed fake would panic if it did).
func TestHandleRefreshStreamKey_ConfirmationGate(t *testing.T) {
	sc := clientstest.Clients(clientstest.WithCommodore(&clientstest.FakeCommodore{}))
	res, _, err := handleRefreshStreamKey(keyToolCtx(), RefreshStreamKeyInput{StreamID: "s1", Confirm: "nope"}, sc, clientstest.DiscardLogger())
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Fatal("wrong confirmation should be a tool error")
	}
}

func TestHandleRefreshStreamKey_Success(t *testing.T) {
	commo := &clientstest.FakeCommodore{
		RefreshKeyFn: func(context.Context, string) (*commodorepb.RefreshStreamKeyResponse, error) {
			return &commodorepb.RefreshStreamKeyResponse{StreamId: "s1", StreamKey: "sk_rotated"}, nil
		},
	}
	sc := clientstest.Clients(clientstest.WithCommodore(commo))
	res, out, err := handleRefreshStreamKey(keyToolCtx(), RefreshStreamKeyInput{StreamID: "s1", Confirm: "ROTATE STREAM KEY"}, sc, clientstest.DiscardLogger())
	if err != nil || res.IsError {
		t.Fatalf("success expected: err=%v text=%s", err, extractToolText(res))
	}
	if rk := out.(RefreshStreamKeyResult); rk.NewStreamKey != "sk_rotated" {
		t.Fatalf("expected rotated key, got %+v", rk)
	}
}

func TestHandleRefreshStreamKey_RequiresAuth(t *testing.T) {
	sc := clientstest.Clients(clientstest.WithCommodore(&clientstest.FakeCommodore{}))
	if _, _, err := handleRefreshStreamKey(context.Background(), RefreshStreamKeyInput{StreamID: "s1", Confirm: "ROTATE STREAM KEY"}, sc, clientstest.DiscardLogger()); err == nil {
		t.Fatal("expected auth error without tenant")
	}
}

func TestHandleDisconnectIngest_ConfirmationGate(t *testing.T) {
	sc := clientstest.Clients(clientstest.WithCommodore(&clientstest.FakeCommodore{}))
	result, _, err := handleDisconnectIngest(keyToolCtx(), DisconnectIngestInput{StreamID: "s1", Confirm: "no"}, sc)
	if err != nil || !result.IsError {
		t.Fatalf("unconfirmed disconnect reached backend: %v %v", result, err)
	}
}

func TestHandleRefreshStreamKey_EndUsesDistinctFencedRPC(t *testing.T) {
	backend := &clientstest.FakeCommodore{RefreshKeyAndEndFn: func(context.Context, string) (*commodorepb.RefreshStreamKeyResponse, error) {
		return &commodorepb.RefreshStreamKeyResponse{StreamId: "s1", StreamKey: "new"}, nil
	}}
	result, _, err := handleRefreshStreamKey(keyToolCtx(), RefreshStreamKeyInput{StreamID: "s1", EndActiveSession: true, Confirm: "ROTATE STREAM KEY"}, clientstest.Clients(clientstest.WithCommodore(backend)), clientstest.DiscardLogger())
	if err != nil || result.IsError {
		t.Fatalf("rotate-and-end used an unfenced RPC: %v %v", result, err)
	}
}

func TestIngestStopMCPFencedCalls(t *testing.T) {
	for _, scope := range []string{"streams:write"} {
		t.Run(scope, func(t *testing.T) {
			ctx := context.WithValue(clientstest.AuthedCtx("tenant"), ctxkeys.KeyAuthType, "api_token")
			ctx = context.WithValue(ctx, ctxkeys.KeyPermissions, []string{scope})
			var calls atomic.Int32
			backend := &clientstest.FakeCommodore{
				DisconnectIngestFn: func(context.Context, string) (*commodorepb.DisconnectIngestResponse, error) {
					calls.Add(1)
					return &commodorepb.DisconnectIngestResponse{StreamId: "stream", StopRequested: true}, nil
				},
				RefreshKeyAndEndFn: func(context.Context, string) (*commodorepb.RefreshStreamKeyResponse, error) {
					calls.Add(1)
					return &commodorepb.RefreshStreamKeyResponse{StreamId: "stream", StreamKey: "rotated"}, nil
				},
			}
			server := mcp.NewServer(&mcp.Implementation{Name: "stop-test", Version: "test"}, nil)
			RegisterStreamTools(server, clientstest.Clients(clientstest.WithCommodore(backend)), nil, nil, clientstest.DiscardLogger())
			a, b := mcp.NewInMemoryTransports()
			ss, err := server.Connect(ctx, a, nil)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = ss.Close() })
			client := mcp.NewClient(&mcp.Implementation{Name: "stop-test", Version: "test"}, nil)
			session, err := client.Connect(ctx, b, nil)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = session.Close() })
			for _, name := range []string{"disconnect_ingest", "refresh_stream_key"} {
				args := map[string]any{"stream_id": "stream", "confirm": "DISCONNECT INGEST"}
				if name == "refresh_stream_key" {
					args["confirm"] = "ROTATE STREAM KEY"
					args["end_active_session"] = true
				}
				result, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: name, Arguments: args})
				if err != nil || result.IsError == (scope == "streams:write") {
					t.Fatalf("%s under %s: %v %v", name, scope, result, err)
				}
			}
			expected := int32(0)
			if scope == "streams:write" {
				expected = 2
			}
			if calls.Load() != expected {
				t.Fatalf("scope %s made %d backend calls, want %d", scope, calls.Load(), expected)
			}
		})
	}
}

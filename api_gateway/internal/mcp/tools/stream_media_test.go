package tools

import (
	"context"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/pagination"
	"testing"
	"time"

	"frameworks/api_gateway/internal/clients/clientstest"
	"frameworks/api_gateway/internal/resolvers"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	commonpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/common"
	sharedpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/shared"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestStreamMediaMCPUsesStreamsReadAndForwardsCursor(t *testing.T) {
	for _, scope := range []string{"streams:read", "billing:read", "analytics:read"} {
		t.Run(scope, func(t *testing.T) {
			ctx := context.WithValue(clientstest.AuthedCtx("tenant"), ctxkeys.KeyAuthType, "api_token")
			ctx = context.WithValue(ctx, ctxkeys.KeyPermissions, []string{scope})
			reads := 0
			cursor := pagination.EncodeCursor(time.Now().UTC(), "00000000-0000-0000-0000-000000000001")
			backend := &clientstest.FakeCommodore{
				GetStreamFn: func(context.Context, string) (*pb.Stream, error) { reads++; return &pb.Stream{StreamId: "stream"}, nil },
				ListStreamArtifactsFn: func(_ context.Context, request *pb.ListStreamArtifactsRequest) (*pb.ListStreamArtifactsResponse, error) {
					reads++
					if request.StreamId != "stream" || request.Pagination.GetFirst() != 2 || request.Pagination.GetAfter() != cursor {
						t.Errorf("media page not forwarded: %v", request)
					}
					return &pb.ListStreamArtifactsResponse{Pagination: &commonpb.CursorPaginationResponse{}}, nil
				},
				GetDVRRecordingFn: func(_ context.Context, id string) (*sharedpb.DVRInfo, error) {
					reads++
					if id != "recording" {
						t.Errorf("wrong recording id: %s", id)
					}
					return &sharedpb.DVRInfo{DvrHash: "recording"}, nil
				},
			}
			server := mcp.NewServer(&mcp.Implementation{Name: "stream-media-test", Version: "test"}, nil)
			registerStreamMediaTools(server, &resolvers.Resolver{Clients: clientstest.Clients(clientstest.WithCommodore(backend))})
			clientTransport, serverTransport := mcp.NewInMemoryTransports()
			serverSession, err := server.Connect(ctx, serverTransport, nil)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = serverSession.Close() })
			client := mcp.NewClient(&mcp.Implementation{Name: "media-test", Version: "test"}, nil)
			session, err := client.Connect(ctx, clientTransport, nil)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = session.Close() })
			for _, name := range []string{"list_stream_recordings", "list_stream_clips", "get_dvr_recording"} {
				args := map[string]any{"stream_id": "stream", "first": 2, "after": cursor}
				if name == "get_dvr_recording" {
					args = map[string]any{"id": "recording"}
				}
				result, callErr := session.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
				allowed := scope == "streams:read"
				if callErr != nil || result.IsError == allowed {
					t.Fatalf("%s with %s returned %v %v", name, scope, result, callErr)
				}
			}
			if scope != "streams:read" && reads != 0 {
				t.Fatalf("denied scope made %d catalog calls", reads)
			}
			if scope == "streams:read" && reads != 5 {
				t.Fatalf("media tools made %d catalog calls, want 5", reads)
			}
		})
	}
}

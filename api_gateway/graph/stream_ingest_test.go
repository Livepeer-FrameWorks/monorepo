package graph

import (
	"context"
	"encoding/json"
	"errors"
	"frameworks/api_gateway/internal/loaders"
	"frameworks/api_gateway/internal/middleware"
	"frameworks/api_gateway/internal/resolvers"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	commonpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/common"
	"net/http"
	"sync/atomic"
	"testing"

	"frameworks/api_gateway/internal/clients/clientstest"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/globalid"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn_control"
)

func TestStreamIngestFieldFailureDoesNotNullStreams(t *testing.T) {
	clients := fakeServiceClients()
	clients.Commodore = &clientstest.FakeCommodore{
		GetStreamFn: func(_ context.Context, id string) (*commodorepb.Stream, error) {
			return &commodorepb.Stream{StreamId: id}, nil
		},
		GetStreamIngestStatesFn: func(_ context.Context, ids []string) (*pb.GetStreamIngestStatesResponse, error) {
			response := &pb.GetStreamIngestStatesResponse{States: map[string]*pb.StreamIngest{}}
			for _, id := range ids {
				if id == "healthy" {
					response.States[id] = &pb.StreamIngest{State: pb.StreamIngestState_STREAM_INGEST_STATE_LIVE, IsLive: true, SessionId: "session-1"}
				} else {
					response.UnavailableStreamIds = append(response.UnavailableStreamIds, id)
				}
			}
			return response, nil
		},
	}
	server := newRealPathTestServer(clients)
	query := `{healthy:stream(id:"` + globalid.Encode(globalid.TypeStream, "healthy") + `"){streamId ingest{state isLive sessionId}} unavailable:stream(id:"` + globalid.Encode(globalid.TypeStream, "unavailable") + `"){streamId ingest{state}}}`
	response, code := tryExecuteRealPath(server, query, nil)
	var data struct {
		Healthy struct {
			StreamID string
			Ingest   *struct {
				State     string
				IsLive    bool
				SessionID string
			}
		}
		Unavailable struct {
			StreamID string
			Ingest   any
		}
	}
	if err := json.Unmarshal(response.Data, &data); err != nil {
		t.Fatal(err)
	}
	if code != 200 || data.Healthy.Ingest == nil || !data.Healthy.Ingest.IsLive || data.Healthy.Ingest.SessionID != "session-1" || data.Unavailable.StreamID != "unavailable" || data.Unavailable.Ingest != nil || len(response.Errors) != 1 {
		t.Fatalf("partial cell result: data=%s errors=%v", response.Data, response.Errors)
	}
	if string(response.Errors[0].Path) != `["unavailable","ingest"]` {
		t.Fatalf("wrong field error path: %s", response.Errors[0].Path)
	}
	if field := server.schema.Types["Stream"].Fields.ForName("ingest"); field == nil || field.Type.NonNull {
		t.Fatal("Stream.ingest must be nullable")
	}
}

func TestStreamIngestUsesStreamsReadScope(t *testing.T) {
	for _, permission := range []string{"streams:read", "billing:read", "analytics:read"} {
		t.Run(permission, func(t *testing.T) {
			clients := fakeServiceClients()
			clients.Commodore = &clientstest.FakeCommodore{GetStreamIngestStatesFn: func(context.Context, []string) (*pb.GetStreamIngestStatesResponse, error) {
				return &pb.GetStreamIngestStatesResponse{States: map[string]*pb.StreamIngest{"stream": {State: pb.StreamIngestState_STREAM_INGEST_STATE_LIVE, IsLive: true}}}, nil
			}}
			root := &Resolver{Resolver: &resolvers.Resolver{Clients: clients}}
			ctx := context.WithValue(t.Context(), ctxkeys.KeyAuthType, "api_token")
			ctx = context.WithValue(ctx, ctxkeys.KeyTenantID, "tenant")
			ctx = context.WithValue(ctx, ctxkeys.KeyPermissions, []string{permission})
			view, err := (&streamResolver{root}).Ingest(ctx, &commodorepb.Stream{StreamId: "stream"})
			if permission == "streams:read" {
				if err != nil || view == nil || !view.IsLive {
					t.Fatalf("streams read state: %v %v", view, err)
				}
			} else if view != nil || !errors.Is(err, middleware.ErrForbidden) {
				t.Fatalf("%s bypassed stream scope: %v %v", permission, view, err)
			}
		})
	}
}

func TestStreamIngestConnectionBatchesAndKeepsFailedCellNullable(t *testing.T) {
	clients := fakeServiceClients()
	var calls atomic.Int32
	clients.Commodore = &clientstest.FakeCommodore{
		ListStreamsFn: func(context.Context, *commonpb.CursorPaginationRequest) (*commodorepb.ListStreamsResponse, error) {
			return &commodorepb.ListStreamsResponse{Streams: []*commodorepb.Stream{{StreamId: "healthy"}, {StreamId: "unavailable"}}, Pagination: &commonpb.CursorPaginationResponse{TotalCount: 2}}, nil
		},
		GetStreamIngestStatesFn: func(_ context.Context, ids []string) (*pb.GetStreamIngestStatesResponse, error) {
			calls.Add(1)
			if len(ids) != 2 {
				t.Errorf("ingest lookup was not batched: %v", ids)
			}
			return &pb.GetStreamIngestStatesResponse{States: map[string]*pb.StreamIngest{"healthy": {State: pb.StreamIngestState_STREAM_INGEST_STATE_LIVE, IsLive: true, SessionId: "session"}}, UnavailableStreamIds: []string{"unavailable"}}, nil
		},
	}
	server := newRealPathTestServer(clients)
	inner := server.handler
	server.handler = http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		ctx := context.WithValue(request.Context(), ctxkeys.KeyAuthType, "api_token")
		ctx = context.WithValue(ctx, ctxkeys.KeyPermissions, []string{"streams:read"})
		ctx = loaders.ContextWithLoaders(ctx, loaders.New(clients))
		inner.ServeHTTP(w, request.WithContext(ctx))
	})
	response, code := tryExecuteRealPath(server, `{streamsConnection(page:{first:2}){nodes{streamId ingest{state sessionId}}}}`, nil)
	var data struct {
		StreamsConnection struct {
			Nodes []struct {
				StreamID string
				Ingest   *struct{ State, SessionID string }
			}
		}
	}
	if err := json.Unmarshal(response.Data, &data); err != nil {
		t.Fatal(err)
	}
	nodes := data.StreamsConnection.Nodes
	if code != 200 || len(nodes) != 2 || nodes[0].Ingest == nil || nodes[0].Ingest.State != "LIVE" || nodes[1].StreamID != "unavailable" || nodes[1].Ingest != nil || calls.Load() != 1 || len(response.Errors) != 1 || string(response.Errors[0].Path) != `["streamsConnection","nodes",1,"ingest"]` {
		t.Fatalf("connection cell isolation failed: %s errors=%v calls=%d", response.Data, response.Errors, calls.Load())
	}
}

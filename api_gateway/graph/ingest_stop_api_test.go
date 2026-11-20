package graph

import (
	"context"
	"encoding/json"
	"errors"
	"frameworks/api_gateway/internal/middleware"
	"frameworks/api_gateway/internal/resolvers"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"frameworks/api_gateway/internal/clients/clientstest"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/globalid"
	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
)

func TestIngestStopMutationsReachFencedAPIsWithStreamsWrite(t *testing.T) {
	for _, action := range []string{"disconnect", "rotate_and_end"} {
		t.Run(action, func(t *testing.T) {
			var calls atomic.Int32
			clients := fakeServiceClients()
			clients.Commodore = &clientstest.FakeCommodore{
				DisconnectIngestFn: func(_ context.Context, id string) (*pb.DisconnectIngestResponse, error) {
					calls.Add(1)
					if id != "stream" {
						t.Errorf("wrong disconnect stream %s", id)
					}
					return &pb.DisconnectIngestResponse{StreamId: id, StopRequested: true}, nil
				},
				RefreshKeyAndEndFn: func(_ context.Context, id string) (*pb.RefreshStreamKeyResponse, error) {
					calls.Add(1)
					if id != "stream" {
						t.Errorf("wrong rotated stream %s", id)
					}
					return &pb.RefreshStreamKeyResponse{StreamId: id, StreamKey: "new-key"}, nil
				},
				GetStreamFn: func(context.Context, string) (*pb.Stream, error) {
					return &pb.Stream{StreamId: "stream", StreamKey: "new-key"}, nil
				},
			}
			server := newRealPathTestServer(clients)
			inner := server.handler
			server.handler = http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				ctx := context.WithValue(request.Context(), ctxkeys.KeyAuthType, "api_token")
				ctx = context.WithValue(ctx, ctxkeys.KeyPermissions, []string{"streams:write"})
				inner.ServeHTTP(writer, request.WithContext(ctx))
			})
			id := globalid.Encode(globalid.TypeStream, "stream")
			query := `mutation {disconnectIngest(streamId:"` + id + `"){__typename ... on IngestDisconnectSuccess{streamId stopRequested}}}`
			if action == "rotate_and_end" {
				query = `mutation {refreshStreamKey(id:"` + id + `",endActiveSession:true){__typename ... on Stream{streamId streamKey}}}`
			}
			response, code := tryExecuteRealPath(server, query, nil)
			var data map[string]map[string]any
			if err := json.Unmarshal(response.Data, &data); err != nil {
				t.Fatal(err)
			}
			if code != 200 || len(response.Errors) != 0 || calls.Load() != 1 {
				t.Fatalf("%s skipped the generation-fenced API: data=%s errors=%v calls=%d", action, response.Data, response.Errors, calls.Load())
			}
			if action == "disconnect" && data["disconnectIngest"]["stopRequested"] != true {
				t.Fatalf("disconnect did not report its durable request: %s", response.Data)
			}
		})
	}
}

func TestIngestStopMutationsDenyBillingAndReadScopes(t *testing.T) {
	for _, scope := range []string{"billing:write", "streams:read", "analytics:write"} {
		t.Run(scope, func(t *testing.T) {
			clients := fakeServiceClients()
			clients.Commodore = &clientstest.FakeCommodore{}
			server := newRealPathTestServer(clients)
			inner := server.handler
			server.handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				ctx := context.WithValue(r.Context(), ctxkeys.KeyAuthType, "api_token")
				ctx = context.WithValue(ctx, ctxkeys.KeyPermissions, []string{scope})
				inner.ServeHTTP(w, r.WithContext(ctx))
			})
			for _, query := range []string{`mutation{disconnectIngest(streamId:"stream"){__typename}}`, `mutation{refreshStreamKey(id:"stream",endActiveSession:true){__typename}}`} {
				response, _ := tryExecuteRealPath(server, query, nil)
				if len(response.Errors) == 0 && !strings.Contains(string(response.Data), `"__typename":"AuthError"`) {
					t.Fatalf("scope %s reached ingest mutation: %s", scope, response.Data)
				}
			}
		})
	}
}

func TestDisconnectPullReturnsTypedActionableError(t *testing.T) {
	clients := fakeServiceClients()
	clients.Commodore = &clientstest.FakeCommodore{DisconnectIngestFn: func(context.Context, string) (*pb.DisconnectIngestResponse, error) {
		return nil, status.Error(codes.FailedPrecondition, "pull stream: set pullSource.enabled=false instead")
	}}
	server := newRealPathTestServer(clients)
	response, _ := tryExecuteRealPath(server, `mutation{disconnectIngest(streamId:"stream"){__typename ... on ValidationError{message}}}`, nil)
	if len(response.Errors) != 0 {
		t.Fatalf("pull error escaped its typed result: %v", response.Errors)
	}
	var data struct {
		Result struct {
			Type    string `json:"__typename"`
			Message string
		} `json:"disconnectIngest"`
	}
	if err := json.Unmarshal(response.Data, &data); err != nil {
		t.Fatal(err)
	}
	if data.Result.Type != "ValidationError" || data.Result.Message == "" {
		t.Fatalf("pull instruction lost: %s", response.Data)
	}
}

func TestFirehoseRequiresEveryChannelScope(t *testing.T) {
	all := []string{"streams:read", "analytics:read", "infrastructure:read", "consultant:use"}
	resolver := &subscriptionResolver{&Resolver{Resolver: &resolvers.Resolver{Logger: logging.NewLogger()}}}
	for omitted := range all {
		scopes := append([]string{}, all[:omitted]...)
		scopes = append(scopes, all[omitted+1:]...)
		ctx := context.WithValue(t.Context(), ctxkeys.KeyAuthType, "api_token")
		ctx = context.WithValue(ctx, ctxkeys.KeyPermissions, scopes)
		if _, err := resolver.LiveFirehose(ctx); !errors.Is(err, middleware.ErrForbidden) || !strings.Contains(err.Error(), all[omitted]) {
			t.Fatalf("firehose opened without %s: %v", all[omitted], err)
		}
	}
}

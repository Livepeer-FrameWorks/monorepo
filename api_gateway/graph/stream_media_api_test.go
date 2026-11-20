package graph

import (
	"context"
	"encoding/json"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"frameworks/api_gateway/internal/clients/clientstest"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/globalid"
	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	commonpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/common"
	sharedpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/shared"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestStreamMediaFieldsReadCatalogWithoutAnalytics(t *testing.T) {
	clients := fakeServiceClients()
	var calls atomic.Int32
	clients.Commodore = &clientstest.FakeCommodore{
		GetStreamFn: func(context.Context, string) (*pb.Stream, error) { return &pb.Stream{StreamId: "stream"}, nil },
		ListStreamArtifactsFn: func(_ context.Context, request *pb.ListStreamArtifactsRequest) (*pb.ListStreamArtifactsResponse, error) {
			calls.Add(1)
			if request.StreamId != "stream" || request.Pagination.GetFirst() != 2 {
				t.Errorf("media page not forwarded: %v", request)
			}
			return &pb.ListStreamArtifactsResponse{Artifacts: []*pb.StorageArtifactInfo{{Id: "asset-" + request.Kind, Kind: request.Kind, ArtifactHash: "hash-" + request.Kind, Title: "Saved", Status: "ready", CreatedAt: timestamppb.Now(), UpdatedAt: timestamppb.Now()}}, Pagination: &commonpb.CursorPaginationResponse{TotalCount: 1}}, nil
		},
		GetDVRRecordingFn: func(_ context.Context, id string) (*sharedpb.DVRInfo, error) {
			if id != "recording" {
				t.Errorf("unexpected recording id %q", id)
			}
			return &sharedpb.DVRInfo{Id: proto.String("recording"), DvrHash: "hash", StreamId: proto.String("stream"), PlaybackId: proto.String("playback"), Status: "completed", StartedAt: timestamppb.New(time.Unix(1800000000, 0))}, nil
		},
	}
	server := newRealPathTestServer(clients)
	inner := server.handler
	server.handler = http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		ctx := context.WithValue(request.Context(), ctxkeys.KeyAuthType, "api_token")
		ctx = context.WithValue(ctx, ctxkeys.KeyPermissions, []string{"streams:read"})
		inner.ServeHTTP(w, request.WithContext(ctx))
	})
	response, code := tryExecuteRealPath(server, `{stream(id:"`+globalid.Encode(globalid.TypeStream, "stream")+`"){recordings(page:{first:2}){nodes{id status}totalCount}clips(page:{first:2}){nodes{id status}totalCount}}dvrRecording(id:"recording"){id status startedAt}}`, nil)
	var data struct {
		Stream struct {
			Recordings, Clips *struct {
				Nodes      []struct{ ID, Status string }
				TotalCount int
			}
		}
		DvrRecording *struct{ ID, Status, StartedAt string }
	}
	if err := json.Unmarshal(response.Data, &data); err != nil {
		t.Fatal(err)
	}
	if code != 200 || len(response.Errors) > 0 || calls.Load() != 2 || data.Stream.Recordings == nil || data.Stream.Clips == nil || data.DvrRecording == nil || data.DvrRecording.Status != "completed" || data.DvrRecording.StartedAt == "" {
		t.Fatalf("stream media fields did not read tenant catalog: %s errors=%v calls=%d", response.Data, response.Errors, calls.Load())
	}
}

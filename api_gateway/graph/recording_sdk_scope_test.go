package graph

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"frameworks/api_gateway/internal/clients/clientstest"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	sharedpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/shared"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestSDKRecordingCatalogReadWithStreamScope(t *testing.T) {
	raw, err := os.ReadFile("../../pkg/graphql/public/catalog.json")
	if err != nil {
		t.Fatal(err)
	}
	var catalog struct {
		Operations []struct{ Name, Document string }
	}
	if err := json.Unmarshal(raw, &catalog); err != nil {
		t.Fatal(err)
	}
	query := ""
	for _, operation := range catalog.Operations {
		if operation.Name == "GetDvrRecording" {
			query = operation.Document
		}
	}
	if query == "" {
		t.Fatal("SDK recording operation missing")
	}
	const recordingID = "d362d832-8d88-45c4-920c-464545ff1be1"
	for _, scope := range []string{"streams:read", "streams:write"} {
		t.Run(scope, func(t *testing.T) {
			fake := &clientstest.FakeCommodore{GetDVRRecordingFn: func(ctx context.Context, id string) (*sharedpb.DVRInfo, error) {
				if id != recordingID || ctxkeys.GetTenantID(ctx) != "tenant-1" {
					t.Errorf("catalog read identity: id=%q tenant=%q", id, ctxkeys.GetTenantID(ctx))
				}
				return &sharedpb.DVRInfo{Id: proto.String(recordingID), DvrHash: "recording-hash", Status: "ready", Title: proto.String("Saved recording"), PlaybackId: proto.String("recording-playback"), StreamId: proto.String("8ae91eda-bc06-45f3-b5e5-bacda0c5efb0"), CreatedAt: timestamppb.Now(), UpdatedAt: timestamppb.Now()}, nil
			}}
			fake.GetStreamFn = func(context.Context, string) (*commodorepb.Stream, error) { return nil, nil }
			services := fakeServiceClients()
			services.Commodore = fake
			server := newRealPathTestServer(services)
			body, err := json.Marshal(map[string]any{"query": query, "variables": map[string]any{"id": recordingID}})
			if err != nil {
				t.Fatal(err)
			}
			request := httptest.NewRequestWithContext(artifactScopeContext(scope), http.MethodPost, "/graphql", bytes.NewReader(body))
			request.Header.Set("Content-Type", "application/json")
			recorder := httptest.NewRecorder()
			server.handler.ServeHTTP(recorder, request)
			var response gqlResponse
			if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			if recorder.Code != http.StatusOK || len(response.Errors) != 0 {
				t.Fatalf("SDK catalog query with %s: HTTP %d, errors %+v", scope, recorder.Code, response.Errors)
			}
			var data struct {
				Recording struct {
					Status, DvrHash, PlaybackID string
				} `json:"dvrRecording"`
			}
			if err := json.Unmarshal(response.Data, &data); err != nil {
				t.Fatal(err)
			}
			if data.Recording.Status != "ready" || data.Recording.DvrHash != "recording-hash" || data.Recording.PlaybackID != "recording-playback" || fake.Calls != 1 {
				t.Fatalf("SDK catalog response=%s, catalog calls=%d", response.Data, fake.Calls)
			}
		})
	}
}

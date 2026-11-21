package tools

import (
	"context"
	"encoding/json"
	"frameworks/api_gateway/internal/clients/clientstest"
	"frameworks/api_gateway/internal/resolvers"
	sharedpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/shared"
	"strings"
	"testing"
)

func TestRecommendedActionForVodUpload(t *testing.T) {
	tests := []struct {
		name          string
		state         pbVodStatusEnum
		missingCount  int
		lastErrorCode string
		want          string
	}{
		{
			name:         "uploading with no missing parts can complete",
			state:        vodStatusUploading,
			missingCount: 0,
			want:         "complete_upload",
		},
		{
			name:         "uploading with missing parts retries missing parts",
			state:        vodStatusUploading,
			missingCount: 2,
			want:         "retry_missing_parts",
		},
		{
			name:          "reconciliation failure does not recommend completion",
			state:         vodStatusUploading,
			lastErrorCode: "storage_reconciliation_failed",
			want:          "retry_missing_parts",
		},
		{
			name:  "ready",
			state: vodStatusReady,
			want:  "ready",
		},
		{
			name:  "processing",
			state: vodStatusProcessing,
			want:  "wait_processing",
		},
		{
			name:  "expired",
			state: vodStatusExpired,
			want:  "restart_expired",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := recommendedActionForVodUpload(tt.state, tt.missingCount, tt.lastErrorCode)
			if got != tt.want {
				t.Fatalf("recommendedActionForVodUpload() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestMCPUploadRefusesUnsupportedFilenameWithoutCreatingWork(t *testing.T) {
	backend := &clientstest.FakeCommodore{CreateVodUploadFn: func(context.Context, *sharedpb.CreateVodUploadRequest) (*sharedpb.CreateVodUploadResponse, error) {
		t.Fatal("unsupported MCP filename reached upload creation")
		return nil, nil
	}}
	clients, checker, _ := streamToolSetup(backend, clientstest.SolventPurser())
	resolver := &resolvers.Resolver{Clients: clients, Logger: clientstest.DiscardLogger()}
	for _, filename := range []string{"payload.xyz", "playlist.m3u8", "playlist.mpd"} {
		result, _, err := handleCreateVodUpload(clientstest.AuthedCtx("mcp-format-refusal"), CreateVodUploadInput{Filename: filename, SizeBytes: 1024}, resolver, checker, clientstest.DiscardLogger())
		if err != nil || result == nil || !result.IsError || !strings.Contains(extractToolText(result), "unsupported filename") {
			t.Fatalf("%s refusal: result=%+v error=%v", filename, result, err)
		}
	}
}

func TestMCPUploadReportsUnavailableOriginalRetention(t *testing.T) {
	retained := false
	backend := &clientstest.FakeCommodore{CreateVodUploadFn: func(context.Context, *sharedpb.CreateVodUploadRequest) (*sharedpb.CreateVodUploadResponse, error) {
		return &sharedpb.CreateVodUploadResponse{UploadId: "upload", KeepOriginal: &retained}, nil
	}}
	clients, checker, _ := streamToolSetup(backend, clientstest.SolventPurser())
	resolver := &resolvers.Resolver{Clients: clients, Logger: clientstest.DiscardLogger()}
	result, data, err := handleCreateVodUpload(clientstest.AuthedCtx("retention-policy"), CreateVodUploadInput{Filename: "phone.mov", SizeBytes: 1024}, resolver, checker, clientstest.DiscardLogger())
	encoded, marshalErr := json.Marshal(data)
	if err != nil || marshalErr != nil || result == nil || result.IsError || !strings.Contains(string(encoded), `"keep_original":false`) {
		t.Fatalf("accepted upload did not expose its disabled retention: %s %v %v", encoded, err, marshalErr)
	}
	if !strings.Contains(string(encoded), "keeps only the processed video") {
		t.Fatalf("inherited no-original result was not explained: %s", encoded)
	}
	if strings.Contains(string(encoded), "temporarily unavailable") {
		t.Fatal("inheriting an account opt-out must not claim retention is unavailable")
	}
	wantOriginal := true
	result, data, err = handleCreateVodUpload(clientstest.AuthedCtx("retention-policy"), CreateVodUploadInput{Filename: "phone.mov", SizeBytes: 1024, KeepOriginal: &wantOriginal}, resolver, checker, clientstest.DiscardLogger())
	encoded, marshalErr = json.Marshal(data)
	if err != nil || marshalErr != nil || result == nil || result.IsError || (!strings.Contains(string(encoded), "Only the processed video will be kept") || strings.Contains(string(encoded), "temporarily unavailable")) {
		t.Fatalf("explicit retention downgrade was not explained: %s %v %v", encoded, err, marshalErr)
	}
}

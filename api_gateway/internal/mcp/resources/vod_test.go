package resources

import (
	"testing"
	"time"

	"frameworks/pkg/globalid"
	pb "frameworks/pkg/proto"

	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestProtoToVODAssetInfo_StatusMapping(t *testing.T) {
	tests := []struct {
		name   string
		status pb.VodStatus
		want   string
	}{
		{
			name:   "uploading",
			status: pb.VodStatus_VOD_STATUS_UPLOADING,
			want:   "UPLOADING",
		},
		{
			name:   "processing",
			status: pb.VodStatus_VOD_STATUS_PROCESSING,
			want:   "PROCESSING",
		},
		{
			name:   "ready",
			status: pb.VodStatus_VOD_STATUS_READY,
			want:   "READY",
		},
		{
			name:   "failed",
			status: pb.VodStatus_VOD_STATUS_FAILED,
			want:   "FAILED",
		},
		{
			name:   "deleted",
			status: pb.VodStatus_VOD_STATUS_DELETED,
			want:   "DELETED",
		},
		{
			name:   "unknown fallback",
			status: pb.VodStatus(999),
			want:   "UNKNOWN",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := protoToVODAssetInfo(&pb.VodAssetInfo{
				ArtifactHash: "artifact-hash-1",
				Status:       tc.status,
			})
			if got.Status != tc.want {
				t.Fatalf("Status: got %q, want %q", got.Status, tc.want)
			}
		})
	}
}

func TestProtoToVODAssetInfo_FieldMappingAndIDFallback(t *testing.T) {
	playbackID := "playback-key-1"
	sizeBytes := int64(1024)
	durationMs := int32(45000)
	resolution := "1920x1080"
	videoCodec := "h264"
	audioCodec := "aac"
	bitrateKbps := int32(2500)
	errorMessage := "transcode failed"
	expiresAt := time.Date(2026, 2, 21, 11, 45, 9, 0, time.UTC)
	createdAt := time.Date(2026, 2, 10, 6, 7, 8, 0, time.UTC)
	updatedAt := time.Date(2026, 2, 11, 9, 10, 11, 0, time.UTC)

	p := &pb.VodAssetInfo{
		Id:              "vod-uuid-1",
		ArtifactHash:    "",
		Title:           "Launch Stream",
		Description:     "Product launch recording",
		Filename:        "launch.mp4",
		Status:          pb.VodStatus_VOD_STATUS_READY,
		StorageLocation: "s3",
		SizeBytes:       &sizeBytes,
		DurationMs:      &durationMs,
		Resolution:      &resolution,
		VideoCodec:      &videoCodec,
		AudioCodec:      &audioCodec,
		BitrateKbps:     &bitrateKbps,
		CreatedAt:       timestamppb.New(createdAt),
		UpdatedAt:       timestamppb.New(updatedAt),
		ExpiresAt:       timestamppb.New(expiresAt),
		ErrorMessage:    &errorMessage,
		PlaybackId:      &playbackID,
	}

	got := protoToVODAssetInfo(p)

	wantID := globalid.Encode(globalid.TypeVodAsset, "vod-uuid-1")
	if got.ID != wantID {
		t.Fatalf("ID: got %q, want %q", got.ID, wantID)
	}
	if got.PlaybackID != playbackID {
		t.Fatalf("PlaybackID: got %q, want %q", got.PlaybackID, playbackID)
	}
	if got.ArtifactHash != "" {
		t.Fatalf("ArtifactHash: got %q, want empty string", got.ArtifactHash)
	}
	if got.StorageLocation != "s3" {
		t.Fatalf("StorageLocation: got %q, want %q", got.StorageLocation, "s3")
	}
	if got.Title == nil || *got.Title != "Launch Stream" {
		t.Fatalf("Title: got %v, want %q", got.Title, "Launch Stream")
	}
	if got.Description == nil || *got.Description != "Product launch recording" {
		t.Fatalf("Description: got %v, want %q", got.Description, "Product launch recording")
	}
	if got.Filename == nil || *got.Filename != "launch.mp4" {
		t.Fatalf("Filename: got %v, want %q", got.Filename, "launch.mp4")
	}
	if got.SizeBytes == nil || *got.SizeBytes != sizeBytes {
		t.Fatalf("SizeBytes: got %v, want %d", got.SizeBytes, sizeBytes)
	}
	if got.DurationMs == nil || *got.DurationMs != int(durationMs) {
		t.Fatalf("DurationMs: got %v, want %d", got.DurationMs, durationMs)
	}
	if got.Resolution == nil || *got.Resolution != resolution {
		t.Fatalf("Resolution: got %v, want %q", got.Resolution, resolution)
	}
	if got.VideoCodec == nil || *got.VideoCodec != videoCodec {
		t.Fatalf("VideoCodec: got %v, want %q", got.VideoCodec, videoCodec)
	}
	if got.AudioCodec == nil || *got.AudioCodec != audioCodec {
		t.Fatalf("AudioCodec: got %v, want %q", got.AudioCodec, audioCodec)
	}
	if got.BitrateKbps == nil || *got.BitrateKbps != int(bitrateKbps) {
		t.Fatalf("BitrateKbps: got %v, want %d", got.BitrateKbps, bitrateKbps)
	}
	if got.ErrorMessage == nil || *got.ErrorMessage != errorMessage {
		t.Fatalf("ErrorMessage: got %v, want %q", got.ErrorMessage, errorMessage)
	}
	if got.CreatedAt != "2026-02-10T06:07:08Z" {
		t.Fatalf("CreatedAt: got %q, want %q", got.CreatedAt, "2026-02-10T06:07:08Z")
	}
	if got.UpdatedAt != "2026-02-11T09:10:11Z" {
		t.Fatalf("UpdatedAt: got %q, want %q", got.UpdatedAt, "2026-02-11T09:10:11Z")
	}
	if got.ExpiresAt == nil || *got.ExpiresAt != "2026-02-21T11:45:09Z" {
		t.Fatalf("ExpiresAt: got %v, want %q", got.ExpiresAt, "2026-02-21T11:45:09Z")
	}
}

func TestProtoToVODAssetInfo_OmitsEmptyOptionalFields(t *testing.T) {
	emptyPlaybackID := ""

	p := &pb.VodAssetInfo{
		Id:           "vod-uuid-2",
		ArtifactHash: "artifact-2",
		Status:       pb.VodStatus_VOD_STATUS_UPLOADING,
		PlaybackId:   &emptyPlaybackID,
	}

	got := protoToVODAssetInfo(p)

	wantID := globalid.Encode(globalid.TypeVodAsset, "artifact-2")
	if got.ID != wantID {
		t.Fatalf("ID: got %q, want %q", got.ID, wantID)
	}
	if got.PlaybackID != "" {
		t.Fatalf("PlaybackID: got %q, want empty string when playback_id is empty", got.PlaybackID)
	}
	if got.Title != nil {
		t.Fatalf("Title: got %v, want nil", got.Title)
	}
	if got.Description != nil {
		t.Fatalf("Description: got %v, want nil", got.Description)
	}
	if got.Filename != nil {
		t.Fatalf("Filename: got %v, want nil", got.Filename)
	}
	if got.SizeBytes != nil {
		t.Fatalf("SizeBytes: got %v, want nil", got.SizeBytes)
	}
	if got.DurationMs != nil {
		t.Fatalf("DurationMs: got %v, want nil", got.DurationMs)
	}
	if got.BitrateKbps != nil {
		t.Fatalf("BitrateKbps: got %v, want nil", got.BitrateKbps)
	}
	if got.ErrorMessage != nil {
		t.Fatalf("ErrorMessage: got %v, want nil", got.ErrorMessage)
	}
	if got.ExpiresAt != nil {
		t.Fatalf("ExpiresAt: got %v, want nil", got.ExpiresAt)
	}
}

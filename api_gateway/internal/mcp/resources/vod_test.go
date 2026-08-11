package resources

import (
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/globalid"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"

	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestVodStatusLabel_Mapping(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"ready", "ready", "READY"},
		{"processing", "processing", "PROCESSING"},
		{"failed", "failed", "FAILED"},
		{"expired maps to deleted", "expired", "DELETED"},
		{"uploading", "uploading", "UPLOADING"},
		{"unknown fallback", "weird", "UNKNOWN"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := vodStatusLabel(tc.in); got != tc.want {
				t.Fatalf("vodStatusLabel(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// The MCP status is the catalog's DERIVED lifecycle status: a ready/failed asset maps to
// READY/FAILED. Fields (description/errorMessage/tracks/timestamps) map from the catalog row.
func TestStorageArtifactToVODAssetInfo_FieldMappingAndIDFallback(t *testing.T) {
	sizeBytes := int64(1024)
	durationMs := int64(45000)
	playbackID := "playback-key-1"
	secondaryLabel := "launch.mp4"
	createdAt := time.Date(2026, 2, 10, 6, 7, 8, 0, time.UTC)
	updatedAt := time.Date(2026, 2, 11, 9, 10, 11, 0, time.UTC)
	expiresAt := time.Date(2026, 2, 21, 11, 45, 9, 0, time.UTC)

	a := &commodorepb.StorageArtifactInfo{
		Id:              "vod-uuid-1",
		ArtifactHash:    "",
		Kind:            "vod",
		Title:           "Launch Stream",
		SecondaryLabel:  secondaryLabel,
		Description:     strp("Product launch recording"),
		ErrorMessage:    strp("transcode failed"),
		Status:          "ready",
		StorageLocation: strp("s3"),
		PlaybackId:      &playbackID,
		SizeBytes:       &sizeBytes,
		DurationMs:      &durationMs,
		Tracks: []*commodorepb.MediaTrack{
			{Type: "video", Codec: "h264", Resolution: strp("1920x1080"), BitrateKbps: i32p(2500)},
			{Type: "audio", Codec: "aac"},
		},
		CreatedAt: timestamppb.New(createdAt),
		UpdatedAt: timestamppb.New(updatedAt),
		ExpiresAt: timestamppb.New(expiresAt),
	}

	got := storageArtifactToVODAssetInfo(a)

	if want := globalid.Encode(globalid.TypeVodAsset, "vod-uuid-1"); got.ID != want {
		t.Fatalf("ID: got %q, want %q (id fallback when hash empty)", got.ID, want)
	}
	if got.Status != "READY" {
		t.Fatalf("Status: got %q, want READY", got.Status)
	}
	if got.PlaybackID != playbackID {
		t.Fatalf("PlaybackID: got %q", got.PlaybackID)
	}
	if got.StorageLocation != "s3" {
		t.Fatalf("StorageLocation: got %q", got.StorageLocation)
	}
	if got.Title == nil || *got.Title != "Launch Stream" {
		t.Fatalf("Title: got %v", got.Title)
	}
	if got.Filename == nil || *got.Filename != secondaryLabel {
		t.Fatalf("Filename (from secondary_label): got %v", got.Filename)
	}
	if got.Description == nil || *got.Description != "Product launch recording" {
		t.Fatalf("Description: got %v", got.Description)
	}
	if got.ErrorMessage == nil || *got.ErrorMessage != "transcode failed" {
		t.Fatalf("ErrorMessage: got %v", got.ErrorMessage)
	}
	if got.SizeBytes == nil || *got.SizeBytes != sizeBytes {
		t.Fatalf("SizeBytes: got %v", got.SizeBytes)
	}
	if got.DurationMs == nil || *got.DurationMs != int(durationMs) {
		t.Fatalf("DurationMs: got %v", got.DurationMs)
	}
	if got.Resolution == nil || *got.Resolution != "1920x1080" {
		t.Fatalf("Resolution (from video track): got %v", got.Resolution)
	}
	if got.VideoCodec == nil || *got.VideoCodec != "h264" {
		t.Fatalf("VideoCodec: got %v", got.VideoCodec)
	}
	if got.AudioCodec == nil || *got.AudioCodec != "aac" {
		t.Fatalf("AudioCodec: got %v", got.AudioCodec)
	}
	if got.BitrateKbps == nil || *got.BitrateKbps != 2500 {
		t.Fatalf("BitrateKbps: got %v", got.BitrateKbps)
	}
	if got.CreatedAt != "2026-02-10T06:07:08Z" {
		t.Fatalf("CreatedAt: got %q", got.CreatedAt)
	}
	if got.ExpiresAt == nil || *got.ExpiresAt != "2026-02-21T11:45:09Z" {
		t.Fatalf("ExpiresAt: got %v", got.ExpiresAt)
	}
}

func TestStorageArtifactToVODAssetInfo_OmitsEmptyOptionalFields(t *testing.T) {
	a := &commodorepb.StorageArtifactInfo{
		Id:           "vod-uuid-2",
		ArtifactHash: "artifact-2",
		Kind:         "vod",
		Status:       "processing",
	}

	got := storageArtifactToVODAssetInfo(a)

	if want := globalid.Encode(globalid.TypeVodAsset, "artifact-2"); got.ID != want {
		t.Fatalf("ID: got %q, want %q", got.ID, want)
	}
	if got.Status != "PROCESSING" {
		t.Fatalf("Status: got %q, want PROCESSING", got.Status)
	}
	if got.PlaybackID != "" {
		t.Fatalf("PlaybackID: got %q, want empty", got.PlaybackID)
	}
	if got.Title != nil || got.Filename != nil || got.SizeBytes != nil ||
		got.DurationMs != nil || got.Resolution != nil || got.BitrateKbps != nil || got.ExpiresAt != nil {
		t.Fatalf("expected empty optionals, got %+v", got)
	}
}

func strp(s string) *string { return &s }
func i32p(v int32) *int32   { return &v }

package handlers

import (
	"strings"
	"testing"
	"time"

	pb "frameworks/pkg/proto"
)

func TestExtractVODHash(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		expect string
	}{
		{name: "empty", input: "", expect: ""},
		{name: "vod prefix", input: "vod+abc123", expect: "abc123"},
		{name: "raw hash", input: strings.Repeat("a", 32), expect: strings.Repeat("a", 32)},
		{name: "non vod", input: "stream-123", expect: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractVODHash(tt.input)
			if got != tt.expect {
				t.Fatalf("expected %q, got %q", tt.expect, got)
			}
		})
	}
}

func TestIsVideoFile(t *testing.T) {
	if !IsVideoFile(".mp4") {
		t.Fatalf("expected mp4 to be recognized as video")
	}
	if IsVideoFile(".txt") {
		t.Fatalf("expected txt to be rejected as video")
	}
}

func TestDetermineQualityTier(t *testing.T) {
	tests := []struct {
		name   string
		tracks []*pb.StreamTrack
		expect string
	}{
		{
			name: "full data",
			tracks: []*pb.StreamTrack{
				{
					TrackType:   "video",
					Height:      int32Ptr(1080),
					Fps:         float64Ptr(59.6),
					BitrateKbps: int32Ptr(6000),
					Codec:       "H264",
				},
			},
			expect: "1080p60 H264 @ 6.0Mbps",
		},
		{
			name: "bps fallback",
			tracks: []*pb.StreamTrack{
				{
					TrackType:  "video",
					Height:     int32Ptr(720),
					BitrateBps: int64Ptr(500000),
				},
			},
			expect: "720p @ 500kbps",
		},
		{
			name: "no video tracks",
			tracks: []*pb.StreamTrack{
				{TrackType: "audio"},
			},
			expect: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := determineQualityTier(tt.tracks)
			if got != tt.expect {
				t.Fatalf("expected %q, got %q", tt.expect, got)
			}
		})
	}
}

func TestEnrichStreamBufferTrigger(t *testing.T) {
	trigger := &pb.StreamBufferTrigger{
		MistIssues: stringPtr("VeryLowBuffer"),
		Tracks: []*pb.StreamTrack{
			{
				TrackName: "video_1",
				TrackType: "video",
				Jitter:    int32Ptr(150),
				Buffer:    int32Ptr(40),
				Height:    int32Ptr(1080),
				Fps:       float64Ptr(30),
			},
		},
	}

	enrichStreamBufferTrigger(trigger)

	if trigger.TrackCount == nil || *trigger.TrackCount != 1 {
		t.Fatalf("expected track count to be 1, got %v", trigger.TrackCount)
	}
	if trigger.HasIssues == nil || !*trigger.HasIssues {
		t.Fatalf("expected issues to be detected")
	}
	if trigger.IssuesDescription == nil || !strings.Contains(*trigger.IssuesDescription, "VeryLowBuffer") {
		t.Fatalf("expected issues description to include mist issues, got %v", trigger.IssuesDescription)
	}
	if trigger.QualityTier == nil || *trigger.QualityTier == "" {
		t.Fatalf("expected quality tier to be set")
	}
}

func TestEnrichLiveTrackListTrigger(t *testing.T) {
	trigger := &pb.StreamTrackListTrigger{
		Tracks: []*pb.StreamTrack{
			{
				TrackType:   "video",
				Width:       int32Ptr(1920),
				Height:      int32Ptr(1080),
				Fps:         float64Ptr(30),
				BitrateKbps: int32Ptr(6000),
				Codec:       "H264",
			},
			{
				TrackType:   "audio",
				BitrateKbps: int32Ptr(128),
				Codec:       "AAC",
				Channels:    int32Ptr(2),
				SampleRate:  int32Ptr(48000),
			},
		},
	}

	enrichLiveTrackListTrigger(trigger)

	if trigger.TotalTracks == nil || *trigger.TotalTracks != 2 {
		t.Fatalf("expected total tracks to be 2, got %v", trigger.TotalTracks)
	}
	if trigger.VideoTrackCount == nil || *trigger.VideoTrackCount != 1 {
		t.Fatalf("expected video track count to be 1, got %v", trigger.VideoTrackCount)
	}
	if trigger.AudioTrackCount == nil || *trigger.AudioTrackCount != 1 {
		t.Fatalf("expected audio track count to be 1, got %v", trigger.AudioTrackCount)
	}
	if trigger.PrimaryWidth == nil || *trigger.PrimaryWidth != 1920 {
		t.Fatalf("expected primary width to be set, got %v", trigger.PrimaryWidth)
	}
	if trigger.PrimaryAudioChannels == nil || *trigger.PrimaryAudioChannels != 2 {
		t.Fatalf("expected primary audio channels to be set, got %v", trigger.PrimaryAudioChannels)
	}
	if trigger.QualityTier == nil || *trigger.QualityTier == "" {
		t.Fatalf("expected quality tier to be set")
	}
}

func TestCalculateFreezePriorityRecentAccess(t *testing.T) {
	sm := &StorageManager{}
	now := time.Now()

	recent := FreezeCandidate{
		CreatedAt:    now.Add(-48 * time.Hour),
		LastAccessed: now.Add(-2 * time.Hour),
		SizeBytes:    1024 * 1024,
	}
	stale := FreezeCandidate{
		CreatedAt:    now.Add(-48 * time.Hour),
		LastAccessed: now.Add(-72 * time.Hour),
		SizeBytes:    1024 * 1024,
	}

	recentPriority := sm.calculateFreezePriority(recent)
	stalePriority := sm.calculateFreezePriority(stale)

	if recentPriority >= stalePriority {
		t.Fatalf("expected recent access to reduce freeze priority: recent=%f stale=%f", recentPriority, stalePriority)
	}
}

func TestStorageManagerHelpers(t *testing.T) {
	sm := &StorageManager{}

	if !sm.isClipFile("/tmp/example.mp4") {
		t.Fatalf("expected mp4 to be recognized as clip file")
	}
	if sm.isClipFile("/tmp/example.txt") {
		t.Fatalf("expected txt to be rejected as clip file")
	}

	if got := sm.extractHashFromPath("/var/clip/short.mp4"); got != "" {
		t.Fatalf("expected short hash to be empty, got %q", got)
	}

	longName := strings.Repeat("b", 18)
	if got := sm.extractHashFromPath("/var/clip/" + longName + ".mp4"); got != longName {
		t.Fatalf("expected hash %q, got %q", longName, got)
	}
}

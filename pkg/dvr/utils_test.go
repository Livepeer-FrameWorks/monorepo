package dvr

import (
	"strings"
	"testing"
	"time"
)

func TestGenerateDVRHash(t *testing.T) {
	t.Run("generates 32 character hex string", func(t *testing.T) {
		hash, err := GenerateDVRHash()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(hash) != 32 {
			t.Errorf("hash length = %d, want 32", len(hash))
		}
		for _, c := range hash {
			if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
				t.Errorf("invalid hex character: %c", c)
			}
		}
	})

	t.Run("generates unique hashes", func(t *testing.T) {
		hash1, err1 := GenerateDVRHash()
		if err1 != nil {
			t.Fatalf("first hash generation failed: %v", err1)
		}
		hash2, err2 := GenerateDVRHash()
		if err2 != nil {
			t.Fatalf("second hash generation failed: %v", err2)
		}
		if hash1 == hash2 {
			t.Error("hashes should differ due to random bytes (collision is astronomically unlikely)")
		}
	})
}

func TestBuildDVRStoragePath(t *testing.T) {
	tests := []struct {
		nodeID     string
		dvrHash    string
		streamName string
		want       string
	}{
		{"node1", "abc123", "stream1", "dvr/node1/abc123/stream1"},
		{"node-2", "def456", "tenant/stream", "dvr/node-2/def456/tenant/stream"},
	}

	for _, tt := range tests {
		t.Run(tt.nodeID, func(t *testing.T) {
			got := BuildDVRStoragePath(tt.nodeID, tt.dvrHash, tt.streamName)
			if !strings.HasPrefix(got, "dvr/") {
				t.Errorf("path should start with dvr/, got %q", got)
			}
			if !strings.Contains(got, tt.nodeID) {
				t.Errorf("path should contain nodeID %q, got %q", tt.nodeID, got)
			}
			if !strings.Contains(got, tt.dvrHash) {
				t.Errorf("path should contain dvrHash %q, got %q", tt.dvrHash, got)
			}
		})
	}
}

func TestBuildDVRManifestPath(t *testing.T) {
	got := BuildDVRManifestPath("/var/dvr/node1/abc123", "stream1")
	if !strings.HasSuffix(got, "stream1.m3u8") {
		t.Errorf("manifest path should end with stream1.m3u8, got %q", got)
	}
}

func TestBuildDVRSegmentPath(t *testing.T) {
	tests := []struct {
		basePath   string
		streamName string
		segmentNum int
		wantSuffix string
	}{
		{"/var/dvr", "stream1", 0, "stream1_000000.ts"},
		{"/var/dvr", "stream1", 1, "stream1_000001.ts"},
		{"/var/dvr", "stream1", 999999, "stream1_999999.ts"},
	}

	for _, tt := range tests {
		t.Run(tt.wantSuffix, func(t *testing.T) {
			got := BuildDVRSegmentPath(tt.basePath, tt.streamName, tt.segmentNum)
			if !strings.HasSuffix(got, tt.wantSuffix) {
				t.Errorf("got %q, want suffix %q", got, tt.wantSuffix)
			}
		})
	}
}

func TestDefaultDVRConfig(t *testing.T) {
	cfg := DefaultDVRConfig()
	if cfg.Enabled {
		t.Error("default should be disabled")
	}
	if cfg.RetentionDays <= 0 {
		t.Error("retention days should be positive")
	}
	if cfg.Format == "" {
		t.Error("format should not be empty")
	}
	if cfg.SegmentDuration <= 0 {
		t.Error("segment duration should be positive")
	}
}

func TestDVRConfigIsRecordingEnabled(t *testing.T) {
	tests := []struct {
		enabled bool
		want    bool
	}{
		{true, true},
		{false, false},
	}

	for _, tt := range tests {
		cfg := DVRConfig{Enabled: tt.enabled}
		if got := cfg.IsRecordingEnabled(); got != tt.want {
			t.Errorf("IsRecordingEnabled() = %v, want %v", got, tt.want)
		}
	}
}

func TestDVRConfigGetRetentionTime(t *testing.T) {
	tests := []struct {
		days int
		want time.Duration
	}{
		{1, 24 * time.Hour},
		{7, 7 * 24 * time.Hour},
		{30, 30 * 24 * time.Hour},
	}

	for _, tt := range tests {
		cfg := DVRConfig{RetentionDays: tt.days}
		if got := cfg.GetRetentionTime(); got != tt.want {
			t.Errorf("GetRetentionTime() = %v, want %v", got, tt.want)
		}
	}
}

func TestDVRConfigGetSegmentDuration(t *testing.T) {
	tests := []struct {
		secs int
		want time.Duration
	}{
		{6, 6 * time.Second},
		{10, 10 * time.Second},
		{1, 1 * time.Second},
	}

	for _, tt := range tests {
		cfg := DVRConfig{SegmentDuration: tt.secs}
		if got := cfg.GetSegmentDuration(); got != tt.want {
			t.Errorf("GetSegmentDuration() = %v, want %v", got, tt.want)
		}
	}
}

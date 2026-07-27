package handlers

import (
	"math"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/sirupsen/logrus"
)

func newTestCleanupMonitor() *CleanupMonitor {
	return &CleanupMonitor{logger: logrus.New()}
}

// TestGetCleanupCandidates_ConcurrentDtshMutationNoRace is the aliasing regression: getCleanupCandidates
// snapshots the artifact index under the monitor lock and then reads each entry's CreatedAt/AccessCount/
// LastAccessed AFTER releasing it. Before the fix the snapshot used maps.Copy, which duplicated only the
// map buckets and left every *ClipInfo shared with the live index — so those post-unlock reads raced the
// DTSH/access mutations other handlers apply under the lock. The deep-copy snapshot makes the read view
// immutable. Run under -race: with aliasing this fails; with the value-struct copy it is clean.
func TestGetCleanupCandidates_ConcurrentDtshMutationNoRace(t *testing.T) {
	oldMonitor := prometheusMonitor
	t.Cleanup(func() { prometheusMonitor = oldMonitor })

	const hash = "202401011200abcdef1234" // >=18 chars so extractArtifactHashFromPath accepts it
	prometheusMonitor = &PrometheusMonitor{artifactIndex: map[string]*ClipInfo{
		hash: {CreatedAt: time.Now().Add(-2 * time.Hour), AccessCount: 1, LastAccessed: time.Now()},
	}}

	clipsDir := t.TempDir()
	clipPath := filepath.Join(clipsDir, hash+".mp4")
	if err := os.WriteFile(clipPath, []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-2 * time.Hour) // older than minRetentionHours so it becomes a candidate
	if err := os.Chtimes(clipPath, old, old); err != nil {
		t.Fatal(err)
	}

	cm := &CleanupMonitor{logger: logrus.New(), minRetentionHours: 1}

	var wg sync.WaitGroup
	wg.Add(2)
	// Reader: repeatedly snapshot + read the *ClipInfo fields after unlocking.
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			if _, err := cm.getCleanupCandidates(clipsDir, "clip"); err != nil {
				t.Errorf("getCleanupCandidates: %v", err)
				return
			}
		}
	}()
	// Mutator: mutate the live entry's fields under the monitor lock, exactly as markLocalDtshPresent /
	// access recording do.
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			prometheusMonitor.mutex.Lock()
			if ci := prometheusMonitor.artifactIndex[hash]; ci != nil {
				ci.HasDtsh = true
				ci.AccessCount++
				ci.LastAccessed = time.Now()
				ci.CreatedAt = ci.CreatedAt.Add(time.Millisecond)
			}
			prometheusMonitor.mutex.Unlock()
		}
	}()
	wg.Wait()
}

func TestCalculateCleanupPriority_OldUnaccessed(t *testing.T) {
	cm := newTestCleanupMonitor()
	now := time.Now()

	clip := ClipCleanupInfo{
		CreatedAt:    now.Add(-48 * time.Hour),
		LastAccessed: now.Add(-200 * time.Hour),
		SizeBytes:    10 * 1024 * 1024,
		AccessCount:  0,
	}

	priority := cm.calculateCleanupPriority(clip)
	if priority <= 0 {
		t.Fatalf("expected positive priority, got %f", priority)
	}

	// ageFactor = 48/24 = 2.0, sizeFactor = 10/100 = 0.1
	// accessFactor = 1, recentAccessFactor = 1.0 (last access > 7d ago)
	// expected ≈ (2.0 + 0.1*0.1) / (1 * 1.0) = 2.01
	if math.Abs(priority-2.01) > 0.05 {
		t.Fatalf("expected priority ≈ 2.01 for old unaccessed clip, got %f", priority)
	}
}

func TestCalculateCleanupPriority_RecentlyAccessed(t *testing.T) {
	cm := newTestCleanupMonitor()
	now := time.Now()

	base := ClipCleanupInfo{
		CreatedAt:    now.Add(-48 * time.Hour),
		LastAccessed: now.Add(-200 * time.Hour),
		SizeBytes:    10 * 1024 * 1024,
		AccessCount:  0,
	}
	basePriority := cm.calculateCleanupPriority(base)

	recent := ClipCleanupInfo{
		CreatedAt:    now.Add(-48 * time.Hour),
		LastAccessed: now.Add(-1 * time.Hour),
		SizeBytes:    10 * 1024 * 1024,
		AccessCount:  0,
	}
	recentPriority := cm.calculateCleanupPriority(recent)

	ratio := recentPriority / basePriority
	if math.Abs(ratio-0.1) > 0.01 {
		t.Fatalf("expected recently accessed priority to be 10x higher (ratio ≈ 0.1), got ratio %f (base=%f, recent=%f)",
			ratio, basePriority, recentPriority)
	}
}

func TestCalculateCleanupPriority_HighAccessCount(t *testing.T) {
	cm := newTestCleanupMonitor()
	now := time.Now()

	low := ClipCleanupInfo{
		CreatedAt:    now.Add(-48 * time.Hour),
		LastAccessed: now.Add(-48 * time.Hour),
		SizeBytes:    10 * 1024 * 1024,
		AccessCount:  0,
	}
	lowPriority := cm.calculateCleanupPriority(low)

	high := ClipCleanupInfo{
		CreatedAt:    now.Add(-48 * time.Hour),
		LastAccessed: now.Add(-48 * time.Hour),
		SizeBytes:    10 * 1024 * 1024,
		AccessCount:  99,
	}
	highPriority := cm.calculateCleanupPriority(high)

	if highPriority >= lowPriority {
		t.Fatalf("expected high access count to yield higher priority (kept longer), got high=%f >= low=%f",
			highPriority, lowPriority)
	}
}

func TestCalculateCleanupPriority_LargeFile(t *testing.T) {
	cm := newTestCleanupMonitor()
	now := time.Now()

	small := ClipCleanupInfo{
		CreatedAt:    now.Add(-48 * time.Hour),
		LastAccessed: now.Add(-200 * time.Hour),
		SizeBytes:    10 * 1024 * 1024,
		AccessCount:  0,
	}
	smallPriority := cm.calculateCleanupPriority(small)

	large := ClipCleanupInfo{
		CreatedAt:    now.Add(-48 * time.Hour),
		LastAccessed: now.Add(-200 * time.Hour),
		SizeBytes:    500 * 1024 * 1024,
		AccessCount:  0,
	}
	largePriority := cm.calculateCleanupPriority(large)

	if largePriority <= smallPriority {
		t.Fatalf("expected large file to have slightly higher priority number (size increases numerator), got large=%f <= small=%f",
			largePriority, smallPriority)
	}

	diff := largePriority - smallPriority
	if diff > 1.0 {
		t.Fatalf("expected size difference to be minor (< 1.0), got %f", diff)
	}
}

func TestIsArtifactFile_ClipVod(t *testing.T) {
	cm := newTestCleanupMonitor()

	tests := []struct {
		path      string
		assetType string
		want      bool
	}{
		{"/data/clips/abc123.mp4", "clip", true},
		{"/data/clips/abc123.mkv", "clip", true},
		{"/data/clips/abc123.m3u8", "clip", false},
		{"/data/vod/abc123.mp4", "vod", true},
		{"/data/vod/abc123.mkv", "vod", true},
		{"/data/vod/abc123.m3u8", "vod", false},
	}

	for _, tt := range tests {
		t.Run(tt.assetType+"_"+tt.path, func(t *testing.T) {
			got := cm.isArtifactFile(tt.path, tt.assetType)
			if got != tt.want {
				t.Fatalf("isArtifactFile(%q, %q) = %v, want %v", tt.path, tt.assetType, got, tt.want)
			}
		})
	}
}

func TestIsArtifactFile_DVR(t *testing.T) {
	cm := newTestCleanupMonitor()

	tests := []struct {
		path string
		want bool
	}{
		{"/data/dvr/stream123/index.m3u8", true},
		{"/data/dvr/stream123/segment.ts", false},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			got := cm.isArtifactFile(tt.path, "dvr")
			if got != tt.want {
				t.Fatalf("isArtifactFile(%q, \"dvr\") = %v, want %v", tt.path, got, tt.want)
			}
		})
	}
}

func TestExtractArtifactHashFromPath(t *testing.T) {
	cm := newTestCleanupMonitor()

	tests := []struct {
		name string
		path string
		want string
	}{
		{
			name: "valid 22 char hash",
			path: "/data/clips/20240101120000abcdef12.mp4",
			want: "20240101120000abcdef12",
		},
		{
			name: "valid long hash",
			path: "/data/clips/20240101120000abcdef1234567890.mp4",
			want: "20240101120000abcdef1234567890",
		},
		{
			name: "exactly 18 chars",
			path: "/data/clips/123456789012345678.mkv",
			want: "123456789012345678",
		},
		{
			name: "short filename returns empty",
			path: "/data/clips/short.mp4",
			want: "",
		},
		{
			name: "17 char name returns empty",
			path: "/data/clips/12345678901234567.mp4",
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := cm.extractArtifactHashFromPath(tt.path, "clip")
			if got != tt.want {
				t.Fatalf("extractArtifactHashFromPath(%q) = %q, want %q", tt.path, got, tt.want)
			}
		})
	}
}

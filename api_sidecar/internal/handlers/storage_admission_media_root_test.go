package handlers

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"frameworks/api_sidecar/internal/admission"
	"frameworks/api_sidecar/internal/storage"
)

const mediaRootTestCapacity = 1 << 20

// roomyFilesystem reports a 1 TiB filesystem with 500 GiB free, far above
// storage.SystemReserveBytes, so a logical capacity alone bounds the media
// space whatever the free space of the host running the tests.
func roomyFilesystem(string) (*storage.DiskSpace, error) {
	return &storage.DiskSpace{TotalBytes: 1 << 40, AvailableBytes: 500 << 30}, nil
}

// newCapacityStorageManager returns a storage manager whose media root is
// bounded by a 1 MiB logical capacity on a roomy filesystem, with the given
// subdirectories created.
func newCapacityStorageManager(t *testing.T, subdirs ...string) *StorageManager {
	t.Helper()
	sm := newTestStorageManager(t)
	sm.capacity = mediaRootTestCapacity
	sm.statDiskSpace = roomyFilesystem
	for _, sub := range subdirs {
		if err := os.MkdirAll(filepath.Join(sm.basePath, sub), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return sm
}

func writeMediaFile(t *testing.T, path string, size int, modTime time.Time) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, make([]byte, size), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, modTime, modTime); err != nil {
		t.Fatal(err)
	}
}

// TestDecide_AdmissionReclaimsClipsForWriteOutsideClipsDir pins that a write
// admitted into processing/ reclaims durable clip copies from clips/. The
// reclaim engine's clip stage reads the media root's clips directory, not the
// directory the write lands in.
func TestDecide_AdmissionReclaimsClipsForWriteOutsideClipsDir(t *testing.T) {
	sm := newCapacityStorageManager(t, "clips", "processing")
	sm.presignedClient = &fakePresignedClient{}
	sm.requestCanDelete = func(_ context.Context, _ string) (bool, string, int64, error) {
		return true, "remote_synced", 0, nil
	}
	clipPath := filepath.Join(sm.basePath, "clips", "durablecliphashaaaaa.mp4")
	writeMediaFile(t, clipPath, mediaRootTestCapacity*95/100, time.Now().Add(-2*time.Hour))

	procDir := filepath.Join(sm.basePath, "processing")
	decision, err := sm.Decide(context.Background(), procDir, admission.IntentProcessingSourceStage, mediaRootTestCapacity/5)
	if err != nil || decision != admission.CacheToDisk {
		t.Fatalf("Decide = (%v, %v), want CacheToDisk after reclaiming the durable clip", decision, err)
	}
	if _, statErr := os.Stat(clipPath); !os.IsNotExist(statErr) {
		t.Fatalf("durable clip copy must be reclaimed, stat err: %v", statErr)
	}
}

// TestDecide_PlaybackCacheCriticalUsageCountsCapacity pins that the playback
// cache falls back to memory once the capacity-bounded media root is past the
// critical relay threshold, however empty the filesystem underneath is.
func TestDecide_PlaybackCacheCriticalUsageCountsCapacity(t *testing.T) {
	sm := newCapacityStorageManager(t, "vod")
	writeMediaFile(t, filepath.Join(sm.basePath, "vod", "202401011200abcdef1234.mp4"), mediaRootTestCapacity*96/100, time.Now())

	blocksDir := filepath.Join(sm.basePath, "vod", "202401011200abcdef9999.mp4.blocks")
	decision, err := sm.Decide(context.Background(), blocksDir, admission.IntentPlaybackCache, 1024)
	if err != nil || decision != admission.CacheMemoryOnly {
		t.Fatalf("Decide = (%v, %v), want CacheMemoryOnly with the media root 96%% of capacity", decision, err)
	}
}

// TestDecide_WarmCacheSoftThresholdCountsCapacity pins that a warm-cache fill
// is refused once the capacity-bounded media root is past the soft cleanup
// threshold.
func TestDecide_WarmCacheSoftThresholdCountsCapacity(t *testing.T) {
	sm := newCapacityStorageManager(t, "vod")
	sm.softCleanupThreshold = 0.85
	writeMediaFile(t, filepath.Join(sm.basePath, "vod", "202401011200abcdef1234.mp4"), mediaRootTestCapacity*90/100, time.Now())

	decision, err := sm.Decide(context.Background(), filepath.Join(sm.basePath, "vod"), admission.IntentWarmCache, 1024)
	if err != nil || decision != admission.CacheReject {
		t.Fatalf("Decide = (%v, %v), want CacheReject with the media root 90%% of capacity", decision, err)
	}
}

// TestKickoffBackgroundCleanup_SoftTargetCountsCapacity pins that proactive
// cleanup sizes its target from the capacity-bounded media root: just past the
// soft threshold it frees only the overshoot, so of two block caches only the
// least recently used one goes.
func TestKickoffBackgroundCleanup_SoftTargetCountsCapacity(t *testing.T) {
	resetBackgroundCleanupSentinel(t)
	sm := newCapacityStorageManager(t, "vod")
	sm.softCleanupThreshold = 0.85

	vodDir := filepath.Join(sm.basePath, "vod")
	writeMediaFile(t, filepath.Join(vodDir, "202401011200abcdef1234.mp4"), 800<<10, time.Now())
	olderBlocks := filepath.Join(vodDir, "202401011200abcdef0001.mp4.blocks")
	newerBlocks := filepath.Join(vodDir, "202401011200abcdef0002.mp4.blocks")
	writeMediaFile(t, filepath.Join(olderBlocks, "block0.bin"), 40<<10, time.Now().Add(-2*time.Hour))
	writeMediaFile(t, filepath.Join(newerBlocks, "block0.bin"), 40<<10, time.Now().Add(-time.Hour))
	for dir, mod := range map[string]time.Time{olderBlocks: time.Now().Add(-2 * time.Hour), newerBlocks: time.Now().Add(-time.Hour)} {
		if err := os.Chtimes(dir, mod, mod); err != nil {
			t.Fatal(err)
		}
	}

	sm.kickoffBackgroundCleanup(200 << 10)
	deadline := time.Now().Add(5 * time.Second)
	for backgroundCleanupRunning.Load() {
		if time.Now().After(deadline) {
			t.Fatal("background cleanup did not finish")
		}
		time.Sleep(5 * time.Millisecond)
	}

	if _, err := os.Stat(olderBlocks); !os.IsNotExist(err) {
		t.Fatalf("least recently used block cache must be evicted, stat err: %v", err)
	}
	if _, err := os.Stat(newerBlocks); err != nil {
		t.Fatalf("block cache beyond the soft-threshold overshoot must stay, stat err: %v", err)
	}
}

package handlers

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"frameworks/api_sidecar/internal/admission"
	"frameworks/api_sidecar/internal/storage"
)

// Regression: cold playback-cache admission must read filesystem
// stats even when the asset's <hash>.blocks dir doesn't exist yet.
// The pre-fix code stat'd the leaf, hit ENOENT, and pinned every
// cold asset to CacheMemoryOnly forever — the .blocks dir is created
// only AFTER a CacheToDisk decision, so the disk cache could never
// warm on first request.
//
// The fix routes IntentPlaybackCache through GetDiskSpaceWalk which
// walks to the nearest existing ancestor (the storage root) and
// returns real filesystem stats. We pin that contract here against
// the walk helper directly — the end-to-end Decide() outcome also
// depends on whether the dev volume is under pressure, which makes
// the higher-level path environment-sensitive.
// Pins every declared StorageIntent against the Decide switch so a
// future enum addition can't silently fall through to "unknown
// storage intent" — that was the bug that swallowed chapter
// finalization (admission rejected before remux).
func TestStorageManager_Decide_HandlesEveryDeclaredIntent(t *testing.T) {
	intents := []admission.StorageIntent{
		admission.IntentDVRRecording,
		admission.IntentProcessingOutput,
		admission.IntentProcessingSourceStage,
		admission.IntentDVRChapterFinalization,
		admission.IntentUnsafeImportStage,
		admission.IntentPlaybackCache,
		admission.IntentProcessingInput,
		admission.IntentWarmCache,
	}
	dir := t.TempDir()
	sm := &StorageManager{
		basePath:             dir,
		freezeThreshold:      0.85,
		targetThreshold:      0.70,
		deleteThreshold:      0.95,
		softCleanupThreshold: 0.85,
	}
	for _, intent := range intents {
		t.Run(string(intent), func(t *testing.T) {
			_, err := sm.Decide(context.Background(), dir, intent, 0)
			if err != nil && strings.Contains(err.Error(), "unknown storage intent") {
				t.Fatalf("intent %s fell through to default branch: %v", intent, err)
			}
		})
	}
}

func TestGetDiskSpaceWalk_ColdLeafReturnsAncestorStats(t *testing.T) {
	root := t.TempDir()
	coldBlocksDir := filepath.Join(root, "vod", "abc123.mkv.blocks")

	// Sanity: leaf must not exist.
	if _, err := os.Stat(coldBlocksDir); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("setup error: expected leaf missing, stat err=%v", err)
	}

	space, err := storage.GetDiskSpaceWalk(coldBlocksDir)
	if err != nil {
		t.Fatalf("walk must succeed against a missing leaf, got %v", err)
	}
	if space == nil || space.TotalBytes == 0 {
		t.Fatalf("walk must return real fs stats, got %+v", space)
	}
}

func resetBackgroundCleanupSentinel(t *testing.T) {
	t.Helper()
	backgroundCleanupRunning.Store(false)
}

func TestBackgroundCleanupSentinel_SingleRunner(t *testing.T) {
	resetBackgroundCleanupSentinel(t)

	if !backgroundCleanupRunning.CompareAndSwap(false, true) {
		t.Fatal("first acquisition must succeed when sentinel is idle")
	}
	if backgroundCleanupRunning.CompareAndSwap(false, true) {
		t.Fatal("second acquisition must fail while one is running")
	}
	backgroundCleanupRunning.Store(false)
	if !backgroundCleanupRunning.CompareAndSwap(false, true) {
		t.Fatal("third acquisition must succeed after release")
	}
}

func TestAdmissionThresholds_ProjectedUsageDecision(t *testing.T) {
	// Pure-math sanity check of the soft-threshold projection used by
	// admitDiskWrite: when (used + size) / total > softCleanupThreshold the
	// proactive cleanup tier should fire. This isolates the policy decision
	// from the syscalls that would normally feed it.

	type fixture struct {
		name              string
		total, used, size uint64
		soft              float64
		expectKickoff     bool
	}
	for _, f := range []fixture{
		{"low usage, no kickoff", 1000, 200, 100, 0.85, false},
		{"projected crosses soft", 1000, 700, 200, 0.85, true},
		{"projected exactly at soft", 1000, 700, 150, 0.85, false},
		{"already over soft, kicks off", 1000, 900, 10, 0.85, true},
	} {
		t.Run(f.name, func(t *testing.T) {
			projected := f.used + f.size
			ratio := float64(projected) / float64(f.total)
			got := ratio > f.soft
			if got != f.expectKickoff {
				t.Fatalf("projected=%d ratio=%.3f soft=%.2f: expected kickoff=%v got=%v",
					projected, ratio, f.soft, f.expectKickoff, got)
			}
		})
	}
}

func TestBackgroundCleanupSentinel_GoroutineRelease(t *testing.T) {
	resetBackgroundCleanupSentinel(t)

	if !backgroundCleanupRunning.CompareAndSwap(false, true) {
		t.Fatal("setup: acquire failed")
	}
	released := make(chan struct{})
	go func() {
		defer close(released)
		// Mimic kickoffBackgroundCleanup's defer release.
		defer backgroundCleanupRunning.Store(false)
		time.Sleep(2 * time.Millisecond)
	}()
	select {
	case <-released:
	case <-time.After(time.Second):
		t.Fatal("background work did not release sentinel in time")
	}
	if !backgroundCleanupRunning.CompareAndSwap(false, true) {
		t.Fatal("sentinel must be free after goroutine exits")
	}
	backgroundCleanupRunning.Store(false)
}

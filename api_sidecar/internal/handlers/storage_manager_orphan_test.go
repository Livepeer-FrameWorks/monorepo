package handlers

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
)

func writeAged(t *testing.T, path string, size int, age time.Duration) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, make([]byte, size), 0o644); err != nil {
		t.Fatal(err)
	}
	ts := time.Now().Add(-age)
	if err := os.Chtimes(path, ts, ts); err != nil {
		t.Fatal(err)
	}
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// A VOD's .dtsh/.gop/.partial sidecars are not artifacts. Enumerating them as
// "<hash>.<ext>" candidates makes Foghorn answer not_found for every pass and
// the pressure loop reports them as uncataloged forever.
func TestGetFreezeCandidates_VODSidecarsAreNotCandidates(t *testing.T) {
	sm := newTestStorageManager(t)
	sm.minRetentionHours = 1
	vodDir := filepath.Join(sm.basePath, "vod")
	const hash = "20260927120000abcdef0123"
	writeAged(t, filepath.Join(vodDir, hash+".mkv"), 300, 2*time.Hour)
	writeAged(t, filepath.Join(vodDir, hash+".mkv.dtsh"), 20, 2*time.Hour)
	writeAged(t, filepath.Join(vodDir, hash+".mkv.gop"), 20, 2*time.Hour)
	writeAged(t, filepath.Join(vodDir, "20260927130000abcdef4567.mkv.dtsh"), 20, 2*time.Hour)

	candidates, err := sm.getFreezeCandidates(vodDir, AssetTypeVOD)
	if err != nil {
		t.Fatal(err)
	}
	var hashes []string
	for _, c := range candidates {
		hashes = append(hashes, c.AssetHash)
	}
	if len(hashes) != 1 || hashes[0] != hash {
		t.Fatalf("candidate hashes = %v, want only the media artifact [%s]", hashes, hash)
	}
}

// A sidecar whose media, .partial and .blocks are all gone indexes bytes the
// node no longer holds; pressure reclaim removes it. Sidecars with media or a
// block cache present, and freshly fetched ones, stay.
func TestReclaimToTarget_RemovesOrphanSidecars(t *testing.T) {
	sm := newTestStorageManager(t)
	sm.minRetentionHours = 1
	sm.requestCanDelete = func(_ context.Context, _ string) (bool, string, int64, error) {
		return false, "not_found", 0, nil
	}
	vodDir := filepath.Join(sm.basePath, "vod")
	clipsDir := filepath.Join(sm.basePath, "clips")

	orphanVOD := filepath.Join(vodDir, "20260927120000abcdef0123.mkv.dtsh")
	orphanGop := filepath.Join(vodDir, "20260927120000abcdef0123.mkv.gop")
	orphanClip := filepath.Join(clipsDir, "live+stream", "20260927120000abcdef4567.mp4.dtsh")
	writeAged(t, orphanVOD, 40, 2*time.Hour)
	writeAged(t, orphanGop, 10, 2*time.Hour)
	writeAged(t, orphanClip, 30, 2*time.Hour)

	warmMedia := filepath.Join(vodDir, "20260927140000abcdef8901.mkv")
	warmSidecar := warmMedia + ".dtsh"
	writeAged(t, warmMedia, 10, 30*time.Minute)
	writeAged(t, warmSidecar, 10, 2*time.Hour)

	youngOrphan := filepath.Join(vodDir, "20260927150000abcdef2345.mkv.dtsh")
	writeAged(t, youngOrphan, 10, time.Minute)

	res := sm.reclaimToTarget(1 << 20)

	for _, p := range []string{orphanVOD, orphanGop, orphanClip} {
		if exists(p) {
			t.Fatalf("orphan sidecar %s must be removed under pressure", p)
		}
	}
	for _, p := range []string{warmMedia, warmSidecar, youngOrphan} {
		if !exists(p) {
			t.Fatalf("%s must be kept", p)
		}
	}
	if res.freedBytes != 80 {
		t.Fatalf("freedBytes = %d, want 80 (orphan sidecar bytes)", res.freedBytes)
	}
}

// The relay caches upload-source blocks under upload/<hash>.<ext>.blocks; they
// are as rebuildable as vod/clip block caches and must be reclaimed first.
func TestReclaimToTarget_EvictsUploadBlockCache(t *testing.T) {
	sm := newTestStorageManager(t)
	blocksDir := filepath.Join(sm.basePath, "upload", "20260927120000abcdef0123.mp4.blocks")
	writeAged(t, filepath.Join(blocksDir, "0000000000.blk"), 100, 24*time.Hour)

	res := sm.reclaimToTarget(50)

	if exists(blocksDir) {
		t.Fatalf("upload block cache %s must be evicted under pressure", blocksDir)
	}
	if res.freedBytes != 100 {
		t.Fatalf("freedBytes = %d, want 100", res.freedBytes)
	}
}

// A DVR cleanup candidate is the whole recording directory, so it must carry
// the directory's bytes. Counting only the manifest makes the freed total miss
// the target and evicts every durable DVR on the node.
func TestGetCleanupCandidates_DVRSizeIsRecordingDirectory(t *testing.T) {
	root := t.TempDir()
	recDir := filepath.Join(root, "dvr", "live+stream", "20260927120000abcdef0123")
	writeAged(t, filepath.Join(recDir, "20260927120000abcdef0123.m3u8"), 50, 2*time.Hour)
	writeAged(t, filepath.Join(recDir, "segments", "seg-0001.ts"), 1000, 2*time.Hour)
	writeAged(t, filepath.Join(recDir, "segments", "seg-0002.ts"), 2000, 2*time.Hour)

	cm := &CleanupMonitor{logger: logging.NewLogger(), minRetentionHours: 1}
	candidates, err := cm.getCleanupCandidates(filepath.Join(root, "dvr"), "dvr")
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 1 {
		t.Fatalf("candidates = %+v, want one recording", candidates)
	}
	if got := candidates[0].SizeBytes; got != 3050 {
		t.Fatalf("DVR candidate SizeBytes = %d, want 3050 (manifest + segments)", got)
	}
}

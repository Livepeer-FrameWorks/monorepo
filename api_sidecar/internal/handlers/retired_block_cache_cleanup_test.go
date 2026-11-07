package handlers

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"frameworks/api_sidecar/internal/relay"
)

func TestOrdinaryCleanupReclaimsRetiredBlockGeneration(t *testing.T) {
	sm := newCapacityStorageManager(t, "vod")
	asset := filepath.Join(sm.basePath, "vod", "202401011200abcdef1234.mkv")
	store := relay.NewBlockStore(asset, 8)
	if _, err := store.EnsureMeta("202401011200abcdef1234", ".mkv", "old", 8); err != nil {
		t.Fatal(err)
	}
	if err := store.WriteBlock(0, []byte("OLD-BODY")); err != nil {
		t.Fatal(err)
	}
	retired, retireErr := os.MkdirTemp(filepath.Dir(asset), filepath.Base(asset)+".retired-*.blocks")
	if retireErr != nil {
		t.Fatal(retireErr)
	}
	if err := os.Remove(retired); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(store.Dir(), retired); err != nil {
		t.Fatal(err)
	}
	if _, err := store.EnsureMeta("202401011200abcdef1234", ".mkv", "new", 8); err != nil {
		t.Fatal(err)
	}
	if err := store.WriteBlock(0, []byte("NEW-BODY")); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(store.Dir(), old, old); err != nil {
		t.Fatal(err)
	}
	monitor := &CleanupMonitor{logger: sm.logger, basePath: sm.basePath, minRetentionHours: 1}
	candidates, candidatesErr := monitor.getCleanupCandidates(filepath.Dir(asset), "vod")
	if candidatesErr != nil {
		t.Fatal(candidatesErr)
	}
	found := false
	for _, candidate := range candidates {
		if candidate.FilePath == retired {
			found = true
			if candidate.ClipHash != "" {
				t.Errorf("retired garbage was attributed to malformed artifact hash %q", candidate.ClipHash)
			}
			if err := monitor.cleanupClip(candidate); err != nil {
				t.Errorf("retired garbage required artifact control-plane authorization: %v", err)
			}
		}
	}
	if !found {
		t.Fatal("ordinary cleanup monitor ignored the retired cache generation")
	}
	// Recreate garbage after testing the monitor path, then pressure eviction
	// must prefer it to the older active generation.
	if err := os.MkdirAll(retired, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(retired, "old.blk"), []byte("OLD-BODY"), 0o644); err != nil {
		t.Fatal(err)
	}
	if freed := sm.evictBlockCaches(8); freed < 8 {
		t.Fatalf("pressure cleanup reclaimed only %d bytes", freed)
	}
	if _, statErr := os.Stat(retired); !os.IsNotExist(statErr) {
		t.Fatalf("pressure cleanup left the retired generation: %v", statErr)
	}
	if meta, loadErr := store.LoadMeta(); loadErr != nil || meta.Object != "new" {
		t.Fatalf("pressure cleanup changed active identity: meta=%+v err=%v", meta, loadErr)
	}
	if bytes, readErr := os.ReadFile(store.BlockPath(0)); readErr != nil || string(bytes) != "NEW-BODY" {
		t.Fatalf("pressure cleanup changed active bytes=%q err=%v", bytes, readErr)
	}
}

func TestRetiredBlockCleanupUsesNoArtifactIdentity(t *testing.T) {
	sm := newCapacityStorageManager(t, "vod")
	retired := filepath.Join(sm.basePath, "vod", "202401011200abcdef1234.mkv.retired-123.blocks")
	if err := os.MkdirAll(retired, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(retired, "old.blk"), []byte("OLD-BODY"), 0o644); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(retired, old, old); err != nil {
		t.Fatal(err)
	}
	monitor := &CleanupMonitor{logger: sm.logger, basePath: sm.basePath, minRetentionHours: 1}
	candidates, err := monitor.getCleanupCandidates(filepath.Dir(retired), "vod")
	if err != nil || len(candidates) != 1 {
		t.Fatalf("retired candidates=%+v err=%v", candidates, err)
	}
	if candidates[0].ClipHash != "" {
		t.Errorf("garbage acquired artifact identity %q", candidates[0].ClipHash)
	}
	// No connected control plane or hydrated asset lease state exists here.
	// Detached garbage must not request deletion authority or emit asset events.
	if err := monitor.cleanupClip(candidates[0]); err != nil {
		t.Fatalf("retired garbage entered artifact lifecycle: %v", err)
	}
	if _, err := os.Stat(retired); !os.IsNotExist(err) {
		t.Fatalf("garbage retained: %v", err)
	}
}

func TestPressureCleanupPrefersRetiredGeneration(t *testing.T) {
	sm := newCapacityStorageManager(t, "vod")
	active := filepath.Join(sm.basePath, "vod", "202401011200abcdef1234.mkv.blocks")
	retired := filepath.Join(sm.basePath, "vod", "202401011200abcdef1234.mkv.retired-123.blocks")
	for _, dir := range []string{active, retired} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "body.blk"), []byte("BODYBODY"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	old := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(active, old, old); err != nil {
		t.Fatal(err)
	}
	if freed := sm.evictBlockCaches(8); freed != 8 {
		t.Fatalf("freed=%d want 8", freed)
	}
	if _, err := os.Stat(retired); !os.IsNotExist(err) {
		t.Errorf("newer retired garbage survived pressure cleanup: %v", err)
	}
	if _, err := os.Stat(active); err != nil {
		t.Errorf("older active cache was evicted ahead of garbage: %v", err)
	}
}

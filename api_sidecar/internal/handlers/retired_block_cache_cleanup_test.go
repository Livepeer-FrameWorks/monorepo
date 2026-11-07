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
	if err := os.Chtimes(retired, old, old); err != nil {
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
		}
	}
	if !found {
		t.Fatal("ordinary cleanup monitor ignored the retired cache generation")
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

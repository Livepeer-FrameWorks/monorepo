package handlers

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"frameworks/api_sidecar/internal/leases"
)

// relayCacheFixture writes every form of local copy the relay and Mist leave
// for one artifact hash across the relay cache roots, and returns the paths.
func relayCacheFixture(t *testing.T, storagePath, hash string) []string {
	t.Helper()
	files := []string{
		filepath.Join(storagePath, "vod", hash+".mkv"),
		filepath.Join(storagePath, "vod", hash+".mkv.dtsh"),
		filepath.Join(storagePath, "vod", hash+".mkv.gop"),
		filepath.Join(storagePath, "vod", hash+".mkv.blocks", "0000000000.blk"),
		filepath.Join(storagePath, "upload", hash+".mp4.partial"),
		filepath.Join(storagePath, "upload", hash+".mp4.dtsh"),
		filepath.Join(storagePath, "upload", hash+".mp4.blocks", "0000000000.blk"),
		filepath.Join(storagePath, "upload", hash+".mp4.blocks", "meta.json"),
		filepath.Join(storagePath, "clips", "stream-a", hash+".mp4.blocks", "0000000000.blk"),
		filepath.Join(storagePath, "clips", "stream-a", hash+".mp4.dtsh"),
	}
	for _, f := range files {
		if err := os.MkdirAll(filepath.Dir(f), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(f, make([]byte, 100), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return files
}

func remainingFor(t *testing.T, storagePath, hash string) []string {
	t.Helper()
	var left []string
	for _, root := range relayCacheRoots {
		for _, pattern := range []string{
			filepath.Join(storagePath, root, hash+".*"),
			filepath.Join(storagePath, root, "*", hash+".*"),
		} {
			found, err := filepath.Glob(pattern)
			if err != nil {
				t.Fatal(err)
			}
			left = append(left, found...)
		}
	}
	return left
}

// A deleted artifact must leave no bytes on the node: the relay caches it
// under upload/ and clips/<stream>/ as well as vod/, and every one of those
// copies is the deleted customer content.
func TestDeleteVOD_RemovesRelayCachesUnderEveryRoot(t *testing.T) {
	h := setupDeleteHandlers(t)
	hash := "20261021120000aabbccdd"
	other := "20261021120000aabbccee"
	files := relayCacheFixture(t, h.storagePath, hash)
	otherFile := filepath.Join(h.storagePath, "upload", other+".mp4.dtsh")
	if err := os.WriteFile(otherFile, make([]byte, 7), 0o644); err != nil {
		t.Fatal(err)
	}

	size, err := h.DeleteVOD(hash)
	if err != nil {
		t.Fatalf("DeleteVOD: %v", err)
	}
	if want := uint64(100 * len(files)); size != want {
		t.Fatalf("freed %d bytes, want %d", size, want)
	}
	if left := remainingFor(t, h.storagePath, hash); len(left) != 0 {
		t.Fatalf("files of the deleted VOD remain: %v", left)
	}
	if _, err := os.Stat(otherFile); err != nil {
		t.Fatalf("another artifact's file was removed: %v", err)
	}
}

func TestDeleteClip_RemovesRelayCachesUnderEveryRoot(t *testing.T) {
	h := setupDeleteHandlers(t)
	hash := "20261021120000aabbccdd"
	relayCacheFixture(t, h.storagePath, hash)

	if _, err := h.DeleteClip(hash); err != nil {
		t.Fatalf("DeleteClip: %v", err)
	}
	if left := remainingFor(t, h.storagePath, hash); len(left) != 0 {
		t.Fatalf("files of the deleted clip remain: %v", left)
	}
}

// A relay cache an active read holds keeps its lease: the delete removes the
// unleased copies, keeps the leased one and reports the lease so the caller
// defers the rest.
func TestDeleteVODImmediate_LeasedRelayCacheStaysForDeferredDelete(t *testing.T) {
	h := setupDeleteHandlers(t)
	tracker := installTestTracker(t)
	hash := "20261021120000aabbccdd"
	relayCacheFixture(t, h.storagePath, hash)
	leasedDir := filepath.Join(h.storagePath, "upload", hash+".mp4.blocks")
	tracker.AcquireViewer("viewer-1", "vod+"+hash, leasedDir)

	_, err := h.deleteVODImmediate(hash)
	if !errors.Is(err, leases.ErrLeaseHeld) {
		t.Fatalf("deleteVODImmediate error = %v, want ErrLeaseHeld", err)
	}
	if _, statErr := os.Stat(filepath.Join(leasedDir, "0000000000.blk")); statErr != nil {
		t.Fatalf("leased relay cache was removed: %v", statErr)
	}
	left := remainingFor(t, h.storagePath, hash)
	if len(left) != 1 || left[0] != leasedDir {
		t.Fatalf("remaining files = %v, want only the leased %s", left, leasedDir)
	}

	tracker.ReleaseViewer("viewer-1")
	if _, err := h.deleteVODImmediate(hash); err != nil {
		t.Fatalf("retry after lease release: %v", err)
	}
	if left := remainingFor(t, h.storagePath, hash); len(left) != 0 {
		t.Fatalf("files remain after the lease released: %v", left)
	}
}

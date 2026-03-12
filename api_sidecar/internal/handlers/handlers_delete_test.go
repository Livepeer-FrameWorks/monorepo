package handlers

import (
	"os"
	"path/filepath"
	"testing"

	"frameworks/pkg/logging"
)

func setupDeleteHandlers(t *testing.T) *Handlers {
	t.Helper()
	oldLogger := logger
	logger = logging.NewLogger()
	t.Cleanup(func() { logger = oldLogger })

	// Ensure prometheusMonitor is nil so delete methods don't panic
	oldMonitor := prometheusMonitor
	prometheusMonitor = nil
	t.Cleanup(func() { prometheusMonitor = oldMonitor })

	return &Handlers{storagePath: t.TempDir()}
}

// --- DeleteClip ---

func TestDeleteClip_SingleFile(t *testing.T) {
	h := setupDeleteHandlers(t)
	clipsDir := filepath.Join(h.storagePath, "clips", "stream-a")
	if err := os.MkdirAll(clipsDir, 0o755); err != nil {
		t.Fatal(err)
	}

	hash := "aabbccddeeff001122"
	filePath := filepath.Join(clipsDir, hash+".mp4")
	if err := os.WriteFile(filePath, make([]byte, 4096), 0o644); err != nil {
		t.Fatal(err)
	}

	size, err := h.DeleteClip(hash)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if size != 4096 {
		t.Fatalf("expected 4096 bytes deleted, got %d", size)
	}
	if _, err := os.Stat(filePath); !os.IsNotExist(err) {
		t.Fatal("expected file to be deleted")
	}
}

func TestDeleteClip_WithDtsh(t *testing.T) {
	h := setupDeleteHandlers(t)
	clipsDir := filepath.Join(h.storagePath, "clips", "stream-a")
	if err := os.MkdirAll(clipsDir, 0o755); err != nil {
		t.Fatal(err)
	}

	hash := "aabbccddeeff001122"
	if err := os.WriteFile(filepath.Join(clipsDir, hash+".mp4"), make([]byte, 2048), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(clipsDir, hash+".mp4.dtsh"), make([]byte, 256), 0o644); err != nil {
		t.Fatal(err)
	}

	size, err := h.DeleteClip(hash)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if size != 2048+256 {
		t.Fatalf("expected %d bytes deleted, got %d", 2048+256, size)
	}
}

func TestDeleteClip_NotFound(t *testing.T) {
	h := setupDeleteHandlers(t)
	clipsDir := filepath.Join(h.storagePath, "clips")
	if err := os.MkdirAll(clipsDir, 0o755); err != nil {
		t.Fatal(err)
	}

	size, err := h.DeleteClip("nonexistent-hash")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if size != 0 {
		t.Fatalf("expected 0 bytes, got %d", size)
	}
}

func TestDeleteClip_EmptyHash(t *testing.T) {
	h := setupDeleteHandlers(t)
	_, err := h.DeleteClip("")
	if err == nil {
		t.Fatal("expected error for empty hash")
	}
}

// --- DeleteDVR ---

func TestDeleteDVR_FullDirectory(t *testing.T) {
	h := setupDeleteHandlers(t)
	dvrHash := "aabbccddeeff001122"
	recordingDir := filepath.Join(h.storagePath, "dvr", "stream-1", dvrHash)
	segDir := filepath.Join(recordingDir, "segments")
	if err := os.MkdirAll(segDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Manifest
	if err := os.WriteFile(filepath.Join(recordingDir, dvrHash+".m3u8"), make([]byte, 512), 0o644); err != nil {
		t.Fatal(err)
	}
	// Segments
	if err := os.WriteFile(filepath.Join(segDir, "chunk000.ts"), make([]byte, 5000), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(segDir, "chunk001.ts"), make([]byte, 3000), 0o644); err != nil {
		t.Fatal(err)
	}

	size, err := h.DeleteDVR(dvrHash)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	expectedSize := uint64(512 + 5000 + 3000)
	if size != expectedSize {
		t.Fatalf("expected %d bytes deleted, got %d", expectedSize, size)
	}
	if _, err := os.Stat(recordingDir); !os.IsNotExist(err) {
		t.Fatal("expected recording directory to be removed")
	}
}

func TestDeleteDVR_CleansEmptyParent(t *testing.T) {
	h := setupDeleteHandlers(t)
	dvrHash := "aabbccddeeff001122"
	streamDir := filepath.Join(h.storagePath, "dvr", "stream-1")
	recordingDir := filepath.Join(streamDir, dvrHash)
	if err := os.MkdirAll(recordingDir, 0o755); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(recordingDir, dvrHash+".m3u8"), make([]byte, 100), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := h.DeleteDVR(dvrHash)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Stream directory should be cleaned up because it's empty
	if _, err := os.Stat(streamDir); !os.IsNotExist(err) {
		t.Fatal("expected empty stream directory to be cleaned up")
	}
}

func TestDeleteDVR_NotFound(t *testing.T) {
	h := setupDeleteHandlers(t)
	dvrDir := filepath.Join(h.storagePath, "dvr")
	if err := os.MkdirAll(dvrDir, 0o755); err != nil {
		t.Fatal(err)
	}

	size, err := h.DeleteDVR("nonexistent")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if size != 0 {
		t.Fatalf("expected 0, got %d", size)
	}
}

func TestDeleteDVR_EmptyHash(t *testing.T) {
	h := setupDeleteHandlers(t)
	_, err := h.DeleteDVR("")
	if err == nil {
		t.Fatal("expected error for empty hash")
	}
}

// --- DeleteVOD ---

func TestDeleteVOD_SingleFile(t *testing.T) {
	h := setupDeleteHandlers(t)
	vodDir := filepath.Join(h.storagePath, "vod")
	if err := os.MkdirAll(vodDir, 0o755); err != nil {
		t.Fatal(err)
	}

	hash := "aabbccddeeff001122"
	if err := os.WriteFile(filepath.Join(vodDir, hash+".mp4"), make([]byte, 8192), 0o644); err != nil {
		t.Fatal(err)
	}

	size, err := h.DeleteVOD(hash)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if size != 8192 {
		t.Fatalf("expected 8192, got %d", size)
	}
}

func TestDeleteVOD_MultipleFormats(t *testing.T) {
	h := setupDeleteHandlers(t)
	vodDir := filepath.Join(h.storagePath, "vod")
	if err := os.MkdirAll(vodDir, 0o755); err != nil {
		t.Fatal(err)
	}

	hash := "aabbccddeeff001122"
	if err := os.WriteFile(filepath.Join(vodDir, hash+".mp4"), make([]byte, 4096), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(vodDir, hash+".mkv"), make([]byte, 2048), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(vodDir, hash+".mp4.dtsh"), make([]byte, 512), 0o644); err != nil {
		t.Fatal(err)
	}

	size, err := h.DeleteVOD(hash)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if size != 4096+2048+512 {
		t.Fatalf("expected %d, got %d", 4096+2048+512, size)
	}
}

func TestDeleteVOD_NotFound(t *testing.T) {
	h := setupDeleteHandlers(t)
	vodDir := filepath.Join(h.storagePath, "vod")
	if err := os.MkdirAll(vodDir, 0o755); err != nil {
		t.Fatal(err)
	}

	size, err := h.DeleteVOD("nonexistent")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if size != 0 {
		t.Fatalf("expected 0, got %d", size)
	}
}

func TestDeleteVOD_EmptyHash(t *testing.T) {
	h := setupDeleteHandlers(t)
	_, err := h.DeleteVOD("")
	if err == nil {
		t.Fatal("expected error for empty hash")
	}
}

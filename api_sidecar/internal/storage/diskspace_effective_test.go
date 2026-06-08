package storage

import (
	"os"
	"path/filepath"
	"testing"
)

func writeSizedFile(t *testing.T, path string, size int) {
	t.Helper()
	if err := os.WriteFile(path, make([]byte, size), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// DirectorySize sums regular-file bytes recursively and treats a missing path as
// empty (0, nil) rather than an error — admission checks run before the leaf dir
// exists.
func TestDirectorySize(t *testing.T) {
	dir := t.TempDir()
	writeSizedFile(t, filepath.Join(dir, "a.bin"), 100)
	sub := filepath.Join(dir, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	writeSizedFile(t, filepath.Join(sub, "b.bin"), 200)

	got, err := DirectorySize(dir)
	if err != nil {
		t.Fatalf("DirectorySize: %v", err)
	}
	if got != 300 {
		t.Fatalf("DirectorySize = %d, want 300", got)
	}

	empty := t.TempDir()
	if got, err := DirectorySize(empty); err != nil || got != 0 {
		t.Fatalf("empty dir: got %d, err %v; want 0, nil", got, err)
	}

	missing := filepath.Join(dir, "does-not-exist")
	if got, err := DirectorySize(missing); err != nil || got != 0 {
		t.Fatalf("missing path: got %d, err %v; want 0, nil", got, err)
	}
}

// EffectiveDiskSpace overlays an optional logical capacity cap on the real
// filesystem stats. TotalBytes is stable across calls (Available can fluctuate),
// so assertions key off Total plus the capacity arithmetic.
func TestEffectiveDiskSpace(t *testing.T) {
	dir := t.TempDir()
	writeSizedFile(t, filepath.Join(dir, "used.bin"), 1000)

	raw, err := GetDiskSpaceWalk(dir)
	if err != nil {
		t.Fatalf("GetDiskSpaceWalk: %v", err)
	}

	t.Run("capacity zero returns raw filesystem space", func(t *testing.T) {
		eff, err := EffectiveDiskSpace(dir, 0)
		if err != nil {
			t.Fatalf("EffectiveDiskSpace: %v", err)
		}
		if eff.TotalBytes != raw.TotalBytes {
			t.Fatalf("TotalBytes = %d, want raw %d (no cap)", eff.TotalBytes, raw.TotalBytes)
		}
	})

	t.Run("capacity below usage yields zero logical availability", func(t *testing.T) {
		// used (1000) >= capacity (500) -> logicalAvailable clamps to 0.
		eff, err := EffectiveDiskSpace(dir, 500)
		if err != nil {
			t.Fatalf("EffectiveDiskSpace: %v", err)
		}
		if eff.AvailableBytes != 0 {
			t.Fatalf("AvailableBytes = %d, want 0 when used exceeds capacity", eff.AvailableBytes)
		}
		if eff.TotalBytes != 500 {
			t.Fatalf("TotalBytes = %d, want capacity 500", eff.TotalBytes)
		}
	})

	t.Run("huge capacity does not raise reported total above the filesystem", func(t *testing.T) {
		eff, err := EffectiveDiskSpace(dir, 1<<60)
		if err != nil {
			t.Fatalf("EffectiveDiskSpace: %v", err)
		}
		if eff.TotalBytes != raw.TotalBytes {
			t.Fatalf("TotalBytes = %d, want filesystem total %d", eff.TotalBytes, raw.TotalBytes)
		}
		if eff.AvailableBytes > raw.AvailableBytes {
			t.Fatalf("AvailableBytes = %d exceeds filesystem available %d", eff.AvailableBytes, raw.AvailableBytes)
		}
	})
}

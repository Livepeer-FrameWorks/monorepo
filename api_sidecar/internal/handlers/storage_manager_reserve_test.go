package handlers

import (
	"testing"

	"frameworks/api_sidecar/internal/storage"
)

// The storage manager's freeze/evict decisions read usage as a fraction of
// the filesystem minus the fixed system reserve.
func TestStorageManagerUsageExcludesSystemReserve(t *testing.T) {
	dir := t.TempDir()
	raw, err := storage.GetDiskSpaceWalk(dir)
	if err != nil {
		t.Fatalf("GetDiskSpaceWalk: %v", err)
	}
	if raw.TotalBytes <= storage.SystemReserveBytes {
		t.Skipf("test filesystem (%d bytes) is not larger than the reserve", raw.TotalBytes)
	}
	sm := &StorageManager{basePath: dir}
	usage, used, total, err := sm.getStorageUsage(dir)
	if err != nil {
		t.Fatalf("getStorageUsage: %v", err)
	}
	if want := raw.TotalBytes - storage.SystemReserveBytes; total != want {
		t.Fatalf("total = %d, want filesystem %d minus reserve = %d", total, raw.TotalBytes, want)
	}
	if used > total {
		t.Fatalf("used %d exceeds total %d", used, total)
	}
	if want := float64(used) / float64(total); usage != want {
		t.Fatalf("usage = %v, want used/total = %v", usage, want)
	}
}

// A 40 GiB root with 25 GiB free is half-used against the 30 GiB media
// share. With 5 GiB free the whole remainder sits inside the reserve, so the
// media share is full and past the delete threshold although the raw
// filesystem is only 87.5% used.
func TestStorageThresholdsApplyToCapacityMinusReserve(t *testing.T) {
	const gib = uint64(1) << 30
	sm := &StorageManager{freezeThreshold: 0.85, deleteThreshold: 0.95}

	roomy := storage.MediaDiskSpace(storage.DiskSpace{TotalBytes: 40 * gib, AvailableBytes: 25 * gib}, storage.SystemReserveBytes, 0, 0)
	if got := roomy.UsageFraction(); got != 0.5 {
		t.Fatalf("usage = %v, want 0.5 of the 30 GiB media share", got)
	}
	if roomy.UsageFraction() >= sm.freezeThreshold {
		t.Fatal("half-used media share must not freeze")
	}

	tight := storage.MediaDiskSpace(storage.DiskSpace{TotalBytes: 40 * gib, AvailableBytes: 5 * gib}, storage.SystemReserveBytes, 0, 0)
	rawUsage := float64(35*gib) / float64(40*gib)
	if rawUsage >= sm.deleteThreshold {
		t.Fatalf("setup: raw usage %v should be below the delete threshold", rawUsage)
	}
	if tight.UsageFraction() < sm.deleteThreshold {
		t.Fatalf("usage = %v; free space inside the reserve must count as full", tight.UsageFraction())
	}
}

package storage

import "testing"

func TestRequiredAvailableBytesKeepsAbsoluteReserve(t *testing.T) {
	if got := RequiredAvailableBytes(0); got != MinFreeBytes {
		t.Fatalf("RequiredAvailableBytes(0) = %d, want %d", got, MinFreeBytes)
	}
	required := uint64(512 << 20)
	if got := RequiredAvailableBytes(required); got != required+MinFreeBytes {
		t.Fatalf("RequiredAvailableBytes(%d) = %d, want %d", required, got, required+MinFreeBytes)
	}
}

func TestRequiredAvailableBytesSaturates(t *testing.T) {
	if got := RequiredAvailableBytes(^uint64(0)); got != ^uint64(0) {
		t.Fatalf("RequiredAvailableBytes(max) = %d, want max", got)
	}
}

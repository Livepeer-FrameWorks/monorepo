package storage

import "testing"

const gib = uint64(1) << 30

func TestMediaDiskSpaceSubtractsSystemReserve(t *testing.T) {
	cases := []struct {
		name          string
		raw           DiskSpace
		capacity      uint64
		dirUsed       uint64
		wantTotal     uint64
		wantAvailable uint64
	}{
		{
			name:          "reserve comes off total and available",
			raw:           DiskSpace{TotalBytes: 100 * gib, AvailableBytes: 40 * gib},
			wantTotal:     90 * gib,
			wantAvailable: 30 * gib,
		},
		{
			name:          "less free than the reserve leaves nothing for media",
			raw:           DiskSpace{TotalBytes: 100 * gib, AvailableBytes: 6 * gib},
			wantTotal:     90 * gib,
			wantAvailable: 0,
		},
		{
			name:          "filesystem no larger than the reserve has no media space",
			raw:           DiskSpace{TotalBytes: 8 * gib, AvailableBytes: 8 * gib},
			wantTotal:     0,
			wantAvailable: 0,
		},
		{
			name:          "capacity cap below the reserved filesystem wins",
			raw:           DiskSpace{TotalBytes: 100 * gib, AvailableBytes: 80 * gib},
			capacity:      50 * gib,
			dirUsed:       20 * gib,
			wantTotal:     50 * gib,
			wantAvailable: 30 * gib,
		},
		{
			name:          "reserve still binds under a capacity cap",
			raw:           DiskSpace{TotalBytes: 100 * gib, AvailableBytes: 15 * gib},
			capacity:      50 * gib,
			dirUsed:       20 * gib,
			wantTotal:     50 * gib,
			wantAvailable: 5 * gib,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := MediaDiskSpace(tc.raw, SystemReserveBytes, tc.capacity, tc.dirUsed)
			if got.TotalBytes != tc.wantTotal || got.AvailableBytes != tc.wantAvailable {
				t.Fatalf("MediaDiskSpace = total %d available %d; want %d/%d", got.TotalBytes, got.AvailableBytes, tc.wantTotal, tc.wantAvailable)
			}
		})
	}
}

func TestDiskSpaceUsageFraction(t *testing.T) {
	if got := (DiskSpace{}).UsageFraction(); got != 1 {
		t.Fatalf("zero total usage = %v, want 1 (full)", got)
	}
	if got := (DiskSpace{TotalBytes: 90 * gib, AvailableBytes: 9 * gib}).UsageFraction(); got != 0.9 {
		t.Fatalf("usage = %v, want 0.9", got)
	}
}

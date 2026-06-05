package handlers

import (
	"testing"

	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

func TestSourceRuntimeSeconds(t *testing.T) {
	// Intent: convert a start/end millisecond pair into a non-negative
	// duration in seconds. Out-of-order or equal timestamps must yield 0, not
	// a negative runtime that would corrupt a billed/audited duration.
	cases := []struct {
		name           string
		started, ended int64
		want           float64
	}{
		{"one second", 1_000, 2_000, 1.0},
		{"sub-second", 0, 1_500, 1.5},
		{"equal is zero", 5_000, 5_000, 0},
		{"reversed is zero", 2_000, 1_000, 0},
		{"large span", 0, 3_600_000, 3600},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := sourceRuntimeSeconds(tc.started, tc.ended); got != tc.want {
				t.Errorf("sourceRuntimeSeconds(%d, %d) = %v, want %v", tc.started, tc.ended, got, tc.want)
			}
		})
	}
}

func TestAbsDelta(t *testing.T) {
	// These guard divergence math against unsigned underflow: the delta must
	// be order-independent and never wrap around. absDeltaUint32(0, 5) wrapping
	// would produce ~4 billion and falsely trip a divergence alarm.
	if got := absDeltaUint32(0, 5); got != 5 {
		t.Errorf("absDeltaUint32(0,5) = %d, want 5", got)
	}
	if got := absDeltaUint32(5, 0); got != 5 {
		t.Errorf("absDeltaUint32(5,0) = %d, want 5", got)
	}
	if got := absDeltaUint32(7, 7); got != 0 {
		t.Errorf("absDeltaUint32(7,7) = %d, want 0", got)
	}

	if got := absDeltaUint64(0, 9); got != 9 {
		t.Errorf("absDeltaUint64(0,9) = %d, want 9", got)
	}
	if got := absDeltaUint64(9, 0); got != 9 {
		t.Errorf("absDeltaUint64(9,0) = %d, want 9", got)
	}

	if got := absDeltaFloat64(1.5, 4.0); got != 2.5 {
		t.Errorf("absDeltaFloat64(1.5,4.0) = %v, want 2.5", got)
	}
	if got := absDeltaFloat64(4.0, 1.5); got != 2.5 {
		t.Errorf("absDeltaFloat64(4.0,1.5) = %v, want 2.5", got)
	}
}

func TestNormalizeCountryCode(t *testing.T) {
	// Intent: produce a stable 2-byte key (ClickHouse FixedString(2)). Upper-
	// cased, trimmed, padded with NUL for short inputs and truncated to two
	// bytes for long ones. ASCII country codes only, by contract.
	cases := []struct {
		in   string
		want string
	}{
		{"us", "US"},
		{"US", "US"},
		{" us ", "US"},
		{"usa", "US"}, // truncated to 2
		{"u", "U\x00"},
		{"", "\x00\x00"},
		{"  ", "\x00\x00"}, // all whitespace trims to empty
	}
	for _, tc := range cases {
		if got := normalizeCountryCode(tc.in); got != tc.want {
			t.Errorf("normalizeCountryCode(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestSessionTimeSharesToTuples(t *testing.T) {
	// Empty input returns nil so the ClickHouse column default ([]) applies.
	if got := sessionTimeSharesToTuples(nil); got != nil {
		t.Errorf("nil input should return nil, got %v", got)
	}
	if got := sessionTimeSharesToTuples([]*ipcpb.SessionTimeShare{}); got != nil {
		t.Errorf("empty slice should return nil, got %v", got)
	}

	// Real entries become [name, seconds] tuples in order.
	shares := []*ipcpb.SessionTimeShare{
		{Name: "h264", Seconds: 10},
		{Name: "av1", Seconds: 20},
	}
	got := sessionTimeSharesToTuples(shares)
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	if got[0][0] != "h264" || got[0][1] != uint32(10) {
		t.Errorf("tuple[0] = %v, want [h264 10]", got[0])
	}
	if got[1][0] != "av1" || got[1][1] != uint32(20) {
		t.Errorf("tuple[1] = %v, want [av1 20]", got[1])
	}

	// nil entries are skipped, not turned into zero-value tuples.
	got = sessionTimeSharesToTuples([]*ipcpb.SessionTimeShare{nil, {Name: "vp9", Seconds: 5}, nil})
	if len(got) != 1 || got[0][0] != "vp9" {
		t.Fatalf("nil entries should be skipped, got %v", got)
	}
}

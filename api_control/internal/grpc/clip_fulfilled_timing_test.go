package grpc

import (
	sharedpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/shared"
	"testing"
)

// commodore.clips stores the fulfilled range Foghorn harvested. Foghorn's
// effective timing is authoritative for every clip mode, and a successful clip
// always reports one — a zero/absent range is a contract violation the caller
// fails closed on, never a fallback to request-derived timing.
func TestFulfilledClipTiming(t *testing.T) {
	cases := []struct {
		name         string
		resp         *sharedpb.CreateClipResponse
		wantStart    int64
		wantDuration int64
		wantOK       bool
	}{
		{
			name:         "full absolute clip",
			resp:         &sharedpb.CreateClipResponse{EffectiveStartMs: 1_700_000_000_000, EffectiveDurationMs: 60_000},
			wantStart:    1_700_000_000_000,
			wantDuration: 60_000,
			wantOK:       true,
		},
		{
			name:         "partial best-effort clip",
			resp:         &sharedpb.CreateClipResponse{EffectiveStartMs: 1_700_000_035_000, EffectiveDurationMs: 25_000, Partial: true},
			wantStart:    1_700_000_035_000,
			wantDuration: 25_000,
			wantOK:       true,
		},
		{
			name:   "missing fulfilled range fails closed",
			resp:   &sharedpb.CreateClipResponse{},
			wantOK: false,
		},
		{
			name:   "zero duration fails closed",
			resp:   &sharedpb.CreateClipResponse{EffectiveStartMs: 1_700_000_000_000, EffectiveDurationMs: 0},
			wantOK: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotStart, gotDuration, ok := fulfilledClipTiming(tc.resp)
			if ok != tc.wantOK {
				t.Fatalf("ok: got %v want %v", ok, tc.wantOK)
			}
			if ok && (gotStart != tc.wantStart || gotDuration != tc.wantDuration) {
				t.Fatalf("got start=%d duration=%d, want start=%d duration=%d", gotStart, gotDuration, tc.wantStart, tc.wantDuration)
			}
		})
	}
}

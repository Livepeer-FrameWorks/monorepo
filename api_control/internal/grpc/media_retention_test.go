package grpc

import (
	"testing"
	"time"

	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
)

func TestSystemRetentionDefault(t *testing.T) {
	// Intent (per the resolution-cascade doc): VOD uploads are kept forever
	// by default (0), live recordings (DVR/clip) are ephemeral (30d). An
	// UNSPECIFIED target must fail safe to a finite horizon, not forever.
	cases := []struct {
		target commodorepb.MediaRetentionTarget
		want   int32
	}{
		{commodorepb.MediaRetentionTarget_MEDIA_RETENTION_TARGET_VOD, 0},
		{commodorepb.MediaRetentionTarget_MEDIA_RETENTION_TARGET_DVR, 30},
		{commodorepb.MediaRetentionTarget_MEDIA_RETENTION_TARGET_CLIP, 30},
		{commodorepb.MediaRetentionTarget_MEDIA_RETENTION_TARGET_UNSPECIFIED, 30},
	}
	for _, tc := range cases {
		if got := systemRetentionDefault(tc.target); got != tc.want {
			t.Errorf("systemRetentionDefault(%v) = %d, want %d", tc.target, got, tc.want)
		}
	}
}

func TestResolveTenantPerClassEffective(t *testing.T) {
	vod := commodorepb.MediaRetentionTarget_MEDIA_RETENTION_TARGET_VOD
	dvr := commodorepb.MediaRetentionTarget_MEDIA_RETENTION_TARGET_DVR

	cases := []struct {
		name   string
		target commodorepb.MediaRetentionTarget
		days   int32
		set    bool
		cap    int32
		want   int32
	}{
		// Unset → system default, no cap.
		{"unset VOD no cap", vod, 0, false, 0, 0},
		{"unset DVR no cap", dvr, 0, false, 0, 30},
		// Set value passes through when under an (or no) cap.
		{"set under cap", dvr, 7, true, 30, 7},
		{"set no cap", dvr, 365, true, 0, 365},
		// Cap clamps a value above it.
		{"set above cap clamps", dvr, 90, true, 30, 30},
		// "Forever" (0/negative) must be clamped UP to the cap when a finite
		// cap exists — this is the Free-tier safety: forever is not allowed.
		{"forever VOD clamped by cap", vod, 0, false, 30, 30},
		{"explicit forever clamped", dvr, 0, true, 30, 30},
		{"negative treated as forever, clamped", dvr, -5, true, 30, 30},
		// Negative cap behaves as "no cap" (only cap > 0 clamps).
		{"negative cap is no cap", dvr, 0, true, -1, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := resolveTenantPerClassEffective(tc.target, tc.days, tc.set, tc.cap); got != tc.want {
				t.Errorf("resolveTenantPerClassEffective(%v, days=%d, set=%v, cap=%d) = %d, want %d",
					tc.target, tc.days, tc.set, tc.cap, got, tc.want)
			}
		})
	}
}

func TestDaysUntil(t *testing.T) {
	now := time.Now()

	// Past times floor at zero — never negative (a stale retention_until must
	// not read as a future horizon).
	if got := daysUntil(now.Add(-time.Hour)); got != 0 {
		t.Errorf("daysUntil(1h ago) = %d, want 0", got)
	}
	if got := daysUntil(now.Add(-100 * 24 * time.Hour)); got != 0 {
		t.Errorf("daysUntil(100d ago) = %d, want 0", got)
	}

	// Future times round UP to whole days (a partial day still counts as a
	// day of retention). Offsets are deliberately non-integer-day so tiny
	// clock drift between now() here and inside daysUntil can't flip the ceil.
	if got := daysUntil(now.Add(10*24*time.Hour + time.Hour)); got != 11 {
		t.Errorf("daysUntil(~10d1h) = %d, want 11", got)
	}
	if got := daysUntil(now.Add(time.Hour)); got != 1 {
		t.Errorf("daysUntil(1h) = %d, want 1", got)
	}
}

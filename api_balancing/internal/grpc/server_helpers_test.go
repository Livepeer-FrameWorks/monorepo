package grpc

import (
	"testing"

	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	sharedpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/shared"
)

// TestDVRRetentionDays pins the retention default applied when a DVR policy (or
// its retention field) is absent. The proto contract is: unset → 30-day
// default. Getting this wrong means artifacts either never expire or expire
// against the wrong window, so the nil paths must resolve to 30, not 0.
func TestDVRRetentionDays(t *testing.T) {
	days := int32(7)
	zero := int32(0)
	cases := []struct {
		name   string
		policy *sharedpb.DVRPolicy
		want   int32
	}{
		{"nil policy", nil, 30},
		{"nil retention field", &sharedpb.DVRPolicy{}, 30},
		{"explicit value passes through", &sharedpb.DVRPolicy{RecordingRetentionDays: &days}, 7},
		{"explicit zero (keep forever) passes through", &sharedpb.DVRPolicy{RecordingRetentionDays: &zero}, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := dvrRetentionDays(tc.policy); got != tc.want {
				t.Fatalf("dvrRetentionDays = %d, want %d", got, tc.want)
			}
		})
	}
}

// TestClipProcessingSourceKind pins the enum→label mapping that tags a clip's
// processing source. Each known kind maps to its stable string; any
// unrecognized/unspecified kind maps to "" so a new enum value never silently
// inherits another kind's routing label.
func TestClipProcessingSourceKind(t *testing.T) {
	cases := []struct {
		kind ipcpb.ClipPullRequest_SourceKind
		want string
	}{
		{ipcpb.ClipPullRequest_SOURCE_KIND_LIVE, "live"},
		{ipcpb.ClipPullRequest_SOURCE_KIND_DVR_ROLLING, "dvr_rolling"},
		{ipcpb.ClipPullRequest_SOURCE_KIND_CHAPTER, "chapter"},
		{ipcpb.ClipPullRequest_SOURCE_KIND_UNSPECIFIED, ""},
	}
	for _, tc := range cases {
		if got := clipProcessingSourceKind(tc.kind); got != tc.want {
			t.Fatalf("clipProcessingSourceKind(%v) = %q, want %q", tc.kind, got, tc.want)
		}
	}
}

package grpc

import (
	"context"
	"testing"

	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	sharedpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/shared"
	"google.golang.org/grpc/metadata"
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

// TestX402PaidFromMetadata pins the security-relevant gate that decides whether
// an incoming request is treated as already-paid. Only an explicit affirmative
// (1/true/yes, case- and space-insensitive) flips it true; missing metadata, a
// missing header, or any other value must be false so a request is never
// treated as paid by accident.
func TestX402PaidFromMetadata(t *testing.T) {
	t.Run("affirmative values are paid", func(t *testing.T) {
		for _, v := range []string{"1", "true", "TRUE", "yes", " Yes "} {
			ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("x402-paid", v))
			if !x402PaidFromMetadata(ctx) {
				t.Fatalf("value %q should be treated as paid", v)
			}
		}
	})

	t.Run("non-affirmative and absent are not paid", func(t *testing.T) {
		// No metadata at all.
		if x402PaidFromMetadata(context.Background()) {
			t.Fatal("missing metadata must not be treated as paid")
		}
		// Metadata present but header absent.
		ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("other", "1"))
		if x402PaidFromMetadata(ctx) {
			t.Fatal("missing x402-paid header must not be treated as paid")
		}
		// Header present with non-affirmative values.
		for _, v := range []string{"0", "false", "no", "", "maybe"} {
			ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("x402-paid", v))
			if x402PaidFromMetadata(ctx) {
				t.Fatalf("value %q must not be treated as paid", v)
			}
		}
	})
}

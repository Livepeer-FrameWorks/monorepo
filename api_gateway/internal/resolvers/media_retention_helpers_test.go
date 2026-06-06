package resolvers

import (
	"testing"

	"frameworks/api_gateway/graph/model"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Intent: protoTargetType maps every GraphQL retention target to its proto
// enum, and any unknown target to UNSPECIFIED (never silently to a real type).
func TestProtoTargetType(t *testing.T) {
	cases := []struct {
		in   model.MediaRetentionTarget
		want commodorepb.MediaRetentionTarget
	}{
		{model.MediaRetentionTargetDvr, commodorepb.MediaRetentionTarget_MEDIA_RETENTION_TARGET_DVR},
		{model.MediaRetentionTargetClip, commodorepb.MediaRetentionTarget_MEDIA_RETENTION_TARGET_CLIP},
		{model.MediaRetentionTargetVod, commodorepb.MediaRetentionTarget_MEDIA_RETENTION_TARGET_VOD},
		{model.MediaRetentionTarget("BOGUS"), commodorepb.MediaRetentionTarget_MEDIA_RETENTION_TARGET_UNSPECIFIED},
	}
	for _, tc := range cases {
		if got := protoTargetType(tc.in); got != tc.want {
			t.Fatalf("protoTargetType(%v) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

// Intent: RetentionSourceFromString maps the persisted source strings to the
// GraphQL enum.
//
// FINDING: only "tenant_default" and "per_asset_override" are mapped
// explicitly; every other input — including "per_stream_override" and
// "tier_entitlement" — falls through to TIER_ENTITLEMENT. If a per-stream
// source string ever reaches this function it would be mislabeled. Behavior
// pinned as-is; worth confirming the caller never passes per_stream_override.
func TestRetentionSourceFromString(t *testing.T) {
	cases := []struct {
		in   string
		want model.RetentionSource
	}{
		{"tenant_default", model.RetentionSourceTenantDefault},
		{"per_asset_override", model.RetentionSourcePerAssetOverride},
		{"tier_entitlement", model.RetentionSourceTierEntitlement},
		{"per_stream_override", model.RetentionSourceTierEntitlement}, // not explicitly mapped
		{"", model.RetentionSourceTierEntitlement},
	}
	for _, tc := range cases {
		if got := RetentionSourceFromString(tc.in); got != tc.want {
			t.Fatalf("RetentionSourceFromString(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

// Intent: camelCase turns snake_case field names into camelCase for client
// field hints; it tolerates empty input and stray underscores.
func TestCamelCase(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"retention_days", "retentionDays"},
		{"default_vod_retention_days", "defaultVodRetentionDays"},
		{"days", "days"},
		{"", ""},
		{"trailing_", "trailing"},
		{"_leading", "Leading"},
		{"already", "already"},
	}
	for _, tc := range cases {
		if got := camelCase(tc.in); got != tc.want {
			t.Fatalf("camelCase(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// Intent: guessFieldFromMessage extracts the first known field token from an
// error message (longest candidates first so substrings don't shadow them),
// returning the camelCased hint or "" when no token is present.
func TestGuessFieldFromMessage(t *testing.T) {
	cases := []struct {
		msg, want string
	}{
		{"value default_vod_retention_days must be >= 0", "defaultVodRetentionDays"},
		{"retention_days out of range", "retentionDays"},
		{"target_id is required", "targetId"},
		{"nothing identifiable here", ""},
	}
	for _, tc := range cases {
		if got := guessFieldFromMessage(tc.msg); got != tc.want {
			t.Fatalf("guessFieldFromMessage(%q) = %q, want %q", tc.msg, got, tc.want)
		}
	}
}

// Intent: the gRPC-status mappers only fire on their matching code, attach a
// field hint where the message carries one, and return nil for nil errors,
// non-status errors, and mismatched codes.
func TestStatusMappers(t *testing.T) {
	t.Run("invalid argument with field hint", func(t *testing.T) {
		v := mapInvalidArgument(status.Error(codes.InvalidArgument, "stream_id is malformed"))
		if v == nil || v.Field == nil || *v.Field != "streamId" {
			t.Fatalf("mapInvalidArgument = %+v, want field streamId", v)
		}
		if mapInvalidArgument(status.Error(codes.NotFound, "x")) != nil {
			t.Fatal("wrong code should map to nil")
		}
		if mapInvalidArgument(nil) != nil {
			t.Fatal("nil err should map to nil")
		}
	})

	t.Run("not found", func(t *testing.T) {
		if mapNotFound(status.Error(codes.NotFound, "missing")) == nil {
			t.Fatal("NotFound should map")
		}
		if mapNotFound(status.Error(codes.InvalidArgument, "x")) != nil {
			t.Fatal("wrong code should map to nil")
		}
	})

	t.Run("permission denied", func(t *testing.T) {
		if mapPermissionDenied(status.Error(codes.PermissionDenied, "not yours")) == nil {
			t.Fatal("PermissionDenied should map")
		}
		if mapPermissionDenied(status.Error(codes.NotFound, "x")) != nil {
			t.Fatal("wrong code should map to nil")
		}
	})

	t.Run("failed precondition with field hint", func(t *testing.T) {
		v := mapFailedPrecondition(status.Error(codes.FailedPrecondition, "retention_days not allowed"))
		if v == nil || v.Field == nil || *v.Field != "retentionDays" {
			t.Fatalf("mapFailedPrecondition = %+v, want field retentionDays", v)
		}
		if mapFailedPrecondition(status.Error(codes.NotFound, "x")) != nil {
			t.Fatal("wrong code should map to nil")
		}
	})
}

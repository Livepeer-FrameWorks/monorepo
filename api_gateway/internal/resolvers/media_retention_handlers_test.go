package resolvers

import (
	"context"
	"errors"
	"testing"
	"time"

	"frameworks/api_gateway/graph/model"
	"frameworks/api_gateway/internal/clients/clientstest"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// DoMediaRetentionPolicy maps the proto response (incl. optional per-class
// defaults) into the model. Permission gate fires before any backend call.
func TestDoMediaRetentionPolicy(t *testing.T) {
	vod := int32(45)
	c := &clientstest.FakeCommodore{
		GetMediaRetentionPolicyFn: func(_ context.Context, _ *commodorepb.GetMediaRetentionPolicyRequest) (*commodorepb.GetMediaRetentionPolicyResponse, error) {
			return &commodorepb.GetMediaRetentionPolicyResponse{
				DefaultVodRetentionDays:    &vod,
				EffectiveVodRetentionDays:  45,
				EffectiveDvrRetentionDays:  30,
				EffectiveClipRetentionDays: 30,
				UpdatedBy:                  "alice",
				UpdatedAt:                  timestamppb.New(time.Now()),
			}, nil
		},
	}
	out, err := commoW2(c).DoMediaRetentionPolicy(clientstest.AuthedCtx("t1"))
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if out.DefaultVodRetentionDays == nil || *out.DefaultVodRetentionDays != 45 {
		t.Fatalf("DefaultVodRetentionDays mapped wrong: %+v", out.DefaultVodRetentionDays)
	}
	if out.EffectiveDvrRetentionDays != 30 || out.UpdatedBy == nil || *out.UpdatedBy != "alice" || out.UpdatedAt == nil {
		t.Fatalf("policy mapped wrong: %+v", out)
	}

	denied := &clientstest.FakeCommodore{}
	if _, err := commoW2(denied).DoMediaRetentionPolicy(context.Background()); err == nil {
		t.Fatal("expected permission error")
	}
	if denied.Calls != 0 {
		t.Fatalf("guard must not reach backend, Calls=%d", denied.Calls)
	}

	fail := commoW2(&clientstest.FakeCommodore{
		GetMediaRetentionPolicyFn: func(context.Context, *commodorepb.GetMediaRetentionPolicyRequest) (*commodorepb.GetMediaRetentionPolicyResponse, error) {
			return nil, errors.New("down")
		},
	})
	if _, err := fail.DoMediaRetentionPolicy(clientstest.AuthedCtx("t1")); err == nil {
		t.Fatal("backend error should propagate")
	}
}

// DoSetMediaRetentionPolicy translates the GraphQL target enum to the proto
// enum and forwards days/clear; it validates locally (unspecified target,
// missing days, negative days) before any backend call.
func TestDoSetMediaRetentionPolicy(t *testing.T) {
	var got *commodorepb.SetMediaRetentionPolicyRequest
	c := &clientstest.FakeCommodore{
		SetMediaRetentionPolicyFn: func(_ context.Context, req *commodorepb.SetMediaRetentionPolicyRequest) (*commodorepb.SetMediaRetentionPolicyResponse, error) {
			got = req
			return &commodorepb.SetMediaRetentionPolicyResponse{
				Policy: &commodorepb.GetMediaRetentionPolicyResponse{EffectiveDvrRetentionDays: 14},
			}, nil
		},
	}
	days := 14
	res, err := commoW2(c).DoSetMediaRetentionPolicy(clientstest.AuthedCtx("t1"), model.SetMediaRetentionPolicyInput{
		TargetType: model.MediaRetentionTargetDvr,
		Days:       &days,
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got.TargetType != commodorepb.MediaRetentionTarget_MEDIA_RETENTION_TARGET_DVR || got.Days != 14 || got.Clear {
		t.Fatalf("request built wrong: %+v", got)
	}
	if policy, ok := res.(*model.MediaRetentionPolicy); !ok || policy.EffectiveDvrRetentionDays != 14 {
		t.Fatalf("expected mapped policy, got %T %+v", res, res)
	}

	// Unspecified target → ValidationError, no backend call.
	bad := &clientstest.FakeCommodore{}
	res, err = commoW2(bad).DoSetMediaRetentionPolicy(clientstest.AuthedCtx("t1"), model.SetMediaRetentionPolicyInput{
		TargetType: model.MediaRetentionTarget("BOGUS"),
		Days:       &days,
	})
	if err != nil {
		t.Fatalf("validation should not be a Go error: %v", err)
	}
	if _, ok := res.(*model.ValidationError); !ok {
		t.Fatalf("expected ValidationError for bad target, got %T", res)
	}
	if bad.Calls != 0 {
		t.Fatalf("validation must not reach backend, Calls=%d", bad.Calls)
	}

	// days required unless clear=true.
	res, _ = commoW2(&clientstest.FakeCommodore{}).DoSetMediaRetentionPolicy(clientstest.AuthedCtx("t1"), model.SetMediaRetentionPolicyInput{
		TargetType: model.MediaRetentionTargetVod,
	})
	if ve, ok := res.(*model.ValidationError); !ok || ve.Field == nil || *ve.Field != "days" {
		t.Fatalf("expected days ValidationError, got %T %+v", res, res)
	}

	// Commodore InvalidArgument is mapped to a ValidationError union member.
	inv := commoW2(&clientstest.FakeCommodore{
		SetMediaRetentionPolicyFn: func(context.Context, *commodorepb.SetMediaRetentionPolicyRequest) (*commodorepb.SetMediaRetentionPolicyResponse, error) {
			return nil, status.Error(codes.InvalidArgument, "days out of range")
		},
	})
	res, err = inv.DoSetMediaRetentionPolicy(clientstest.AuthedCtx("t1"), model.SetMediaRetentionPolicyInput{
		TargetType: model.MediaRetentionTargetVod, Days: &days,
	})
	if err != nil {
		t.Fatalf("InvalidArgument should be a union member: %v", err)
	}
	if _, ok := res.(*model.ValidationError); !ok {
		t.Fatalf("expected ValidationError from InvalidArgument, got %T", res)
	}
}

// DoSetStreamRetentionOverrides forwards per-class overrides + clear flags and
// requires streamId and at least one override/clear flag locally.
func TestDoSetStreamRetentionOverrides(t *testing.T) {
	var got *commodorepb.SetStreamRetentionOverridesRequest
	out2 := int32(20)
	c := &clientstest.FakeCommodore{
		SetStreamRetentionOverridesFn: func(_ context.Context, req *commodorepb.SetStreamRetentionOverridesRequest) (*commodorepb.SetStreamRetentionOverridesResponse, error) {
			got = req
			return &commodorepb.SetStreamRetentionOverridesResponse{
				StreamId:                 req.StreamId,
				DvrRetentionDaysOverride: &out2,
			}, nil
		},
	}
	dvr := 20
	clearClip := true
	res, err := commoW2(c).DoSetStreamRetentionOverrides(clientstest.AuthedCtx("t1"), model.SetStreamRetentionOverridesInput{
		StreamID:                   "s1",
		DvrRetentionDaysOverride:   &dvr,
		ClearClipRetentionOverride: &clearClip,
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got.StreamId != "s1" || got.DvrRetentionDaysOverride == nil || *got.DvrRetentionDaysOverride != 20 || !got.ClearClipRetentionOverride {
		t.Fatalf("request built wrong: %+v", got)
	}
	if so, ok := res.(*model.StreamRetentionOverrides); !ok || so.DvrRetentionDaysOverride == nil || *so.DvrRetentionDaysOverride != 20 {
		t.Fatalf("expected mapped overrides, got %T %+v", res, res)
	}

	// Missing streamId → ValidationError, no backend call.
	bad := &clientstest.FakeCommodore{}
	res, _ = commoW2(bad).DoSetStreamRetentionOverrides(clientstest.AuthedCtx("t1"), model.SetStreamRetentionOverridesInput{DvrRetentionDaysOverride: &dvr})
	if ve, ok := res.(*model.ValidationError); !ok || ve.Field == nil || *ve.Field != "streamId" {
		t.Fatalf("expected streamId ValidationError, got %T %+v", res, res)
	}
	if bad.Calls != 0 {
		t.Fatalf("validation must not reach backend, Calls=%d", bad.Calls)
	}

	// No override or clear flag → ValidationError.
	res, _ = commoW2(&clientstest.FakeCommodore{}).DoSetStreamRetentionOverrides(clientstest.AuthedCtx("t1"), model.SetStreamRetentionOverridesInput{StreamID: "s1"})
	if _, ok := res.(*model.ValidationError); !ok {
		t.Fatalf("expected ValidationError for empty input, got %T", res)
	}
}

// DoUpdateMediaRetention forwards target type/id + retentionDays, validating
// the mutually-exclusive day/until fields and the >= 0 rule locally.
func TestDoUpdateMediaRetention(t *testing.T) {
	var got *commodorepb.UpdateAssetRetentionRequest
	c := &clientstest.FakeCommodore{
		UpdateAssetRetentionFn: func(_ context.Context, req *commodorepb.UpdateAssetRetentionRequest) (*commodorepb.UpdateAssetRetentionResponse, error) {
			got = req
			return &commodorepb.UpdateAssetRetentionResponse{
				TargetId:      req.TargetId,
				RetentionDays: 5,
				Source:        "per_asset_override",
			}, nil
		},
	}
	days := 5
	res, err := commoW2(c).DoUpdateMediaRetention(clientstest.AuthedCtx("t1"), model.UpdateMediaRetentionInput{
		TargetType:    model.MediaRetentionTargetClip,
		TargetID:      "clip-1",
		RetentionDays: &days,
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got.TargetType != commodorepb.MediaRetentionTarget_MEDIA_RETENTION_TARGET_CLIP || got.TargetId != "clip-1" ||
		got.RetentionDays == nil || *got.RetentionDays != 5 {
		t.Fatalf("request built wrong: %+v", got)
	}
	if er, ok := res.(*model.EffectiveRetention); !ok || er.RetentionDays != 5 || er.Source != model.RetentionSourcePerAssetOverride {
		t.Fatalf("expected mapped EffectiveRetention, got %T %+v", res, res)
	}

	// targetId required → ValidationError, no backend call.
	bad := &clientstest.FakeCommodore{}
	res, _ = commoW2(bad).DoUpdateMediaRetention(clientstest.AuthedCtx("t1"), model.UpdateMediaRetentionInput{
		TargetType: model.MediaRetentionTargetClip, RetentionDays: &days,
	})
	if ve, ok := res.(*model.ValidationError); !ok || ve.Field == nil || *ve.Field != "targetId" {
		t.Fatalf("expected targetId ValidationError, got %T %+v", res, res)
	}
	if bad.Calls != 0 {
		t.Fatalf("validation must not reach backend, Calls=%d", bad.Calls)
	}

	// retentionDays and retentionUntil are mutually exclusive.
	until := time.Now().Add(48 * time.Hour)
	res, _ = commoW2(&clientstest.FakeCommodore{}).DoUpdateMediaRetention(clientstest.AuthedCtx("t1"), model.UpdateMediaRetentionInput{
		TargetType: model.MediaRetentionTargetClip, TargetID: "c", RetentionDays: &days, RetentionUntil: &until,
	})
	if _, ok := res.(*model.ValidationError); !ok {
		t.Fatalf("expected mutual-exclusion ValidationError, got %T", res)
	}
}

// DoResetMediaRetentionOverride forwards target type/id and maps the resolved
// horizon back; targetId is required locally.
func TestDoResetMediaRetentionOverride(t *testing.T) {
	var got *commodorepb.ResetAssetRetentionRequest
	c := &clientstest.FakeCommodore{
		ResetAssetRetentionFn: func(_ context.Context, req *commodorepb.ResetAssetRetentionRequest) (*commodorepb.UpdateAssetRetentionResponse, error) {
			got = req
			return &commodorepb.UpdateAssetRetentionResponse{
				TargetId:      req.TargetId,
				RetentionDays: 30,
				Source:        "tenant_default",
			}, nil
		},
	}
	res, err := commoW2(c).DoResetMediaRetentionOverride(clientstest.AuthedCtx("t1"), model.ResetMediaRetentionOverrideInput{
		TargetType: model.MediaRetentionTargetDvr,
		TargetID:   "dvr-1",
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got.TargetType != commodorepb.MediaRetentionTarget_MEDIA_RETENTION_TARGET_DVR || got.TargetId != "dvr-1" {
		t.Fatalf("request built wrong: %+v", got)
	}
	if er, ok := res.(*model.EffectiveRetention); !ok || er.RetentionDays != 30 || er.Source != model.RetentionSourceTenantDefault {
		t.Fatalf("expected mapped EffectiveRetention, got %T %+v", res, res)
	}

	bad := &clientstest.FakeCommodore{}
	res, _ = commoW2(bad).DoResetMediaRetentionOverride(clientstest.AuthedCtx("t1"), model.ResetMediaRetentionOverrideInput{
		TargetType: model.MediaRetentionTargetDvr,
	})
	if _, ok := res.(*model.ValidationError); !ok {
		t.Fatalf("expected targetId ValidationError, got %T", res)
	}
	if bad.Calls != 0 {
		t.Fatalf("validation must not reach backend, Calls=%d", bad.Calls)
	}

	// NotFound from Commodore maps to NotFoundError union member.
	nf := commoW2(&clientstest.FakeCommodore{
		ResetAssetRetentionFn: func(context.Context, *commodorepb.ResetAssetRetentionRequest) (*commodorepb.UpdateAssetRetentionResponse, error) {
			return nil, status.Error(codes.NotFound, "asset missing")
		},
	})
	res, err = nf.DoResetMediaRetentionOverride(clientstest.AuthedCtx("t1"), model.ResetMediaRetentionOverrideInput{
		TargetType: model.MediaRetentionTargetDvr, TargetID: "x",
	})
	if err != nil {
		t.Fatalf("NotFound should be a union member: %v", err)
	}
	if _, ok := res.(*model.NotFoundError); !ok {
		t.Fatalf("expected NotFoundError, got %T", res)
	}
}

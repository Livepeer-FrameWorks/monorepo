package resolvers

import (
	"context"
	"errors"
	"testing"

	"frameworks/api_gateway/graph/model"
	"frameworks/api_gateway/internal/clients/clientstest"
	commodoreclient "github.com/Livepeer-FrameWorks/monorepo/pkg/clients/commodore"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	commonpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/common"
	sharedpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/shared"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// commoR builds a resolver backed by a FakeCommodore. Named distinctly from the
// package-level newResolver helper so both can coexist.
func commoR(commo *clientstest.FakeCommodore) *Resolver {
	return &Resolver{
		Clients: clientstest.Clients(clientstest.WithCommodore(commo)),
		Logger:  clientstest.DiscardLogger(),
	}
}

func TestDoGetClip(t *testing.T) {
	var gotHash string
	commo := &clientstest.FakeCommodore{
		GetClipFn: func(_ context.Context, clipHash string) (*sharedpb.ClipInfo, error) {
			gotHash = clipHash
			return &sharedpb.ClipInfo{Id: clipHash, Title: "T", ClipHash: clipHash}, nil
		},
	}
	r := commoR(commo)
	got, err := r.DoGetClip(clientstest.AuthedCtx("t1"), "clip9")
	if err != nil || got == nil || got.Id != "clip9" {
		t.Fatalf("DoGetClip = (%+v, %v)", got, err)
	}
	if gotHash != "clip9" {
		t.Errorf("clip id not forwarded: %q", gotHash)
	}

	failing := commoR(&clientstest.FakeCommodore{
		GetClipFn: func(context.Context, string) (*sharedpb.ClipInfo, error) { return nil, errors.New("boom") },
	})
	if _, err := failing.DoGetClip(clientstest.AuthedCtx("t1"), "x"); err == nil {
		t.Fatal("DoGetClip should surface backend error")
	}
}

func TestDoGetClips(t *testing.T) {
	// Happy path forwards the tenant from context and the optional streamID filter.
	var gotTenant string
	var gotStream *string
	commo := &clientstest.FakeCommodore{
		GetClipsFn: func(_ context.Context, tenantID string, streamID *string, _ *commonpb.CursorPaginationRequest, _ ...commodoreclient.MediaListOptions) (*sharedpb.GetClipsResponse, error) {
			gotTenant = tenantID
			gotStream = streamID
			return &sharedpb.GetClipsResponse{Clips: []*sharedpb.ClipInfo{{Id: "c1"}}}, nil
		},
	}
	r := commoR(commo)
	sid := "s1"
	clips, err := r.DoGetClips(clientstest.AuthedCtx("t1"), &sid)
	if err != nil || len(clips) != 1 || clips[0].Id != "c1" {
		t.Fatalf("DoGetClips = (%+v, %v)", clips, err)
	}
	if gotTenant != "t1" {
		t.Errorf("tenant not forwarded: %q", gotTenant)
	}
	if gotStream == nil || *gotStream != "s1" {
		t.Errorf("streamID filter not forwarded: %v", gotStream)
	}

	// No tenant in context → guard fires before the backend.
	guard := commoR(&clientstest.FakeCommodore{})
	if _, err := guard.DoGetClips(context.Background(), nil); err == nil {
		t.Fatal("DoGetClips should require tenant context")
	}
	if guard.Clients.Commodore.(*clientstest.FakeCommodore).Calls != 0 {
		t.Fatal("tenant guard must short-circuit before backend")
	}

	failing := commoR(&clientstest.FakeCommodore{
		GetClipsFn: func(context.Context, string, *string, *commonpb.CursorPaginationRequest, ...commodoreclient.MediaListOptions) (*sharedpb.GetClipsResponse, error) {
			return nil, errors.New("boom")
		},
	})
	if _, err := failing.DoGetClips(clientstest.AuthedCtx("t1"), nil); err == nil {
		t.Fatal("DoGetClips should surface backend error")
	}
}

// DoCreateClip routes by the SOURCE stream's public UUID via req.StreamId. The
// clip's own playback_id must NOT leak onto the outbound request as a routing
// key — req.PlaybackId stays nil so Foghorn resolves the source by stream_id.
func TestDoCreateClipRouting(t *testing.T) {
	var gotReq *sharedpb.CreateClipRequest
	commo := &clientstest.FakeCommodore{
		CreateClipFn: func(_ context.Context, req *sharedpb.CreateClipRequest) (*sharedpb.CreateClipResponse, error) {
			gotReq = req
			return &sharedpb.CreateClipResponse{
				RequestId:  "req1",
				ClipHash:   "ch1",
				PlaybackId: "pl_clip1",
				NodeId:     "n1",
				Status:     "processing",
			}, nil
		},
	}
	r := commoR(commo)

	start, stop := 1000, 1060
	res, err := r.DoCreateClip(clientstest.AuthedCtx("t1"), model.CreateClipInput{
		StreamID:  "src-uuid",
		Title:     "My Clip",
		StartUnix: &start,
		StopUnix:  &stop,
	})
	if err != nil {
		t.Fatalf("DoCreateClip err: %v", err)
	}

	// Outbound routing: source stream UUID rides StreamId, NOT PlaybackId.
	if gotReq.StreamId == nil || *gotReq.StreamId != "src-uuid" {
		t.Fatalf("expected req.StreamId=src-uuid, got %v", gotReq.StreamId)
	}
	if gotReq.PlaybackId != nil {
		t.Fatalf("clip playback_id must not be used as source routing key: %v", *gotReq.PlaybackId)
	}
	if gotReq.Mode != sharedpb.ClipMode_CLIP_MODE_ABSOLUTE {
		t.Fatalf("default mode should be ABSOLUTE, got %v", gotReq.Mode)
	}
	// ABSOLUTE mode derives duration from start/stop.
	if gotReq.DurationSec == nil || *gotReq.DurationSec != 60 {
		t.Fatalf("expected derived duration 60, got %v", gotReq.DurationSec)
	}

	clip, ok := res.(*sharedpb.ClipInfo)
	if !ok {
		t.Fatalf("expected ClipInfo, got %T", res)
	}
	// Returned model carries the response's artifact identifiers + the source stream.
	if clip.Id != "req1" || clip.ClipHash != "ch1" || clip.PlaybackId != "pl_clip1" {
		t.Fatalf("response identifiers not mapped: %+v", clip)
	}
	if clip.StreamId != "src-uuid" {
		t.Fatalf("clip should carry source stream id, got %q", clip.StreamId)
	}
}

// DoCreateClip maps gRPC status codes to typed union members instead of hard errors.
func TestDoCreateClipErrorClassification(t *testing.T) {
	ctx := clientstest.AuthedCtx("t1")
	input := model.CreateClipInput{StreamID: "s1", Title: "t"}

	rNF := commoR(&clientstest.FakeCommodore{
		CreateClipFn: func(context.Context, *sharedpb.CreateClipRequest) (*sharedpb.CreateClipResponse, error) {
			return nil, status.Error(codes.NotFound, "stream gone")
		},
	})
	res, err := rNF.DoCreateClip(ctx, input)
	if err != nil {
		t.Fatalf("NotFound should be a typed result, not a Go error: %v", err)
	}
	if nf, ok := res.(*model.NotFoundError); !ok || nf.ResourceID != "s1" {
		t.Fatalf("expected NotFoundError for s1, got %T %+v", res, res)
	}

	rVal := commoR(&clientstest.FakeCommodore{
		CreateClipFn: func(context.Context, *sharedpb.CreateClipRequest) (*sharedpb.CreateClipResponse, error) {
			return nil, status.Error(codes.InvalidArgument, "bad window")
		},
	})
	res, err = rVal.DoCreateClip(ctx, input)
	if err != nil {
		t.Fatalf("InvalidArgument should be a typed result: %v", err)
	}
	if _, ok := res.(*model.ValidationError); !ok {
		t.Fatalf("expected ValidationError, got %T", res)
	}
}

func TestDoDeleteClip(t *testing.T) {
	ctx := clientstest.AuthedCtx("t1")

	r := commoR(&clientstest.FakeCommodore{
		DeleteClipFn: func(context.Context, string) error { return nil },
	})
	res, err := r.DoDeleteClip(ctx, "c1")
	if err != nil {
		t.Fatal(err)
	}
	if ok, isOK := res.(*model.DeleteSuccess); !isOK || ok.DeletedID != "c1" {
		t.Fatalf("expected DeleteSuccess for c1, got %T %+v", res, res)
	}

	// "not found" → typed NotFoundError, no Go error.
	rNF := commoR(&clientstest.FakeCommodore{
		DeleteClipFn: func(context.Context, string) error { return errors.New("clip not found") },
	})
	res, err = rNF.DoDeleteClip(ctx, "ghost")
	if err != nil {
		t.Fatalf("not-found should not be a Go error: %v", err)
	}
	if nf, isNF := res.(*model.NotFoundError); !isNF || nf.ResourceType != "Clip" || nf.ResourceID != "ghost" {
		t.Fatalf("expected Clip NotFoundError, got %T %+v", res, res)
	}

	rErr := commoR(&clientstest.FakeCommodore{
		DeleteClipFn: func(context.Context, string) error { return errors.New("permission denied") },
	})
	if _, err := rErr.DoDeleteClip(ctx, "c1"); err == nil {
		t.Fatal("non-not-found backend error should be a Go error")
	}
}

// DoStartDVR builds a StartDVRRequest carrying the stream ID as a *string and
// returns the backend response verbatim.
func TestDoStartDVR(t *testing.T) {
	var gotReq *sharedpb.StartDVRRequest
	commo := &clientstest.FakeCommodore{
		StartDVRFn: func(_ context.Context, req *sharedpb.StartDVRRequest) (*sharedpb.StartDVRResponse, error) {
			gotReq = req
			return &sharedpb.StartDVRResponse{Status: "started", DvrHash: "dvr1", PlaybackId: "pl_dvr1"}, nil
		},
	}
	r := commoR(commo)
	res, err := r.DoStartDVR(clientstest.AuthedCtx("t1"), "s1")
	if err != nil {
		t.Fatal(err)
	}
	if gotReq.StreamId == nil || *gotReq.StreamId != "s1" {
		t.Fatalf("StreamId not forwarded on request: %v", gotReq.StreamId)
	}
	if res.DvrHash != "dvr1" || res.PlaybackId != "pl_dvr1" || res.Status != "started" {
		t.Fatalf("response not mapped: %+v", res)
	}

	failing := commoR(&clientstest.FakeCommodore{
		StartDVRFn: func(context.Context, *sharedpb.StartDVRRequest) (*sharedpb.StartDVRResponse, error) {
			return nil, errors.New("boom")
		},
	})
	if _, err := failing.DoStartDVR(clientstest.AuthedCtx("t1"), "s1"); err == nil {
		t.Fatal("DoStartDVR should surface backend error")
	}
}

func TestDoStopDVR(t *testing.T) {
	ctx := clientstest.AuthedCtx("t1")

	r := commoR(&clientstest.FakeCommodore{
		StopDVRFn: func(context.Context, string) error { return nil },
	})
	res, err := r.DoStopDVR(ctx, "dvr1")
	if err != nil {
		t.Fatal(err)
	}
	if ok, isOK := res.(*model.DeleteSuccess); !isOK || ok.DeletedID != "dvr1" {
		t.Fatalf("expected DeleteSuccess for dvr1, got %T %+v", res, res)
	}

	// "not found" → typed NotFoundError for DVRRequest.
	rNF := commoR(&clientstest.FakeCommodore{
		StopDVRFn: func(context.Context, string) error { return errors.New("dvr not found") },
	})
	res, err = rNF.DoStopDVR(ctx, "ghost")
	if err != nil {
		t.Fatalf("not-found should not be a Go error: %v", err)
	}
	if nf, isNF := res.(*model.NotFoundError); !isNF || nf.ResourceType != "DVRRequest" || nf.ResourceID != "ghost" {
		t.Fatalf("expected DVRRequest NotFoundError, got %T %+v", res, res)
	}
}

func TestDoDeleteDVR(t *testing.T) {
	ctx := clientstest.AuthedCtx("t1")

	r := commoR(&clientstest.FakeCommodore{
		DeleteDVRFn: func(context.Context, string) error { return nil },
	})
	res, err := r.DoDeleteDVR(ctx, "dvr1")
	if err != nil {
		t.Fatal(err)
	}
	if ok, isOK := res.(*model.DeleteSuccess); !isOK || ok.DeletedID != "dvr1" {
		t.Fatalf("expected DeleteSuccess for dvr1, got %T %+v", res, res)
	}

	rNF := commoR(&clientstest.FakeCommodore{
		DeleteDVRFn: func(context.Context, string) error { return errors.New("recording not found") },
	})
	res, err = rNF.DoDeleteDVR(ctx, "ghost")
	if err != nil {
		t.Fatalf("not-found should not be a Go error: %v", err)
	}
	if nf, isNF := res.(*model.NotFoundError); !isNF || nf.ResourceType != "DVRRequest" || nf.ResourceID != "ghost" {
		t.Fatalf("expected DVRRequest NotFoundError, got %T %+v", res, res)
	}
}

func TestDoListDVRRequests(t *testing.T) {
	var gotTenant string
	var gotStream *string
	commo := &clientstest.FakeCommodore{
		ListDVRRequestsFn: func(_ context.Context, tenantID string, streamID *string, _ *commonpb.CursorPaginationRequest, _ ...commodoreclient.MediaListOptions) (*sharedpb.ListDVRRecordingsResponse, error) {
			gotTenant = tenantID
			gotStream = streamID
			return &sharedpb.ListDVRRecordingsResponse{
				DvrRecordings: []*sharedpb.DVRInfo{{DvrHash: "d1"}},
			}, nil
		},
	}
	r := commoR(commo)
	sid := "s1"
	out, err := r.DoListDVRRequests(clientstest.AuthedCtx("t1"), &sid, nil)
	if err != nil || len(out.DvrRecordings) != 1 || out.DvrRecordings[0].DvrHash != "d1" {
		t.Fatalf("DoListDVRRequests = (%+v, %v)", out, err)
	}
	if gotTenant != "t1" {
		t.Errorf("tenant not forwarded: %q", gotTenant)
	}
	if gotStream == nil || *gotStream != "s1" {
		t.Errorf("streamID not forwarded: %v", gotStream)
	}

	// No tenant → guard short-circuits before the backend.
	guard := commoR(&clientstest.FakeCommodore{})
	if _, err := guard.DoListDVRRequests(context.Background(), nil, nil); err == nil {
		t.Fatal("DoListDVRRequests should require tenant context")
	}
	if guard.Clients.Commodore.(*clientstest.FakeCommodore).Calls != 0 {
		t.Fatal("tenant guard must short-circuit before backend")
	}
}

// DoValidateStreamKey never returns a Go error: a backend failure maps to a
// StreamValidation with ERROR status so the surrounding query keeps resolving.
func TestDoValidateStreamKey(t *testing.T) {
	ctx := clientstest.AuthedCtx("t1")

	// Valid key.
	rValid := commoR(&clientstest.FakeCommodore{
		ValidateStreamKeyFn: func(_ context.Context, key string, _ ...string) (*commodorepb.ValidateStreamKeyResponse, error) {
			return &commodorepb.ValidateStreamKeyResponse{Valid: true}, nil
		},
	})
	v, err := rValid.DoValidateStreamKey(ctx, "sk_real")
	if err != nil {
		t.Fatal(err)
	}
	if v.Status != model.ValidationStatusValid || v.StreamKey != "sk_real" || v.Error != nil {
		t.Fatalf("expected VALID status echoing input key, got %+v", v)
	}

	// Invalid key with a reason.
	rInvalid := commoR(&clientstest.FakeCommodore{
		ValidateStreamKeyFn: func(context.Context, string, ...string) (*commodorepb.ValidateStreamKeyResponse, error) {
			return &commodorepb.ValidateStreamKeyResponse{Valid: false, Error: "revoked"}, nil
		},
	})
	v, err = rInvalid.DoValidateStreamKey(ctx, "sk_bad")
	if err != nil {
		t.Fatal(err)
	}
	if v.Status != model.ValidationStatusInvalid || v.Error == nil || *v.Error != "revoked" {
		t.Fatalf("expected INVALID status with reason, got %+v", v)
	}

	// Backend error → ERROR status, never a Go error.
	rErr := commoR(&clientstest.FakeCommodore{
		ValidateStreamKeyFn: func(context.Context, string, ...string) (*commodorepb.ValidateStreamKeyResponse, error) {
			return nil, errors.New("commodore down")
		},
	})
	v, err = rErr.DoValidateStreamKey(ctx, "sk_x")
	if err != nil {
		t.Fatalf("validate must not surface a Go error: %v", err)
	}
	if v.Status != model.ValidationStatusError || v.Error == nil {
		t.Fatalf("expected ERROR status, got %+v", v)
	}
}

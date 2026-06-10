package resolvers

import (
	"context"
	"errors"
	"testing"

	"frameworks/api_gateway/graph/model"
	"frameworks/api_gateway/internal/clients/clientstest"
	foghorncontrolpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn_control"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// DoTestPlaybackAccess forwards exactly one of playbackId/internalName plus the
// viewer probe context, and maps the decision (incl. optional fields) back.
// Supplying neither or both is a local ValidationError.
func TestDoTestPlaybackAccess(t *testing.T) {
	var got *foghorncontrolpb.TestPlaybackAccessRequest
	c := &clientstest.FakeCommodore{
		TestPlaybackAccessFn: func(_ context.Context, req *foghorncontrolpb.TestPlaybackAccessRequest) (*foghorncontrolpb.TestPlaybackAccessResponse, error) {
			got = req
			return &foghorncontrolpb.TestPlaybackAccessResponse{
				Allowed:       true,
				PolicyType:    "JWT",
				Reason:        "ok",
				Kid:           "kid-1",
				WebhookStatus: 200,
			}, nil
		},
	}
	playback := "pb1"
	viewer := "eyJ..."
	fire := true
	res, err := commoW2(c).DoTestPlaybackAccess(clientstest.AuthedCtx("t1"), model.TestPlaybackAccessInput{
		PlaybackID:  &playback,
		ViewerToken: &viewer,
		FireWebhook: &fire,
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got.PlaybackId != "pb1" || got.InternalName != "" || got.ViewerToken != "eyJ..." || !got.FireWebhook {
		t.Fatalf("request built wrong: %+v", got)
	}
	dec, ok := res.(*model.PlaybackAccessDecision)
	if !ok || !dec.Allowed || dec.PolicyType != "JWT" {
		t.Fatalf("expected decision, got %T %+v", res, res)
	}
	if dec.Reason == nil || *dec.Reason != "ok" || dec.Kid == nil || *dec.Kid != "kid-1" {
		t.Fatalf("optional fields mapped wrong: %+v", dec)
	}
	if dec.WebhookStatus == nil || *dec.WebhookStatus != 200 {
		t.Fatalf("webhook status mapped wrong: %+v", dec.WebhookStatus)
	}

	// Neither identifier → ValidationError, no backend call.
	bad := &clientstest.FakeCommodore{}
	res, _ = commoW2(bad).DoTestPlaybackAccess(clientstest.AuthedCtx("t1"), model.TestPlaybackAccessInput{})
	if _, ok := res.(*model.ValidationError); !ok {
		t.Fatalf("expected ValidationError when neither set, got %T", res)
	}
	if bad.Calls != 0 {
		t.Fatalf("validation must not reach backend, Calls=%d", bad.Calls)
	}

	// Both identifiers → ValidationError.
	internal := "live+abc"
	res, _ = commoW2(&clientstest.FakeCommodore{}).DoTestPlaybackAccess(clientstest.AuthedCtx("t1"), model.TestPlaybackAccessInput{
		PlaybackID: &playback, InternalName: &internal,
	})
	if _, ok := res.(*model.ValidationError); !ok {
		t.Fatalf("expected ValidationError when both set, got %T", res)
	}

	denied := &clientstest.FakeCommodore{}
	if _, derr := commoW2(denied).DoTestPlaybackAccess(context.Background(), model.TestPlaybackAccessInput{PlaybackID: &playback}); derr == nil {
		t.Fatal("expected permission error")
	}
	if denied.Calls != 0 {
		t.Fatalf("guard must not reach backend, Calls=%d", denied.Calls)
	}

	// NotFound from Commodore maps to NotFoundError union member.
	nf := commoW2(&clientstest.FakeCommodore{
		TestPlaybackAccessFn: func(context.Context, *foghorncontrolpb.TestPlaybackAccessRequest) (*foghorncontrolpb.TestPlaybackAccessResponse, error) {
			return nil, status.Error(codes.NotFound, "missing")
		},
	})
	res, err = nf.DoTestPlaybackAccess(clientstest.AuthedCtx("t1"), model.TestPlaybackAccessInput{PlaybackID: &playback})
	if err != nil {
		t.Fatalf("NotFound should be a union member: %v", err)
	}
	if _, ok := res.(*model.NotFoundError); !ok {
		t.Fatalf("expected NotFoundError, got %T", res)
	}

	// Other RPC errors propagate.
	fail := commoW2(&clientstest.FakeCommodore{
		TestPlaybackAccessFn: func(context.Context, *foghorncontrolpb.TestPlaybackAccessRequest) (*foghorncontrolpb.TestPlaybackAccessResponse, error) {
			return nil, errors.New("down")
		},
	})
	if _, err := fail.DoTestPlaybackAccess(clientstest.AuthedCtx("t1"), model.TestPlaybackAccessInput{PlaybackID: &playback}); err == nil {
		t.Fatal("RPC error should propagate")
	}
}

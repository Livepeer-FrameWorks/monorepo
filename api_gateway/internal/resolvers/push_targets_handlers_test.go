package resolvers

import (
	"context"
	"errors"
	"testing"

	"frameworks/api_gateway/graph/model"
	"frameworks/api_gateway/internal/clients/clientstest"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/globalid"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
)

func commoW2(c *clientstest.FakeCommodore) *Resolver {
	return &Resolver{Clients: clientstest.Clients(clientstest.WithCommodore(c)), Logger: clientstest.DiscardLogger()}
}

// DoGetStreamPushTargets has no permission gate: it forwards the raw streamID
// and returns the proto list verbatim.
func TestDoGetStreamPushTargets(t *testing.T) {
	var gotStream string
	c := &clientstest.FakeCommodore{
		ListPushTargetsFn: func(_ context.Context, streamID string) (*commodorepb.ListPushTargetsResponse, error) {
			gotStream = streamID
			return &commodorepb.ListPushTargetsResponse{PushTargets: []*commodorepb.PushTarget{
				{Id: "pt1"}, {Id: "pt2"},
			}}, nil
		},
	}
	got, err := commoW2(c).DoGetStreamPushTargets(clientstest.AuthedCtx("t1"), "s9")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if gotStream != "s9" {
		t.Fatalf("streamID = %q, want s9", gotStream)
	}
	if len(got) != 2 || got[0].Id != "pt1" {
		t.Fatalf("unexpected targets: %+v", got)
	}

	fail := commoW2(&clientstest.FakeCommodore{
		ListPushTargetsFn: func(context.Context, string) (*commodorepb.ListPushTargetsResponse, error) {
			return nil, errors.New("down")
		},
	})
	if _, err := fail.DoGetStreamPushTargets(clientstest.AuthedCtx("t1"), "s9"); err == nil {
		t.Fatal("backend error should propagate")
	}
}

// DoCreatePushTarget maps the input (incl. optional platform) into the proto
// request and returns the created proto target. Platform is only set on the
// request when supplied.
func TestDoCreatePushTarget(t *testing.T) {
	var got *commodorepb.CreatePushTargetRequest
	c := &clientstest.FakeCommodore{
		CreatePushTargetFn: func(_ context.Context, req *commodorepb.CreatePushTargetRequest) (*commodorepb.PushTarget, error) {
			got = req
			return &commodorepb.PushTarget{Id: "pt-new", StreamId: req.StreamId, Name: req.Name}, nil
		},
	}
	platform := "twitch"
	out, err := commoW2(c).DoCreatePushTarget(clientstest.AuthedCtx("t1"), "s1", model.CreatePushTargetInput{
		Name:      "Backup",
		TargetURI: "rtmp://x/app/key",
		Platform:  &platform,
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got.StreamId != "s1" || got.Name != "Backup" || got.TargetUri != "rtmp://x/app/key" || got.Platform != "twitch" {
		t.Fatalf("request built wrong: %+v", got)
	}
	if out.Id != "pt-new" {
		t.Fatalf("unexpected output: %+v", out)
	}

	// Permission gate fires before any backend call.
	denied := &clientstest.FakeCommodore{}
	if _, err := commoW2(denied).DoCreatePushTarget(context.Background(), "s1", model.CreatePushTargetInput{Name: "n", TargetURI: "u"}); err == nil {
		t.Fatal("expected permission error")
	}
	if denied.Calls != 0 {
		t.Fatalf("guard must not reach backend, Calls=%d", denied.Calls)
	}

	// Backend error is wrapped.
	fail := commoW2(&clientstest.FakeCommodore{
		CreatePushTargetFn: func(context.Context, *commodorepb.CreatePushTargetRequest) (*commodorepb.PushTarget, error) {
			return nil, errors.New("boom")
		},
	})
	if _, err := fail.DoCreatePushTarget(clientstest.AuthedCtx("t1"), "s1", model.CreatePushTargetInput{Name: "n", TargetURI: "u"}); err == nil {
		t.Fatal("backend error should propagate")
	}
}

// DoUpdatePushTarget decodes the PushTarget global ID to a raw ID before
// building the request, and only sets the fields that were provided.
func TestDoUpdatePushTarget(t *testing.T) {
	gid := globalid.Encode(globalid.TypePushTarget, "raw-123")
	var got *commodorepb.UpdatePushTargetRequest
	c := &clientstest.FakeCommodore{
		UpdatePushTargetFn: func(_ context.Context, req *commodorepb.UpdatePushTargetRequest) (*commodorepb.PushTarget, error) {
			got = req
			return &commodorepb.PushTarget{Id: req.Id, StreamId: "s1"}, nil
		},
	}
	name := "Renamed"
	enabled := false
	out, err := commoW2(c).DoUpdatePushTarget(clientstest.AuthedCtx("t1"), gid, model.UpdatePushTargetInput{
		Name:      &name,
		IsEnabled: &enabled,
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got.Id != "raw-123" {
		t.Fatalf("ID not decoded to raw: %q", got.Id)
	}
	if got.Name == nil || *got.Name != "Renamed" {
		t.Fatalf("Name not forwarded: %+v", got.Name)
	}
	if got.IsEnabled == nil || *got.IsEnabled != false {
		t.Fatalf("IsEnabled not forwarded: %+v", got.IsEnabled)
	}
	if got.TargetUri != nil {
		t.Fatalf("TargetUri should stay unset when not provided")
	}
	if out.Id != "raw-123" {
		t.Fatalf("unexpected output: %+v", out)
	}

	// A non-PushTarget global ID is rejected before any backend call.
	bad := &clientstest.FakeCommodore{}
	if _, err := commoW2(bad).DoUpdatePushTarget(clientstest.AuthedCtx("t1"), globalid.Encode(globalid.TypeStream, "x"), model.UpdatePushTargetInput{Name: &name}); err == nil {
		t.Fatal("expected invalid push target ID error")
	}
	if bad.Calls != 0 {
		t.Fatalf("invalid ID must not reach backend, Calls=%d", bad.Calls)
	}

	denied := &clientstest.FakeCommodore{}
	if _, err := commoW2(denied).DoUpdatePushTarget(context.Background(), gid, model.UpdatePushTargetInput{Name: &name}); err == nil {
		t.Fatal("expected permission error")
	}
	if denied.Calls != 0 {
		t.Fatalf("guard must not reach backend, Calls=%d", denied.Calls)
	}
}

// DoDeletePushTarget decodes the global ID, returns a DeleteSuccess on the
// original (encoded) ID, and surfaces a not-found backend error as a plain
// error (not a typed union).
func TestDoDeletePushTarget(t *testing.T) {
	gid := globalid.Encode(globalid.TypePushTarget, "raw-9")
	var gotRaw string
	c := &clientstest.FakeCommodore{
		DeletePushTargetFn: func(_ context.Context, id string) (*commodorepb.DeletePushTargetResponse, error) {
			gotRaw = id
			return &commodorepb.DeletePushTargetResponse{Id: id}, nil
		},
	}
	res, err := commoW2(c).DoDeletePushTarget(clientstest.AuthedCtx("t1"), gid)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if gotRaw != "raw-9" {
		t.Fatalf("raw ID = %q, want raw-9", gotRaw)
	}
	if !res.Success || res.DeletedID != gid {
		t.Fatalf("DeleteSuccess should echo the encoded ID: %+v", res)
	}

	// "not found" backend error becomes a distinct error message.
	nf := commoW2(&clientstest.FakeCommodore{
		DeletePushTargetFn: func(context.Context, string) (*commodorepb.DeletePushTargetResponse, error) {
			return nil, errors.New("push target not found")
		},
	})
	if _, err := nf.DoDeletePushTarget(clientstest.AuthedCtx("t1"), gid); err == nil || err.Error() != "push target not found" {
		t.Fatalf("expected not-found error, got %v", err)
	}

	denied := &clientstest.FakeCommodore{}
	if _, err := commoW2(denied).DoDeletePushTarget(context.Background(), gid); err == nil {
		t.Fatal("expected permission error")
	}
	if denied.Calls != 0 {
		t.Fatalf("guard must not reach backend, Calls=%d", denied.Calls)
	}
}

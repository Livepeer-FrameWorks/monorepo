package resolvers

import (
	"context"
	"errors"
	"testing"

	"frameworks/api_gateway/graph/model"
	"frameworks/api_gateway/internal/clients/clientstest"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	commonpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/common"
)

func newResolver(commo *clientstest.FakeCommodore) *Resolver {
	return &Resolver{
		Clients: clientstest.Clients(clientstest.WithCommodore(commo)),
		Logger:  clientstest.DiscardLogger(),
	}
}

func TestDoGetStreams(t *testing.T) {
	commo := &clientstest.FakeCommodore{
		ListStreamsFn: func(context.Context, *commonpb.CursorPaginationRequest) (*commodorepb.ListStreamsResponse, error) {
			return &commodorepb.ListStreamsResponse{Streams: []*commodorepb.Stream{
				{StreamId: "s1"}, {StreamId: "s2"},
			}}, nil
		},
	}
	r := newResolver(commo)

	streams, err := r.DoGetStreams(clientstest.AuthedCtx("t1"))
	if err != nil {
		t.Fatalf("DoGetStreams err: %v", err)
	}
	if len(streams) != 2 || streams[0].StreamId != "s1" {
		t.Fatalf("unexpected streams: %+v", streams)
	}

	// Backend error is wrapped, not swallowed.
	failing := newResolver(&clientstest.FakeCommodore{
		ListStreamsFn: func(context.Context, *commonpb.CursorPaginationRequest) (*commodorepb.ListStreamsResponse, error) {
			return nil, errors.New("commodore down")
		},
	})
	if _, err := failing.DoGetStreams(clientstest.AuthedCtx("t1")); err == nil {
		t.Fatal("DoGetStreams should surface backend error")
	}
}

func TestDoGetStream(t *testing.T) {
	// No dataloader in context → resolver calls Commodore.GetStream directly.
	commo := &clientstest.FakeCommodore{
		GetStreamFn: func(_ context.Context, id string) (*commodorepb.Stream, error) {
			return &commodorepb.Stream{StreamId: id, Title: "T"}, nil
		},
	}
	r := newResolver(commo)
	got, err := r.DoGetStream(clientstest.AuthedCtx("t1"), "s9")
	if err != nil || got.StreamId != "s9" {
		t.Fatalf("DoGetStream = (%+v, %v)", got, err)
	}

	failing := newResolver(&clientstest.FakeCommodore{
		GetStreamFn: func(context.Context, string) (*commodorepb.Stream, error) { return nil, errors.New("nope") },
	})
	if _, err := failing.DoGetStream(clientstest.AuthedCtx("t1"), "x"); err == nil {
		t.Fatal("DoGetStream should surface backend error")
	}
}

// DoDeleteStream classifies a "not found" backend error into a typed
// NotFoundError union member rather than a hard failure — the API distinguishes
// "already gone" from a real error.
func TestDoDeleteStream(t *testing.T) {
	ctx := clientstest.AuthedCtx("t1")

	// Success → DeleteSuccess (Decklog is nil, so the trailing service event no-ops).
	r := newResolver(&clientstest.FakeCommodore{
		DeleteStreamFn: func(_ context.Context, id string) (*commodorepb.DeleteStreamResponse, error) {
			return &commodorepb.DeleteStreamResponse{StreamId: id}, nil
		},
	})
	res, err := r.DoDeleteStream(ctx, "s1")
	if err != nil {
		t.Fatal(err)
	}
	ok, isOK := res.(*model.DeleteSuccess)
	if !isOK || !ok.Success || ok.DeletedID != "s1" {
		t.Fatalf("expected DeleteSuccess for s1, got %T %+v", res, res)
	}

	// "not found" backend error → typed NotFoundError, no Go error.
	rNF := newResolver(&clientstest.FakeCommodore{
		DeleteStreamFn: func(context.Context, string) (*commodorepb.DeleteStreamResponse, error) {
			return nil, errors.New("stream not found")
		},
	})
	res, err = rNF.DoDeleteStream(ctx, "ghost")
	if err != nil {
		t.Fatalf("not-found should not be a Go error: %v", err)
	}
	nf, isNF := res.(*model.NotFoundError)
	if !isNF || nf.ResourceID != "ghost" || nf.ResourceType != "Stream" {
		t.Fatalf("expected NotFoundError for ghost, got %T %+v", res, res)
	}

	// Any other backend error → Go error.
	rErr := newResolver(&clientstest.FakeCommodore{
		DeleteStreamFn: func(context.Context, string) (*commodorepb.DeleteStreamResponse, error) {
			return nil, errors.New("permission denied")
		},
	})
	if _, err := rErr.DoDeleteStream(ctx, "s1"); err == nil {
		t.Fatal("non-not-found backend error should be a Go error")
	}
}

func TestDoRefreshStreamKey(t *testing.T) {
	// Refresh then refetch: both Commodore calls must fire, refetched stream returned.
	commo := &clientstest.FakeCommodore{
		RefreshKeyFn: func(_ context.Context, id string) (*commodorepb.RefreshStreamKeyResponse, error) {
			return &commodorepb.RefreshStreamKeyResponse{}, nil
		},
		GetStreamFn: func(_ context.Context, id string) (*commodorepb.Stream, error) {
			return &commodorepb.Stream{StreamId: id, StreamKey: "sk_new"}, nil
		},
	}
	r := newResolver(commo)
	got, err := r.DoRefreshStreamKey(clientstest.AuthedCtx("t1"), "s1")
	if err != nil {
		t.Fatal(err)
	}
	if got.StreamKey != "sk_new" {
		t.Fatalf("expected refreshed key, got %+v", got)
	}
	if commo.Calls != 2 {
		t.Fatalf("expected refresh + refetch = 2 calls, got %d", commo.Calls)
	}

	// Refresh failure surfaces and the stream is never refetched.
	rFail := newResolver(&clientstest.FakeCommodore{
		RefreshKeyFn: func(context.Context, string) (*commodorepb.RefreshStreamKeyResponse, error) {
			return nil, errors.New("refresh failed")
		},
	})
	if _, err := rFail.DoRefreshStreamKey(clientstest.AuthedCtx("t1"), "s1"); err == nil {
		t.Fatal("refresh failure should surface as error")
	}
}

func TestDoGetStreamKeys(t *testing.T) {
	commo := &clientstest.FakeCommodore{
		ListStreamKeysFn: func(_ context.Context, streamID string, _ *commonpb.CursorPaginationRequest) (*commodorepb.ListStreamKeysResponse, error) {
			return &commodorepb.ListStreamKeysResponse{StreamKeys: []*commodorepb.StreamKey{
				{Id: "k1", StreamId: streamID},
			}}, nil
		},
	}
	r := newResolver(commo)
	keys, err := r.DoGetStreamKeys(clientstest.AuthedCtx("t1"), "s1")
	if err != nil || len(keys) != 1 || keys[0].Id != "k1" {
		t.Fatalf("DoGetStreamKeys = (%+v, %v)", keys, err)
	}
}

func TestDoCreateStreamKey(t *testing.T) {
	var gotName string
	commo := &clientstest.FakeCommodore{
		CreateStreamKeyFn: func(_ context.Context, streamID, keyName string) (*commodorepb.StreamKeyResponse, error) {
			gotName = keyName
			return &commodorepb.StreamKeyResponse{StreamKey: &commodorepb.StreamKey{Id: "k9", StreamId: streamID, KeyName: keyName}}, nil
		},
	}
	r := newResolver(commo)
	key, err := r.DoCreateStreamKey(clientstest.AuthedCtx("t1"), "s1", model.CreateStreamKeyInput{Name: "ci-key"})
	if err != nil || key == nil || key.Id != "k9" {
		t.Fatalf("DoCreateStreamKey = (%+v, %v)", key, err)
	}
	if gotName != "ci-key" {
		t.Errorf("key name not forwarded: %q", gotName)
	}
}

func TestDoDeleteStreamKey(t *testing.T) {
	ctx := clientstest.AuthedCtx("t1")

	// Success.
	r := newResolver(&clientstest.FakeCommodore{
		DeactivateKeyFn: func(context.Context, string, string) error { return nil },
	})
	res, err := r.DoDeleteStreamKey(ctx, "s1", "k1")
	if err != nil {
		t.Fatal(err)
	}
	if ok, isOK := res.(*model.DeleteSuccess); !isOK || ok.DeletedID != "k1" {
		t.Fatalf("expected DeleteSuccess for k1, got %T %+v", res, res)
	}

	// "not found" → typed NotFoundError for the StreamKey resource.
	rNF := newResolver(&clientstest.FakeCommodore{
		DeactivateKeyFn: func(context.Context, string, string) error { return errors.New("key not found") },
	})
	res, err = rNF.DoDeleteStreamKey(ctx, "s1", "ghost")
	if err != nil {
		t.Fatalf("not-found should not be a Go error: %v", err)
	}
	if nf, isNF := res.(*model.NotFoundError); !isNF || nf.ResourceType != "StreamKey" || nf.ResourceID != "ghost" {
		t.Fatalf("expected StreamKey NotFoundError, got %T %+v", res, res)
	}
}

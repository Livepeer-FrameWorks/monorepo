package resolvers

import (
	"context"
	"errors"
	"strings"
	"testing"

	"frameworks/api_gateway/graph/model"
	"frameworks/api_gateway/internal/clients/clientstest"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	commonpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/common"
)

// resolverWith builds a Resolver around a fake Commodore, bypassing NewResolver
// (which requires SIGNALMAN_GRPC_ADDR and builds a live SubscriptionManager).
func resolverWith(fake *clientstest.FakeCommodore) *Resolver {
	return &Resolver{
		Clients: clientstest.Clients(clientstest.WithCommodore(fake)),
		Logger:  clientstest.DiscardLogger(),
	}
}

func TestDoGetStreams_RealPath(t *testing.T) {
	fake := &clientstest.FakeCommodore{
		ListStreamsFn: func(_ context.Context, _ *commonpb.CursorPaginationRequest) (*commodorepb.ListStreamsResponse, error) {
			return &commodorepb.ListStreamsResponse{Streams: []*commodorepb.Stream{{StreamId: "s1"}, {StreamId: "s2"}}}, nil
		},
	}
	r := resolverWith(fake)
	streams, err := r.DoGetStreams(clientstest.AuthedCtx("t1"))
	if err != nil {
		t.Fatal(err)
	}
	if len(streams) != 2 {
		t.Fatalf("got %d streams, want 2", len(streams))
	}
}

func TestDoGetStreams_WrapsError(t *testing.T) {
	sentinel := errors.New("commodore unavailable")
	fake := &clientstest.FakeCommodore{
		ListStreamsFn: func(context.Context, *commonpb.CursorPaginationRequest) (*commodorepb.ListStreamsResponse, error) {
			return nil, sentinel
		},
	}
	r := resolverWith(fake)
	_, err := r.DoGetStreams(clientstest.AuthedCtx("t1"))
	if !errors.Is(err, sentinel) {
		t.Fatalf("error should wrap sentinel, got %v", err)
	}
	if !strings.Contains(err.Error(), "failed to get streams") {
		t.Fatalf("error should be annotated, got %v", err)
	}
}

func TestDoGetStream_DirectClientWhenNoLoaders(t *testing.T) {
	// No loaders in context → resolver calls the client directly.
	fake := &clientstest.FakeCommodore{
		GetStreamFn: func(_ context.Context, id string) (*commodorepb.Stream, error) {
			return &commodorepb.Stream{StreamId: id}, nil
		},
	}
	r := resolverWith(fake)
	s, err := r.DoGetStream(clientstest.AuthedCtx("t1"), "s1")
	if err != nil || s.StreamId != "s1" {
		t.Fatalf("DoGetStream → (%v,%v)", s, err)
	}
}

func TestDoGetStream_WrapsError(t *testing.T) {
	sentinel := errors.New("boom")
	fake := &clientstest.FakeCommodore{
		GetStreamFn: func(context.Context, string) (*commodorepb.Stream, error) { return nil, sentinel },
	}
	r := resolverWith(fake)
	_, err := r.DoGetStream(clientstest.AuthedCtx("t1"), "s1")
	if !errors.Is(err, sentinel) || !strings.Contains(err.Error(), "failed to get stream") {
		t.Fatalf("want wrapped sentinel, got %v", err)
	}
}

func TestDoCreateStream_CreatesThenFetches(t *testing.T) {
	var created *commodorepb.CreateStreamRequest
	fake := &clientstest.FakeCommodore{
		CreateStreamFn: func(_ context.Context, req *commodorepb.CreateStreamRequest) (*commodorepb.CreateStreamResponse, error) {
			created = req
			return &commodorepb.CreateStreamResponse{Id: "new-id"}, nil
		},
		GetStreamFn: func(_ context.Context, id string) (*commodorepb.Stream, error) {
			return &commodorepb.Stream{StreamId: id, Title: "My Stream"}, nil
		},
	}
	r := resolverWith(fake)
	desc := "a desc"
	rec := true
	out, err := r.DoCreateStream(clientstest.AuthedCtx("t1"), model.CreateStreamInput{
		Name:        "My Stream",
		Description: &desc,
		Record:      &rec,
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.StreamId != "new-id" {
		t.Fatalf("returned stream id = %q, want new-id (fetched post-create)", out.StreamId)
	}
	// Optional fields are threaded into the create request.
	if created.Title != "My Stream" || created.Description != "a desc" || !created.IsRecording {
		t.Fatalf("create request not populated from input: %+v", created)
	}
}

func TestDoCreateStream_WrapsCreateError(t *testing.T) {
	sentinel := errors.New("insufficient balance")
	fake := &clientstest.FakeCommodore{
		CreateStreamFn: func(context.Context, *commodorepb.CreateStreamRequest) (*commodorepb.CreateStreamResponse, error) {
			return nil, sentinel
		},
	}
	r := resolverWith(fake)
	_, err := r.DoCreateStream(clientstest.AuthedCtx("t1"), model.CreateStreamInput{Name: "x"})
	if !errors.Is(err, sentinel) || !strings.Contains(err.Error(), "failed to create stream") {
		t.Fatalf("want wrapped create error, got %v", err)
	}
}

func TestDoDeleteStream_SuccessAndNotFoundMapping(t *testing.T) {
	// Success path.
	okFake := &clientstest.FakeCommodore{
		DeleteStreamFn: func(context.Context, string) (*commodorepb.DeleteStreamResponse, error) {
			return &commodorepb.DeleteStreamResponse{}, nil
		},
	}
	res, err := resolverWith(okFake).DoDeleteStream(clientstest.AuthedCtx("t1"), "s1")
	if err != nil {
		t.Fatal(err)
	}
	if succ, ok := res.(*model.DeleteSuccess); !ok || !succ.Success || succ.DeletedID != "s1" {
		t.Fatalf("expected DeleteSuccess{s1}, got %#v", res)
	}

	// "not found" client error is mapped to a typed NotFoundError union member,
	// not a Go error.
	nfFake := &clientstest.FakeCommodore{
		DeleteStreamFn: func(context.Context, string) (*commodorepb.DeleteStreamResponse, error) {
			return nil, errors.New("stream not found")
		},
	}
	res, err = resolverWith(nfFake).DoDeleteStream(clientstest.AuthedCtx("t1"), "s1")
	if err != nil {
		t.Fatalf("not-found should be a typed result, not an error: %v", err)
	}
	if nf, ok := res.(*model.NotFoundError); !ok || nf.ResourceID != "s1" {
		t.Fatalf("expected NotFoundError{s1}, got %#v", res)
	}

	// Other client errors propagate as Go errors.
	errFake := &clientstest.FakeCommodore{
		DeleteStreamFn: func(context.Context, string) (*commodorepb.DeleteStreamResponse, error) {
			return nil, errors.New("internal")
		},
	}
	if _, err := resolverWith(errFake).DoDeleteStream(clientstest.AuthedCtx("t1"), "s1"); err == nil {
		t.Fatal("expected generic error to propagate")
	}
}

func TestDoRefreshStreamKey_RotatesThenFetches(t *testing.T) {
	fake := &clientstest.FakeCommodore{
		RefreshKeyFn: func(context.Context, string) (*commodorepb.RefreshStreamKeyResponse, error) {
			return &commodorepb.RefreshStreamKeyResponse{}, nil
		},
		GetStreamFn: func(_ context.Context, id string) (*commodorepb.Stream, error) {
			return &commodorepb.Stream{StreamId: id, StreamKey: "sk_new"}, nil
		},
	}
	out, err := resolverWith(fake).DoRefreshStreamKey(clientstest.AuthedCtx("t1"), "s1")
	if err != nil {
		t.Fatal(err)
	}
	if out.StreamKey != "sk_new" {
		t.Fatalf("expected refetched key, got %q", out.StreamKey)
	}
}

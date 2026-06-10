package resolvers

import (
	"context"
	"errors"
	"testing"

	"frameworks/api_gateway/internal/clients/clientstest"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
)

func TestDoStreamRecentPullSourceEvents(t *testing.T) {
	// Nil stream → nil, nil (no backend call).
	got, err := commoB3(&clientstest.FakeCommodore{}).DoStreamRecentPullSourceEvents(clientstest.AuthedCtx("t1"), nil, nil)
	if got != nil || err != nil {
		t.Fatalf("nil stream = (%v, %v)", got, err)
	}

	// Push stream → nil, nil (events only meaningful for pull streams).
	pushCommo := &clientstest.FakeCommodore{}
	got, err = commoB3(pushCommo).DoStreamRecentPullSourceEvents(
		clientstest.AuthedCtx("t1"),
		&commodorepb.Stream{StreamId: "s1", IngestMode: "push"}, nil)
	if got != nil || err != nil {
		t.Fatalf("push stream = (%v, %v)", got, err)
	}
	if pushCommo.Calls != 0 {
		t.Fatalf("push stream hit backend %d times", pushCommo.Calls)
	}

	// Pull stream, custom limit → forwards stream ID + limit, returns events.
	var gotReq *commodorepb.ListPullSourceEventsRequest
	commo := &clientstest.FakeCommodore{
		ListPullSourceEventsFn: func(_ context.Context, req *commodorepb.ListPullSourceEventsRequest) (*commodorepb.ListPullSourceEventsResponse, error) {
			gotReq = req
			return &commodorepb.ListPullSourceEventsResponse{Events: []*commodorepb.PullSourceEvent{
				{Id: "e1", EventKind: "connect"},
			}}, nil
		},
	}
	limit := 5
	events, err := commoB3(commo).DoStreamRecentPullSourceEvents(
		clientstest.AuthedCtx("t1"),
		&commodorepb.Stream{StreamId: "s9", IngestMode: "pull"}, &limit)
	if err != nil {
		t.Fatalf("pull stream err: %v", err)
	}
	if len(events) != 1 || events[0].Id != "e1" {
		t.Fatalf("events = %+v", events)
	}
	if gotReq.StreamId != "s9" || gotReq.Limit != 5 {
		t.Fatalf("req = %+v", gotReq)
	}

	// Pull stream, nil limit → default 50.
	commo2 := &clientstest.FakeCommodore{
		ListPullSourceEventsFn: func(_ context.Context, req *commodorepb.ListPullSourceEventsRequest) (*commodorepb.ListPullSourceEventsResponse, error) {
			gotReq = req
			return &commodorepb.ListPullSourceEventsResponse{}, nil
		},
	}
	if _, err := commoB3(commo2).DoStreamRecentPullSourceEvents(
		clientstest.AuthedCtx("t1"),
		&commodorepb.Stream{StreamId: "s9", IngestMode: "pull"}, nil); err != nil {
		t.Fatalf("default-limit err: %v", err)
	}
	if gotReq.Limit != 50 {
		t.Fatalf("default limit = %d", gotReq.Limit)
	}

	// Backend error is wrapped.
	failing := commoB3(&clientstest.FakeCommodore{
		ListPullSourceEventsFn: func(context.Context, *commodorepb.ListPullSourceEventsRequest) (*commodorepb.ListPullSourceEventsResponse, error) {
			return nil, errors.New("commodore down")
		},
	})
	if _, err := failing.DoStreamRecentPullSourceEvents(
		clientstest.AuthedCtx("t1"),
		&commodorepb.Stream{StreamId: "s9", IngestMode: "pull"}, nil); err == nil {
		t.Fatal("backend error should surface")
	}
}

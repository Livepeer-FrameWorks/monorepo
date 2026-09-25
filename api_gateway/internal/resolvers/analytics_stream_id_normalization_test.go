package resolvers

import (
	"context"
	"testing"

	"frameworks/api_gateway/internal/clients/clientstest"

	periscope "github.com/Livepeer-FrameWorks/monorepo/pkg/clients/periscope"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/globalid"
	periscopepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/periscope"
)

// Stream.id is a Relay global ID; Periscope keys every stream-scoped query on
// the raw UUID. Each resolver must decode before forwarding, or ClickHouse
// rejects the value.
func TestStreamScopedAnalyticsResolversForwardRawStreamID(t *testing.T) {
	const raw = "b2213481-eeb8-4902-9f7f-21d4947e7dc6"
	relay := globalid.Encode(globalid.TypeStream, raw)
	ctx := clientstest.AuthedCtx("t1")
	noCache := true

	var got []string
	record := func(id *string) {
		if id == nil {
			got = append(got, "<nil>")
			return
		}
		got = append(got, *id)
	}
	p := &clientstest.FakePeriscope{
		GetTrackListEventsFn: func(_ context.Context, _ string, streamID string, _ *periscope.TimeRangeOpts, _ *periscope.CursorPaginationOpts) (*periscopepb.GetTrackListEventsResponse, error) {
			record(&streamID)
			return &periscopepb.GetTrackListEventsResponse{}, nil
		},
		GetRoutingEfficiencyFn: func(_ context.Context, _ string, streamID *string, _ *periscope.TimeRangeOpts) (*periscopepb.GetRoutingEfficiencyResponse, error) {
			record(streamID)
			return &periscopepb.GetRoutingEfficiencyResponse{}, nil
		},
		GetStreamHealthSummaryFn: func(_ context.Context, _ string, streamID *string, _ *periscope.TimeRangeOpts) (*periscopepb.GetStreamHealthSummaryResponse, error) {
			record(streamID)
			return &periscopepb.GetStreamHealthSummaryResponse{}, nil
		},
		GetClientQoeSummaryFn: func(_ context.Context, _ string, streamID *string, _ *periscope.TimeRangeOpts) (*periscopepb.GetClientQoeSummaryResponse, error) {
			record(streamID)
			return &periscopepb.GetClientQoeSummaryResponse{}, nil
		},
	}
	r := periO(p)

	first := 1
	if _, err := r.DoGetTrackListEventsConnection(ctx, relay, nil, &first, nil, nil, nil, &noCache); err != nil {
		t.Fatalf("trackList: %v", err)
	}
	if _, err := r.DoGetRoutingEfficiency(ctx, &relay, nil, &noCache); err != nil {
		t.Fatalf("routingEfficiency: %v", err)
	}
	if _, err := r.DoGetStreamHealthSummary(ctx, &relay, nil, &noCache); err != nil {
		t.Fatalf("streamHealthSummary: %v", err)
	}
	if _, err := r.DoGetClientQoeSummary(ctx, &relay, nil, &noCache); err != nil {
		t.Fatalf("clientQoeSummary: %v", err)
	}

	if len(got) != 4 {
		t.Fatalf("expected 4 Periscope calls, got %d (%v)", len(got), got)
	}
	for i, id := range got {
		if id != raw {
			t.Fatalf("call %d forwarded %q, want raw UUID %q", i, id, raw)
		}
	}

	// A global ID of another type is rejected before reaching Periscope.
	clip := globalid.Encode(globalid.TypeClip, raw)
	if _, err := r.DoGetRoutingEfficiency(ctx, &clip, nil, &noCache); err == nil {
		t.Fatal("clip global ID accepted as a stream ID")
	}
}

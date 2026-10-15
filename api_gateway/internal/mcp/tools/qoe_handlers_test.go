package tools

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"frameworks/api_gateway/internal/clients/clientstest"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/clients/periscope"
	periscopepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/periscope"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// bufferEdge is one buffer edge of node at unix second at, shaped like
// Periscope's rebuffering transitions: a DRY after FULL or RECOVER starts a
// rebuffer, a RECOVER or EMPTY after DRY ends it, on separate edges.
func bufferEdge(node string, at int64, prev, state string) *periscopepb.RebufferingEvent {
	ts := timestamppb.New(time.Unix(at, 0))
	evt := &periscopepb.RebufferingEvent{Timestamp: ts, NodeId: node, PrevState: prev, BufferState: state}
	if state == "DRY" && (prev == "FULL" || prev == "RECOVER") {
		evt.RebufferStart = ts
	}
	if prev == "DRY" && (state == "RECOVER" || state == "EMPTY") {
		evt.RebufferEnd = ts
	}
	return evt
}

// rebuffer is one rebuffer of node: its start edge at start and its end edge
// at end.
func rebuffer(node string, start, end int64) []*periscopepb.RebufferingEvent {
	return []*periscopepb.RebufferingEvent{bufferEdge(node, start, "FULL", "DRY"), bufferEdge(node, end, "DRY", "RECOVER")}
}

// ----- diagnose_rebuffering -----

func TestHandleDiagnoseRebuffering_Thresholds(t *testing.T) {
	mk := func(events []*periscopepb.RebufferingEvent) *clientstest.FakePeriscope {
		return &clientstest.FakePeriscope{
			GetRebufferingEventsFn: func(_ context.Context, tenantID string, _ *string, _ *string, _ *periscope.TimeRangeOpts, _ *periscope.CursorPaginationOpts) (*periscopepb.GetRebufferingEventsResponse, error) {
				if tenantID != "t1" {
					t.Errorf("tenant not forwarded: %q", tenantID)
				}
				return &periscopepb.GetRebufferingEventsResponse{Events: events}, nil
			},
		}
	}

	// No events → healthy with the "no rebuffering" analysis.
	res, out, err := handleDiagnoseRebuffering(clientstest.AuthedCtx("t1"),
		DiagnoseRebufferingInput{StreamID: "s1"}, clientstest.Clients(clientstest.WithPeriscope(mk(nil))), clientstest.DiscardLogger())
	if err != nil || res.IsError {
		t.Fatalf("empty case should succeed: err=%v text=%s", err, extractToolText(res))
	}
	if dr := out.(DiagnosticResult); dr.Status != "healthy" || dr.Metrics["rebuffer_count"].(int) != 0 {
		t.Fatalf("empty case: unexpected result %+v", dr)
	}

	// >20 events → critical with remediation recommendations.
	var many []*periscopepb.RebufferingEvent
	for i := range int64(21) {
		many = append(many, rebuffer("n1", i*10, i*10+1)...)
	}
	_, out, _ = handleDiagnoseRebuffering(clientstest.AuthedCtx("t1"),
		DiagnoseRebufferingInput{StreamID: "s1"}, clientstest.Clients(clientstest.WithPeriscope(mk(many))), clientstest.DiscardLogger())
	if dr := out.(DiagnosticResult); dr.Status != "critical" || len(dr.Recommendations) == 0 {
		t.Fatalf("21 events should be critical with recommendations: %+v", dr)
	}

	// A few short events → healthy "minor rebuffering".
	var few []*periscopepb.RebufferingEvent
	for _, at := range []int64{0, 10, 20} {
		few = append(few, rebuffer("n1", at, at+1)...)
	}
	_, out, _ = handleDiagnoseRebuffering(clientstest.AuthedCtx("t1"),
		DiagnoseRebufferingInput{StreamID: "s1"}, clientstest.Clients(clientstest.WithPeriscope(mk(few))), clientstest.DiscardLogger())
	if dr := out.(DiagnosticResult); dr.Status != "healthy" || dr.Metrics["rebuffer_count"].(int) != 3 {
		t.Fatalf("3 short events should be healthy: %+v", dr)
	}
}

// A rebuffer is a start edge and a later end edge of the same node; edges that
// neither start nor end one are not rebuffers, and a rebuffer still open at the
// end of the range has no duration yet.
func TestHandleDiagnoseRebuffering_PairsStartAndEndEdgesPerNode(t *testing.T) {
	events := []*periscopepb.RebufferingEvent{
		bufferEdge("n1", 100, "", "FULL"),
		bufferEdge("n1", 110, "FULL", "DRY"),
		bufferEdge("n2", 111, "", "FULL"),
		bufferEdge("n2", 112, "FULL", "DRY"),
		bufferEdge("n1", 112, "DRY", "RECOVER"),
		bufferEdge("n1", 120, "RECOVER", "DRY"),
		bufferEdge("n2", 113, "DRY", "EMPTY"),
		bufferEdge("n1", 124, "DRY", "EMPTY"),
		bufferEdge("n1", 130, "EMPTY", "FULL"),
		bufferEdge("n1", 140, "FULL", "DRY"),
	}
	slices.Reverse(events)
	sc := clientstest.Clients(clientstest.WithPeriscope(&clientstest.FakePeriscope{
		GetRebufferingEventsFn: func(context.Context, string, *string, *string, *periscope.TimeRangeOpts, *periscope.CursorPaginationOpts) (*periscopepb.GetRebufferingEventsResponse, error) {
			return &periscopepb.GetRebufferingEventsResponse{Events: events}, nil
		},
	}))
	_, out, err := handleDiagnoseRebuffering(clientstest.AuthedCtx("t1"), DiagnoseRebufferingInput{StreamID: "s1"}, sc, clientstest.DiscardLogger())
	if err != nil {
		t.Fatal(err)
	}
	m := out.(DiagnosticResult).Metrics
	// n1: 110-112 (2 s), 120-124 (4 s), 140 open; n2: 112-113 (1 s).
	if m["rebuffer_count"] != 4 || m["open_rebuffer_count"] != 1 || m["total_rebuffer_time_ms"] != int64(7000) || m["avg_rebuffer_duration_ms"] != int64(2333) {
		t.Fatalf("metrics = %v, want 4 rebuffers (1 open), 7000 ms total, 2333 ms average", m)
	}
}

func TestHandleDiagnoseRebuffering_Guards(t *testing.T) {
	peri := &clientstest.FakePeriscope{} // unstubbed → panics if reached
	sc := clientstest.Clients(clientstest.WithPeriscope(peri))

	// Missing tenant.
	res, _, err := handleDiagnoseRebuffering(context.Background(), DiagnoseRebufferingInput{StreamID: "s1"}, sc, clientstest.DiscardLogger())
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Fatal("missing tenant should be a tool error")
	}
	// Missing stream_id.
	res, _, _ = handleDiagnoseRebuffering(clientstest.AuthedCtx("t1"), DiagnoseRebufferingInput{StreamID: ""}, sc, clientstest.DiscardLogger())
	if !res.IsError {
		t.Fatal("missing stream_id should be a tool error")
	}
	if peri.Calls != 0 {
		t.Fatalf("backend consulted before validation passed: %d calls", peri.Calls)
	}

	// Backend error surfaces as a tool error.
	failing := clientstest.Clients(clientstest.WithPeriscope(&clientstest.FakePeriscope{
		GetRebufferingEventsFn: func(context.Context, string, *string, *string, *periscope.TimeRangeOpts, *periscope.CursorPaginationOpts) (*periscopepb.GetRebufferingEventsResponse, error) {
			return nil, errors.New("periscope down")
		},
	}))
	res, _, err = handleDiagnoseRebuffering(clientstest.AuthedCtx("t1"), DiagnoseRebufferingInput{StreamID: "s1"}, failing, clientstest.DiscardLogger())
	if err != nil {
		t.Fatalf("backend failure should be a tool-error result, not a Go error: %v", err)
	}
	if !res.IsError {
		t.Fatal("backend error should surface as a tool error")
	}
}

// ----- diagnose_routing -----

func strp(s string) *string   { return &s }
func f64p(f float64) *float64 { return &f }

func TestHandleDiagnoseRouting(t *testing.T) {
	// No events → no_data.
	noData := clientstest.Clients(clientstest.WithPeriscope(&clientstest.FakePeriscope{
		GetRoutingEventsFn: func(context.Context, string, *string, *periscope.TimeRangeOpts, *periscope.CursorPaginationOpts, []string, *string, *string) (*periscopepb.GetRoutingEventsResponse, error) {
			return &periscopepb.GetRoutingEventsResponse{}, nil
		},
	}))
	_, out, err := handleDiagnoseRouting(clientstest.AuthedCtx("t1"), DiagnoseRoutingInput{StreamID: "s1"}, noData, clientstest.DiscardLogger())
	if err != nil {
		t.Fatal(err)
	}
	if dr := out.(DiagnosticResult); dr.Status != "no_data" {
		t.Fatalf("no events should be no_data: %+v", dr)
	}

	// >10% failed routings → critical.
	events := make([]*periscopepb.RoutingEvent, 0, 10)
	for i := 0; i < 8; i++ {
		events = append(events, &periscopepb.RoutingEvent{SelectedNode: "node-a", ClientCountry: strp("US"), Status: "ok", RoutingDistance: f64p(100)})
	}
	events = append(events,
		&periscopepb.RoutingEvent{SelectedNode: "node-b", Status: "failed"},
		&periscopepb.RoutingEvent{SelectedNode: "node-b", Status: "error"},
	)
	crit := clientstest.Clients(clientstest.WithPeriscope(&clientstest.FakePeriscope{
		GetRoutingEventsFn: func(context.Context, string, *string, *periscope.TimeRangeOpts, *periscope.CursorPaginationOpts, []string, *string, *string) (*periscopepb.GetRoutingEventsResponse, error) {
			return &periscopepb.GetRoutingEventsResponse{Events: events}, nil
		},
	}))
	_, out, _ = handleDiagnoseRouting(clientstest.AuthedCtx("t1"), DiagnoseRoutingInput{StreamID: "s1"}, crit, clientstest.DiscardLogger())
	dr := out.(DiagnosticResult)
	if dr.Status != "critical" || dr.Metrics["failed_routings"].(int) != 2 {
		t.Fatalf("20%% failure should be critical: %+v", dr)
	}
}

// ----- diagnose_packet_loss -----

func f32p(f float32) *float32 { return &f }

func TestHandleDiagnosePacketLoss(t *testing.T) {
	// No loss samples → no_data (records exist but PacketLossRate all nil).
	noSamples := clientstest.Clients(clientstest.WithPeriscope(&clientstest.FakePeriscope{
		GetClientMetrics5mFn: func(context.Context, string, *string, *string, *periscope.TimeRangeOpts, *periscope.CursorPaginationOpts) (*periscopepb.GetClientMetrics5MResponse, error) {
			return &periscopepb.GetClientMetrics5MResponse{Records: []*periscopepb.ClientMetrics5M{{AvgBandwidthOut: 1000}}}, nil
		},
	}))
	_, out, err := handleDiagnosePacketLoss(clientstest.AuthedCtx("t1"), DiagnosePacketLossInput{StreamID: "s1"}, noSamples, clientstest.DiscardLogger())
	if err != nil {
		t.Fatal(err)
	}
	if dr := out.(DiagnosticResult); dr.Status != "no_data" {
		t.Fatalf("no loss samples should be no_data: %+v", dr)
	}

	// Realtime protocol (WebRTC) with measurable loss → protocol classified,
	// status comes from the tested packetLossStatus, analysis names the protocol.
	sc := clientstest.Clients(clientstest.WithPeriscope(&clientstest.FakePeriscope{
		GetClientMetrics5mFn: func(context.Context, string, *string, *string, *periscope.TimeRangeOpts, *periscope.CursorPaginationOpts) (*periscopepb.GetClientMetrics5MResponse, error) {
			return &periscopepb.GetClientMetrics5MResponse{Records: []*periscopepb.ClientMetrics5M{
				{AvgBandwidthOut: 2000, PacketLossRate: f32p(0.02)},
				{AvgBandwidthOut: 3000, PacketLossRate: f32p(0.04)},
			}}, nil
		},
		GetStreamEventsFn: func(context.Context, string, string, *periscope.TimeRangeOpts, *periscope.CursorPaginationOpts) (*periscopepb.GetStreamEventsResponse, error) {
			return &periscopepb.GetStreamEventsResponse{Events: []*periscopepb.StreamEvent{{Protocol: strp("WebRTC")}}}, nil
		},
	}))
	_, out, err = handleDiagnosePacketLoss(clientstest.AuthedCtx("t1"), DiagnosePacketLossInput{StreamID: "s1"}, sc, clientstest.DiscardLogger())
	if err != nil {
		t.Fatal(err)
	}
	dr := out.(DiagnosticResult)
	if dr.Metrics["protocol_type"].(string) != protocolTypeRealtime {
		t.Fatalf("WebRTC should classify as realtime: %+v", dr.Metrics)
	}
	if dr.Status == "" || dr.Status == "no_data" {
		t.Fatalf("loss present should yield a health status: %+v", dr)
	}
	if dr.Metrics["loss_sample_count"].(int) != 2 {
		t.Fatalf("expected 2 loss samples: %+v", dr.Metrics)
	}
}

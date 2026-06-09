package tools

import (
	"context"
	"errors"
	"testing"
	"time"

	"frameworks/api_gateway/internal/clients/clientstest"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/clients/periscope"
	periscopepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/periscope"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func rebufferEvent(startUnix, endUnix int64) *periscopepb.RebufferingEvent {
	return &periscopepb.RebufferingEvent{
		RebufferStart: timestamppb.New(time.Unix(startUnix, 0)),
		RebufferEnd:   timestamppb.New(time.Unix(endUnix, 0)),
	}
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
	many := make([]*periscopepb.RebufferingEvent, 21)
	for i := range many {
		many[i] = rebufferEvent(0, 0)
	}
	_, out, _ = handleDiagnoseRebuffering(clientstest.AuthedCtx("t1"),
		DiagnoseRebufferingInput{StreamID: "s1"}, clientstest.Clients(clientstest.WithPeriscope(mk(many))), clientstest.DiscardLogger())
	if dr := out.(DiagnosticResult); dr.Status != "critical" || len(dr.Recommendations) == 0 {
		t.Fatalf("21 events should be critical with recommendations: %+v", dr)
	}

	// A few short events → healthy "minor rebuffering".
	few := []*periscopepb.RebufferingEvent{rebufferEvent(0, 0), rebufferEvent(10, 10), rebufferEvent(20, 20)}
	_, out, _ = handleDiagnoseRebuffering(clientstest.AuthedCtx("t1"),
		DiagnoseRebufferingInput{StreamID: "s1"}, clientstest.Clients(clientstest.WithPeriscope(mk(few))), clientstest.DiscardLogger())
	if dr := out.(DiagnosticResult); dr.Status != "healthy" || dr.Metrics["rebuffer_count"].(int) != 3 {
		t.Fatalf("3 short events should be healthy: %+v", dr)
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

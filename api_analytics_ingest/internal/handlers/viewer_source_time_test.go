package handlers

import (
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/kafka"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	"github.com/google/uuid"
)

func TestViewerLifecycleUsesSourceTimeDespiteReversedArrival(t *testing.T) {
	conn := newFakeClickhouseConn()
	handler := NewAnalyticsHandler(conn, logging.NewLogger(), nil)
	tenant, stream := uuid.NewString(), uuid.NewString()
	node := "source-edge"
	second := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	arrival := second.Add(time.Hour)
	var observed []time.Time
	for _, tc := range []struct {
		kind string
		at   time.Time
	}{
		{"viewer_connect", second.Add(800 * time.Millisecond)},
		{"viewer_disconnect", second.Add(100 * time.Millisecond)},
	} {
		trigger := &ipcpb.MistTrigger{StreamId: &stream, NodeId: "source-edge", TriggerUnixMillis: tc.at.UnixMilli()}
		if tc.kind == "viewer_connect" {
			trigger.TriggerPayload = &ipcpb.MistTrigger_ViewerConnect{ViewerConnect: &ipcpb.ViewerConnectTrigger{StreamName: "live+source-time", SessionId: "resumed", Connector: "hls"}}
		} else {
			trigger.TriggerPayload = &ipcpb.MistTrigger_ViewerDisconnect{ViewerDisconnect: &ipcpb.ViewerDisconnectTrigger{StreamName: "live+source-time", SessionId: "resumed", Connector: "hls", NodeId: &node}}
		}
		event := kafka.AnalyticsEvent{EventID: uuid.NewString(), EventType: tc.kind, Timestamp: arrival, TenantID: tenant, Data: mustMistTriggerData(t, trigger)}
		if err := handler.HandleAnalyticsEvent(event); err != nil {
			t.Fatal(err)
		}
		row := conn.batches["viewer_connection_events"].rows[0]
		at, ok := row[1].(time.Time)
		if !ok || !at.Equal(tc.at) {
			t.Fatalf("%s replaced source milliseconds with arrival time: got=%v want=%s", tc.kind, row[1], tc.at)
		}
		if row[2] != uuid.MustParse(tenant) || row[3] != uuid.MustParse(stream) || row[6] != "resumed" || row[30] != tc.at.UnixMilli() {
			t.Fatalf("source timestamp changed attribution or lost precision: row=%#v", row)
		}
		observed = append(observed, at)
		arrival = arrival.Add(time.Second)
	}
	if !observed[0].After(observed[1]) {
		t.Fatalf("arrival order overrode source reconnect ordering: %v", observed)
	}
}

func TestViewerLifecycleWithoutSourceHeaderKeepsPublishedTime(t *testing.T) {
	for _, millis := range []int64{0, -1} {
		conn := newFakeClickhouseConn()
		handler := NewAnalyticsHandler(conn, logging.NewLogger(), nil)
		stream := uuid.NewString()
		published := time.Date(2026, 11, 9, 12, 0, 0, 321000000, time.UTC)
		trigger := &ipcpb.MistTrigger{StreamId: &stream, TriggerUnixMillis: millis, TriggerPayload: &ipcpb.MistTrigger_ViewerConnect{ViewerConnect: &ipcpb.ViewerConnectTrigger{StreamName: "live+fallback", SessionId: "legacy", Connector: "hls"}}}
		if err := handler.HandleAnalyticsEvent(kafka.AnalyticsEvent{EventID: uuid.NewString(), EventType: "viewer_connect", Timestamp: published, TenantID: uuid.NewString(), Data: mustMistTriggerData(t, trigger)}); err != nil {
			t.Fatal(err)
		}
		row := conn.batches["viewer_connection_events"].rows[0]
		if at, ok := row[1].(time.Time); !ok || !at.Equal(published) || row[30] != published.UnixMilli() {
			t.Fatalf("missing/invalid source header lost published timestamp: row=%#v", row)
		}
	}
}

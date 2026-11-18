package grpc

import (
	"context"
	"errors"
	"testing"
	"time"

	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	"google.golang.org/protobuf/proto"
)

func TestSendEventPreservesSourceTimestamp(t *testing.T) {
	for _, tc := range []struct {
		name      string
		timestamp int64
		want      time.Time
	}{
		{"milliseconds", 1794967349882, time.Date(2026, 11, 18, 2, 2, 29, 882000000, time.UTC)},
		{"seconds", 1794967349, time.Date(2026, 11, 18, 2, 2, 29, 0, time.UTC)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			producer := &fakeProducer{}
			server := newTestServer(producer)
			trigger := &ipcpb.MistTrigger{
				TriggerType: "STREAM_END", RequestId: "source-time-end", Timestamp: tc.timestamp,
				TenantId: proto.String("aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"),
				TriggerPayload: &ipcpb.MistTrigger_StreamEnd{
					StreamEnd: &ipcpb.StreamEndTrigger{StreamName: "live+source-time"},
				},
			}
			if _, err := server.SendEvent(context.Background(), trigger); err != nil {
				t.Fatal(err)
			}
			if len(producer.publishCalls) != 1 || len(producer.produceCalls) != 1 {
				t.Fatalf("typed/raw publishes = %d/%d, want 1/1", len(producer.publishCalls), len(producer.produceCalls))
			}
			if got := producer.publishCalls[0].Timestamp; !got.Equal(tc.want) {
				t.Errorf("typed event time = %s, want source observation %s", got, tc.want)
			}
			var raw ipcpb.MistTrigger
			if err := proto.Unmarshal(producer.produceCalls[0].value, &raw); err != nil {
				t.Fatal(err)
			}
			if !proto.Equal(trigger, &raw) {
				t.Fatal("raw journal changed the original trigger")
			}
		})
	}
}

func TestSendEventMissingSourceTimestampUsesReceiptTime(t *testing.T) {
	for _, tc := range []struct {
		name      string
		timestamp int64
	}{
		{"missing", 0},
		{"negative", -1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			producer := &fakeProducer{}
			server := newTestServer(producer)
			trigger := &ipcpb.MistTrigger{
				TriggerType: "STREAM_END", RequestId: "missing-source-time", Timestamp: tc.timestamp,
				TenantId: proto.String("aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"),
				TriggerPayload: &ipcpb.MistTrigger_StreamEnd{
					StreamEnd: &ipcpb.StreamEndTrigger{StreamName: "live+source-time"},
				},
			}
			before := time.Now()
			if _, err := server.SendEvent(context.Background(), trigger); err != nil {
				t.Fatal(err)
			}
			after := time.Now()
			if len(producer.publishCalls) != 1 {
				t.Fatalf("typed publishes = %d, want 1", len(producer.publishCalls))
			}
			if got := producer.publishCalls[0].Timestamp; got.Before(before) || got.After(after) {
				t.Fatalf("missing source time fell outside receipt interval: %s", got)
			}
		})
	}
}

func TestSendEventRawJournalRetryPreservesSourceTimeAndIdentity(t *testing.T) {
	journalFailure := errors.New("raw journal unavailable")
	producer := &fakeProducer{produceErr: journalFailure}
	server := newTestServer(producer)
	const eventID = "11111111-1111-4111-8111-111111111111"
	trigger := &ipcpb.MistTrigger{
		TriggerType: "STREAM_END", RequestId: "source-time-retry", EventId: eventID,
		Timestamp: 1794967349882, TenantId: proto.String("aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"),
		TriggerPayload: &ipcpb.MistTrigger_StreamEnd{
			StreamEnd: &ipcpb.StreamEndTrigger{StreamName: "live+source-time"},
		},
	}
	if _, err := server.SendEvent(context.Background(), trigger); !errors.Is(err, journalFailure) {
		t.Fatalf("first SendEvent error = %v, want raw journal failure", err)
	}
	producer.produceErr = nil
	if _, err := server.SendEvent(context.Background(), trigger); err != nil {
		t.Fatalf("retry SendEvent: %v", err)
	}
	if len(producer.publishCalls) != 2 || len(producer.produceCalls) != 2 {
		t.Fatalf("typed/raw attempts = %d/%d, want 2/2", len(producer.publishCalls), len(producer.produceCalls))
	}
	want := time.Date(2026, 11, 18, 2, 2, 29, 882000000, time.UTC)
	for i, event := range producer.publishCalls {
		if event.EventID != eventID || !event.Timestamp.Equal(want) {
			t.Errorf("attempt %d: event identity/time = %s/%s, want %s/%s", i, event.EventID, event.Timestamp, eventID, want)
		}
		var raw ipcpb.MistTrigger
		if err := proto.Unmarshal(producer.produceCalls[i].value, &raw); err != nil {
			t.Fatal(err)
		}
		if !proto.Equal(trigger, &raw) {
			t.Fatalf("attempt %d changed the raw trigger", i)
		}
	}
}

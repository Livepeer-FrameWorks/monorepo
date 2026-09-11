package mist

import (
	"net/http"
	"strings"
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
)

func TestConnectionPlayTrigger(t *testing.T) {
	headers := http.Header{"X-Trigger-Uuid": []string{"connection-event"}}
	trigger, err := ParseTriggerToProtobufWithHeaders(TriggerConnPlay, []byte("live+stream\n192.0.2.1\nDTSC\ndtsc://source/live+stream"), headers, "source-node", logging.NewLogger())
	if err != nil {
		t.Fatal(err)
	}
	play := trigger.GetConnectionPlay()
	if !trigger.Blocking || trigger.NodeId != "source-node" || trigger.TriggerUuid != "connection-event" || play == nil || play.Host != "192.0.2.1" || play.Connector != "DTSC" || play.StreamName != "live+stream" || play.RequestUrl != "dtsc://source/live+stream" {
		t.Fatalf("connection context was not preserved: %v", trigger)
	}
	if IsDurableTriggerType(string(TriggerConnPlay)) {
		t.Fatal("source admission must not enter the durable event WAL")
	}
	for _, body := range []string{"", "stream\nhost\nDTSC", "stream\n\nDTSC\nurl", "stream\nhost\nDTSC\nurl\nextra", "stream\nhost\nDTSC\nurl\x00", "stream\nhost\nDTSC\n" + strings.Repeat("a", 16<<10)} {
		if _, err := ParseTriggerToProtobuf(TriggerConnPlay, []byte(body), "node", logging.NewLogger()); err == nil {
			t.Fatalf("accepted malformed payload of length %d", len(body))
		}
	}
}

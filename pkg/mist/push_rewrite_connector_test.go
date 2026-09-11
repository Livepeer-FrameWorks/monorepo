package mist

import (
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
)

func TestParsePushRewriteCarriesObservedConnector(t *testing.T) {
	logger := logging.NewLogger()
	four, err := ParseTriggerToProtobuf(TriggerPushRewrite, []byte("rtmp://in/app/key\n203.0.113.4\nlive+stream_id\nTSSRT\n"), "node-1", logger)
	if err != nil || four.GetPushRewrite().GetObservedConnector() != "TSSRT" || four.GetPushRewrite().GetPushUrl() != "rtmp://in/app/key" {
		t.Fatalf("four-line payload lost the connector: %v %v", four, err)
	}
	three, err := ParseTriggerToProtobuf(TriggerPushRewrite, []byte("rtmp://in/app/key\n203.0.113.4\nlive+stream_id\n"), "node-1", logger)
	if err != nil || three.GetPushRewrite().GetObservedConnector() != "" {
		t.Fatalf("legacy three-line payload invented a connector: %v %v", three, err)
	}
	for _, bad := range []string{"RTMP\x00", string(make([]byte, 65))} {
		if _, err := ParseTriggerToProtobuf(TriggerPushRewrite, []byte("rtmp://in/app/key\n203.0.113.4\nlive+stream_id\n"+bad+"\n"), "node-1", logger); err == nil {
			t.Fatalf("invalid connector %q accepted", bad)
		}
	}
}

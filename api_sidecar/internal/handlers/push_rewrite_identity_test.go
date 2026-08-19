package handlers

import (
	"net/http"
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/mist"
)

// The PUSH_REWRITE handler must carry MistServer's OWN publisher-connection identity
// (X-PID / X-Trigger-UUID / X-Trigger-UnixMillis headers), not receive-time synthetic
// values — that PID is what Foghorn correlates against the connector's PUSH_INPUT_CLOSE.
func TestCapturePushRewriteIdentity_UsesRealMistHeaders(t *testing.T) {
	h := http.Header{}
	h.Set("X-PID", "48123")
	h.Set("X-Trigger-UUID", "mist-tuuid-abc")
	h.Set("X-Trigger-UnixMillis", "1699999999123")

	trigger, err := mist.ParseTriggerToProtobufWithHeaders(mist.TriggerPushRewrite, []byte("rtmp://example/live/s1\nhost\nlive+s1"), h, "node-1", logging.NewLogger())
	if err != nil {
		t.Fatal(err)
	}
	pr := trigger.GetPushRewrite()

	if pr.GetPid() != 48123 {
		t.Fatalf("Pid = %d, want the X-PID header value 48123", pr.GetPid())
	}
	if pr.GetTriggerUuid() != "mist-tuuid-abc" {
		t.Fatalf("TriggerUuid = %q, want the X-Trigger-UUID header value", pr.GetTriggerUuid())
	}
	if pr.GetTriggerUnixMillis() != 1699999999123 {
		t.Fatalf("TriggerUnixMillis = %d, want the X-Trigger-UnixMillis header value", pr.GetTriggerUnixMillis())
	}
}

// Missing/blank headers (older Helmsman path or a non-HTTP connector) leave the
// identity zero — the caller falls back to source-node binding, never a synthetic PID.
func TestCapturePushRewriteIdentity_MissingHeadersLeaveZero(t *testing.T) {
	trigger, err := mist.ParseTriggerToProtobufWithHeaders(mist.TriggerPushRewrite, []byte("rtmp://example/live/s1\nhost\nlive+s1"), http.Header{}, "node-1", logging.NewLogger())
	if err != nil {
		t.Fatal(err)
	}
	pr := trigger.GetPushRewrite()
	if pr.GetPid() != 0 || pr.GetTriggerUuid() != "" || pr.GetTriggerUnixMillis() != 0 {
		t.Fatalf("missing headers must leave identity zero, got pid=%d uuid=%q millis=%d", pr.GetPid(), pr.GetTriggerUuid(), pr.GetTriggerUnixMillis())
	}
}

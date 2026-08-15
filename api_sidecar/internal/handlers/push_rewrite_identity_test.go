package handlers

import (
	"net/http"
	"testing"

	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

// The PUSH_REWRITE handler must carry MistServer's OWN publisher-connection identity
// (X-PID / X-Trigger-UUID / X-Trigger-UnixMillis headers), not receive-time synthetic
// values — that PID is what Foghorn correlates against the connector's PUSH_INPUT_CLOSE.
func TestCapturePushRewriteIdentity_UsesRealMistHeaders(t *testing.T) {
	h := http.Header{}
	h.Set("X-PID", "48123")
	h.Set("X-Trigger-UUID", "mist-tuuid-abc")
	h.Set("X-Trigger-UnixMillis", "1699999999123")

	pr := &ipcpb.PushRewriteTrigger{StreamName: "live+s1"}
	capturePushRewriteIdentity(pr, h)

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
	pr := &ipcpb.PushRewriteTrigger{StreamName: "live+s1"}
	capturePushRewriteIdentity(pr, http.Header{})
	if pr.GetPid() != 0 || pr.GetTriggerUuid() != "" || pr.GetTriggerUnixMillis() != 0 {
		t.Fatalf("missing headers must leave identity zero, got pid=%d uuid=%q millis=%d", pr.GetPid(), pr.GetTriggerUuid(), pr.GetTriggerUnixMillis())
	}
	// Nil trigger must not panic.
	capturePushRewriteIdentity(nil, http.Header{})
}

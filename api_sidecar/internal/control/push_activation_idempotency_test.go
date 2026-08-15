package control

import (
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/mist"
)

// Activation can be re-dispatched (Foghorn re-runs its once-only admission effects when a crash
// preceded the durable effects marker), and PushStart is not idempotent — the filter is what makes
// the re-dispatch safe: targets that already have a live Mist push for this stream are skipped, so
// a retry never starts a duplicate writer, while other streams' pushes never mask a first start.
func TestLivePushTargetURIs_FiltersOnlyThisStreamsLivePushes(t *testing.T) {
	pushes := []mist.PushInfo{
		{StreamName: "live+stream-a", TargetURI: "rtmp://x/one"},
		{StreamName: "live+stream-a", TargetURI: "rtmp://x/two"},
		{StreamName: "live+stream-b", TargetURI: "rtmp://x/other-stream"},
		{StreamName: "live+stream-a", TargetURI: ""}, // malformed entry must not blanket-match
	}

	live := livePushTargetURIs(pushes, "live+stream-a")

	if !live["rtmp://x/one"] || !live["rtmp://x/two"] {
		t.Fatalf("this stream's live targets must be filtered: %v", live)
	}
	if live["rtmp://x/other-stream"] {
		t.Fatal("another stream's push must not suppress this stream's activation")
	}
	if live[""] {
		t.Fatal("an empty target URI must never match (it would suppress unrelated targets)")
	}
	if len(livePushTargetURIs(nil, "live+stream-a")) != 0 {
		t.Fatal("no live pushes → nothing suppressed (first activation starts everything)")
	}
}

package mediaauthority

import (
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/streamident"
)

func TestPhysicalRuntimeAuthorityKey(t *testing.T) {
	for _, role := range []streamident.RuntimeRole{streamident.RuntimePublisher, streamident.RuntimeRelay} {
		runtime, err := streamident.BoundRuntimeName("stream", role, "84ed1d31-13d2-468d-b7b5-ef132d4e3ee9")
		if err != nil {
			t.Fatal(err)
		}
		if got := stripRuntimePrefix(runtime); got != "stream" {
			t.Fatalf("physical runtime lost its signed authority: got key %q", got)
		}
	}
	if got := stripRuntimePrefix("processing+stream"); got != "processing+stream" {
		t.Fatalf("processing input resolved through its own output: %q", got)
	}
}

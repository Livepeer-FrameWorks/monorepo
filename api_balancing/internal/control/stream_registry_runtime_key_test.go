package control

import (
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/streamident"
)

func TestRuntimeSourceLookupUsesLogicalRegistryKey(t *testing.T) {
	for _, role := range []streamident.RuntimeRole{streamident.RuntimePublisher, streamident.RuntimeRelay} {
		runtime, err := streamident.BoundRuntimeName("stream", role, "84ed1d31-13d2-468d-b7b5-ef132d4e3ee9")
		if err != nil {
			t.Fatal(err)
		}
		t.Run(runtime, func(t *testing.T) {
			registry := NewStreamRegistry(nil, "cell", time.Minute)
			pull, err := registry.RecordInboundPull(t.Context(), "stream", InboundPull{
				TenantID: "tenant", SourceClusterID: "origin", SourceNodeID: "publisher", DestNodeID: "edge", DTSCURL: "dtsc://origin/source",
			})
			if err != nil {
				t.Fatal(err)
			}
			location, err := registry.CurrentSourceLocation(t.Context(), runtime)
			if err != nil || location.InboundPulls["edge"].AttemptID != pull.AttemptID {
				t.Fatalf("physical runtime lost the logical source binding: location=%+v error=%v", location, err)
			}
		})
	}
}

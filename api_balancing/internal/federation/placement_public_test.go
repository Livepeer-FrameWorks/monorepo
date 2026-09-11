package federation

import (
	"testing"
)

func TestConfigureLivePublicPlacementSharesTheDestinationGate(t *testing.T) {
	destination, deps, _, _, _ := bootstrapFixture(t)
	if _, err := ConfigureLivePublicPlacement(destination); err == nil {
		t.Fatal("unconfigured destination produced public resolvers")
	}
	if err := ConfigureLivePlacementDestination(destination, deps); err != nil {
		t.Fatal(err)
	}
	runtime, err := ConfigureLivePublicPlacement(destination)
	if err != nil {
		t.Fatal(err)
	}
	gate := destination.Runtime.(*PolicyBoundPlacementRuntime).Policy
	// Authority and fence readers are func values in this fixture, so their
	// identity cannot be compared; presence plus the shared gate/source
	// pointers prove the resolvers were assembled from the destination.
	if runtime.Gate != gate || runtime.Source != destination.Discovery.Paths ||
		runtime.Ingest.Authority == nil || runtime.Ingest.Fence == nil || runtime.Ingest.Router.Observe == nil || runtime.Ingest.Router.Prepare == nil ||
		runtime.Viewer.Authority == nil || runtime.Viewer.Source != runtime.Source || runtime.Viewer.Router.Observe == nil || runtime.Viewer.Router.Prepare == nil {
		t.Fatalf("public runtime split from destination gate: %+v", runtime)
	}
	for _, mismatch := range []string{"nil", "gate-cell", "gate-authority", "gate-fence", "media"} {
		t.Run(mismatch, func(t *testing.T) {
			destination, deps, _, _, _ := bootstrapFixture(t)
			if err := ConfigureLivePlacementDestination(destination, deps); err != nil {
				t.Fatal(err)
			}
			policy := destination.Runtime.(*PolicyBoundPlacementRuntime)
			switch mismatch {
			case "nil":
				destination = nil
			case "gate-cell":
				policy.Policy.CellID = "other"
			case "gate-authority":
				policy.Policy.Authority = nil
			case "gate-fence":
				policy.Policy.IngestFence = nil
			case "media":
				policy.Media = nil
			}
			if _, err := ConfigureLivePublicPlacement(destination); err == nil {
				t.Fatal("mismatched destination produced public resolvers")
			}
		})
	}
}

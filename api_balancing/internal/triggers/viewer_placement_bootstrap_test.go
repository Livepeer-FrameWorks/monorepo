package triggers

import (
	"context"
	"errors"
	"testing"

	"frameworks/api_balancing/internal/balancer"
	"frameworks/api_balancing/internal/federation"
	localauthority "frameworks/api_balancing/internal/mediaauthority"
)

type placementOnlyReader struct{}

func (placementOnlyReader) Placement(context.Context, string, string, string) (localauthority.PlacementPair, error) {
	return localauthority.PlacementPair{}, nil
}

func TestConfigureLiveViewerPlacementAdmissionUsesPublicRuntime(t *testing.T) {
	for _, missing := range []string{"none", "runtime", "gate", "source", "gate-cell", "source-cell", "authority", "internal-name-lookup", "router", "already-installed"} {
		t.Run(missing, func(t *testing.T) {
			processor := newTestProcessor(t)
			observe := func(context.Context, balancer.PlacementCell, balancer.PlacementRouteRequest) (balancer.PlacementCellObservation, error) {
				return balancer.PlacementCellObservation{}, errors.New("unused")
			}
			gate := &federation.PlacementPolicyGate{CellID: "cell", Authority: viewerPlacementPairReader{}, Router: balancer.PlacementRouter{Observe: observe}}
			runtime := &federation.LivePublicPlacementRuntime{Gate: gate, Source: &federation.MediaPlacementPaths{Push: &federation.LivePushPlacementPaths{CellID: "cell"}}}
			switch missing {
			case "runtime":
				runtime = nil
			case "gate":
				runtime.Gate = nil
			case "source":
				runtime.Source = nil
			case "gate-cell":
				gate.CellID = ""
			case "source-cell":
				runtime.Source = &federation.MediaPlacementPaths{Push: &federation.LivePushPlacementPaths{CellID: "other"}}
			case "authority":
				gate.Authority = nil
			case "internal-name-lookup":
				gate.Authority = placementOnlyReader{}
			case "router":
				gate.Router.Observe = nil
			case "already-installed":
				processor.SetViewerPlacementAdmission(func(context.Context, ViewerPlacementConnection) (federation.PlacementAdmissionDecision, error) {
					return federation.PlacementAdmissionDecision{}, errors.New("existing")
				})
			}
			err := processor.ConfigureLiveViewerPlacementAdmission(runtime, nil, nil)
			if missing == "none" {
				if err != nil || !processor.viewerPlacementRequired || processor.viewerPlacementAdmission == nil {
					t.Fatalf("startup did not install viewer admission: %v", err)
				}
				return
			}
			if err == nil || (missing != "already-installed" && (processor.viewerPlacementRequired || processor.viewerPlacementAdmission != nil)) {
				t.Fatalf("incomplete startup installed viewer admission: %v", err)
			}
		})
	}
}

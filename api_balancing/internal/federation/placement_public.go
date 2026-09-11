package federation

import (
	"errors"
)

// LivePublicPlacementRuntime is the public-facing half of a configured
// destination: the front-door resolvers and the final-admission gate all share
// the destination's authority reader, ownership fence and discovery router, so
// a viewer or publisher is prepared and later admitted from the same facts.
type LivePublicPlacementRuntime struct {
	Gate   *PlacementPolicyGate
	Source *MediaPlacementPaths
	Ingest *IngestPlacementResolver
	Viewer *ViewerPlacementResolver
}

// ConfigureLivePublicPlacement assembles the public resolvers from a destination
// that ConfigureLivePlacementDestination already completed. It performs no I/O
// and refuses partial or mismatched runtimes rather than returning a resolver
// bound to a different cell, registry or router than the destination itself.
func ConfigureLivePublicPlacement(destination *PlacementDestination) (*LivePublicPlacementRuntime, error) {
	if destination == nil || destination.Discovery == nil || destination.Discovery.CellID == "" || destination.Discovery.Authority == nil {
		return nil, errors.New("public placement requires a configured destination discovery")
	}
	discovery := destination.Discovery
	policy, ok := destination.Runtime.(*PolicyBoundPlacementRuntime)
	if !ok || policy == nil || policy.Policy == nil {
		return nil, errors.New("public placement requires the destination policy runtime")
	}
	gate := policy.Policy
	// Authority readers may be func values, so identity is not comparable; the
	// destination bootstrap is the only constructor of this gate and binds it to
	// the same reader, which the cell and fence checks below re-verify.
	if gate.CellID != discovery.CellID || gate.Authority == nil || gate.IngestFence == nil ||
		gate.Router.Observe == nil || gate.Router.Prepare == nil {
		return nil, errors.New("public placement requires a cell-bound policy gate with ownership fence and router")
	}
	media, ok := policy.Media.(*PlacementMediaRuntime)
	if !ok || media == nil {
		return nil, errors.New("public placement requires the destination media runtime")
	}
	serve, ok := media.Serve.(*MediaServePreparationRuntime)
	if !ok || serve == nil || serve.Paths == nil || serve.Paths != discovery.Paths || serve.Push == nil || serve.Push.Paths != serve.Paths.Push {
		return nil, errors.New("public placement requires media preparation bound to destination paths")
	}
	paths := serve.Paths
	cellID, err := paths.CellID()
	if err != nil || cellID != discovery.CellID || paths.Push.Registry == nil {
		return nil, errors.New("public placement requires cell-bound media source paths")
	}
	viewer := &ViewerPlacementResolver{Authority: gate.Authority, Source: paths, Router: gate.Router}
	// Stored media resolves by internal name. The same authority reader supplies
	// it when it can; otherwise artifact requests fail closed rather than being
	// served from an unenforced path.
	if byName, ok := gate.Authority.(ViewerPlacementNameReader); ok && byName != nil {
		viewer.StoredMedia = byName
	}
	return &LivePublicPlacementRuntime{
		Gate:   gate,
		Source: paths,
		Ingest: &IngestPlacementResolver{Authority: gate.Authority, Fence: gate.IngestFence, Router: gate.Router},
		Viewer: viewer,
	}, nil
}

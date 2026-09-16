package federation

import (
	"errors"

	"frameworks/api_balancing/internal/control"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	goredis "github.com/redis/go-redis/v9"
)

type LivePlacementDependencies struct {
	Redis       goredis.UniversalClient
	Registry    *control.StreamRegistry
	Arrange     *ArrangeOriginPullDeps
	IngestFence PlacementIngestFenceReader
	Client      *FederationClient
	CellAddress func(string) string
	// Logger receives placement observation failures and refused evaluations.
	Logger logging.Logger
}

// ConfigureLivePlacementDestination assembles the destination before listeners
// start. It performs no network work and does not promote authority readiness or
// switch public admission paths. Every request still requires ready signed policy.
func ConfigureLivePlacementDestination(destination *PlacementDestination, deps LivePlacementDependencies) error {
	if destination == nil || destination.Discovery == nil {
		return errors.New("live placement discovery is required")
	}
	discovery := destination.Discovery
	paths, ok := discovery.Paths.(*MediaPlacementPaths)
	if discovery.CellID == "" || discovery.Authority == nil || discovery.Inventory == nil || discovery.Snapshot == nil || !ok || paths == nil {
		return errors.New("live placement discovery dependencies are incomplete")
	}
	cellID, err := paths.CellID()
	if err != nil || cellID != discovery.CellID || paths.Push.Registry == nil || paths.Push.Snapshot == nil {
		return errors.New("live placement discovery dependencies are incomplete")
	}
	if deps.Redis == nil || deps.Registry == nil || deps.Arrange == nil || deps.Arrange.Registry != deps.Registry || deps.Arrange.Cache == nil || deps.Arrange.LocalSource == nil || deps.IngestFence == nil {
		return errors.New("live placement preparation dependencies are incomplete")
	}
	if deps.Arrange.LocalSource.sourceControlCellID() != discovery.CellID {
		return errors.New("live placement source and discovery control cells differ")
	}
	if destination.Runtime != nil || destination.Receipts != nil || destination.RetainPrepared != nil {
		return errors.New("live placement destination is already configured")
	}
	transport := PlacementTransport{LocalCellID: discovery.CellID, Local: destination, Client: deps.Client, CellAddress: deps.CellAddress,
		Observations: NewPlacementObservationCache(destination.Now), Logger: deps.Logger}
	gate := &PlacementPolicyGate{CellID: discovery.CellID, Authority: discovery.Authority,
		IngestFence: deps.IngestFence, Router: transport.Router(), Now: destination.Now}
	push := &LivePushPreparationRuntime{Authority: discovery.Authority, Paths: paths.Push, Registry: deps.Registry, Arrange: deps.Arrange, Now: destination.Now}
	serve := &MediaServePreparationRuntime{CellID: discovery.CellID, Authority: discovery.Authority, Paths: paths,
		Push: push, Registry: deps.Registry, Arrange: deps.Arrange, Snapshot: discovery.Snapshot, Now: destination.Now}
	media := &PlacementMediaRuntime{
		Ingest: &LiveIngestPreparationRuntime{CellID: discovery.CellID, Authority: discovery.Authority, Snapshot: discovery.Snapshot, Now: destination.Now},
		Serve:  serve,
	}
	destination.Receipts = &PlacementReceiptStore{Client: deps.Redis, CellID: discovery.CellID, Now: destination.Now}
	destination.Runtime = &PolicyBoundPlacementRuntime{Policy: gate, Media: media}
	// Push and configured relays retain the viewer context needed to renew
	// permission for an existing physical pull. Local configured inputs and
	// stored media have no pull, so this hook is a no-op for them.
	destination.RetainPrepared = push.RetainPreparedSourceDemand
	return nil
}

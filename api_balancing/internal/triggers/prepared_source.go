package triggers

import (
	"context"
	"errors"
	"strings"
	"time"

	"frameworks/api_balancing/internal/control"
	"frameworks/api_balancing/internal/federation"
	localauthority "frameworks/api_balancing/internal/mediaauthority"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/mist"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/placement"
)

type PreparedSourceConnection struct {
	TenantID, InternalName, ClusterID, NodeID string
}

type PreparedSourceAdmission func(context.Context, PreparedSourceConnection) (federation.PreparedPlacementSource, error)

// SetPreparedSourceAdmission requires placement evidence for tracked source pulls
// before processing starts. Clearing the adapter cannot restore unbound access.
func (p *Processor) SetPreparedSourceAdmission(admission PreparedSourceAdmission) {
	p.preparedSourceRequired = true
	p.preparedSourceAdmission = admission
}

// ConfigureLivePreparedSourceAdmission shares the destination's receipt-backed
// source runtime. Every source pull requires completed placement evidence.
func (p *Processor) ConfigureLivePreparedSourceAdmission(destination *federation.PlacementDestination) error {
	if p == nil || destination == nil || destination.Discovery == nil || destination.Receipts == nil || p.preparedSourceAdmission != nil {
		return errors.New("prepared source startup dependencies are unavailable or already configured")
	}
	if destination.Discovery.CellID == "" || destination.Receipts.CellID != destination.Discovery.CellID || destination.Receipts.Client == nil {
		return errors.New("prepared source requires cell-bound shared receipts")
	}
	policy, ok := destination.Runtime.(*federation.PolicyBoundPlacementRuntime)
	if !ok || policy == nil || policy.Policy == nil {
		return errors.New("prepared source requires the destination policy runtime")
	}
	media, ok := policy.Media.(*federation.PlacementMediaRuntime)
	if !ok || media == nil {
		return errors.New("prepared source requires the destination media runtime")
	}
	serve := destination.MediaServePreparation()
	push := destination.PushPreparation()
	if serve == nil || push == nil || push.Authority == nil || serve.Registry == nil || push.Paths == nil || serve.Arrange == nil ||
		push.Paths.CellID != destination.Discovery.CellID || push.Paths.Registry != serve.Registry || serve.Arrange.Registry != serve.Registry {
		return errors.New("prepared source requires live serving preparation")
	}
	authority, ok := destination.Discovery.Authority.(ViewerPlacementAuthorityReader)
	if !ok || authority == nil {
		return errors.New("prepared source requires tenant-scoped authority lookup")
	}
	adapter := &MediaSourcePlacementAdapter{Authority: authority, Push: push, Serve: serve, Receipts: destination.Receipts, Destination: destination}
	p.preparedSourceRegistry = serve.Registry
	p.preparedSourceAdmission = adapter.ResolveSource
	return nil
}

func (p *Processor) checkPreparedSource(ctx context.Context, streamName, nodeID string, pull control.InboundPull) error {
	if p.preparedSourceAdmission == nil || pull.TenantID == "" || pull.DestClusterID == "" || pull.DestNodeID != nodeID {
		return errors.New("prepared source admission is unavailable")
	}
	result, err := p.preparedSourceAdmission(ctx, PreparedSourceConnection{
		TenantID: pull.TenantID, InternalName: mist.ExtractInternalName(streamName), ClusterID: pull.DestClusterID, NodeID: nodeID,
	})
	if err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	// Fresh preparation can issue a new receipt during this call. Its lifetime
	// is bounded at consumption, not against the start of an older lookup.
	now := time.Now()
	if result.DTSCURL != pull.DTSCURL || result.AttemptID != pull.AttemptID || !now.Before(result.ExpiresAt) || result.ExpiresAt.After(now.Add(placement.PreparationLifetime)) {
		return errors.New("prepared source result is expired or differs from the current pull")
	}
	return nil
}

// MediaSourcePlacementAdapter resolves signed object identity locally, then
// dispatches completed source admission by signed ingest mode.
type MediaSourcePlacementAdapter struct {
	Authority   ViewerPlacementAuthorityReader
	Push        *federation.LivePushPreparationRuntime
	Serve       *federation.MediaServePreparationRuntime
	Receipts    *federation.PlacementReceiptStore
	Destination *federation.PlacementDestination
}

func (adapter *MediaSourcePlacementAdapter) ResolveSource(ctx context.Context, connection PreparedSourceConnection) (federation.PreparedPlacementSource, error) {
	if adapter == nil || adapter.Authority == nil || adapter.Push == nil || adapter.Receipts == nil {
		return federation.PreparedPlacementSource{}, errors.New("media source placement adapter is unavailable")
	}
	for _, id := range []string{connection.TenantID, connection.InternalName, connection.ClusterID, connection.NodeID} {
		if id == "" || strings.TrimSpace(id) != id {
			return federation.PreparedPlacementSource{}, errors.New("push source placement identity is invalid")
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	fence, ok := control.LocalSourceConnectionFence(connection.NodeID, connection.ClusterID)
	if !ok {
		return federation.PreparedPlacementSource{}, errors.New("authenticated source connection is unavailable")
	}
	readCtx, stopRead := context.WithTimeout(ctx, localauthority.PlacementReadTimeout)
	pair, err := adapter.Authority.PlacementForInternalName(readCtx, connection.TenantID, connection.InternalName)
	readErr := readCtx.Err()
	stopRead()
	if readErr != nil {
		return federation.PreparedPlacementSource{}, readErr
	}
	if err != nil {
		return federation.PreparedPlacementSource{}, err
	}
	if err = ctx.Err(); err != nil {
		return federation.PreparedPlacementSource{}, err
	}
	identity := federation.PlacementSourceIdentity{
		TenantID: connection.TenantID, ObjectID: pair.Object.AuthorityID, InternalName: connection.InternalName,
		ClusterID: connection.ClusterID, NodeID: connection.NodeID,
		DestinationFence: fence,
	}
	var result federation.PreparedPlacementSource
	if adapter.Destination != nil && adapter.Serve != nil {
		result, err = adapter.Destination.ResolveOrReauthorizeMediaSource(ctx, adapter.Serve, identity)
	} else if adapter.Destination != nil {
		result, err = adapter.Destination.ResolveOrReauthorizeSource(ctx, adapter.Push, identity)
	} else {
		result, err = adapter.Push.ResolvePreparedSource(ctx, adapter.Receipts, identity)
	}
	if current, currentOK := control.LocalSourceConnectionFence(connection.NodeID, connection.ClusterID); !currentOK || current != fence {
		return federation.PreparedPlacementSource{}, errors.New("source connection changed during admission")
	}
	return result, err
}

package federation

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"frameworks/api_balancing/internal/control"
	"frameworks/api_balancing/internal/state"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	foghornfederationpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn_federation"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	"github.com/google/uuid"
)

// defaultArrangeDeps holds a process-wide set of dependencies for
// arrangement callsites that aren't naturally wired to PeerManager /
// FederationClient / RemoteEdgeCache (e.g., the trigger processor
// arranging DVR cross-cluster pulls). main.go sets this at startup.
var (
	defaultArrangeDepsMu sync.RWMutex
	defaultArrangeDeps   *ArrangeOriginPullDeps
)

// SetDefaultArrangeDeps installs the process-wide arrangement deps.
// Called once from main.go after PeerManager / FederationClient /
// RemoteEdgeCache are constructed.
func SetDefaultArrangeDeps(d *ArrangeOriginPullDeps) {
	defaultArrangeDepsMu.Lock()
	defaultArrangeDeps = d
	defaultArrangeDepsMu.Unlock()
}

// DefaultArrange is the package-level entry point for callsites that
// don't construct their own deps (trigger processor for DVR cross-
// cluster federation). Returns ErrOriginPullDepsMissing when no deps
// have been set — caller should fall back to its non-federated path.
func DefaultArrange(ctx context.Context, req ArrangeOriginPullRequest) (*ArrangeOriginPullResult, error) {
	defaultArrangeDepsMu.RLock()
	d := defaultArrangeDeps
	defaultArrangeDepsMu.RUnlock()
	if d == nil {
		return nil, ErrOriginPullDepsMissing
	}
	return d.ArrangeOriginPull(ctx, req)
}

// Errors returned by ArrangeOriginPull. Every error is a refuse — callers
// must fail closed (no untracked pulls) and surface an offline/empty
// response to the requesting Mist or HTTP client.
var (
	ErrOriginPullDepsMissing      = errors.New("origin-pull dependencies unavailable")
	ErrOriginPullRegistryNil      = errors.New("origin-pull stream registry unavailable")
	ErrOriginPullLockContention   = errors.New("origin-pull lock contention")
	ErrOriginPullLoop             = errors.New("origin-pull replication loop prevented")
	ErrOriginPullNoDest           = errors.New("origin-pull destination unidentified")
	ErrOriginPullPeerUnreachable  = errors.New("origin-pull peer address unknown")
	ErrOriginPullNotifyFailed     = errors.New("origin-pull NotifyOriginPull rejected")
	ErrOriginPullStateUnavailable = errors.New("origin-pull coordination state unavailable")
	ErrOriginPullSourceBinding    = errors.New("origin-pull source binding is inconsistent")
)

// IsArrangeInfraError classifies an ArrangeOriginPull error as an
// infrastructure failure (registry unavailable, deps missing, peer
// unreachable, notify rejected by source cluster) versus a capacity /
// soft refusal (no destination, lock contention, replication loop).
//
// Callers of /play use this to split: viewer requests get redirected to
// the peer cluster on soft refusals (LB-miss, contention) but surface as
// a 5xx on infra failures so operators see the underlying break instead
// of a silently degraded redirect path.
func IsArrangeInfraError(err error) bool {
	switch {
	case err == nil:
		return false
	case errors.Is(err, ErrOriginPullDepsMissing),
		errors.Is(err, context.Canceled),
		errors.Is(err, context.DeadlineExceeded),
		errors.Is(err, ErrOriginPullSourceBinding),
		errors.Is(err, ErrOriginPullRegistryNil),
		errors.Is(err, ErrOriginPullStateUnavailable),
		errors.Is(err, ErrOriginPullPeerUnreachable),
		errors.Is(err, ErrOriginPullNotifyFailed):
		return true
	}
	return false
}

// OriginPullPeerResolver is the minimum peer-lookup surface ArrangeOriginPull
// needs. *PeerManager satisfies it.
type OriginPullPeerResolver interface {
	GetPeerAddr(clusterID string) string
}

// OriginPullFederationClient is the minimum federation-client surface
// ArrangeOriginPull needs. *FederationClient satisfies it.
type OriginPullFederationClient interface {
	NotifyOriginPull(ctx context.Context, peerClusterID, peerAddr string, req *foghornfederationpb.OriginPullNotification) (*foghornfederationpb.OriginPullAck, error)
}

// OriginPullLBPicker selects a local edge to become the puller when the
// caller doesn't already know its destination node (gRPC viewer-routing
// path). Implementations return the chosen node's public BaseURL host
// plus its registered NodeID and ClusterID. /source-style callers can pass nil because
// they identify themselves and supply DestNodeID directly.
type OriginPullLBPicker func(ctx context.Context, lat, lon float64, tenantID string) (host, nodeID, clusterID string, err error)

// ArrangeOriginPullDeps captures the long-lived dependencies shared by
// every callsite. Build once at process bootstrap and reuse — these
// fields are concurrency-safe (RemoteEdgeCache uses Redis with its own
// locking, FederationClient is goroutine-safe, peerManager has its own
// mutex).
type ArrangeOriginPullDeps struct {
	Cache *RemoteEdgeCache
	// Registry is mandatory for receipt-bound placement. Legacy callers may
	// use the process registry when no explicit registry is supplied.
	Registry     *control.StreamRegistry
	PeerResolver OriginPullPeerResolver
	// CellAddress is required for receipt-bound placement pulls. Cluster-keyed
	// addresses cannot establish a canonical source control-cell identity.
	CellAddress func(string) string
	FedClient   OriginPullFederationClient
	LocalSource *FederationServer
	InstanceID  string
	Logger      logging.Logger
	// EventEmitter receives federation lifecycle events. Optional —
	// nil-safe. HTTP /source supplies it; gRPC /play arrangement runs
	// without it.
	EventEmitter func(*ipcpb.FederationEventData)
	// Now stamps source acceptance. It must share the clock that reads the
	// acceptance window, otherwise every source resolution looks overdue for
	// renewal and re-notifies the origin.
	Now func() time.Time
}

func (d *ArrangeOriginPullDeps) now() time.Time {
	if d != nil && d.Now != nil {
		return d.Now()
	}
	return time.Now()
}

// ArrangeOriginPullRequest is per-call state.
type ArrangeOriginPullRequest struct {
	InternalName      string
	Remote            *foghornfederationpb.EdgeCandidate
	RemoteCluster     string
	TenantID          string
	SourceGeneration  string
	SourceRevision    int64
	AttemptID         string
	DestinationFence  int64
	RefreshAcceptance bool
	// AllowEquivalentSourceReplacement is reserved for configured sources. Their
	// configuration generation remains stable when the live origin node changes;
	// push publisher ownership must never set this flag.
	AllowEquivalentSourceReplacement bool
	// BindPull records the physical attempt before source notification or reuse.
	// A failure leaves preparation pending and cannot advertise a source URL.
	BindPull func(*PlacementPullBinding) error
	// DestClusterID is the authenticated virtual media cluster of the
	// destination node. It must not be inferred from the Foghorn process:
	// one control cell can serve nodes from multiple virtual clusters.
	DestClusterID string

	// DestNodeID identifies the puller when the caller already knows
	// it (the /source HTTP path: caller IS the puller). When empty,
	// LBPicker is called to select one. Mutually exclusive with
	// LBPicker — supplying both prefers DestNodeID.
	DestNodeID      string
	DestNodeBaseURL string
	LBPicker        OriginPullLBPicker
	Lat, Lon        float64
}

// ArrangeOriginPullResult carries the per-call outcome. Reused=true
// means the pull was already arranged by an earlier caller and the
// returned fields come from the registry's existing Location.
type ArrangeOriginPullResult struct {
	AttemptID            string
	SourceCellID         string
	SourceMediaClusterID string
	SourceNodeID         string
	SourceGeneration     string
	SourceRevision       int64
	DestNodeID           string
	DestNodeBaseURL      string
	PullDTSCURL          string
	Reused               bool
}

// ArrangeOriginPull is the single source of truth for cross-cluster
// origin-pull arrangement. Called from the placement path, HTTP /source via
// arrangeRemoteOriginPullFromSource (the caller is the puller, identified by
// its clientIP), and the DVR path via tryArrangeDVRCrossCluster. Result: a registry-tracked Location with
// ReplicatingFrom + PullDTSCURL + DestNodeID, and a source-cluster
// OutboundPullers entry mirroring it. Loop prevention, single-flight
// per destination, and NotifyOriginPull rejection are uniformly handled.
func (d *ArrangeOriginPullDeps) ArrangeOriginPull(ctx context.Context, req ArrangeOriginPullRequest) (*ArrangeOriginPullResult, error) {
	ctx, cancelArrange := context.WithTimeout(ctx, 5*time.Second)
	defer cancelArrange()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if d == nil || d.Cache == nil {
		return nil, ErrOriginPullDepsMissing
	}
	registry := d.Registry
	if registry == nil && req.BindPull == nil {
		registry = control.StreamRegistryInstance
	}
	if registry == nil {
		return nil, ErrOriginPullRegistryNil
	}
	localSource := d.LocalSource != nil && d.LocalSource.matchesSourceCell(req.RemoteCluster)
	if req.BindPull != nil {
		localSource = d.LocalSource != nil && d.LocalSource.sourceControlCellID() == req.RemoteCluster
	}
	// A receipt-bound pull still requires the cell resolver, because its source
	// identity is a control cell by construction. Every other caller needs at
	// least one resolver: which one answers depends on the namespace it names,
	// and that is decided at the call below rather than here.
	if !localSource && (d.FedClient == nil || (req.BindPull != nil && d.CellAddress == nil) ||
		(d.CellAddress == nil && d.PeerResolver == nil)) {
		return nil, ErrOriginPullDepsMissing
	}
	if req.InternalName == "" || req.Remote == nil || req.RemoteCluster == "" {
		return nil, fmt.Errorf("invalid arrange request: stream=%q remote=%v cluster=%q",
			req.InternalName, req.Remote, req.RemoteCluster)
	}
	if err := bindOriginPullRequest(&req); err != nil {
		return nil, err
	}
	emit := func(data *ipcpb.FederationEventData) {
		if d.EventEmitter == nil {
			return
		}
		if req.TenantID != "" {
			data.StreamTenantId = &req.TenantID
		}
		if data.GetOriginClusterId() == "" {
			// Only the registry answers this in the media-cluster namespace.
			// RemoteCluster is a control cell on the placement and DVR paths, so
			// falling back to it would file a cell id under an analytics dimension
			// that means media cluster; an unset dimension is honest, a wrong one
			// is not. RemoteCluster is already carried as remote_cluster.
			if originClusterID, _ := registry.OriginCluster(req.InternalName); originClusterID != "" {
				data.OriginClusterId = &originClusterID
			}
		}
		d.EventEmitter(data)
	}
	// Resolve the destination before reuse; another node's pull is never a
	// substitute for the node selected for this viewer or source request.
	destNodeID := req.DestNodeID
	destNodeBaseURL := req.DestNodeBaseURL
	if destNodeID == "" {
		if req.LBPicker == nil {
			return nil, ErrOriginPullNoDest
		}
		host, nodeID, pickedClusterID, err := req.LBPicker(ctx, req.Lat, req.Lon, req.TenantID)
		if err != nil {
			return nil, fmt.Errorf("LB pick: %w", err)
		}
		if nodeID == "" {
			return nil, ErrOriginPullNoDest
		}
		destNodeID, destNodeBaseURL = nodeID, host
		if strings.TrimSpace(req.DestClusterID) == "" {
			req.DestClusterID = pickedClusterID
		}
	}
	destClusterID := strings.TrimSpace(req.DestClusterID)
	if node := state.DefaultManager().GetNodeState(destNodeID); node != nil {
		nodeClusterID := strings.TrimSpace(node.ClusterID)
		if destClusterID != "" && nodeClusterID != "" && destClusterID != nodeClusterID {
			return nil, fmt.Errorf("%w: destination node cluster mismatch", ErrOriginPullNoDest)
		}
		if nodeClusterID != "" {
			destClusterID = nodeClusterID
		}
	}
	if destClusterID == "" {
		return nil, fmt.Errorf("%w: destination node cluster unavailable", ErrOriginPullNoDest)
	}
	if localSource {
		if err := d.LocalSource.validateLocalPullDestination(req.RemoteCluster, req.Remote.GetClusterId(), req.Remote.GetNodeId(), destClusterID, destNodeID); err != nil {
			return nil, fmt.Errorf("%w: local destination: %w", ErrOriginPullSourceBinding, err)
		}
	}
	if reused, reuseErr := lookupExistingReplication(ctx, registry, req, destNodeID, destClusterID); reused != nil || reuseErr != nil {
		return reused, reuseErr
	}

	lockOwner := d.InstanceID + ":" + uuid.NewString()
	lockKey := originPullDestinationKey(req.InternalName, destNodeID)
	acquired, leaseErr := d.Cache.AcquireOriginPullLock(ctx, lockKey, lockOwner)
	if leaseErr != nil {
		return nil, fmt.Errorf("%w: acquire destination lease: %w", ErrOriginPullStateUnavailable, leaseErr)
	}
	if !acquired {
		// Another foghorn instance is arranging right now. Brief wait,
		// then look for the resulting registry entry.
		timer := time.NewTimer(50 * time.Millisecond)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-timer.C:
		}
		if reused, reuseErr := lookupExistingReplication(ctx, registry, req, destNodeID, destClusterID); reused != nil || reuseErr != nil {
			return reused, reuseErr
		}
		return nil, ErrOriginPullLockContention
	}
	defer func() {
		releaseCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
		defer cancel()
		d.Cache.ReleaseOriginPullLock(releaseCtx, lockKey, lockOwner)
	}()
	if reused, reuseErr := lookupExistingReplication(ctx, registry, req, destNodeID, destClusterID); reused != nil || reuseErr != nil {
		return reused, reuseErr
	}

	// Loop prevention: refuse to pull from a cluster that's already
	// pulling THIS stream from us.
	replications, replicationErr := d.Cache.GetRemoteReplications(ctx, req.InternalName)
	if replicationErr != nil {
		return nil, fmt.Errorf("%w: read loop prevention: %w", ErrOriginPullStateUnavailable, replicationErr)
	}
	if len(replications) > 0 {
		for _, r := range replications {
			// RemoteCluster is not one namespace across callers: the placement
			// and DVR paths name a cell, while the legacy /source path names the
			// origin media cluster it got from Commodore. All three recorded
			// identities are compared, so the guard holds on every arranging path
			// rather than silently matching nothing on one of them. ClusterID
			// alone would not cover the /source path, because it is the sender's
			// PeerChannel identity rather than the media cluster it pulls into.
			if (r.ControlCellID != "" && r.ControlCellID == req.RemoteCluster) ||
				(r.MediaClusterID != "" && r.MediaClusterID == req.RemoteCluster) ||
				(r.ClusterID != "" && r.ClusterID == req.RemoteCluster) {
				if d.EventEmitter != nil {
					emit(&ipcpb.FederationEventData{
						EventType:                  ipcpb.FederationEventType_REPLICATION_LOOP_PREVENTED,
						RemoteCluster:              req.RemoteCluster,
						StreamName:                 &req.InternalName,
						BlockedCluster:             &req.RemoteCluster,
						ExistingReplicationCluster: &r.ClusterID,
					})
				}
				return nil, ErrOriginPullLoop
			}
		}
	}
	revalidatedExisting := false
	current, found, readErr := registry.CurrentInboundPull(ctx, req.InternalName, destNodeID)
	if readErr != nil {
		return nil, fmt.Errorf("%w: read current destination: %w", ErrOriginPullStateUnavailable, readErr)
	}
	if found &&
		req.Remote.GetClusterId() != "" && originPullRecordMatches(req, destClusterID, current) {
		if current.SourceMediaClusterID != "" && current.SourceMediaClusterID != req.Remote.GetClusterId() {
			return nil, ErrOriginPullSourceBinding
		}
		if !canonicalPullAttempt(current.AttemptID) {
			return nil, ErrOriginPullSourceBinding
		}
		// Revalidate missing metadata using the existing physical attempt so
		// source and destination do not end up tracking different attempts.
		req.AttemptID = current.AttemptID
		revalidatedExisting = true
	}

	// NotifyOriginPull — tell the source cluster we're pulling.
	//
	// RemoteCluster is not one namespace across callers: placement and the
	// cross-cluster DVR path name a control cell, while the legacy /source path
	// names the origin media cluster. The two resolvers are keyed accordingly —
	// GetPeerAddr by media cluster, CellAddress by cell — so both are tried
	// rather than choosing one from whether a placement binding is present.
	// Selecting on BindPull left every cell-named caller without a binding
	// resolving through the cluster-keyed map, which can only miss.
	peerAddr := ""
	if !localSource {
		if d.CellAddress != nil {
			peerAddr = d.CellAddress(req.RemoteCluster)
		}
		// A receipt-bound pull resolves through the cell and nowhere else: a
		// cluster-keyed address cannot establish canonical source cell identity,
		// so falling back would let it bind to an address it cannot vouch for.
		// Every other caller may fall back, because whether RemoteCluster names
		// a cell or a media cluster depends on which path built the request.
		if peerAddr == "" && req.BindPull == nil && d.PeerResolver != nil {
			peerAddr = d.PeerResolver.GetPeerAddr(req.RemoteCluster)
		}
		if peerAddr == "" {
			return nil, ErrOriginPullPeerUnreachable
		}
	}
	if err := bindArrangedPull(ctx, req, PlacementPullBinding{
		DestinationFence: req.DestinationFence,
		AttemptID:        req.AttemptID, SourceCellID: req.RemoteCluster, SourceClusterID: req.Remote.GetClusterId(),
		SourceNodeID: req.Remote.GetNodeId(), SourceGeneration: req.SourceGeneration, SourceRevision: req.SourceRevision,
	}); err != nil {
		return nil, err
	}
	notifyCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	notification := &foghornfederationpb.OriginPullNotification{
		StreamName:       req.InternalName,
		SourceNodeId:     req.Remote.NodeId,
		DestClusterId:    destClusterID,
		DestNodeId:       destNodeID,
		TenantId:         req.TenantID,
		SourceGeneration: req.SourceGeneration,
		SourceRevision:   req.SourceRevision,
		AttemptId:        req.AttemptID,
	}
	if req.Remote.GetClusterId() != "" {
		notification.SourceCellId, notification.SourceClusterId = req.RemoteCluster, req.Remote.GetClusterId()
	}
	var ack *foghornfederationpb.OriginPullAck
	var err error
	if localSource {
		ack, err = d.LocalSource.prepareLocalOriginPull(notifyCtx, notification)
	} else {
		ack, err = d.FedClient.NotifyOriginPull(notifyCtx, req.RemoteCluster, peerAddr, notification)
	}
	if contextErr := notifyCtx.Err(); contextErr != nil {
		return nil, fmt.Errorf("%w: source notification deadline: %w", ErrOriginPullNotifyFailed, contextErr)
	}
	if err != nil || ack == nil || !ack.GetAccepted() || strings.TrimSpace(ack.GetDtscUrl()) == "" {
		reason := "rejected"
		if err != nil {
			reason = err.Error()
		} else if ack != nil && ack.GetReason() != "" {
			reason = ack.GetReason()
		}
		if d.EventEmitter != nil {
			emit(&ipcpb.FederationEventData{
				EventType:     ipcpb.FederationEventType_ORIGIN_PULL_FAILED,
				RemoteCluster: req.RemoteCluster,
				StreamName:    &req.InternalName,
				SourceNode:    &req.Remote.NodeId,
				FailureReason: &reason,
			})
		}
		return nil, fmt.Errorf("%w: %s", ErrOriginPullNotifyFailed, reason)
	}
	if !originPullAckMatches(req, destClusterID, destNodeID, ack) {
		return nil, ErrOriginPullSourceBinding
	}

	// Persist the exact destination before advertising a source URL or starting
	// its pull. A durable-state failure cannot become an untracked success.
	record := registry.RecordInboundPull
	if req.AllowEquivalentSourceReplacement {
		record = registry.RecordInboundConfiguredPull
	}
	pull, err := record(ctx, req.InternalName, control.InboundPull{
		TenantID: req.TenantID, AttemptID: req.AttemptID, SourceClusterID: req.RemoteCluster, SourceNodeID: req.Remote.NodeId,
		SourceMediaClusterID: ack.GetSourceClusterId(),
		SourceGeneration:     req.SourceGeneration, SourceRevision: req.SourceRevision, DestClusterID: destClusterID,
		DestNodeID: destNodeID, DestNodeBaseURL: destNodeBaseURL, DTSCURL: ack.DtscUrl,
		SourceAcceptedAt:  d.now(),
		PlacementRequired: req.BindPull != nil,
	})
	if err != nil {
		return nil, fmt.Errorf("%w: persist destination: %w", ErrOriginPullRegistryNil, err)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if req.BindPull != nil && pull.AttemptID != req.AttemptID {
		return nil, ErrOriginPullSourceBinding
	}

	d.Logger.WithFields(logging.Fields{
		"stream":         req.InternalName,
		"source_cluster": req.RemoteCluster,
		"source_node":    req.Remote.NodeId,
		"dest_node":      destNodeID,
		"dtsc_url":       control.SourcePullBaseURL(ack.DtscUrl),
	}).Info("Origin-pull arranged")

	if d.EventEmitter != nil {
		mediaURL := control.SourcePullBaseURL(ack.DtscUrl)
		emit(&ipcpb.FederationEventData{
			EventType:     ipcpb.FederationEventType_ORIGIN_PULL_ARRANGED,
			RemoteCluster: req.RemoteCluster,
			StreamName:    &req.InternalName,
			SourceNode:    &req.Remote.NodeId,
			DestNode:      &destNodeID,
			DtscUrl:       &mediaURL,
		})
	}

	return &ArrangeOriginPullResult{
		AttemptID:    pull.AttemptID,
		SourceCellID: pull.SourceClusterID, SourceMediaClusterID: pull.SourceMediaClusterID, SourceNodeID: pull.SourceNodeID,
		SourceGeneration: pull.SourceGeneration, SourceRevision: pull.SourceRevision,
		DestNodeID:      destNodeID,
		DestNodeBaseURL: destNodeBaseURL,
		PullDTSCURL:     ack.DtscUrl,
		Reused:          revalidatedExisting,
	}, nil
}

func originPullDestinationKey(internalName, nodeID string) string {
	return fmt.Sprintf("%d:%s:%s", len(internalName), internalName, nodeID)
}

func lookupExistingReplication(ctx context.Context, registry *control.StreamRegistry, req ArrangeOriginPullRequest, nodeID, destClusterID string) (*ArrangeOriginPullResult, error) {
	if registry == nil {
		return nil, ErrOriginPullRegistryNil
	}
	if req.RefreshAcceptance {
		return nil, nil
	}
	pull, ok, err := registry.CurrentInboundPull(ctx, req.InternalName, nodeID)
	if err != nil {
		return nil, fmt.Errorf("%w: read current destination: %w", ErrOriginPullStateUnavailable, err)
	}
	if !ok || !originPullRecordMatches(req, destClusterID, pull) ||
		(req.Remote.GetClusterId() != "" && pull.SourceMediaClusterID != req.Remote.GetClusterId()) ||
		(req.BindPull != nil && (pull.TenantID != req.TenantID || pull.DestClusterID != destClusterID)) {
		return nil, nil
	}
	if err := bindArrangedPull(ctx, req, PlacementPullBinding{
		DestinationFence: req.DestinationFence,
		AttemptID:        pull.AttemptID, SourceCellID: pull.SourceClusterID, SourceClusterID: pull.SourceMediaClusterID,
		SourceNodeID: pull.SourceNodeID, SourceGeneration: pull.SourceGeneration, SourceRevision: pull.SourceRevision,
	}); err != nil {
		return nil, err
	}
	if req.BindPull != nil && !pull.PlacementRequired {
		if err := registry.RequireInboundPlacement(ctx, req.InternalName, pull); err != nil {
			return nil, fmt.Errorf("%w: require placement for reused source: %w", ErrOriginPullStateUnavailable, err)
		}
	}
	return &ArrangeOriginPullResult{
		AttemptID:    pull.AttemptID,
		SourceCellID: pull.SourceClusterID, SourceMediaClusterID: pull.SourceMediaClusterID, SourceNodeID: pull.SourceNodeID,
		DestNodeID:       pull.DestNodeID,
		SourceGeneration: pull.SourceGeneration, SourceRevision: pull.SourceRevision,
		DestNodeBaseURL: pull.DestNodeBaseURL,
		PullDTSCURL:     pull.DTSCURL,
		Reused:          true,
	}, nil
}

func bindArrangedPull(ctx context.Context, req ArrangeOriginPullRequest, pull PlacementPullBinding) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if req.BindPull != nil {
		if pull.DestinationFence <= 0 || !canonicalPullAttempt(pull.AttemptID) || !validPullIdentity(pull.SourceCellID) || !validPullIdentity(pull.SourceClusterID) || !validPullIdentity(pull.SourceNodeID) {
			return ErrOriginPullSourceBinding
		}
		if err := req.BindPull(&pull); err != nil {
			return fmt.Errorf("%w: bind physical pull: %w", ErrOriginPullStateUnavailable, err)
		}
	}
	return ctx.Err()
}

func originPullRecordMatches(req ArrangeOriginPullRequest, destClusterID string, pull control.InboundPull) bool {
	return (pull.TenantID == "" || pull.TenantID == req.TenantID) && pull.SourceClusterID == req.RemoteCluster &&
		pull.SourceNodeID == req.Remote.GetNodeId() && pull.SourceGeneration == req.SourceGeneration && pull.SourceRevision == req.SourceRevision &&
		(pull.DestClusterID == "" || pull.DestClusterID == destClusterID)
}

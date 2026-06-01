package federation

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"frameworks/api_balancing/internal/control"
	"frameworks/api_balancing/internal/state"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto"
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
	ErrOriginPullDepsMissing     = errors.New("origin-pull dependencies unavailable")
	ErrOriginPullRegistryNil     = errors.New("origin-pull stream registry unavailable")
	ErrOriginPullLockContention  = errors.New("origin-pull lock contention")
	ErrOriginPullLoop            = errors.New("origin-pull replication loop prevented")
	ErrOriginPullNoDest          = errors.New("origin-pull destination unidentified")
	ErrOriginPullPeerUnreachable = errors.New("origin-pull peer address unknown")
	ErrOriginPullNotifyFailed    = errors.New("origin-pull NotifyOriginPull rejected")
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
		errors.Is(err, ErrOriginPullRegistryNil),
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
	NotifyOriginPull(ctx context.Context, peerClusterID, peerAddr string, req *pb.OriginPullNotification) (*pb.OriginPullAck, error)
}

// OriginPullLBPicker selects a local edge to become the puller when the
// caller doesn't already know its destination node (gRPC viewer-routing
// path). Implementations return the chosen node's public BaseURL host
// plus its registered NodeID. /source-style callers can pass nil because
// they identify themselves and supply DestNodeID directly.
type OriginPullLBPicker func(ctx context.Context, lat, lon float64, tenantID string) (host, nodeID string, err error)

// ArrangeOriginPullDeps captures the long-lived dependencies shared by
// every callsite. Build once at process bootstrap and reuse — these
// fields are concurrency-safe (RemoteEdgeCache uses Redis with its own
// locking, FederationClient is goroutine-safe, peerManager has its own
// mutex).
type ArrangeOriginPullDeps struct {
	Cache        *RemoteEdgeCache
	PeerResolver OriginPullPeerResolver
	FedClient    OriginPullFederationClient
	InstanceID   string
	ClusterID    string
	Logger       logging.Logger
	// EventEmitter receives federation lifecycle events. Optional —
	// nil-safe. HTTP /source supplies it; gRPC /play arrangement runs
	// without it.
	EventEmitter func(*pb.FederationEventData)
}

// ArrangeOriginPullRequest is per-call state.
type ArrangeOriginPullRequest struct {
	InternalName  string
	Remote        *pb.EdgeCandidate
	RemoteCluster string
	TenantID      string

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
	DestNodeID      string
	DestNodeBaseURL string
	PullDTSCURL     string
	Reused          bool
}

// ArrangeOriginPull is the single source of truth for cross-cluster
// origin-pull arrangement. Called from gRPC viewer routing
// (arrangeOriginPull wrapper that picks the puller via LB), HTTP
// /source (the caller is the puller via its clientIP), and DVR/native
// federation paths. Result: a registry-tracked Location with
// ReplicatingFrom + PullDTSCURL + DestNodeID, and a source-cluster
// OutboundPullers entry mirroring it. Loop prevention, single-flight
// per stream, and NotifyOriginPull rejection are uniformly handled.
func (d *ArrangeOriginPullDeps) ArrangeOriginPull(ctx context.Context, req ArrangeOriginPullRequest) (*ArrangeOriginPullResult, error) {
	if d == nil || d.Cache == nil || d.PeerResolver == nil || d.FedClient == nil {
		return nil, ErrOriginPullDepsMissing
	}
	if req.InternalName == "" || req.Remote == nil || req.RemoteCluster == "" {
		return nil, fmt.Errorf("invalid arrange request: stream=%q remote=%v cluster=%q",
			req.InternalName, req.Remote, req.RemoteCluster)
	}
	// Refuse pre-NotifyOriginPull when we can't durably MarkReplicating
	// locally. Mirrors the source-side guard in PrepareOriginPull
	// (federation/server.go) so neither side records a half-committed
	// replication when the registry is missing during bootstrap/reconnect.
	if control.StreamRegistryInstance == nil {
		return nil, ErrOriginPullRegistryNil
	}

	// Reuse existing replication if present — idempotent for repeat
	// arrangement requests on the same stream.
	if reused := lookupExistingReplication(ctx, req.InternalName); reused != nil {
		return reused, nil
	}

	lockOwner := d.InstanceID
	if lockOwner == "" {
		lockOwner = "foghorn"
	}
	if !d.Cache.TryAcquireOriginPullLock(ctx, req.InternalName, lockOwner) {
		// Another foghorn instance is arranging right now. Brief wait,
		// then look for the resulting registry entry.
		time.Sleep(50 * time.Millisecond)
		if reused := lookupExistingReplication(ctx, req.InternalName); reused != nil {
			return reused, nil
		}
		return nil, ErrOriginPullLockContention
	}
	defer d.Cache.ReleaseOriginPullLock(ctx, req.InternalName, lockOwner)

	// Loop prevention: refuse to pull from a cluster that's already
	// pulling THIS stream from us.
	if replications, err := d.Cache.GetRemoteReplications(ctx, req.InternalName); err == nil && len(replications) > 0 {
		for _, r := range replications {
			if r.ClusterID == req.RemoteCluster {
				if d.EventEmitter != nil {
					d.EventEmitter(&pb.FederationEventData{
						EventType:                  pb.FederationEventType_REPLICATION_LOOP_PREVENTED,
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

	// Resolve destination node — caller-supplied wins, else LB pick.
	destNodeID := req.DestNodeID
	destNodeBaseURL := req.DestNodeBaseURL
	if destNodeID == "" {
		if req.LBPicker == nil {
			return nil, ErrOriginPullNoDest
		}
		host, nodeID, err := req.LBPicker(ctx, req.Lat, req.Lon, req.TenantID)
		if err != nil {
			return nil, fmt.Errorf("LB pick: %w", err)
		}
		if nodeID == "" {
			return nil, ErrOriginPullNoDest
		}
		destNodeID = nodeID
		destNodeBaseURL = host
	}

	// NotifyOriginPull — tell the source cluster we're pulling.
	peerAddr := d.PeerResolver.GetPeerAddr(req.RemoteCluster)
	if peerAddr == "" {
		return nil, ErrOriginPullPeerUnreachable
	}
	notifyCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	ack, err := d.FedClient.NotifyOriginPull(notifyCtx, req.RemoteCluster, peerAddr, &pb.OriginPullNotification{
		StreamName:    req.InternalName,
		SourceNodeId:  req.Remote.NodeId,
		DestClusterId: d.ClusterID,
		DestNodeId:    destNodeID,
		TenantId:      req.TenantID,
	})
	if err != nil || ack == nil || !ack.GetAccepted() {
		reason := "rejected"
		if err != nil {
			reason = err.Error()
		} else if ack != nil && ack.GetReason() != "" {
			reason = ack.GetReason()
		}
		if d.EventEmitter != nil {
			d.EventEmitter(&pb.FederationEventData{
				EventType:     pb.FederationEventType_ORIGIN_PULL_FAILED,
				RemoteCluster: req.RemoteCluster,
				StreamName:    &req.InternalName,
				SourceNode:    &req.Remote.NodeId,
				FailureReason: &reason,
			})
		}
		return nil, fmt.Errorf("%w: %s", ErrOriginPullNotifyFailed, reason)
	}

	// MarkReplicating — registry entry that /source + /balance use to
	// resolve viewers to the puller edge. Registry presence was checked
	// at function entry; nil-guard removed so a missing registry can
	// never silently succeed past this point.
	control.StreamRegistryInstance.MarkReplicating(
		req.InternalName,
		req.RemoteCluster,
		ack.DtscUrl,
		destNodeID,
		destNodeBaseURL,
		req.Remote.NodeId,
	)
	state.DefaultManager().UpdateNodeStats(req.InternalName, destNodeID, 0, 1, 0, 0, true)

	d.Logger.WithFields(logging.Fields{
		"stream":         req.InternalName,
		"source_cluster": req.RemoteCluster,
		"source_node":    req.Remote.NodeId,
		"dest_node":      destNodeID,
		"dtsc_url":       ack.DtscUrl,
	}).Info("Origin-pull arranged")

	if d.EventEmitter != nil {
		d.EventEmitter(&pb.FederationEventData{
			EventType:     pb.FederationEventType_ORIGIN_PULL_ARRANGED,
			RemoteCluster: req.RemoteCluster,
			StreamName:    &req.InternalName,
			SourceNode:    &req.Remote.NodeId,
			DestNode:      &destNodeID,
			DtscUrl:       &ack.DtscUrl,
		})
	}

	return &ArrangeOriginPullResult{
		DestNodeID:      destNodeID,
		DestNodeBaseURL: destNodeBaseURL,
		PullDTSCURL:     ack.DtscUrl,
		Reused:          false,
	}, nil
}

func lookupExistingReplication(ctx context.Context, internalName string) *ArrangeOriginPullResult {
	if control.StreamRegistryInstance == nil {
		return nil
	}
	loc, ok := control.StreamRegistryInstance.LocalReplication(ctx, internalName)
	if !ok {
		return nil
	}
	return &ArrangeOriginPullResult{
		DestNodeID:      loc.DestNodeID,
		DestNodeBaseURL: loc.DestNodeBaseURL,
		PullDTSCURL:     loc.PullDTSCURL,
		Reused:          true,
	}
}

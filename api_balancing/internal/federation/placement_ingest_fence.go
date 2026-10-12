package federation

import (
	"context"
	"sync"
	"time"

	"frameworks/api_balancing/internal/balancer"
	sharedauthority "github.com/Livepeer-FrameWorks/monorepo/pkg/mediaauthority"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/placement"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type PlacementStreamContextClient interface {
	ResolveStreamContext(context.Context, string, string, string, string) (*commodorepb.ResolveStreamContextResponse, error)
}

// PlacementIngestOwnershipSource names where a request's ingest ownership
// constraint came from.
type PlacementIngestOwnershipSource string

const (
	// PlacementIngestOwnershipClaim is the Commodore claim the admitted
	// publisher connection itself holds on Cluster.
	PlacementIngestOwnershipClaim PlacementIngestOwnershipSource = "claim"
	// PlacementIngestOwnershipConnected is Commodore's current lease, read
	// without claiming. An empty Cluster is a verified unowned stream.
	PlacementIngestOwnershipConnected PlacementIngestOwnershipSource = "connected"
	// PlacementIngestOwnershipOutageOwner is the pair's signed outage owner,
	// used while Commodore's lease cannot be read.
	PlacementIngestOwnershipOutageOwner PlacementIngestOwnershipSource = "outage_owner"
)

// PlacementIngestOwnership constrains which cluster may admit a publisher for
// one request. Placement reads it once per request; final publisher admission
// still requires the atomic Commodore claim or the verified outage owner.
type PlacementIngestOwnership struct {
	Cluster string
	Source  PlacementIngestOwnershipSource
}

// under restates ownership for a newer authority snapshot of the same request
// without reading Commodore again. Claimed and connected ownership describe the
// publisher, not the pair; an outage owner stands only while the snapshot still
// signs that cluster.
func (ownership PlacementIngestOwnership) under(authority balancer.PlacementAuthority) (PlacementIngestOwnership, error) {
	if authority.Verb != placement.Ingest {
		return PlacementIngestOwnership{}, nil
	}
	switch ownership.Source {
	case PlacementIngestOwnershipConnected:
		if ownership.Cluster != "" && !validPullIdentity(ownership.Cluster) {
			return PlacementIngestOwnership{}, status.Error(codes.FailedPrecondition, "ingest ownership names an invalid cluster")
		}
		return ownership, nil
	case PlacementIngestOwnershipClaim:
		if !validPullIdentity(ownership.Cluster) {
			return PlacementIngestOwnership{}, status.Error(codes.FailedPrecondition, "held ingest claim names no cluster")
		}
		return ownership, nil
	case PlacementIngestOwnershipOutageOwner:
		if authority.OutageIngestClusterID == "" {
			return PlacementIngestOwnership{}, status.Error(codes.Unavailable, "ingest ownership is unavailable and the stream has no signed outage owner")
		}
		if ownership.Cluster != authority.OutageIngestClusterID {
			return PlacementIngestOwnership{}, status.Error(codes.Unavailable, "signed outage ingest owner differs from the authority being decided on")
		}
		return ownership, nil
	default:
		return PlacementIngestOwnership{}, status.Error(codes.Unavailable, "ingest ownership is unavailable")
	}
}

// readIngestOwnership asks the fence once for this request's ownership. A
// serve decision has no ownership constraint.
func readIngestOwnership(ctx context.Context, fence PlacementIngestFenceReader, authority balancer.PlacementAuthority) (PlacementIngestOwnership, error) {
	if authority.Verb != placement.Ingest {
		return PlacementIngestOwnership{}, nil
	}
	if fence == nil {
		return PlacementIngestOwnership{}, status.Error(codes.Unavailable, "ingest ownership is unavailable")
	}
	ownership, err := fence.IngestOwnership(ctx, PlacementIngestIdentity{TenantID: authority.TenantID, ObjectID: authority.ObjectID,
		InternalName: authority.InternalName, OutageIngestClusterID: authority.OutageIngestClusterID})
	if err != nil {
		return PlacementIngestOwnership{}, err
	}
	return ownership.under(authority)
}

// ConnectedPlacementIngestFence reads Commodore's current publisher lease
// without taking or renewing it, in one attempt bounded to 1 s. When Commodore
// is unreachable (Unavailable, including an open circuit breaker, or a timeout)
// the stream's signed outage owner is the only cluster that may admit the
// publisher, and a stream without one is refused. Any other error, including
// a denied or mismatched context, is a refusal. One instance serves the whole
// process, so its outage backoff spares every request the wait.
type ConnectedPlacementIngestFence struct {
	Client PlacementStreamContextClient
	// ClientSnapshot follows a reconnecting client without rewriting a live
	// reader. A configured provider returning nil cannot use the initial client.
	ClientSnapshot func() PlacementStreamContextClient
	// Backoff is the process-wide Commodore outage backoff shared with the
	// PUSH_REWRITE admission calls; nil keeps one for this fence alone.
	Backoff *CentralOutageBackoff
	// Now is the clock of the fence's own backoff when Backoff is nil.
	Now func() time.Time

	ownBackoff     CentralOutageBackoff
	ownBackoffOnce sync.Once
}

var _ PlacementIngestFenceReader = (*ConnectedPlacementIngestFence)(nil)

func (reader *ConnectedPlacementIngestFence) IngestOwnership(ctx context.Context, identity PlacementIngestIdentity) (PlacementIngestOwnership, error) {
	if reader == nil || !validPullIdentity(identity.TenantID) || !validPullIdentity(identity.InternalName) || !validPullIdentity(identity.ObjectID) {
		return PlacementIngestOwnership{}, status.Error(codes.Unavailable, "ingest ownership reader is unavailable")
	}
	if contextErr := ctx.Err(); contextErr != nil {
		return PlacementIngestOwnership{}, status.FromContextError(contextErr).Err()
	}
	backoff := reader.backoff()
	if backoff.InOutage() {
		return outageIngestOwnership(identity)
	}
	client := reader.Client
	if reader.ClientSnapshot != nil {
		client = reader.ClientSnapshot()
	}
	if client == nil {
		// No connected client costs no wait, so it opens no backoff: a
		// reconnected client is used on the next request.
		return outageIngestOwnership(identity)
	}
	var cluster string
	err := backoff.Call(ctx, func(callCtx context.Context) error {
		var readErr error
		cluster, readErr = readIngestLease(callCtx, client, identity)
		return readErr
	})
	if err == nil {
		return PlacementIngestOwnership{Cluster: cluster, Source: PlacementIngestOwnershipConnected}, nil
	}
	// The caller's own budget ending says nothing about Commodore.
	if contextErr := ctx.Err(); contextErr != nil {
		return PlacementIngestOwnership{}, status.FromContextError(contextErr).Err()
	}
	if !CentralUnreachable(err) {
		return PlacementIngestOwnership{}, err
	}
	return outageIngestOwnership(identity)
}

func (reader *ConnectedPlacementIngestFence) backoff() *CentralOutageBackoff {
	if reader.Backoff != nil {
		return reader.Backoff
	}
	reader.ownBackoffOnce.Do(func() {
		reader.ownBackoff.Service = "commodore"
		reader.ownBackoff.Now = reader.Now
	})
	return &reader.ownBackoff
}

func outageIngestOwnership(identity PlacementIngestIdentity) (PlacementIngestOwnership, error) {
	if !validPullIdentity(identity.OutageIngestClusterID) {
		return PlacementIngestOwnership{}, status.Error(codes.Unavailable, "ingest ownership is unavailable and the stream has no signed outage owner")
	}
	return PlacementIngestOwnership{Cluster: identity.OutageIngestClusterID, Source: PlacementIngestOwnershipOutageOwner}, nil
}

func readIngestLease(ctx context.Context, client PlacementStreamContextClient, identity PlacementIngestIdentity) (string, error) {
	// No candidate cluster is asserted: this is a non-claiming read of the
	// existing lease, independent of the node this preparation would select.
	response, err := client.ResolveStreamContext(ctx, "", "", identity.InternalName, "")
	if err != nil {
		return "", err
	}
	if response == nil || response.TenantId != identity.TenantID || response.InternalName != identity.InternalName || response.StreamId == "" ||
		sharedauthority.LiveStreamAuthorityID(response.StreamId) != identity.ObjectID || response.IngestMode != "push" ||
		!response.Admitted || response.IsSuspended || response.IsBalanceNegative {
		// A denied ResolveStreamContext can return before its lease fields are
		// populated. That empty field must not be treated as an unowned stream.
		return "", status.Error(codes.FailedPrecondition, "ingest ownership context is not admitted or does not match")
	}
	if response.ActiveIngestClusterId == nil {
		return "", nil
	}
	clusterID := response.GetActiveIngestClusterId()
	if !validPullIdentity(clusterID) {
		return "", status.Error(codes.FailedPrecondition, "ingest ownership context contains an invalid cluster")
	}
	return clusterID, nil
}

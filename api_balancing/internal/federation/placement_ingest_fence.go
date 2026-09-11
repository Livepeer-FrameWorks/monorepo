package federation

import (
	"context"
	"time"

	sharedauthority "github.com/Livepeer-FrameWorks/monorepo/pkg/mediaauthority"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type PlacementStreamContextClient interface {
	ResolveStreamContext(context.Context, string, string, string, string) (*commodorepb.ResolveStreamContextResponse, error)
}

// ConnectedPlacementIngestFence reads Commodore's current publisher lease without
// taking or renewing it. An unavailable control plane is unknown, never proof
// that a different cluster may admit the publisher.
type ConnectedPlacementIngestFence struct {
	Client PlacementStreamContextClient
	// ClientSnapshot follows a reconnecting client without rewriting a live
	// reader. A configured provider returning nil cannot use the initial client.
	ClientSnapshot func() PlacementStreamContextClient
}

var _ PlacementIngestFenceReader = (*ConnectedPlacementIngestFence)(nil)

func (reader *ConnectedPlacementIngestFence) ActiveIngestCluster(ctx context.Context, identity PlacementIngestIdentity) (string, error) {
	if reader == nil || !validPullIdentity(identity.TenantID) || !validPullIdentity(identity.InternalName) || !validPullIdentity(identity.ObjectID) {
		return "", status.Error(codes.Unavailable, "ingest ownership reader is unavailable")
	}
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	if contextErr := ctx.Err(); contextErr != nil {
		return "", status.FromContextError(contextErr).Err()
	}
	client := reader.Client
	if reader.ClientSnapshot != nil {
		client = reader.ClientSnapshot()
	}
	if client == nil {
		return "", status.Error(codes.Unavailable, "ingest ownership client is unavailable")
	}
	// No candidate cluster is asserted: this is a non-claiming read of the
	// existing lease, independent of the node this preparation would select.
	response, err := client.ResolveStreamContext(ctx, "", "", identity.InternalName, "")
	if contextErr := ctx.Err(); contextErr != nil {
		return "", status.FromContextError(contextErr).Err()
	}
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

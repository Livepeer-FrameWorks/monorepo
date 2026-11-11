package triggers

import (
	"context"

	"frameworks/api_balancing/internal/federation"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/clients/commodore"
	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
)

// publisherClaimClient bounds individual remote attempts while the caller's
// context continues to own local recovery and the composed claim operation.
type publisherClaimClient struct {
	client  *commodore.GRPCClient
	backoff *federation.CentralOutageBackoff
}

func (c publisherClaimClient) ValidateStreamKeyForClaim(ctx context.Context, key, cluster, token string) (response *pb.ValidateStreamKeyResponse, err error) {
	err = c.backoff.Call(ctx, func(attempt context.Context) error {
		var callErr error
		response, callErr = c.client.ValidateStreamKeyForClaim(attempt, key, cluster, token)
		return callErr
	})
	return response, err
}

func (c publisherClaimClient) ValidateStreamKeyForPublisherClaim(ctx context.Context, key, cluster, token, node, cell string) (response *pb.ValidateStreamKeyResponse, err error) {
	err = c.backoff.Call(ctx, func(attempt context.Context) error {
		var callErr error
		response, callErr = c.client.ValidateStreamKeyForPublisherClaim(attempt, key, cluster, token, node, cell)
		return callErr
	})
	return response, err
}

func (c publisherClaimClient) AcquirePublisherLease(ctx context.Context, request *pb.PublisherLeaseRequest) (response *pb.PublisherLeaseResponse, err error) {
	err = c.backoff.Call(ctx, func(attempt context.Context) error {
		var callErr error
		response, callErr = c.client.AcquirePublisherLease(attempt, request)
		return callErr
	})
	return response, err
}

func (c publisherClaimClient) SyncActiveIngestPlacement(ctx context.Context, cluster string, renew, release []*pb.ActiveIngestStream) (response *pb.SyncActiveIngestPlacementResponse, err error) {
	err = c.backoff.Call(ctx, func(attempt context.Context) error {
		var callErr error
		response, callErr = c.client.SyncActiveIngestPlacement(attempt, cluster, renew, release)
		return callErr
	})
	return response, err
}

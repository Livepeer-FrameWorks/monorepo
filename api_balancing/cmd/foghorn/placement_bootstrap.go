package main

import (
	"context"

	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// The shared Quartermaster holder is installed by both initial bootstrap and
// reconnect. Placement must not retain a nil client from degraded startup.
type livePlacementInventory struct{}

func (livePlacementInventory) GetMediaPlacementInventory(ctx context.Context, request *quartermasterpb.GetMediaPlacementInventoryRequest) (*quartermasterpb.MediaPlacementInventory, error) {
	if err := ctx.Err(); err != nil {
		return nil, status.FromContextError(err).Err()
	}
	client := releaseReconcilerClient.Load()
	if client == nil {
		return nil, status.Error(codes.Unavailable, "placement inventory client is unavailable")
	}
	return client.GetMediaPlacementInventory(ctx, request)
}

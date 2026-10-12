package main

import (
	"context"

	"frameworks/api_balancing/internal/federation"

	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// The shared Quartermaster holder is installed by both initial bootstrap and
// reconnect. Placement must not retain a nil client from degraded startup.
//
// This read answers only while the cell holds no membership snapshot, and it
// runs inside a publisher's or viewer's admission budget, so it is one bounded
// attempt behind the process-wide Quartermaster outage backoff when outage is
// set.
type livePlacementInventory struct {
	outage *federation.CentralOutageBackoff
}

func (inventory livePlacementInventory) GetMediaPlacementInventory(ctx context.Context, request *quartermasterpb.GetMediaPlacementInventoryRequest) (*quartermasterpb.MediaPlacementInventory, error) {
	if err := ctx.Err(); err != nil {
		return nil, status.FromContextError(err).Err()
	}
	client := releaseReconcilerClient.Load()
	if client == nil {
		return nil, status.Error(codes.Unavailable, "placement inventory client is unavailable")
	}
	if inventory.outage == nil {
		return client.GetMediaPlacementInventory(ctx, request)
	}
	var response *quartermasterpb.MediaPlacementInventory
	err := inventory.outage.Call(ctx, func(callCtx context.Context) error {
		var readErr error
		response, readErr = client.GetMediaPlacementInventory(callCtx, request)
		return readErr
	})
	return response, err
}

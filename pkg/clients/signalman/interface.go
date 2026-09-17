package signalman

import (
	"context"

	signalmanpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/signalman"
)

// Interface is the unary method surface of the concrete client, extracted so
// that api_gateway can inject fakes for resolver real-path tests. The concrete
// client satisfies it (asserted below).
type Interface interface {
	Close() error
	GetHubStats(ctx context.Context) (*signalmanpb.HubStats, error)
}

var _ Interface = (*GRPCClient)(nil)

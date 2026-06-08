package signalman

import (
	"context"
	signalmanpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/signalman"
)

// Interface is the full method surface of the concrete client, extracted so
// that api_gateway can inject fakes for resolver real-path tests. The concrete
// client satisfies it (asserted below).
type Interface interface {
	Close() error
	Connect(ctx context.Context) error
	Subscribe(channels ...signalmanpb.Channel) error
	Unsubscribe(channels ...signalmanpb.Channel) error
	Ping() error
	Events() <-chan *signalmanpb.SignalmanEvent
	Errors() <-chan error
	IsConnected() bool
	GetSubscribedChannels() []signalmanpb.Channel
	StartEventHandler(handler EventHandler)
	GetHubStats(ctx context.Context) (*signalmanpb.HubStats, error)
	SubscribeToStreams() error
	SubscribeToAnalytics() error
	SubscribeToSystem() error
	SubscribeToAll() error
	SubscribeToMessaging() error
	SubscribeToAI() error
}

var _ Interface = (*GRPCClient)(nil)

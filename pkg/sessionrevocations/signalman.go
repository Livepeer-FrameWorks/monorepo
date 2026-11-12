package sessionrevocations

import (
	"context"

	signalmanclient "github.com/Livepeer-FrameWorks/monorepo/pkg/clients/signalman"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	signalmanpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/signalman"
)

type Transport struct {
	Address       string
	ServiceToken  string
	AllowInsecure bool
	CAPath        string
	ServerName    string
	Logger        logging.Logger
}

// Connect starts background reconciliation and a service-only revocation stream.
// TLS configuration is checked synchronously; peer availability is not.
func Connect(ctx context.Context, cfg Config, transport Transport) (*Feed, func(), error) {
	dialer, err := signalmanclient.NewDialer(signalmanclient.DialerConfig{
		ServiceToken: transport.ServiceToken, AllowInsecure: transport.AllowInsecure,
		CACertFile: transport.CAPath, ServerName: transport.ServerName, Logger: transport.Logger, ConnsPerAddr: 1,
	})
	if err != nil {
		return nil, nil, err
	}
	cfg.Open = func(ctx context.Context) (signalmanclient.EventStream, error) {
		return dialer.Open(ctx, transport.Address, signalmanclient.StreamKey{Channel: signalmanpb.Channel_CHANNEL_SESSIONS})
	}
	feed := Start(ctx, cfg)
	return feed, func() { feed.Close(); _ = dialer.Close() }, nil
}

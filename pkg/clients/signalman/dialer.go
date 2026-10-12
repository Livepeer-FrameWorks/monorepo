package signalman

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/clients"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/grpcutil"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	signalmanpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/signalman"

	"google.golang.org/grpc"
	"google.golang.org/grpc/connectivity"
	"google.golang.org/grpc/metadata"
)

const (
	defaultConnsPerAddr = 4
	defaultOpenTimeout  = 5 * time.Second
	// roundRobinServiceConfig spreads streams across every Signalman replica a
	// service alias resolves to instead of pinning each connection to one.
	roundRobinServiceConfig = `{"loadBalancingConfig":[{"round_robin":{}}]}`
)

// ErrDialerClosed is returned when opening a stream on a closed Dialer.
var ErrDialerClosed = errors.New("signalman dialer closed")

// StreamKey identifies one upstream Signalman subscription stream: one channel
// for one tenant. Signalman scopes delivery by tenant only, so every subscriber
// of the same tenant and channel can share the stream. CHANNEL_PLATFORM
// streams have no tenant: Signalman admits that channel only on tenantless
// service streams.
type StreamKey struct {
	TenantID string
	Channel  signalmanpb.Channel
}

// EventStream is an open Signalman subscription for one StreamKey.
type EventStream interface {
	// Recv blocks for the next event. Subscription confirmations and pongs are
	// consumed internally.
	Recv() (*signalmanpb.SignalmanEvent, error)
	// Close ends the stream.
	Close()
}

// DialerConfig configures the connections a Dialer keeps to Signalman.
type DialerConfig struct {
	ServiceToken  string
	AllowInsecure bool
	CACertFile    string
	ServerName    string
	Logger        logging.Logger
	// ConnsPerAddr is the number of HTTP/2 connections kept per address. Streams
	// are spread across them so no single connection reaches the server's
	// concurrent-stream limit.
	ConnsPerAddr int
	// OpenTimeout bounds how long Open waits for a ready connection.
	OpenTimeout time.Duration
}

// Dialer opens single-channel Signalman subscription streams over a small pool
// of shared connections per address. Streams authenticate with the service
// token and carry their tenant as metadata.
type Dialer struct {
	cfg       DialerConfig
	transport grpc.DialOption

	mu     sync.Mutex
	pools  map[string]*connPool
	closed bool
}

type connPool struct {
	conns []*grpc.ClientConn
	next  atomic.Uint32
}

// NewDialer builds a Dialer. Connections are created lazily per address.
func NewDialer(cfg DialerConfig) (*Dialer, error) {
	if cfg.ConnsPerAddr < 1 {
		cfg.ConnsPerAddr = defaultConnsPerAddr
	}
	if cfg.OpenTimeout <= 0 {
		cfg.OpenTimeout = defaultOpenTimeout
	}
	transport, err := grpcutil.ClientTLS(grpcutil.ClientTLSConfig{
		CACertFile:        cfg.CACertFile,
		ServerName:        cfg.ServerName,
		DefaultServerName: DefaultServerName,
		AllowInsecure:     cfg.AllowInsecure,
	}, cfg.Logger)
	if err != nil {
		return nil, fmt.Errorf("configure Signalman gRPC TLS: %w", err)
	}
	return &Dialer{cfg: cfg, transport: transport, pools: make(map[string]*connPool)}, nil
}

// Open opens a stream subscribed to key.Channel on addr and returns once
// Signalman confirmed the subscription, so every event Signalman dispatches
// after Open returns reaches the stream. Events that arrive before the
// confirmation are kept for Recv. A confirmation that does not name the
// channel is a refusal. The stream lives until ctx is canceled, Close is
// called, or the server ends it.
func (d *Dialer) Open(ctx context.Context, addr string, key StreamKey) (EventStream, error) {
	pool, err := d.pool(addr)
	if err != nil {
		return nil, err
	}
	conn := pool.conns[int(pool.next.Add(1)-1)%len(pool.conns)]
	if readyErr := waitReady(ctx, conn, d.cfg.OpenTimeout); readyErr != nil {
		return nil, fmt.Errorf("signalman %s not ready: %w", addr, readyErr)
	}

	streamCtx, cancel := context.WithCancel(ctx)
	streamCtx = metadata.NewOutgoingContext(streamCtx, streamMetadata(d.cfg.ServiceToken, key))
	stream, err := signalmanpb.NewSignalmanServiceClient(conn).Subscribe(streamCtx)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("open signalman stream on %s: %w", addr, err)
	}
	subscribe := &signalmanpb.SubscribeRequest{Channels: []signalmanpb.Channel{key.Channel}}
	if key.TenantID != "" {
		subscribe.TenantId = &key.TenantID
	}
	if sendErr := stream.Send(&signalmanpb.ClientMessage{
		Message: &signalmanpb.ClientMessage_Subscribe{Subscribe: subscribe},
	}); sendErr != nil {
		cancel()
		return nil, fmt.Errorf("subscribe signalman stream on %s: %w", addr, sendErr)
	}
	pending, err := d.awaitConfirmation(stream, key.Channel, cancel)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("subscribe signalman %s stream on %s: %w", key.Channel, addr, err)
	}
	return &eventStream{stream: stream, cancel: cancel, logger: d.cfg.Logger, pending: pending}, nil
}

// awaitConfirmation reads stream until Signalman confirms the subscription
// to channel, within OpenTimeout, and returns the events read before it.
func (d *Dialer) awaitConfirmation(stream signalmanpb.SignalmanService_SubscribeClient, channel signalmanpb.Channel, cancel context.CancelFunc) ([]*signalmanpb.SignalmanEvent, error) {
	timer := time.AfterFunc(d.cfg.OpenTimeout, cancel)
	defer timer.Stop()
	var pending []*signalmanpb.SignalmanEvent
	for {
		msg, err := stream.Recv()
		if err != nil {
			return nil, fmt.Errorf("no subscription confirmation: %w", err)
		}
		switch m := msg.Message.(type) {
		case *signalmanpb.ServerMessage_SubscriptionConfirmed:
			if !slices.Contains(m.SubscriptionConfirmed.GetSubscribedChannels(), channel) {
				return nil, errors.New("subscription refused")
			}
			return pending, nil
		case *signalmanpb.ServerMessage_Event:
			if m.Event != nil {
				pending = append(pending, m.Event)
			}
		case *signalmanpb.ServerMessage_Error:
			if d.cfg.Logger != nil {
				d.cfg.Logger.WithFields(logging.Fields{
					"code":    m.Error.GetCode(),
					"message": m.Error.GetMessage(),
				}).Warn("Signalman stream reported an error")
			}
		}
	}
}

// Close closes every pooled connection. Open fails afterwards.
func (d *Dialer) Close() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.closed = true
	var errs []error
	for addr, pool := range d.pools {
		for _, conn := range pool.conns {
			if err := conn.Close(); err != nil {
				errs = append(errs, fmt.Errorf("close signalman connection to %s: %w", addr, err))
			}
		}
		delete(d.pools, addr)
	}
	return errors.Join(errs...)
}

func (d *Dialer) pool(addr string) (*connPool, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return nil, ErrDialerClosed
	}
	if pool, ok := d.pools[addr]; ok {
		return pool, nil
	}
	pool := &connPool{conns: make([]*grpc.ClientConn, 0, d.cfg.ConnsPerAddr)}
	for range d.cfg.ConnsPerAddr {
		conn, err := grpc.NewClient(
			addr,
			d.transport,
			grpc.WithDefaultServiceConfig(roundRobinServiceConfig),
			grpc.WithChainStreamInterceptor(clients.FailsafeStreamInterceptor("signalman", d.cfg.Logger)),
		)
		if err != nil {
			for _, opened := range pool.conns {
				_ = opened.Close()
			}
			return nil, fmt.Errorf("create signalman connection to %s: %w", addr, err)
		}
		pool.conns = append(pool.conns, conn)
	}
	d.pools[addr] = pool
	return pool, nil
}

func streamMetadata(serviceToken string, key StreamKey) metadata.MD {
	md := metadata.MD{}
	if serviceToken != "" {
		md.Set("authorization", "Bearer "+serviceToken)
	}
	if key.TenantID != "" {
		md.Set("x-tenant-id", key.TenantID)
	}
	return md
}

func waitReady(ctx context.Context, conn *grpc.ClientConn, timeout time.Duration) error {
	readyCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	conn.Connect()
	for {
		state := conn.GetState()
		if state == connectivity.Ready {
			return nil
		}
		if !conn.WaitForStateChange(readyCtx, state) {
			return readyCtx.Err()
		}
	}
}

type eventStream struct {
	stream signalmanpb.SignalmanService_SubscribeClient
	cancel context.CancelFunc
	logger logging.Logger
	// pending holds the events that arrived before the subscription
	// confirmation; Recv returns them first.
	pending []*signalmanpb.SignalmanEvent
}

func (s *eventStream) Recv() (*signalmanpb.SignalmanEvent, error) {
	if len(s.pending) > 0 {
		event := s.pending[0]
		s.pending = s.pending[1:]
		return event, nil
	}
	for {
		msg, err := s.stream.Recv()
		if err != nil {
			return nil, err
		}
		switch m := msg.Message.(type) {
		case *signalmanpb.ServerMessage_Event:
			if m.Event != nil {
				return m.Event, nil
			}
		case *signalmanpb.ServerMessage_Error:
			if s.logger != nil {
				s.logger.WithFields(logging.Fields{
					"code":    m.Error.GetCode(),
					"message": m.Error.GetMessage(),
				}).Warn("Signalman stream reported an error")
			}
		}
	}
}

func (s *eventStream) Close() {
	s.cancel()
}

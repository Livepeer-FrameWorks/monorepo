package grpc

import (
	"context"
	"io"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	signalmanpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/signalman"
)

// fakeSubscribeStream implements signalmanpb.SignalmanService_SubscribeServer
// (a grpc.BidiStreamingServer[ClientMessage, ServerMessage]) for in-process
// testing of the Subscribe loop. recvCh feeds client messages; closing it
// surfaces io.EOF (clean client disconnect). Every Send is captured and
// signalled on sentCh.
type fakeSubscribeStream struct {
	ctx    context.Context
	recvCh chan *signalmanpb.ClientMessage
	mu     sync.Mutex
	sent   []*signalmanpb.ServerMessage
	sentCh chan *signalmanpb.ServerMessage
}

func newFakeStream(ctx context.Context) *fakeSubscribeStream {
	return &fakeSubscribeStream{
		ctx:    ctx,
		recvCh: make(chan *signalmanpb.ClientMessage, 8),
		sentCh: make(chan *signalmanpb.ServerMessage, 32),
	}
}

func (f *fakeSubscribeStream) Recv() (*signalmanpb.ClientMessage, error) {
	msg, ok := <-f.recvCh
	if !ok {
		return nil, io.EOF
	}
	return msg, nil
}
func (f *fakeSubscribeStream) Send(m *signalmanpb.ServerMessage) error {
	f.mu.Lock()
	f.sent = append(f.sent, m)
	f.mu.Unlock()
	f.sentCh <- m
	return nil
}
func (f *fakeSubscribeStream) Context() context.Context     { return f.ctx }
func (f *fakeSubscribeStream) SetHeader(metadata.MD) error  { return nil }
func (f *fakeSubscribeStream) SendHeader(metadata.MD) error { return nil }
func (f *fakeSubscribeStream) SetTrailer(metadata.MD)       {}
func (f *fakeSubscribeStream) SendMsg(any) error            { return nil }
func (f *fakeSubscribeStream) RecvMsg(any) error            { return nil }

func recvWithin(t *testing.T, ch <-chan *signalmanpb.ServerMessage) *signalmanpb.ServerMessage {
	t.Helper()
	select {
	case msg := <-ch:
		return msg
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for a server message")
		return nil
	}
}

// TestSubscribe_ConfirmsThenReceivesBroadcast drives the full stream lifecycle:
// a subscribe request is confirmed, a tenant-scoped broadcast reaches the
// subscribed client, and closing the recv side (EOF) returns cleanly.
func TestSubscribe_ConfirmsThenReceivesBroadcast(t *testing.T) {
	s := NewSignalmanServer(logging.NewLoggerWithService("signalman-test"), newTestMetrics())

	ctx := context.WithValue(context.Background(), ctxkeys.KeyTenantID, "tenant-1")
	stream := newFakeStream(ctx)
	stream.recvCh <- &signalmanpb.ClientMessage{
		Message: &signalmanpb.ClientMessage_Subscribe{
			Subscribe: &signalmanpb.SubscribeRequest{Channels: []signalmanpb.Channel{signalmanpb.Channel_CHANNEL_STREAMS}},
		},
	}

	errCh := make(chan error, 1)
	go func() { errCh <- s.Subscribe(stream) }()

	// 1. Subscription confirmation lists the subscribed channel.
	conf := recvWithin(t, stream.sentCh).GetSubscriptionConfirmed()
	if conf == nil || len(conf.GetSubscribedChannels()) != 1 || conf.GetSubscribedChannels()[0] != signalmanpb.Channel_CHANNEL_STREAMS {
		t.Fatalf("unexpected subscription confirmation: %+v", conf)
	}

	// 2. A tenant-matched broadcast on the subscribed channel is delivered.
	tenant := "tenant-1"
	s.GetHub().broadcastEvent(&signalmanpb.SignalmanEvent{
		EventType: signalmanpb.EventType_EVENT_TYPE_CLIP_LIFECYCLE,
		Channel:   signalmanpb.Channel_CHANNEL_STREAMS,
		TenantId:  &tenant,
	})
	ev := recvWithin(t, stream.sentCh).GetEvent()
	if ev == nil || ev.GetChannel() != signalmanpb.Channel_CHANNEL_STREAMS {
		t.Fatalf("expected a STREAMS event, got %+v", ev)
	}

	// 3. EOF on the recv side ends the handler cleanly.
	close(stream.recvCh)
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("Subscribe returned %v, want nil on EOF", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Subscribe did not return after EOF")
	}
}

// TestSubscribe_TenantConnectionLimit pins that a tenant at its connection cap
// is rejected with ResourceExhausted before the receive loop starts.
func TestSubscribe_TenantConnectionLimit(t *testing.T) {
	s := NewSignalmanServer(logging.NewLoggerWithService("signalman-test"), newTestMetrics())
	s.GetHub().SetMaxConnectionsPerTenant(1)

	// Pre-register one connection for the tenant (at the cap).
	s.hub.mutex.Lock()
	s.hub.clients[&Client{tenantID: "tenant-1"}] = true
	s.hub.mutex.Unlock()

	ctx := context.WithValue(context.Background(), ctxkeys.KeyTenantID, "tenant-1")
	stream := newFakeStream(ctx)

	err := s.Subscribe(stream) // synchronous: rejected before the recv loop
	if status.Code(err) != codes.ResourceExhausted {
		t.Fatalf("Subscribe error = %v, want ResourceExhausted", err)
	}
}

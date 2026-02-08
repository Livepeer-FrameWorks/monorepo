package grpc

import (
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"frameworks/pkg/ctxkeys"
	"frameworks/pkg/logging"
	pb "frameworks/pkg/proto"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

type fakeSignalmanStream struct {
	ctx     context.Context
	recvCh  chan *pb.ClientMessage
	sendCh  chan *pb.ServerMessage
	recvErr error
	mu      sync.Mutex
	sendNil bool
}

func newFakeSignalmanStream(ctx context.Context) *fakeSignalmanStream {
	return &fakeSignalmanStream{
		ctx:    ctx,
		recvCh: make(chan *pb.ClientMessage, 8),
		sendCh: make(chan *pb.ServerMessage, 8),
	}
}

func (f *fakeSignalmanStream) Context() context.Context {
	return f.ctx
}

func (f *fakeSignalmanStream) Send(msg *pb.ServerMessage) error {
	if msg == nil {
		f.mu.Lock()
		f.sendNil = true
		f.mu.Unlock()
		return errors.New("nil message")
	}
	f.sendCh <- msg
	return nil
}

func (f *fakeSignalmanStream) Recv() (*pb.ClientMessage, error) {
	msg, ok := <-f.recvCh
	if !ok {
		if f.recvErr != nil {
			return nil, f.recvErr
		}
		return nil, io.EOF
	}
	return msg, nil
}

func (f *fakeSignalmanStream) SetHeader(metadata.MD) error {
	return nil
}

func (f *fakeSignalmanStream) SendHeader(metadata.MD) error {
	return nil
}

func (f *fakeSignalmanStream) SetTrailer(metadata.MD) {}

func (f *fakeSignalmanStream) SendMsg(interface{}) error {
	return nil
}

func (f *fakeSignalmanStream) RecvMsg(interface{}) error {
	return nil
}

func (f *fakeSignalmanStream) sawNilSend() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.sendNil
}

func waitForClients(t *testing.T, hub *Hub, want int) {
	t.Helper()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		hub.mutex.RLock()
		got := len(hub.clients)
		hub.mutex.RUnlock()

		if got == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}

	hub.mutex.RLock()
	got := len(hub.clients)
	hub.mutex.RUnlock()
	t.Fatalf("expected %d clients, got %d", want, got)
}

func receiveMessage(t *testing.T, stream *fakeSignalmanStream, timeout time.Duration) *pb.ServerMessage {
	t.Helper()

	select {
	case msg := <-stream.sendCh:
		return msg
	case <-time.After(timeout):
		t.Fatal("timed out waiting for server message")
		return nil
	}
}

func drainSubscriptionConfirmation(t *testing.T, stream *fakeSignalmanStream) {
	t.Helper()
	msg := receiveMessage(t, stream, 2*time.Second)
	if msg.GetSubscriptionConfirmed() == nil {
		t.Fatalf("expected subscription confirmation, got %T", msg.GetMessage())
	}
}

func TestSubscribeLifecycleRegistersAndCleansUp(t *testing.T) {
	logger := logging.NewLoggerWithService("signalman-test")
	server := NewSignalmanServer(logger, nil)

	ctx := context.Background()
	ctx = context.WithValue(ctx, ctxkeys.KeyTenantID, "tenant-a")
	ctx = context.WithValue(ctx, ctxkeys.KeyUserID, "user-1")

	stream := newFakeSignalmanStream(ctx)
	errCh := make(chan error, 1)
	go func() {
		errCh <- server.Subscribe(stream)
	}()

	stream.recvCh <- &pb.ClientMessage{
		Message: &pb.ClientMessage_Subscribe{
			Subscribe: &pb.SubscribeRequest{
				Channels: []pb.Channel{pb.Channel_CHANNEL_ANALYTICS},
			},
		},
	}

	waitForClients(t, server.hub, 1)
	drainSubscriptionConfirmation(t, stream)

	close(stream.recvCh)

	err := <-errCh
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}

	waitForClients(t, server.hub, 0)
}

func TestTenantIsolationAndSystemBroadcast(t *testing.T) {
	logger := logging.NewLoggerWithService("signalman-test")
	server := NewSignalmanServer(logger, nil)

	ctxTenant := context.Background()
	ctxTenant = context.WithValue(ctxTenant, ctxkeys.KeyTenantID, "tenant-a")
	ctxTenant = context.WithValue(ctxTenant, ctxkeys.KeyUserID, "user-a")

	ctxNoTenant := context.Background()

	tenantStream := newFakeSignalmanStream(ctxTenant)
	noTenantStream := newFakeSignalmanStream(ctxNoTenant)

	errCh := make(chan error, 2)
	go func() {
		errCh <- server.Subscribe(tenantStream)
	}()
	go func() {
		errCh <- server.Subscribe(noTenantStream)
	}()

	tenantStream.recvCh <- &pb.ClientMessage{
		Message: &pb.ClientMessage_Subscribe{
			Subscribe: &pb.SubscribeRequest{
				Channels: []pb.Channel{pb.Channel_CHANNEL_ANALYTICS, pb.Channel_CHANNEL_SYSTEM},
			},
		},
	}
	noTenantStream.recvCh <- &pb.ClientMessage{
		Message: &pb.ClientMessage_Subscribe{
			Subscribe: &pb.SubscribeRequest{
				Channels: []pb.Channel{pb.Channel_CHANNEL_SYSTEM},
			},
		},
	}

	waitForClients(t, server.hub, 2)
	drainSubscriptionConfirmation(t, tenantStream)
	drainSubscriptionConfirmation(t, noTenantStream)

	server.hub.BroadcastToTenant("tenant-a", pb.EventType_EVENT_TYPE_VIEWER_CONNECT, pb.Channel_CHANNEL_ANALYTICS, &pb.EventData{})
	tenantMsg := receiveMessage(t, tenantStream, 2*time.Second)
	if tenantMsg.GetEvent() == nil || tenantMsg.GetEvent().GetTenantId() != "tenant-a" {
		t.Fatalf("expected tenant-scoped event, got %v", tenantMsg.GetMessage())
	}

	select {
	case msg := <-noTenantStream.sendCh:
		t.Fatalf("expected no tenant event for unauthenticated stream, got %v", msg.GetMessage())
	case <-time.After(150 * time.Millisecond):
	}

	server.hub.BroadcastInfrastructure(pb.EventType_EVENT_TYPE_NODE_LIFECYCLE_UPDATE, &pb.EventData{})
	noTenantMsg := receiveMessage(t, noTenantStream, 2*time.Second)
	if noTenantMsg.GetEvent() == nil || noTenantMsg.GetEvent().Channel != pb.Channel_CHANNEL_SYSTEM {
		t.Fatalf("expected system event, got %v", noTenantMsg.GetMessage())
	}

	tenantSystemMsg := receiveMessage(t, tenantStream, 2*time.Second)
	if tenantSystemMsg.GetEvent() == nil || tenantSystemMsg.GetEvent().Channel != pb.Channel_CHANNEL_SYSTEM {
		t.Fatalf("expected system event for tenant stream, got %v", tenantSystemMsg.GetMessage())
	}

	close(tenantStream.recvCh)
	close(noTenantStream.recvCh)
	<-errCh
	<-errCh
	waitForClients(t, server.hub, 0)
}

func TestAbruptCloseCleanupAndReconnect(t *testing.T) {
	logger := logging.NewLoggerWithService("signalman-test")
	server := NewSignalmanServer(logger, nil)

	ctx := context.Background()
	ctx = context.WithValue(ctx, ctxkeys.KeyTenantID, "tenant-a")

	stream := newFakeSignalmanStream(ctx)
	stream.recvErr = errors.New("connection reset")

	errCh := make(chan error, 1)
	go func() {
		errCh <- server.Subscribe(stream)
	}()

	waitForClients(t, server.hub, 1)
	close(stream.recvCh)

	err := <-errCh
	if status.Code(err) != codes.Internal {
		t.Fatalf("expected internal error, got %v", err)
	}
	waitForClients(t, server.hub, 0)

	reconnectStream := newFakeSignalmanStream(ctx)
	reconnectErrCh := make(chan error, 1)
	go func() {
		reconnectErrCh <- server.Subscribe(reconnectStream)
	}()

	reconnectStream.recvCh <- &pb.ClientMessage{
		Message: &pb.ClientMessage_Subscribe{
			Subscribe: &pb.SubscribeRequest{
				Channels: []pb.Channel{pb.Channel_CHANNEL_ANALYTICS},
			},
		},
	}
	waitForClients(t, server.hub, 1)
	drainSubscriptionConfirmation(t, reconnectStream)

	server.hub.BroadcastToTenant("tenant-a", pb.EventType_EVENT_TYPE_VIEWER_CONNECT, pb.Channel_CHANNEL_ANALYTICS, &pb.EventData{})
	msg := receiveMessage(t, reconnectStream, 2*time.Second)
	if msg.GetEvent() == nil {
		t.Fatalf("expected reconnect stream to receive tenant event")
	}

	close(reconnectStream.recvCh)
	<-reconnectErrCh
	waitForClients(t, server.hub, 0)
}

func TestSendLoopStopsOnClosedChannel(t *testing.T) {
	logger := logging.NewLoggerWithService("signalman-test")
	stream := newFakeSignalmanStream(context.Background())
	client := &Client{
		stream: stream,
		send:   make(chan *pb.ServerMessage),
		done:   make(chan struct{}),
		logger: logger,
	}

	done := make(chan struct{})
	go func() {
		client.sendLoop()
		close(done)
	}()

	close(client.send)

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("sendLoop did not exit after send channel closed")
	}

	if stream.sawNilSend() {
		t.Fatal("sendLoop attempted to send nil message")
	}
}

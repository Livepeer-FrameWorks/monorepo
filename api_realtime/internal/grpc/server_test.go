package grpc

import (
	"context"
	"errors"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"frameworks/api_realtime/internal/metrics"
	"frameworks/pkg/ctxkeys"
	"frameworks/pkg/logging"
	"frameworks/pkg/middleware"
	pb "frameworks/pkg/proto"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/sirupsen/logrus"
	logrustest "github.com/sirupsen/logrus/hooks/test"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const grpcBufSize = 1024 * 1024

// --- Fakes ---

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

type failingStream struct{}

func (f *failingStream) SetHeader(metadata.MD) error  { return nil }
func (f *failingStream) SendHeader(metadata.MD) error { return nil }
func (f *failingStream) SetTrailer(metadata.MD)       {}
func (f *failingStream) Context() context.Context     { return context.Background() }
func (f *failingStream) Send(*pb.ServerMessage) error {
	return errors.New("send failed")
}
func (f *failingStream) Recv() (*pb.ClientMessage, error) { return nil, io.EOF }
func (f *failingStream) SendMsg(interface{}) error        { return nil }
func (f *failingStream) RecvMsg(interface{}) error        { return nil }

// --- Helpers ---

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

func newTestMetrics() *metrics.Metrics {
	return &metrics.Metrics{
		HubConnections: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{Name: "hub_connections", Help: "hub connections"},
			[]string{"channel"},
		),
		HubMessages: prometheus.NewCounterVec(
			prometheus.CounterOpts{Name: "hub_messages_total", Help: "hub messages"},
			[]string{"channel", "direction"},
		),
		MessageDeliveryLag: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{Name: "message_delivery_lag_seconds", Help: "delivery lag"},
			[]string{"channel", "type"},
		),
	}
}

func newBufConnClient(t *testing.T, server *SignalmanServer, serviceToken string) (pb.SignalmanServiceClient, func()) {
	t.Helper()

	lis := bufconn.Listen(grpcBufSize)
	grpcServer := grpc.NewServer(
		grpc.ChainStreamInterceptor(
			middleware.GRPCStreamAuthInterceptor(middleware.GRPCAuthConfig{
				ServiceToken: serviceToken,
				Logger:       logging.NewLogger(),
			}),
		),
	)
	pb.RegisterSignalmanServiceServer(grpcServer, server)

	go func() {
		_ = grpcServer.Serve(lis)
	}()

	conn, err := grpc.NewClient(
		"passthrough:///bufnet",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) {
			return lis.Dial()
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}

	cleanup := func() {
		_ = conn.Close()
		grpcServer.Stop()
		_ = lis.Close()
	}

	return pb.NewSignalmanServiceClient(conn), cleanup
}

// --- Tests (unit, fake-stream) ---

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

func TestHubBroadcastTenantIsolation(t *testing.T) {
	t.Parallel()

	logger := logging.NewLogger()
	hub := &Hub{
		clients: make(map[*Client]bool),
		logger:  logger,
	}

	tenantA := "tenant-a"
	tenantB := "tenant-b"

	clientA := &Client{
		channels: []pb.Channel{pb.Channel_CHANNEL_STREAMS},
		tenantID: tenantA,
		send:     make(chan *pb.ServerMessage, 2),
		logger:   logger,
	}
	clientB := &Client{
		channels: []pb.Channel{pb.Channel_CHANNEL_STREAMS},
		tenantID: tenantB,
		send:     make(chan *pb.ServerMessage, 2),
		logger:   logger,
	}
	systemClient := &Client{
		channels: []pb.Channel{pb.Channel_CHANNEL_SYSTEM},
		tenantID: tenantA,
		send:     make(chan *pb.ServerMessage, 2),
		logger:   logger,
	}

	hub.clients[clientA] = true
	hub.clients[clientB] = true
	hub.clients[systemClient] = true

	tenantEvent := &pb.SignalmanEvent{
		EventType: pb.EventType_EVENT_TYPE_STREAM_END,
		Channel:   pb.Channel_CHANNEL_STREAMS,
		TenantId:  &tenantA,
		Timestamp: timestamppb.New(time.Now()),
	}
	hub.broadcastEvent(tenantEvent)

	select {
	case <-clientA.send:
	default:
		t.Fatal("expected tenant A client to receive event")
	}

	select {
	case <-clientB.send:
		t.Fatal("tenant B client should not receive tenant A event")
	default:
	}

	infraEvent := &pb.SignalmanEvent{
		EventType: pb.EventType_EVENT_TYPE_NODE_LIFECYCLE_UPDATE,
		Channel:   pb.Channel_CHANNEL_SYSTEM,
		Timestamp: timestamppb.New(time.Now()),
	}
	hub.broadcastEvent(infraEvent)

	select {
	case <-systemClient.send:
	default:
		t.Fatal("expected system client to receive infrastructure event")
	}

	streamInfra := &pb.SignalmanEvent{
		EventType: pb.EventType_EVENT_TYPE_NODE_LIFECYCLE_UPDATE,
		Channel:   pb.Channel_CHANNEL_STREAMS,
		Timestamp: timestamppb.New(time.Now()),
	}
	hub.broadcastEvent(streamInfra)

	select {
	case <-clientA.send:
		t.Fatal("tenant client should not receive infrastructure event on non-system channel")
	default:
	}
}

func TestClientSubscribeSuppressesDuplicates(t *testing.T) {
	t.Parallel()

	logger := logging.NewLogger()
	client := &Client{
		channels: []pb.Channel{pb.Channel_CHANNEL_STREAMS},
		send:     make(chan *pb.ServerMessage, 1),
		logger:   logger,
	}

	client.handleSubscribe(&pb.SubscribeRequest{
		Channels: []pb.Channel{
			pb.Channel_CHANNEL_STREAMS,
			pb.Channel_CHANNEL_ANALYTICS,
			pb.Channel_CHANNEL_STREAMS,
		},
	})

	if len(client.channels) != 2 {
		t.Fatalf("expected 2 unique channels, got %d", len(client.channels))
	}

	if client.channels[0] != pb.Channel_CHANNEL_STREAMS || client.channels[1] != pb.Channel_CHANNEL_ANALYTICS {
		t.Fatalf("unexpected channel order: %#v", client.channels)
	}

	select {
	case msg := <-client.send:
		confirmation := msg.GetSubscriptionConfirmed()
		if confirmation == nil {
			t.Fatal("expected subscription confirmation message")
		}
		if len(confirmation.SubscribedChannels) != 2 {
			t.Fatalf("expected 2 subscribed channels in confirmation, got %d", len(confirmation.SubscribedChannels))
		}
	default:
		t.Fatal("expected subscription confirmation to be sent")
	}
}

func TestHubBroadcastOrdering(t *testing.T) {
	t.Parallel()

	logger := logging.NewLogger()
	hub := &Hub{
		clients: make(map[*Client]bool),
		logger:  logger,
	}

	tenantID := "tenant-order"
	client := &Client{
		channels: []pb.Channel{pb.Channel_CHANNEL_STREAMS},
		tenantID: tenantID,
		send:     make(chan *pb.ServerMessage, 2),
		logger:   logger,
	}
	hub.clients[client] = true

	first := &pb.SignalmanEvent{
		EventType: pb.EventType_EVENT_TYPE_STREAM_TRACK_LIST,
		Channel:   pb.Channel_CHANNEL_STREAMS,
		TenantId:  &tenantID,
		Timestamp: timestamppb.New(time.Now()),
	}
	second := &pb.SignalmanEvent{
		EventType: pb.EventType_EVENT_TYPE_STREAM_END,
		Channel:   pb.Channel_CHANNEL_STREAMS,
		TenantId:  &tenantID,
		Timestamp: timestamppb.New(time.Now().Add(time.Second)),
	}

	hub.broadcastEvent(first)
	hub.broadcastEvent(second)

	msg1 := <-client.send
	msg2 := <-client.send

	if msg1.GetEvent().EventType != pb.EventType_EVENT_TYPE_STREAM_TRACK_LIST {
		t.Fatalf("expected first event to be track list, got %v", msg1.GetEvent().EventType)
	}
	if msg2.GetEvent().EventType != pb.EventType_EVENT_TYPE_STREAM_END {
		t.Fatalf("expected second event to be stream end, got %v", msg2.GetEvent().EventType)
	}
}

func TestHubBroadcastBackpressureDropsWhenFull(t *testing.T) {
	t.Parallel()

	logger := logging.NewLogger()
	hub := &Hub{
		clients: make(map[*Client]bool),
		logger:  logger,
	}

	tenantID := "tenant-backpressure"
	client := &Client{
		channels: []pb.Channel{pb.Channel_CHANNEL_STREAMS},
		tenantID: tenantID,
		send:     make(chan *pb.ServerMessage, 1),
		logger:   logger,
	}
	hub.clients[client] = true

	client.send <- &pb.ServerMessage{
		Message: &pb.ServerMessage_Pong{
			Pong: &pb.Pong{TimestampMs: time.Now().UnixMilli()},
		},
	}

	event := &pb.SignalmanEvent{
		EventType: pb.EventType_EVENT_TYPE_STREAM_BUFFER,
		Channel:   pb.Channel_CHANNEL_STREAMS,
		TenantId:  &tenantID,
		Timestamp: timestamppb.New(time.Now()),
	}

	done := make(chan struct{})
	go func() {
		hub.broadcastEvent(event)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("broadcastEvent blocked with full client buffer")
	}

	if len(client.send) != 1 {
		t.Fatalf("expected buffer to remain full, got %d", len(client.send))
	}
}

// --- Tests (bufconn gRPC) ---

func TestSubscribeAuthFailure(t *testing.T) {
	logger := logging.NewLogger()
	server := NewSignalmanServer(logger, nil)
	client, cleanup := newBufConnClient(t, server, "service-token")
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	stream, err := client.Subscribe(ctx)
	if err == nil {
		_, err = stream.Recv()
	}
	if err == nil {
		t.Fatalf("expected auth error, got nil")
	}

	statusErr, ok := status.FromError(err)
	if !ok {
		t.Fatalf("expected gRPC status error, got %v", err)
	}
	if statusErr.Code() != codes.Unauthenticated {
		t.Fatalf("expected unauthenticated, got %v", statusErr.Code())
	}
}

func TestSubscribeTenantLimit(t *testing.T) {
	logger := logging.NewLogger()
	server := NewSignalmanServer(logger, nil)
	server.hub.SetMaxConnectionsPerTenant(1)
	client, cleanup := newBufConnClient(t, server, "service-token")
	defer cleanup()

	ctx1 := metadata.NewOutgoingContext(
		context.Background(),
		metadata.Pairs(
			"authorization", "Bearer service-token",
			"x-tenant-id", "tenant-123",
		),
	)
	ctx1, cancel1 := context.WithCancel(ctx1)
	defer cancel1()

	stream, err := client.Subscribe(ctx1)
	if err != nil {
		t.Fatalf("expected first subscribe to succeed, got %v", err)
	}

	ctx2 := metadata.NewOutgoingContext(
		context.Background(),
		metadata.Pairs(
			"authorization", "Bearer service-token",
			"x-tenant-id", "tenant-123",
		),
	)
	ctx2, cancel2 := context.WithTimeout(ctx2, 200*time.Millisecond)
	defer cancel2()

	stream2, err := client.Subscribe(ctx2)
	if err == nil {
		_, err = stream2.Recv()
	}
	if err == nil {
		t.Fatalf("expected tenant limit error, got nil")
	}
	statusErr, ok := status.FromError(err)
	if !ok {
		t.Fatalf("expected gRPC status error, got %v", err)
	}
	if statusErr.Code() != codes.ResourceExhausted {
		t.Fatalf("expected resource exhausted, got %v", statusErr.Code())
	}

	_ = stream.CloseSend()
}

// --- Tests (metrics, observability) ---

func TestBroadcastEventRecordsMetrics(t *testing.T) {
	logger := logrus.New()
	logger.SetOutput(io.Discard)
	m := newTestMetrics()
	hub := &Hub{
		clients: make(map[*Client]bool),
		logger:  logger,
		metrics: m,
	}

	client := &Client{
		channels: []pb.Channel{pb.Channel_CHANNEL_ANALYTICS},
		tenantID: "tenant-1",
		send:     make(chan *pb.ServerMessage, 1),
	}
	hub.clients[client] = true

	tenantID := "tenant-1"
	event := &pb.SignalmanEvent{
		EventType: pb.EventType_EVENT_TYPE_VIEWER_CONNECT,
		Channel:   pb.Channel_CHANNEL_ANALYTICS,
		TenantId:  &tenantID,
		Timestamp: timestamppb.New(time.Now().Add(-2 * time.Second)),
	}

	hub.broadcastEvent(event)

	select {
	case <-client.send:
	case <-time.After(500 * time.Millisecond):
		t.Fatalf("expected broadcast message")
	}

	count := testutil.ToFloat64(m.HubMessages.WithLabelValues("analytics", "viewer_connect"))
	if count != 1 {
		t.Fatalf("expected HubMessages counter to be 1, got %v", count)
	}
}

func TestBroadcastEventDefaultsMissingTimestamp(t *testing.T) {
	logger := logrus.New()
	logger.SetOutput(io.Discard)
	hub := &Hub{
		clients: make(map[*Client]bool),
		logger:  logger,
		metrics: newTestMetrics(),
	}

	client := &Client{
		channels: []pb.Channel{pb.Channel_CHANNEL_SYSTEM},
		send:     make(chan *pb.ServerMessage, 1),
	}
	hub.clients[client] = true

	event := &pb.SignalmanEvent{
		EventType: pb.EventType_EVENT_TYPE_NODE_LIFECYCLE_UPDATE,
		Channel:   pb.Channel_CHANNEL_SYSTEM,
	}

	hub.broadcastEvent(event)

	if event.Timestamp == nil {
		t.Fatalf("expected timestamp to be defaulted")
	}
}

func TestSendLoopLogsSendFailure(t *testing.T) {
	logger, hook := logrustest.NewNullLogger()
	client := &Client{
		stream: &failingStream{},
		send:   make(chan *pb.ServerMessage, 1),
		done:   make(chan struct{}),
		logger: logger,
	}

	go client.sendLoop()
	client.send <- &pb.ServerMessage{Message: &pb.ServerMessage_Pong{Pong: &pb.Pong{}}}

	deadline := time.After(500 * time.Millisecond)
	for hook.LastEntry() == nil {
		select {
		case <-deadline:
			t.Fatalf("expected log entry for send failure")
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}

	entry := hook.LastEntry()
	if entry.Message != "Failed to send message to client" {
		t.Fatalf("unexpected log message: %s", entry.Message)
	}
	if entry.Level != logrus.ErrorLevel {
		t.Fatalf("expected error level, got %v", entry.Level)
	}
	errValue := entry.Data["error"]
	if errValue == nil {
		t.Fatalf("expected error field, got nil")
	}
	if err, ok := errValue.(error); ok {
		if err.Error() != "send failed" {
			t.Fatalf("expected error field, got %v", errValue)
		}
	} else if errValue != "send failed" {
		t.Fatalf("expected error field, got %v", errValue)
	}
}

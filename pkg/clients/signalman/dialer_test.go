package signalman

import (
	"context"
	"errors"
	"net"
	"slices"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	signalmanpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/signalman"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

type recordingSignalmanServer struct {
	signalmanpb.UnimplementedSignalmanServiceServer
	metadata  chan metadata.MD
	subscribe chan *signalmanpb.SubscribeRequest
	events    []*signalmanpb.SignalmanEvent
	ended     chan struct{}
}

func (s *recordingSignalmanServer) Subscribe(stream signalmanpb.SignalmanService_SubscribeServer) error {
	md, _ := metadata.FromIncomingContext(stream.Context())
	s.metadata <- md
	msg, err := stream.Recv()
	if err != nil {
		return err
	}
	s.subscribe <- msg.GetSubscribe()
	if err := stream.Send(&signalmanpb.ServerMessage{
		Message: &signalmanpb.ServerMessage_SubscriptionConfirmed{
			SubscriptionConfirmed: &signalmanpb.SubscriptionConfirmation{SubscribedChannels: msg.GetSubscribe().GetChannels()},
		},
	}); err != nil {
		return err
	}
	for _, event := range s.events {
		if err := stream.Send(&signalmanpb.ServerMessage{Message: &signalmanpb.ServerMessage_Event{Event: event}}); err != nil {
			return err
		}
	}
	<-stream.Context().Done()
	close(s.ended)
	return nil
}

func waitFor[T any](t *testing.T, ch <-chan T, what string) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(3 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
	}
	var zero T
	return zero
}

func TestDialerOpensTenantScopedSingleChannelStream(t *testing.T) {
	lis, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	fake := &recordingSignalmanServer{
		metadata:  make(chan metadata.MD, 1),
		subscribe: make(chan *signalmanpb.SubscribeRequest, 1),
		ended:     make(chan struct{}),
		events: []*signalmanpb.SignalmanEvent{
			{EventType: signalmanpb.EventType_EVENT_TYPE_CLIENT_LIFECYCLE_UPDATE},
			{EventType: signalmanpb.EventType_EVENT_TYPE_VIEWER_CONNECT},
		},
	}
	srv := grpc.NewServer()
	signalmanpb.RegisterSignalmanServiceServer(srv, fake)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)

	dialer, err := NewDialer(DialerConfig{
		ServiceToken:  "svc-token",
		AllowInsecure: true,
		Logger:        logging.NewLoggerWithService("test"),
		ConnsPerAddr:  2,
		OpenTimeout:   3 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewDialer: %v", err)
	}
	t.Cleanup(func() { _ = dialer.Close() })

	stream, err := dialer.Open(context.Background(), lis.Addr().String(), StreamKey{
		TenantID: "tenant-a",
		Channel:  signalmanpb.Channel_CHANNEL_ANALYTICS,
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	md := waitFor(t, fake.metadata, "stream metadata")
	if got := md.Get("authorization"); !slices.Equal(got, []string{"Bearer svc-token"}) {
		t.Fatalf("authorization metadata = %v, want service token", got)
	}
	if got := md.Get("x-tenant-id"); !slices.Equal(got, []string{"tenant-a"}) {
		t.Fatalf("x-tenant-id metadata = %v, want tenant-a", got)
	}

	subscribe := waitFor(t, fake.subscribe, "subscribe request")
	if !slices.Equal(subscribe.GetChannels(), []signalmanpb.Channel{signalmanpb.Channel_CHANNEL_ANALYTICS}) || subscribe.GetTenantId() != "tenant-a" {
		t.Fatalf("subscribe request = %+v, want one analytics channel for tenant-a", subscribe)
	}

	for _, want := range fake.events {
		got, err := stream.Recv()
		if err != nil {
			t.Fatalf("Recv: %v", err)
		}
		if got.GetEventType() != want.GetEventType() {
			t.Fatalf("Recv event = %s, want %s (confirmation must be skipped)", got.GetEventType(), want.GetEventType())
		}
	}

	stream.Close()
	select {
	case <-fake.ended:
	case <-time.After(3 * time.Second):
		t.Fatal("closing the stream did not end the server-side subscription")
	}

	if err := dialer.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := dialer.Open(context.Background(), lis.Addr().String(), StreamKey{TenantID: "tenant-a"}); !errors.Is(err, ErrDialerClosed) {
		t.Fatalf("Open after Close err = %v, want ErrDialerClosed", err)
	}
}

// scriptedSignalmanServer answers a subscribe with the messages in script.
type scriptedSignalmanServer struct {
	signalmanpb.UnimplementedSignalmanServiceServer
	script []*signalmanpb.ServerMessage
}

func (s *scriptedSignalmanServer) Subscribe(stream signalmanpb.SignalmanService_SubscribeServer) error {
	if _, err := stream.Recv(); err != nil {
		return err
	}
	for _, msg := range s.script {
		if err := stream.Send(msg); err != nil {
			return err
		}
	}
	<-stream.Context().Done()
	return nil
}

func openScripted(t *testing.T, script ...*signalmanpb.ServerMessage) (EventStream, error) {
	t.Helper()
	lis, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := grpc.NewServer()
	signalmanpb.RegisterSignalmanServiceServer(srv, &scriptedSignalmanServer{script: script})
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)
	dialer, err := NewDialer(DialerConfig{AllowInsecure: true, Logger: logging.NewLoggerWithService("test"), OpenTimeout: 500 * time.Millisecond})
	if err != nil {
		t.Fatalf("NewDialer: %v", err)
	}
	t.Cleanup(func() { _ = dialer.Close() })
	return dialer.Open(context.Background(), lis.Addr().String(), StreamKey{TenantID: "tenant-a", Channel: signalmanpb.Channel_CHANNEL_EVENTS})
}

func confirmation(channels ...signalmanpb.Channel) *signalmanpb.ServerMessage {
	return &signalmanpb.ServerMessage{Message: &signalmanpb.ServerMessage_SubscriptionConfirmed{
		SubscriptionConfirmed: &signalmanpb.SubscriptionConfirmation{SubscribedChannels: channels},
	}}
}

func eventMessage(eventType signalmanpb.EventType) *signalmanpb.ServerMessage {
	return &signalmanpb.ServerMessage{Message: &signalmanpb.ServerMessage_Event{Event: &signalmanpb.SignalmanEvent{EventType: eventType}}}
}

// Open returns once Signalman confirmed the channel, and an event Signalman
// sent before its confirmation is still delivered first.
func TestDialerOpenWaitsForTheSubscriptionConfirmation(t *testing.T) {
	stream, err := openScripted(t,
		eventMessage(signalmanpb.EventType_EVENT_TYPE_TENANT_EVENT),
		confirmation(signalmanpb.Channel_CHANNEL_EVENTS),
		eventMessage(signalmanpb.EventType_EVENT_TYPE_STREAM_END),
	)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer stream.Close()
	for _, want := range []signalmanpb.EventType{signalmanpb.EventType_EVENT_TYPE_TENANT_EVENT, signalmanpb.EventType_EVENT_TYPE_STREAM_END} {
		got, err := stream.Recv()
		if err != nil || got.GetEventType() != want {
			t.Fatalf("Recv = %v, %v; want %s", got, err, want)
		}
	}
}

// A confirmation without the channel is a refusal, and a stream that is
// never confirmed fails Open within the open timeout.
func TestDialerOpenFailsWithoutAConfirmationOfTheChannel(t *testing.T) {
	if _, err := openScripted(t, confirmation(signalmanpb.Channel_CHANNEL_ALL)); err == nil {
		t.Fatal("Open succeeded although Signalman did not confirm the channel")
	}
	start := time.Now()
	if _, err := openScripted(t, eventMessage(signalmanpb.EventType_EVENT_TYPE_TENANT_EVENT)); err == nil {
		t.Fatal("Open succeeded without a confirmation")
	}
	if waited := time.Since(start); waited > 3*time.Second {
		t.Fatalf("Open waited %s for a confirmation, want about the 500ms open timeout", waited)
	}
}

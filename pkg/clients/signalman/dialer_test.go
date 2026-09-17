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

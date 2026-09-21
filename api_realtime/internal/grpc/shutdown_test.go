package grpc

import (
	"context"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	signalmanpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/signalman"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestShutdownEndsOpenSubscribeStreams(t *testing.T) {
	server := NewSignalmanServer(logging.NewLoggerWithService("signalman-test"), nil)
	ctx := context.WithValue(context.Background(), ctxkeys.KeyTenantID, "tenant-a")
	stream := newFakeSignalmanStream(ctx)
	defer close(stream.recvCh)

	errCh := make(chan error, 1)
	go func() { errCh <- server.Subscribe(stream) }()

	stream.recvCh <- &signalmanpb.ClientMessage{
		Message: &signalmanpb.ClientMessage_Ping{Ping: &signalmanpb.Ping{}},
	}
	select {
	case <-stream.sendCh:
	case <-time.After(2 * time.Second):
		t.Fatal("stream did not answer the ping before shutdown")
	}
	select {
	case err := <-errCh:
		t.Fatalf("stream ended before shutdown: %v", err)
	default:
	}

	server.Shutdown()
	server.Shutdown()

	select {
	case err := <-errCh:
		if status.Code(err) != codes.Unavailable {
			t.Fatalf("expected Unavailable after shutdown, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("open Subscribe stream did not end after Shutdown")
	}
}

func TestSubscribeAfterShutdownIsUnavailable(t *testing.T) {
	server := NewSignalmanServer(logging.NewLoggerWithService("signalman-test"), nil)
	server.Shutdown()
	stream := newFakeSignalmanStream(context.Background())
	defer close(stream.recvCh)

	errCh := make(chan error, 1)
	go func() { errCh <- server.Subscribe(stream) }()
	select {
	case err := <-errCh:
		if status.Code(err) != codes.Unavailable {
			t.Fatalf("expected Unavailable, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Subscribe after Shutdown did not return")
	}
}

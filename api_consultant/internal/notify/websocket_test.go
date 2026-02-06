package notify

import (
	"context"
	"net"
	"testing"
	"time"

	"frameworks/pkg/clients/decklog"
	"frameworks/pkg/logging"
	pb "frameworks/pkg/proto"

	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/emptypb"
)

type decklogServer struct {
	pb.UnimplementedDecklogServiceServer
	events chan *pb.ServiceEvent
}

func (s *decklogServer) SendServiceEvent(ctx context.Context, event *pb.ServiceEvent) (*emptypb.Empty, error) {
	s.events <- event
	return &emptypb.Empty{}, nil
}

func (s *decklogServer) SendEvent(ctx context.Context, event *pb.MistTrigger) (*emptypb.Empty, error) {
	return &emptypb.Empty{}, nil
}

func TestWebsocketNotifierSendsServiceEvent(t *testing.T) {
	listener, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer listener.Close()

	server := grpc.NewServer()
	decklogSvc := &decklogServer{events: make(chan *pb.ServiceEvent, 1)}
	pb.RegisterDecklogServiceServer(server, decklogSvc)

	go func() {
		_ = server.Serve(listener)
	}()
	defer server.Stop()

	client, err := decklog.NewBatchedClient(decklog.BatchedClientConfig{
		Target:        listener.Addr().String(),
		AllowInsecure: true,
		Timeout:       time.Second,
		Source:        "skipper",
	}, logging.NewLoggerWithService("skipper-test"))
	if err != nil {
		t.Fatalf("create decklog client: %v", err)
	}
	defer client.Close()

	notifier := NewWebsocketNotifier(client, logging.NewLoggerWithService("skipper-test"))
	err = notifier.Notify(context.Background(), Report{
		TenantID:        "tenant-a",
		InvestigationID: "report-123",
	})
	if err != nil {
		t.Fatalf("notify: %v", err)
	}

	select {
	case event := <-decklogSvc.events:
		if event.GetEventType() != websocketEventType {
			t.Fatalf("expected event type %s, got %s", websocketEventType, event.GetEventType())
		}
		if event.GetTenantId() != "tenant-a" {
			t.Fatalf("unexpected tenant id %s", event.GetTenantId())
		}
		if event.GetResourceId() != "report-123" {
			t.Fatalf("unexpected resource id %s", event.GetResourceId())
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for service event")
	}
}

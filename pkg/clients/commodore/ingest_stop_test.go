package commodore

import (
	"context"
	"net"
	"sync/atomic"
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type rotateOnlyServer struct {
	pb.UnimplementedStreamServiceServer
	rotations atomic.Int32
}

func (s *rotateOnlyServer) RefreshStreamKey(context.Context, *pb.RefreshStreamKeyRequest) (*pb.RefreshStreamKeyResponse, error) {
	s.rotations.Add(1)
	return &pb.RefreshStreamKeyResponse{StreamKey: "rotated"}, nil
}

func TestRotateAndEndRefusesOldServerWithoutRotating(t *testing.T) {
	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer()
	old := &rotateOnlyServer{}
	pb.RegisterStreamServiceServer(server, old)
	go func() { _ = server.Serve(listener) }()
	defer server.Stop()
	client, err := NewGRPCClient(GRPCConfig{GRPCAddr: listener.Addr().String(), AllowInsecure: true, Logger: logging.NewLogger()})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	_, err = client.RefreshStreamKeyAndEndIngest(t.Context(), "stream")
	if status.Code(err) != codes.Unimplemented || old.rotations.Load() != 0 {
		t.Fatalf("old server rotated without durable stop: error=%v rotations=%d", err, old.rotations.Load())
	}
}

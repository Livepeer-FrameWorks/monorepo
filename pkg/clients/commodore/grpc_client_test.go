package commodore

import (
	"context"
	"errors"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/cache"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"

	"google.golang.org/grpc"
)

func TestBuildValidateStreamKeyCacheKey(t *testing.T) {
	tests := []struct {
		name      string
		streamKey string
		clusterID string
		want      string
	}{
		{
			name:      "default route",
			streamKey: "sk_live_abc",
			clusterID: "",
			want:      "commodore:validate:sk_live_abc",
		},
		{
			name:      "cluster-specific route",
			streamKey: "sk_live_abc",
			clusterID: "cluster-us-west",
			want:      "commodore:validate:sk_live_abc:cluster:cluster-us-west",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := buildValidateStreamKeyCacheKey(tt.streamKey, tt.clusterID); got != tt.want {
				t.Fatalf("buildValidateStreamKeyCacheKey() = %q, want %q", got, tt.want)
			}
		})
	}
}

type validateStreamKeyStub struct {
	commodorepb.UnimplementedInternalServiceServer
	calls atomic.Int32
	resp  *commodorepb.ValidateStreamKeyResponse
	err   error
}

func (s *validateStreamKeyStub) ValidateStreamKey(ctx context.Context, req *commodorepb.ValidateStreamKeyRequest) (*commodorepb.ValidateStreamKeyResponse, error) {
	s.calls.Add(1)
	if s.err != nil {
		return nil, s.err
	}
	return s.resp, nil
}

func TestValidateStreamKeyBypassesCache(t *testing.T) {
	c := cache.New(cache.Options{
		TTL:                  time.Hour,
		StaleWhileRevalidate: time.Hour,
		NegativeTTL:          time.Minute,
		MaxEntries:           10,
	}, cache.MetricsHooks{})
	stale := &commodorepb.ValidateStreamKeyResponse{Valid: true, IsRecordingEnabled: false}
	if _, ok, err := c.Get(context.Background(), "commodore:validate:sk_live_abc", func(context.Context, string) (interface{}, bool, error) {
		return stale, true, nil
	}); err != nil || !ok {
		t.Fatalf("failed to seed cache: ok=%v err=%v", ok, err)
	}

	listener, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	server := grpc.NewServer()
	stub := &validateStreamKeyStub{
		resp: &commodorepb.ValidateStreamKeyResponse{Valid: true, IsRecordingEnabled: true},
	}
	commodorepb.RegisterInternalServiceServer(server, stub)
	go func() {
		_ = server.Serve(listener)
	}()
	t.Cleanup(func() {
		server.Stop()
		_ = listener.Close()
	})

	client, err := NewGRPCClient(GRPCConfig{
		GRPCAddr:      listener.Addr().String(),
		Logger:        logging.NewLogger(),
		Cache:         c,
		AllowInsecure: true,
	})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	got, err := client.ValidateStreamKey(context.Background(), "sk_live_abc")
	if err != nil {
		t.Fatalf("ValidateStreamKey: %v", err)
	}
	if !got.GetIsRecordingEnabled() {
		t.Fatalf("ValidateStreamKey returned stale cached DVR-disabled response")
	}
	if stub.calls.Load() != 1 {
		t.Fatalf("ValidateStreamKey called backend %d times, want 1", stub.calls.Load())
	}
}

func TestValidateStreamKeyFallsBackToCachedAdmissionOnBackendError(t *testing.T) {
	c := cache.New(cache.Options{
		TTL:                  time.Hour,
		StaleWhileRevalidate: time.Hour,
		NegativeTTL:          time.Minute,
		MaxEntries:           10,
	}, cache.MetricsHooks{})
	cached := &commodorepb.ValidateStreamKeyResponse{Valid: true, IsRecordingEnabled: true}
	c.SetDefault(buildValidateStreamKeyCacheKey("sk_live_abc", "demo-media"), cached)

	listener, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	server := grpc.NewServer()
	stub := &validateStreamKeyStub{err: errors.New("commodore unavailable")}
	commodorepb.RegisterInternalServiceServer(server, stub)
	go func() {
		_ = server.Serve(listener)
	}()
	t.Cleanup(func() {
		server.Stop()
		_ = listener.Close()
	})

	client, err := NewGRPCClient(GRPCConfig{
		GRPCAddr:      listener.Addr().String(),
		Logger:        logging.NewLogger(),
		Cache:         c,
		AllowInsecure: true,
	})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	got, err := client.ValidateStreamKey(context.Background(), "sk_live_abc", "demo-media")
	if err != nil {
		t.Fatalf("ValidateStreamKey returned backend error instead of cached admission: %v", err)
	}
	if got != cached {
		t.Fatalf("ValidateStreamKey did not return cached admission snapshot")
	}
	if stub.calls.Load() != 1 {
		t.Fatalf("ValidateStreamKey called backend %d times, want 1", stub.calls.Load())
	}
}

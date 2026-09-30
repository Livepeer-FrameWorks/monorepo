package periscope

import (
	"context"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/grpcutil/replicatest"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/sirupsen/logrus"
	"github.com/sirupsen/logrus/hooks/test"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/status"
)

// notServingClient returns a client whose only replica answers the health
// service with NOT_SERVING, so the channel never reaches READY.
func notServingClient(t *testing.T, cfg GRPCConfig) *GRPCClient {
	t.Helper()
	replica := replicatest.StartHealthReplica(t)
	replica.Health.SetServingStatus("", grpc_health_v1.HealthCheckResponse_NOT_SERVING)
	cfg.GRPCAddr = replica.Addr
	cfg.ServiceToken = "service-token"
	cfg.AllowInsecure = true
	client, err := NewGRPCClient(cfg)
	if err != nil {
		t.Fatalf("NewGRPCClient: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client
}

func TestReadyTimeoutFailsCallOnNotReadyChannel(t *testing.T) {
	t.Parallel()
	client := notServingClient(t, GRPCConfig{Timeout: 30 * time.Second, ReadyTimeout: 200 * time.Millisecond})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	start := time.Now()
	_, err := client.GetStreamStatus(ctx, "tenant-1", "live+stream")
	elapsed := time.Since(start)
	if status.Code(err) != codes.Unavailable {
		t.Fatalf("GetStreamStatus error = %v, want Unavailable", err)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("GetStreamStatus returned after %s, want within the ready timeout", elapsed)
	}
}

func TestSlowCallLogsMethodElapsedAndChannelState(t *testing.T) {
	t.Parallel()
	logger, hook := test.NewNullLogger()
	client := notServingClient(t, GRPCConfig{
		Timeout:           30 * time.Second,
		ReadyTimeout:      100 * time.Millisecond,
		SlowCallThreshold: time.Second,
		Logger:            logging.Logger(logger),
	})

	_, err := client.GetStreamStatus(context.Background(), "tenant-1", "live+stream")
	if status.Code(err) != codes.Unavailable {
		t.Fatalf("GetStreamStatus error = %v, want Unavailable", err)
	}
	var warn *logrus.Entry
	for _, entry := range hook.AllEntries() {
		if entry.Level == logrus.WarnLevel && entry.Message == "Slow or failed Periscope RPC" {
			warn = entry
		}
	}
	if warn == nil {
		t.Fatalf("no slow-call Warn logged; entries: %v", hook.AllEntries())
	}
	if warn.Data["method"] != "/periscope.StreamAnalyticsService/GetStreamStatus" {
		t.Fatalf("method = %v", warn.Data["method"])
	}
	if _, ok := warn.Data["elapsed_ms"].(int64); !ok {
		t.Fatalf("elapsed_ms = %#v, want int64", warn.Data["elapsed_ms"])
	}
	if state, _ := warn.Data["channel_state"].(string); state == "" || state == "READY" {
		t.Fatalf("channel_state = %q, want the non-ready state", state)
	}
	if warn.Data["code"] != codes.Unavailable.String() {
		t.Fatalf("code = %v, want Unavailable", warn.Data["code"])
	}
}

func TestSlowCallInterceptorSkipsFastSuccess(t *testing.T) {
	t.Parallel()
	logger, hook := test.NewNullLogger()
	invoker := func(context.Context, string, any, any, *grpc.ClientConn, ...grpc.CallOption) error { return nil }
	if err := periscopeSlowCallInterceptor(logging.Logger(logger), time.Second)(context.Background(), "/test.Method", nil, nil, nil, invoker); err != nil {
		t.Fatal(err)
	}
	if len(hook.AllEntries()) != 0 {
		t.Fatalf("fast call logged %v", hook.AllEntries())
	}
}

package clients

import (
	"context"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/grpcutil/replicatest"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/status"
)

// A Bridge Periscope read against a channel with no SERVING replica must fail
// within the request-path ready bound, not the 30s general call timeout.
func TestBridgePeriscopeReadFailsFastOnNotReadyChannel(t *testing.T) {
	replica := replicatest.StartHealthReplica(t)
	replica.Health.SetServingStatus("", grpc_health_v1.HealthCheckResponse_NOT_SERVING)
	ep := Endpoint{Addr: replica.Addr}
	sc, err := NewServiceClients(Config{
		ServiceToken:  "service-token",
		Logger:        logging.NewLogger(),
		AllowInsecure: true,
		Commodore:     ep,
		Periscope:     ep,
		Purser:        ep,
		Quartermaster: ep,
		Signalman:     ep,
		Decklog:       ep,
	})
	if err != nil {
		t.Fatalf("NewServiceClients: %v", err)
	}
	t.Cleanup(func() { _ = sc.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	start := time.Now()
	_, err = sc.Periscope.GetStreamStatus(ctx, "tenant-1", "live+stream")
	elapsed := time.Since(start)
	if status.Code(err) != codes.Unavailable {
		t.Fatalf("GetStreamStatus error = %v after %s, want Unavailable", err, elapsed)
	}
	if elapsed > 5*time.Second {
		t.Fatalf("GetStreamStatus returned after %s, want within a few seconds", elapsed)
	}
}

// Package replicatest runs stand-in gRPC replicas for tests of clients that
// balance across several replicas of one service.
package replicatest

import (
	"context"
	"net"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/health"
	"google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/peer"
)

// HealthReplica is a plaintext gRPC server that serves only the standard
// health service, standing in for one replica of a control-plane service.
type HealthReplica struct {
	Addr   string
	Server *grpc.Server
	Health *health.Server
}

// StartHealthReplica listens on a loopback port until the test ends.
func StartHealthReplica(t testing.TB) *HealthReplica {
	t.Helper()
	lis, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := grpc.NewServer()
	hs := health.NewServer()
	grpc_health_v1.RegisterHealthServer(srv, hs)
	go func() { _ = srv.Serve(lis) }() //nolint:errcheck // Serve returns when the test stops the server
	t.Cleanup(srv.Stop)
	return &HealthReplica{Addr: lis.Addr().String(), Server: srv, Health: hs}
}

// ReplicasServing runs n health checks over conn and counts them by the
// replica address that answered.
func ReplicasServing(conn *grpc.ClientConn, n int) (map[string]int, error) {
	client := grpc_health_v1.NewHealthClient(conn)
	seen := map[string]int{}
	for range n {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		var p peer.Peer
		_, err := client.Check(ctx, &grpc_health_v1.HealthCheckRequest{}, grpc.Peer(&p))
		cancel()
		if err != nil {
			return seen, err
		}
		seen[p.Addr.String()]++
	}
	return seen, nil
}

// RequireReplicaBalancing fails the test unless conn reaches every replica
// and, once a replica reports NOT_SERVING and another stops, keeps answering
// from the one left.
func RequireReplicaBalancing(t testing.TB, conn *grpc.ClientConn, a, b, c *HealthReplica) {
	t.Helper()
	waitFor := func(what string, ok func(map[string]int) bool) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		seen := map[string]int{}
		var err error
		for time.Now().Before(deadline) {
			var round map[string]int
			round, err = ReplicasServing(conn, 12)
			for addr, count := range round {
				seen[addr] += count
			}
			if err == nil && ok(seen) {
				return
			}
			if err == nil {
				seen = map[string]int{}
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatalf("%s: answers by replica %v (last error: %v)", what, seen, err)
	}
	waitFor("RPCs must reach every replica", func(seen map[string]int) bool {
		return seen[a.Addr] > 0 && seen[b.Addr] > 0 && seen[c.Addr] > 0
	})
	a.Health.SetServingStatus("", grpc_health_v1.HealthCheckResponse_NOT_SERVING)
	b.Server.Stop()
	waitFor("RPCs must leave a NOT_SERVING and a stopped replica", func(seen map[string]int) bool {
		return len(seen) == 1 && seen[c.Addr] > 0
	})
}

package periscope

import (
	"strings"
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/grpcutil/replicatest"
)

func TestNewGRPCClientBalancesAcrossHealthyReplicas(t *testing.T) {
	t.Parallel()
	a, b, c := replicatest.StartHealthReplica(t), replicatest.StartHealthReplica(t), replicatest.StartHealthReplica(t)
	client, err := NewGRPCClient(GRPCConfig{
		GRPCAddr:      strings.Join([]string{a.Addr, b.Addr, c.Addr}, ","),
		ServiceToken:  "service-token",
		AllowInsecure: true,
	})
	if err != nil {
		t.Fatalf("NewGRPCClient: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	replicatest.RequireReplicaBalancing(t, client.conn, a, b, c)
}

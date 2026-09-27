package grpcutil

import (
	"strings"
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/grpcutil/replicatest"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

func TestParseReplicaAddrs(t *testing.T) {
	t.Parallel()
	got := ParseReplicaAddrs(" 10.0.0.1:19002, ,10.0.0.2:19002,10.0.0.1:19002 ")
	want := []string{"10.0.0.1:19002", "10.0.0.2:19002"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("ParseReplicaAddrs = %v, want %v", got, want)
	}
}

func TestReplicaTargetKeepsSingleAddressOnDNS(t *testing.T) {
	t.Parallel()
	target, opts := ReplicaTarget("quartermaster.internal:19002")
	if target != "quartermaster.internal:19002" {
		t.Fatalf("target = %q, want the address unchanged for the dns resolver", target)
	}
	if len(opts) != 2 {
		t.Fatalf("single address wants only the service config and keepalive options, got %d options", len(opts))
	}
}

func TestReplicaTargetBalancesAndSkipsUnhealthyReplicas(t *testing.T) {
	t.Parallel()
	a, b, c := replicatest.StartHealthReplica(t), replicatest.StartHealthReplica(t), replicatest.StartHealthReplica(t)
	target, opts := ReplicaTarget(strings.Join([]string{a.Addr, b.Addr, c.Addr}, ","))
	conn, err := grpc.NewClient(target, append(opts, grpc.WithTransportCredentials(insecure.NewCredentials()))...)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	replicatest.RequireReplicaBalancing(t, conn, a, b, c)
}

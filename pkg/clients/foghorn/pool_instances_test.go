package foghorn

import (
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"google.golang.org/grpc/connectivity"
)

// A cell runs several Foghorns and Commodore round-robins across them under one
// cluster key. Dialing the sibling must not replace — and thereby close, cancelling
// every in-flight RPC on — the connection to the first instance.
func TestFoghornPoolKeepsOneConnectionPerInstance(t *testing.T) {
	pool := NewPool(PoolConfig{Logger: logging.NewLogger(), ServiceToken: "unit", AllowInsecure: true})
	defer pool.Close()

	first, err := pool.GetOrCreate("cell-a", "127.0.0.1:1")
	if err != nil {
		t.Fatal(err)
	}
	second, err := pool.GetOrCreate("cell-a", "127.0.0.1:2")
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("two instances must not share a connection")
	}
	again, err := pool.GetOrCreate("cell-a", "127.0.0.1:1")
	if err != nil {
		t.Fatal(err)
	}
	if again != first {
		t.Fatal("re-requesting the first instance must return its existing connection, not a fresh dial")
	}
	if first.conn.GetState() == connectivity.Shutdown || second.conn.GetState() == connectivity.Shutdown {
		t.Fatal("alternating instances must leave both connections open")
	}
	if _, ok := pool.Get("cell-a"); !ok {
		t.Fatal("Get by cluster must find one of the instance connections")
	}

	pool.Remove("cell-a")
	if first.conn.GetState() != connectivity.Shutdown || second.conn.GetState() != connectivity.Shutdown {
		t.Fatal("Remove must close every connection of the cluster")
	}
	if _, ok := pool.Get("cell-a"); ok {
		t.Fatal("Remove must forget the cluster")
	}
}

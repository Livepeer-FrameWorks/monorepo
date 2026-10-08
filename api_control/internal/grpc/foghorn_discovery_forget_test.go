package grpc

import (
	"testing"
	"time"
)

// TestForgetFoghornDiscoveryDropsAnAgedAnswer keeps the recovery path: once
// an answer is older than foghornDiscoveryMinAge, a failed delivery drops it
// and the next one discovers afresh.
func TestForgetFoghornDiscoveryDropsAnAgedAnswer(t *testing.T) {
	server := &CommodoreServer{}
	server.foghornDiscovery.Store("cell-b", &foghornDiscoveryEntry{addrs: []string{"10.0.0.1:18019"}, at: time.Now().Add(-foghornDiscoveryMinAge - time.Second)})
	server.forgetFoghornDiscovery("cell-b")
	if _, ok := server.foghornDiscovery.Load("cell-b"); ok {
		t.Fatal("an answer older than the minimum age survived a failed delivery")
	}

	server.foghornDiscovery.Store("cell-b", &foghornDiscoveryEntry{addrs: []string{"10.0.0.1:18019"}, at: time.Now()})
	server.forgetFoghornDiscovery("cell-b")
	if _, ok := server.foghornDiscovery.Load("cell-b"); !ok {
		t.Fatal("a fresh answer was dropped by a failure inside the burst window")
	}
}

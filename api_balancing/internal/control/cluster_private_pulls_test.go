package control

import (
	"context"
	"testing"
)

// ClusterAllowsPrivatePulls fails closed: an empty cluster id (or, in tests, an
// unwired Quartermaster client) reports no private-pull capability rather than
// defaulting to permissive.
func TestClusterAllowsPrivatePulls_FailsClosed(t *testing.T) {
	if ClusterAllowsPrivatePulls(context.Background(), "") {
		t.Fatal("empty cluster id must not be granted private-pull capability")
	}
	if ClusterAllowsPrivatePulls(context.Background(), "c1") {
		t.Fatal("no Quartermaster client must fail closed (no capability)")
	}
}

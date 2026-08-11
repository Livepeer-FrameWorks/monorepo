package control

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"

	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
)

// stubServedClusters is an injectable servedClustersAPI for loadServedClustersFrom tests.
type stubServedClusters struct {
	resp        *quartermasterpb.ListServiceClusterAssignmentsResponse
	err         error
	calls       int
	lastInst    string
	lastType    string
	hadDeadline bool
}

func (s *stubServedClusters) ListServiceClusterAssignments(ctx context.Context, instanceID, serviceType string) (*quartermasterpb.ListServiceClusterAssignmentsResponse, error) {
	s.calls++
	s.lastInst = instanceID
	s.lastType = serviceType
	_, s.hadDeadline = ctx.Deadline()
	return s.resp, s.err
}

// resetServedClusters swaps in a fresh empty sync.Map and restores original on cleanup.
func resetServedClusters(t *testing.T) {
	t.Helper()
	prev := servedClusters.Load()
	servedClusters.Store(&sync.Map{})
	t.Cleanup(func() { servedClusters.Store(prev) })
}

func setInstanceID(t *testing.T, id string) {
	t.Helper()
	prev := os.Getenv("FOGHORN_INSTANCE_ID")
	if id == "" {
		_ = os.Unsetenv("FOGHORN_INSTANCE_ID")
	} else {
		if err := os.Setenv("FOGHORN_INSTANCE_ID", id); err != nil {
			t.Fatalf("Setenv: %v", err)
		}
	}
	t.Cleanup(func() {
		if prev == "" {
			_ = os.Unsetenv("FOGHORN_INSTANCE_ID")
		} else {
			_ = os.Setenv("FOGHORN_INSTANCE_ID", prev)
		}
	})
}

func TestLoadServedClustersFrom_SuccessPopulatesWithCorrectArgs(t *testing.T) {
	resetServedClusters(t)
	setInstanceID(t, "foghorn-instance-1")
	stub := &stubServedClusters{resp: &quartermasterpb.ListServiceClusterAssignmentsResponse{
		ClusterIds: []string{"cluster-a", "cluster-b"},
	}}

	loadServedClustersFrom(stub)

	if stub.calls != 1 {
		t.Fatalf("expected one RPC call, got %d", stub.calls)
	}
	if stub.lastInst != "foghorn-instance-1" || stub.lastType != "foghorn" {
		t.Fatalf("unexpected RPC args: inst=%q type=%q", stub.lastInst, stub.lastType)
	}
	if !stub.hadDeadline {
		t.Fatalf("expected the RPC context to carry a deadline (5s timeout)")
	}
	if !isServedCluster("cluster-a") || !isServedCluster("cluster-b") {
		t.Fatalf("expected cluster-a and cluster-b to be served")
	}
	if isServedCluster("cluster-c") {
		t.Fatalf("expected cluster-c to NOT be served")
	}
}

func TestLoadServedClustersFrom_MissingInstanceIDIsNoOp(t *testing.T) {
	resetServedClusters(t)
	servedClusters.Load().Store("existing", true)
	setInstanceID(t, "")
	stub := &stubServedClusters{resp: &quartermasterpb.ListServiceClusterAssignmentsResponse{ClusterIds: []string{"cluster-a"}}}

	loadServedClustersFrom(stub)

	if stub.calls != 0 {
		t.Fatalf("expected no RPC call when instance ID is unset, got %d", stub.calls)
	}
	if !isServedCluster("existing") {
		t.Fatalf("expected snapshot untouched when instance ID is unset")
	}
}

func TestLoadServedClustersFrom_RPCErrorPreservesSnapshot(t *testing.T) {
	resetServedClusters(t)
	servedClusters.Load().Store("existing", true)
	setInstanceID(t, "foghorn-instance-1")
	stub := &stubServedClusters{err: errors.New("qm unavailable")}

	loadServedClustersFrom(stub)

	if stub.calls != 1 {
		t.Fatalf("expected exactly one RPC attempt, got %d", stub.calls)
	}
	if !isServedCluster("existing") {
		t.Fatalf("expected existing cluster to remain on RPC error")
	}
}

func TestLoadServedClustersFrom_NilResponsePreservesSnapshot(t *testing.T) {
	resetServedClusters(t)
	servedClusters.Load().Store("existing", true)
	setInstanceID(t, "foghorn-instance-1")
	stub := &stubServedClusters{resp: nil, err: nil}

	loadServedClustersFrom(stub)

	if !isServedCluster("existing") {
		t.Fatalf("expected existing cluster to remain on nil response")
	}
}

func TestLoadServedClusters_NilClientIsNoOp(t *testing.T) {
	resetServedClusters(t)
	servedClusters.Load().Store("existing", true)

	prev := servedClustersClient.Load()
	servedClustersClient.Store(nil)
	t.Cleanup(func() { servedClustersClient.Store(prev) })

	LoadServedClusters()

	if !isServedCluster("existing") {
		t.Fatalf("expected existing cluster to remain when client is nil")
	}
}

func TestApplyServedClustersRefresh_SwapsOutStaleEntriesAndPreservesLocal(t *testing.T) {
	resetServedClusters(t)

	prevLocal := localClusterID
	localClusterID = "local-primary"
	t.Cleanup(func() { localClusterID = prevLocal })

	applyServedClustersRefresh([]string{"cluster-a", "cluster-b", ""})
	if !isServedCluster("cluster-a") || !isServedCluster("cluster-b") {
		t.Fatalf("expected both clusters after first refresh")
	}
	if isServedCluster("") {
		t.Fatalf("empty cluster id must never be stored")
	}

	// cluster-a de-assigned; local-primary must survive even though it is never returned.
	applyServedClustersRefresh([]string{"cluster-b"})

	if isServedCluster("cluster-a") {
		t.Fatalf("expected cluster-a to be removed after refresh")
	}
	if !isServedCluster("cluster-b") {
		t.Fatalf("expected cluster-b to survive refresh")
	}
	if !isServedCluster("local-primary") {
		t.Fatalf("expected localClusterID to be preserved across refreshes")
	}
}

func TestIsServedCluster_EmptyString(t *testing.T) {
	if isServedCluster("") {
		t.Fatalf("expected empty string to return false")
	}
}

func TestServedClustersSnapshot(t *testing.T) {
	resetServedClusters(t)

	servedClusters.Load().Store("cluster-c", true)
	servedClusters.Load().Store("cluster-a", true)
	servedClusters.Load().Store("cluster-b", true)

	snap := ServedClustersSnapshot()

	if len(snap) != 3 {
		t.Fatalf("expected 3 clusters, got %d", len(snap))
	}
	if snap[0] != "cluster-a" || snap[1] != "cluster-b" || snap[2] != "cluster-c" {
		t.Fatalf("expected sorted [cluster-a cluster-b cluster-c], got %v", snap)
	}
}

package cmd

import (
	"testing"

	"frameworks/cli/pkg/inventory"
	pb "frameworks/pkg/proto"
)

// TestFilterRuntimeEnrolled_SplitsPendingAndInProgress covers the
// idempotency fix: a prior reconcile that wrote GitOps but failed to flip
// Quartermaster's origin leaves a host that is both in the manifest AND
// still runtime_enrolled. That host must land in the in-progress bucket
// (flip-only) rather than being dropped silently.
func TestFilterRuntimeEnrolled_SplitsPendingAndInProgress(t *testing.T) {
	manifest := &inventory.Manifest{
		Type:    "cluster",
		Profile: "test",
		Hosts: map[string]inventory.Host{
			"already-written": { // prior reconcile wrote this host
				Name:               "already-written",
				WireguardIP:        "10.88.0.20",
				WireguardPublicKey: "pub-already",
			},
		},
	}
	manifestClusters := map[string]bool{"cluster-test": true}

	qm := []*pb.InfrastructureNode{
		{
			NodeId:    "qm-already",
			NodeName:  "already-written",
			ClusterId: "cluster-test",
			// Still runtime_enrolled because the prior flip failed.
			EnrollmentOrigin: "runtime_enrolled",
		},
		{
			NodeId:           "qm-fresh",
			NodeName:         "fresh-node",
			ClusterId:        "cluster-test",
			EnrollmentOrigin: "runtime_enrolled",
		},
		{
			NodeId:           "qm-done",
			NodeName:         "done-node",
			ClusterId:        "cluster-test",
			EnrollmentOrigin: "adopted_local", // already promoted
		},
		{
			NodeId:           "qm-other-cluster",
			NodeName:         "other-cluster-node",
			ClusterId:        "some-other-cluster",
			EnrollmentOrigin: "runtime_enrolled",
		},
	}

	pending, inProgress := filterRuntimeEnrolled(qm, manifestClusters, manifest)

	if len(pending) != 1 || pending[0].Name != "fresh-node" {
		t.Fatalf("pending: expected [fresh-node], got %+v", pending)
	}
	if len(inProgress) != 1 || inProgress[0].Name != "already-written" {
		t.Fatalf("inProgress: expected [already-written], got %+v", inProgress)
	}
}

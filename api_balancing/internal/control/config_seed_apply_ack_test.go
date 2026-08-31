package control

import (
	"context"
	"errors"
	"testing"

	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

type recordingConfigSeedApplyAckWriter struct {
	nodeID    string
	clusterID string
	ack       *ipcpb.ConfigSeedApplyResult
	err       error
}

func (w *recordingConfigSeedApplyAckWriter) Enqueue(_ context.Context, nodeID, clusterID string, ack *ipcpb.ConfigSeedApplyResult) error {
	w.nodeID, w.clusterID, w.ack = nodeID, clusterID, ack
	return w.err
}

func TestAcceptConfigSeedApplyResultRequiresDurabilityAndAuthenticatedIdentity(t *testing.T) {
	ack := &ipcpb.ConfigSeedApplyResult{SeedVersion: 7}
	if err := acceptConfigSeedApplyResult(context.Background(), nil, NodeSession{}, ack); !errors.Is(err, errConfigSeedApplyAckDurabilityUnavailable) {
		t.Fatalf("nil writer error=%v", err)
	}
	writer := &recordingConfigSeedApplyAckWriter{}
	if err := acceptConfigSeedApplyResult(context.Background(), writer, NodeSession{CanonicalNodeID: "node-1"}, ack); !errors.Is(err, errConfigSeedApplyAckIdentityMissing) {
		t.Fatalf("missing cluster error=%v", err)
	}
}

func TestAcceptConfigSeedApplyResultPersistsCanonicalSessionIdentity(t *testing.T) {
	ack := &ipcpb.ConfigSeedApplyResult{SeedVersion: 7}
	writer := &recordingConfigSeedApplyAckWriter{}
	session := NodeSession{RawNodeID: "reported-node", CanonicalNodeID: "canonical-node", ClusterID: "cluster-1"}
	if err := acceptConfigSeedApplyResult(context.Background(), writer, session, ack); err != nil {
		t.Fatal(err)
	}
	if writer.nodeID != "canonical-node" || writer.clusterID != "cluster-1" || writer.ack != ack {
		t.Fatalf("persisted identity node=%q cluster=%q ack=%p", writer.nodeID, writer.clusterID, writer.ack)
	}
}

func TestAcceptConfigSeedApplyResultPropagatesPersistenceFailure(t *testing.T) {
	want := errors.New("database unavailable")
	writer := &recordingConfigSeedApplyAckWriter{err: want}
	err := acceptConfigSeedApplyResult(context.Background(), writer, NodeSession{RawNodeID: "node-1", ClusterID: "cluster-1"}, &ipcpb.ConfigSeedApplyResult{SeedVersion: 7})
	if !errors.Is(err, want) {
		t.Fatalf("error=%v, want %v", err, want)
	}
}

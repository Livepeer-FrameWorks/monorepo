package triggers

import (
	"context"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

func artifactClu(tenantID, artifactHash, nodeID, sessionID string) *ipcpb.ClientLifecycleUpdate {
	clu := sampleClu(tenantID, "", nodeID, sessionID)
	clu.ArtifactHash = &artifactHash
	return clu
}

// Uploaded VODs have no stream; two of them on one node must not share a
// batch, or the batch-level identity would attribute one to the other.
func TestBatcherKeepsArtifactsOnOneNodeApart(t *testing.T) {
	sender := &recordingSender{}
	b := newClientLifecycleBatcher(sender.send, testLogger(), nil)

	b.Add(artifactClu("tenant-A", "20261004093042f2da667dcd8ab1ee", "node-1", "sess-1"))
	b.Add(artifactClu("tenant-A", "20261004093042aaaaaaaaaaaaaaaa", "node-1", "sess-2"))
	if err := b.Shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown: %v", err)
	}

	got := sender.snapshot()
	if len(got) != 2 {
		t.Fatalf("expected one batch per artifact, got %d", len(got))
	}
	seen := map[string]bool{}
	for _, batch := range got {
		if batch.GetStreamId() != "" {
			t.Fatalf("artifact batch carries stream_id %q", batch.GetStreamId())
		}
		seen[batch.GetArtifactHash()] = true
	}
	if !seen["20261004093042f2da667dcd8ab1ee"] || !seen["20261004093042aaaaaaaaaaaaaaaa"] {
		t.Fatalf("batches do not carry their artifact hashes: %v", seen)
	}
}

// A viewer of an uploaded VOD has a tenant and an artifact but no stream; its
// client lifecycle sample is kept, carrying the artifact Foghorn resolved, not
// one the node asserted.
func TestHandleClientLifecycleKeepsArtifactOnlySample(t *testing.T) {
	sender := &recordingSender{}
	p := newTestProcessor(t)
	p.clientBatcher = newClientLifecycleBatcher(sender.send, testLogger(), nil)
	const resolvedHash = "20261004093042f2da667dcd8ab1ee"
	p.streamCache.Set("tenant-1:vodInternal", streamContext{TenantID: "tenant-1", ArtifactHash: resolvedHash}, time.Minute)

	clu := artifactClu("tenant-1", "20261004093042ffffffffffffffff", "node-1", "")
	clu.InternalName = "vod+vodInternal"
	_, _, err := p.handleClientLifecycleUpdate(&ipcpb.MistTrigger{
		TriggerType:    "CLIENT_LIFECYCLE_UPDATE",
		NodeId:         "node-1",
		TriggerPayload: &ipcpb.MistTrigger_ClientLifecycleUpdate{ClientLifecycleUpdate: clu},
	})
	if err != nil {
		t.Fatalf("handleClientLifecycleUpdate: %v", err)
	}
	if err := p.clientBatcher.Shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
	got := sender.snapshot()
	if len(got) != 1 || got[0].GetArtifactHash() != resolvedHash {
		t.Fatalf("artifact-only sample was not batched with its resolved artifact: %+v", got)
	}
}

// Playback of non-live content carries the artifact; a processing+ read is an
// internal transcode input of that artifact and does not.
func TestApplyResolvedStreamContextStampsArtifactOnPlaybackOnly(t *testing.T) {
	p := &Processor{logger: logging.NewLogger()}
	info := streamContext{TenantID: "tenant-1", ArtifactHash: "20261004093042f2da667dcd8ab1ee"}

	playback := &ipcpb.MistTrigger{TriggerType: "USER_NEW"}
	p.applyResolvedStreamContext(playback, "vod+someInternalName", info)
	if playback.GetArtifactHash() != info.ArtifactHash {
		t.Fatalf("vod+ playback artifact_hash = %q, want %q", playback.GetArtifactHash(), info.ArtifactHash)
	}
	if playback.GetStreamId() != "" {
		t.Fatalf("uploaded VOD playback got stream_id %q", playback.GetStreamId())
	}

	processing := &ipcpb.MistTrigger{TriggerType: "USER_NEW"}
	p.applyResolvedStreamContext(processing, "processing+20261004093042f2da667dcd8ab1ee", info)
	if processing.GetArtifactHash() != "" {
		t.Fatalf("processing+ read stamped artifact_hash %q", processing.GetArtifactHash())
	}
}

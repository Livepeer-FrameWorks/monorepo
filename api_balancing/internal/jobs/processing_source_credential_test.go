package jobs

import (
	"strings"
	"testing"
	"time"

	"frameworks/api_balancing/internal/appconfig"
	"frameworks/api_balancing/internal/control"
)

func TestAttachProcessingSourceCredential_BindsToTheServingNode(t *testing.T) {
	t.Cleanup(appconfig.Install(func() *appconfig.Foghorn {
		return &appconfig.Foghorn{BalancerCapabilitySecret: "unit-secret"}
	}))
	now := time.Unix(1_800_000_000, 0)

	local := map[string]string{"source_kind": "live", "source_stream_name": "live+stream-1"}
	attachProcessingSourceCredential(local, "tenant-1", "edge-node-1", "clip-1", now)
	token := local[control.ProcessingSourceCredentialParam]
	if !strings.HasPrefix(token, "fwproc.clip-1.") {
		t.Fatalf("expected a processing-source credential, got %q", token)
	}
	if _, ok := control.AcceptedProcessingSourceRead("/live+stream-1.mkv?token="+token, "tenant-1", "stream-1", "edge-node-1", now.Add(processingSourceCredentialTTL-time.Second)); !ok {
		t.Fatal("the dispatched node must be admitted to read the source until the TTL")
	}
	if _, ok := control.AcceptedProcessingSourceRead("/live+stream-1.mkv?token="+token, "tenant-1", "stream-1", "edge-node-1", now.Add(processingSourceCredentialTTL)); ok {
		t.Fatal("the credential must expire at the TTL")
	}

	// A cross-node read is served by the source node's Mist, so the credential
	// binds to that node, not to the node the job is dispatched to.
	remote := map[string]string{"source_kind": "live", "source_stream_name": "live+stream-1", "source_node_id": "edge-node-2"}
	attachProcessingSourceCredential(remote, "tenant-1", "edge-node-1", "clip-2", now)
	if _, ok := control.AcceptedProcessingSourceRead("/live+stream-1.mkv?token="+remote[control.ProcessingSourceCredentialParam], "tenant-1", "stream-1", "edge-node-2", now); !ok {
		t.Fatal("the source node must be admitted")
	}
	if _, ok := control.AcceptedProcessingSourceRead("/live+stream-1.mkv?token="+remote[control.ProcessingSourceCredentialParam], "tenant-1", "stream-1", "edge-node-1", now); ok {
		t.Fatal("the dispatched node must not be admitted for a cross-node read")
	}

	upload := map[string]string{"output_profiles": "[]"}
	attachProcessingSourceCredential(upload, "tenant-1", "edge-node-1", "vod-1", now)
	if _, present := upload[control.ProcessingSourceCredentialParam]; present {
		t.Fatal("a job without a node-local source must get no credential")
	}
}

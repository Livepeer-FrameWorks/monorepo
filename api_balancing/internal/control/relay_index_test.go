package control

import (
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	foghornfederationpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn_federation"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

func TestCrossClusterRelayPreservesPublishedIndex(t *testing.T) {
	previous := crossClusterDeps
	t.Cleanup(func() { SetCrossClusterArtifactDeps(previous) })
	fed := &fakeCrossClusterFedClient{responses: []*foghornfederationpb.PrepareArtifactResponse{{
		Ready: true, Url: "https://storage.example/media", DtshUrl: "https://storage.example/index.att-current", Format: "mkv",
	}}}
	SetCrossClusterArtifactDeps(makeCCDeps(fed, map[string]string{"origin": "peer:443"}))
	response := &ipcpb.RelayResolveResponse{}
	fillCrossClusterArtifact(t.Context(), &ipcpb.RelayResolveRequest{AssetHash: "hash"}, response, logging.NewLogger(), "origin", "tenant", "clip", nil)
	if response.GetMediaPresignedUrl() != "https://storage.example/media" || response.GetDtshPresignedGet() != "https://storage.example/index.att-current" {
		t.Fatalf("relay dropped media or index URL: %v", response)
	}
}

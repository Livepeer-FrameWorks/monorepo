package control

import (
	"context"
	"database/sql"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	foghornfederationpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn_federation"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

// A relay path names the container STREAM_SOURCE built it for. After
// processing replaced an uploaded .mp4 with an .mkv, a request still naming
// .mp4 must resolve to SOURCE_MISSING, never to the .mkv object (or the
// superseded upload) under the .mp4 name.
func TestRelayResolveRefusesExtensionOtherThanCurrentFormat(t *testing.T) {
	ctx := context.Background()

	t.Run("local synced row", func(t *testing.T) {
		for _, tc := range []struct {
			ext      string
			playable bool
		}{{ext: ".mp4"}, {ext: ".MKV", playable: true}, {ext: "", playable: true}} {
			mock, _, _ := setupArtifactTestDeps(t)
			entitlePlatformNode(t, "node-1")
			mock.ExpectQuery(`FROM foghorn.artifacts`).WithArgs("h").
				WillReturnRows(fileArtifactRows().AddRow(
					"s3://bucket/vod/h.mkv", int64(1234), "mkv", false, "",
					"synced", "", "", "t1", "vod", "", true))
			resp := &ipcpb.RelayResolveResponse{State: ipcpb.AssetState_ASSET_STATE_SOURCE_MISSING}
			fillFileArtifactResolve(ctx, &ipcpb.RelayResolveRequest{AssetHash: "h", AssetKind: "vod", Ext: tc.ext}, resp, "node-1", logging.NewLogger())
			if tc.playable {
				if resp.GetState() != ipcpb.AssetState_ASSET_STATE_PLAYABLE || resp.GetMediaPresignedUrl() == "" {
					t.Fatalf("ext %q of an mkv artifact: state=%s url=%q, want playable", tc.ext, resp.GetState(), resp.GetMediaPresignedUrl())
				}
				continue
			}
			if resp.GetState() != ipcpb.AssetState_ASSET_STATE_SOURCE_MISSING || resp.GetMediaPresignedUrl() != "" {
				t.Fatalf("ext %q of an mkv artifact: state=%s url=%q, want source-missing without a URL", tc.ext, resp.GetState(), resp.GetMediaPresignedUrl())
			}
		}
	})

	t.Run("local origin peer relay", func(t *testing.T) {
		mock, _, _ := setupArtifactTestDeps(t)
		entitlePlatformNode(t, "node-1")
		// Processing flipped format to mkv; s3_url still names the upload until
		// the processed output is synced.
		mock.ExpectQuery(`FROM foghorn.artifacts`).WithArgs("h").
			WillReturnRows(fileArtifactRows().AddRow(
				"s3://bucket/uploads/h.mp4", int64(1234), "mkv", false, "",
				"pending", "", "", "t1", "vod", "", true))
		mock.ExpectQuery(`FROM foghorn.artifact_nodes`).WithArgs("h", "node-1").
			WillReturnRows(sqlmock.NewRows([]string{"node_id", "base_url"}).AddRow("origin-1", "https://origin-1.example/view"))
		mock.ExpectQuery(`FROM foghorn.node_config_seeds`).WithArgs("origin-1").WillReturnError(sql.ErrNoRows)
		resp := &ipcpb.RelayResolveResponse{State: ipcpb.AssetState_ASSET_STATE_SOURCE_MISSING}
		fillFileArtifactResolve(ctx, &ipcpb.RelayResolveRequest{AssetHash: "h", AssetKind: "vod", Ext: ".mp4"}, resp, "node-1", logging.NewLogger())
		if resp.GetState() != ipcpb.AssetState_ASSET_STATE_SOURCE_MISSING || resp.GetPeerRelayUrl() != "" {
			t.Fatalf(".mp4 of an mkv artifact via peer relay: state=%s peer=%q, want source-missing without a peer URL", resp.GetState(), resp.GetPeerRelayUrl())
		}
	})

	t.Run("cross-cell origin answer", func(t *testing.T) {
		for _, tc := range []struct {
			ext      string
			playable bool
		}{{ext: ".mp4"}, {ext: ".mkv", playable: true}} {
			mock, _, _ := setupArtifactTestDeps(t)
			entitlePlatformNode(t, "edge-node-1")
			previous := crossClusterDeps
			t.Cleanup(func() { SetCrossClusterArtifactDeps(previous) })
			fed := &fakeCrossClusterFedClient{responses: []*foghornfederationpb.PrepareArtifactResponse{{
				Ready: true, Format: "mkv", Url: "https://s3.origin.example/vod/h.mkv?X-Amz-Signature=x", SizeBytes: 4321,
			}}}
			SetCrossClusterArtifactDeps(makeCCDeps(fed, map[string]string{"demo-media": "foghorn:18019"}))
			// The adopted pointer still carries the format copied before the
			// origin finished processing.
			mock.ExpectQuery(`FROM foghorn.artifacts`).WithArgs("h").
				WillReturnRows(fileArtifactRows().AddRow(
					"", int64(1234), "mp4", false, "",
					"pending", "demo-media", "", "t1", "vod", "", false))
			mock.ExpectQuery(`FROM foghorn.artifact_nodes`).WithArgs("h", "edge-node-1").WillReturnError(sql.ErrNoRows)

			resp := &ipcpb.RelayResolveResponse{State: ipcpb.AssetState_ASSET_STATE_SOURCE_MISSING}
			fillFileArtifactResolve(ctx, &ipcpb.RelayResolveRequest{AssetHash: "h", AssetKind: "vod", Ext: tc.ext}, resp, "edge-node-1", logging.NewLogger())
			if tc.playable {
				if resp.GetState() != ipcpb.AssetState_ASSET_STATE_PLAYABLE || resp.GetMediaPresignedUrl() == "" {
					t.Fatalf("ext %q of an origin mkv: state=%s url=%q, want playable", tc.ext, resp.GetState(), resp.GetMediaPresignedUrl())
				}
				continue
			}
			if resp.GetState() != ipcpb.AssetState_ASSET_STATE_SOURCE_MISSING || resp.GetMediaPresignedUrl() != "" {
				t.Fatalf("ext %q of an origin mkv: state=%s url=%q, want source-missing without a URL", tc.ext, resp.GetState(), resp.GetMediaPresignedUrl())
			}
		}
	})
}

// Upload reads name the uploaded source's own extension; the processing input
// is never compared with the artifact's playback format.
func TestRelayExtMatchesFormatIgnoresUploads(t *testing.T) {
	if !relayExtMatchesFormat(&ipcpb.RelayResolveRequest{AssetKind: "upload", Ext: ".mov"}, "mkv") {
		t.Fatal("an upload read was compared with the artifact format")
	}
	if relayExtMatchesFormat(&ipcpb.RelayResolveRequest{AssetKind: "clip", Ext: ".mp4"}, "mkv") {
		t.Fatal("a clip read naming another container was accepted")
	}
}

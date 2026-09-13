package control

import (
	"context"
	"database/sql"
	"path/filepath"
	"regexp"
	"testing"
	"time"

	"frameworks/api_balancing/internal/state"
	"github.com/DATA-DOG/go-sqlmock"
	clusterpeerpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/cluster_peer"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	federationpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn_federation"
)

type dvrStateClientFunc func(context.Context, string, string, *federationpb.PrepareArtifactRequest) (*federationpb.PrepareArtifactResponse, error)

func (f dvrStateClientFunc) PrepareArtifact(ctx context.Context, cluster, addr string, req *federationpb.PrepareArtifactRequest) (*federationpb.PrepareArtifactResponse, error) {
	return f(ctx, cluster, addr, req)
}

func TestDVRViewerDispatchResolvesOwnerStateWithExactIdentity(t *testing.T) {
	previousDB := db
	db = nil
	t.Cleanup(func() { db = previousDB })
	for _, tc := range []struct {
		name               string
		response           *federationpb.PrepareArtifactResponse
		revoked, wantError bool
	}{
		{name: "recording", response: &federationpb.PrepareArtifactResponse{Ready: true, DvrStatus: "recording", DvrRecordingNodeId: "origin-edge"}},
		{name: "completed", response: &federationpb.PrepareArtifactResponse{DvrStatus: "completed"}},
		{name: "unready", response: &federationpb.PrepareArtifactResponse{DvrStatus: "recording"}, wantError: true},
		{name: "wrong-parent", response: &federationpb.PrepareArtifactResponse{DvrStatus: "completed", StreamInternalName: "another-stream"}, wantError: true},
		{name: "redirect", response: &federationpb.PrepareArtifactResponse{DvrStatus: "completed", RedirectClusterId: "elsewhere"}, wantError: true},
		{name: "missing", response: &federationpb.PrepareArtifactResponse{}, wantError: true},
		{name: "revoked", response: &federationpb.PrepareArtifactResponse{}, revoked: true, wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resolution := &ContentResolution{LocalAuthority: true, ContentType: "dvr", ContentId: "public",
				InternalName: "dvr+recording", ArtifactHash: "hash", ArtifactID: "id", TenantId: "owner",
				StreamId: "parent-id", ParentStreamInternalName: "parent", OriginClusterID: "recording-cell",
				ClusterPeers: []*clusterpeerpb.TenantClusterPeer{{ClusterId: "recording-cell"}}}
			if tc.revoked {
				resolution.ClusterPeers = nil
			}
			tc.response.InternalName = "recording"
			if tc.response.StreamInternalName == "" {
				tc.response.StreamInternalName = "parent"
			}
			calls := 0
			client := dvrStateClientFunc(func(ctx context.Context, cluster, addr string, req *federationpb.PrepareArtifactRequest) (*federationpb.PrepareArtifactResponse, error) {
				calls++
				deadline, bounded := ctx.Deadline()
				if !bounded || time.Until(deadline) > 2*time.Second || cluster != "recording-cell" || addr != "owner:443" || req.GetTenantId() != "owner" || req.GetArtifactId() != "hash" || req.GetArtifactType() != "dvr" {
					t.Fatalf("unbounded or incorrectly scoped state request: %v", req)
				}
				return tc.response, nil
			})
			dispatch, err := ResolveDVRViewerDispatch(t.Context(), resolution, client, &fakeCrossClusterPeerResolver{addrs: map[string]string{"recording-cell": "owner:443"}})
			if (err != nil) != tc.wantError {
				t.Fatalf("dispatch=%+v error=%v", dispatch, err)
			}
			if tc.revoked && calls != 0 {
				t.Fatal("revoked owner contacted")
			}
			if !tc.wantError && (dispatch.Status != tc.response.DvrStatus || dispatch.InternalName != "recording" || dispatch.StreamInternalName != "parent") {
				t.Fatalf("owner state lost: %+v", dispatch)
			}
		})
	}
}

// TestIsActiveDVRStatus enforces the lifecycle status set that gates
// active DVR routing: any of these → the rolling DVR surface fed by
// the recording origin's local artefacts; anything else → the stopped
// DVR resolver falls back to the most-recent playable chapter's VOD
// playback ID. The set must stay in sync with foghorn.artifacts.status
// semantics (see schema/foghorn.sql).
func TestIsActiveDVRStatus(t *testing.T) {
	active := []string{"requested", "starting", "recording"}
	for _, s := range active {
		if !IsActiveDVRStatus(s) {
			t.Errorf("IsActiveDVRStatus(%q) = false, want true", s)
		}
	}
	// 'finalizing' is excluded: FinalizeDVR has claimed the stop, the
	// rolling manifest is closing, and the stopped-DVR resolver should
	// fall back to the latest playable chapter.
	notActive := []string{"", "finalizing", "completed", "completed_partial", "failed", "deleted", "ready", "anything"}
	for _, s := range notActive {
		if IsActiveDVRStatus(s) {
			t.Errorf("IsActiveDVRStatus(%q) = true, want false", s)
		}
	}
}

// TestLocalRollingDVRManifestPath verifies the on-disk layout
// constructed for the recording origin's rolling DVR manifest. The
// path shape must match what the Mist push writer produces (see
// dvr_manager.go: targetURI uses `<outputDir>/<dvr_hash>.m3u8` with
// outputDir = storage/dvr/<stream_id>/<dvr_hash>/).
func TestLocalRollingDVRManifestPath(t *testing.T) {
	sm := state.ResetDefaultManagerForTests()
	defer sm.Shutdown()
	const nodeID = "node-recording-1"
	const storageRoot = "/srv/frameworks/storage"
	sm.SetNodeStoragePaths(nodeID, storageRoot, "", "")

	cases := []struct {
		name       string
		streamName string
		dvrHash    string
		node       string
		want       string
	}{
		{
			name:       "happy path",
			streamName: "5eedfeed-11fe-ca57-feed-11feca570001",
			dvrHash:    "fedcba98",
			node:       nodeID,
			want:       filepath.Join(storageRoot, "dvr", "5eedfeed-11fe-ca57-feed-11feca570001", "fedcba98", "fedcba98.m3u8"),
		},
		{
			name:       "unknown node falls back to defaultStorageBase",
			streamName: "5eedfeed-11fe-ca57-feed-11feca570001",
			dvrHash:    "fedcba98",
			node:       "node-does-not-exist",
			want:       filepath.Join(defaultStorageBase, "dvr", "5eedfeed-11fe-ca57-feed-11feca570001", "fedcba98", "fedcba98.m3u8"),
		},
		{
			name:       "missing stream name returns empty",
			streamName: "",
			dvrHash:    "fedcba98",
			node:       nodeID,
			want:       "",
		},
		{
			name:       "missing dvr hash returns empty",
			streamName: "stream_abc",
			dvrHash:    "",
			node:       nodeID,
			want:       "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := LocalRollingDVRManifestPath(tc.streamName, tc.dvrHash, tc.node)
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestResolveLocalDVRArtifactDispatchUsesSignedIdentityAndDurableRuntime(t *testing.T) {
	localDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	previousDB, previousCommodore := db, CommodoreClient
	SetDB(localDB)
	CommodoreClient = nil
	t.Cleanup(func() {
		SetDB(previousDB)
		CommodoreClient = previousCommodore
		_ = localDB.Close()
	})

	mock.ExpectQuery(regexp.QuoteMeta("SELECT COALESCE(artifact_type, '')::text AS artifact_type")).
		WithArgs("dvr-hash-local").
		WillReturnRows(sqlmock.NewRows([]string{"artifact_type", "stream_id", "stream_internal_name"}).
			AddRow("dvr", "stream-id-local", "stream-internal-local"))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT status\nFROM foghorn.artifacts")).
		WithArgs("dvr-hash-local").
		WillReturnRows(sqlmock.NewRows([]string{"status"}).AddRow("recording"))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT node_id, COALESCE(is_orphaned, false)::boolean AS is_orphaned")).
		WithArgs("dvr-hash-local").
		WillReturnRows(sqlmock.NewRows([]string{"node_id", "is_orphaned"}).AddRow("edge-recording", false))

	artifact := &commodorepb.ResolveArtifactInternalNameResponse{
		Found: true, ArtifactHash: "dvr-hash-local", InternalName: "dvr-internal-local",
		StreamId: "stream-id-local", ContentType: "dvr", TenantId: "tenant-local",
	}
	dispatch, err := ResolveLocalDVRArtifactDispatch(context.Background(), artifact, "playback-local", false)
	if err != nil {
		t.Fatalf("ResolveLocalDVRArtifactDispatch: %v", err)
	}
	if dispatch == nil || dispatch.DVRHash != "dvr-hash-local" || dispatch.StreamID != "stream-id-local" ||
		dispatch.StreamInternalName != "stream-internal-local" || dispatch.PlaybackID != "playback-local" ||
		dispatch.Status != "recording" || dispatch.RecordingNode != "edge-recording" {
		t.Fatalf("local DVR dispatch = %+v", dispatch)
	}
	if CommodoreClient != nil {
		t.Fatal("test unexpectedly installed a central control-plane client")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestResolveLocalDVRArtifactDispatchUsesSignedParentNameWithoutLocalArtifactRow(t *testing.T) {
	localDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	previousDB, previousCommodore := db, CommodoreClient
	SetDB(localDB)
	CommodoreClient = nil
	t.Cleanup(func() {
		SetDB(previousDB)
		CommodoreClient = previousCommodore
		_ = localDB.Close()
	})

	mock.ExpectQuery(regexp.QuoteMeta("SELECT COALESCE(artifact_type, '')::text AS artifact_type")).
		WithArgs("remote-dvr-hash").WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT status\nFROM foghorn.artifacts")).
		WithArgs("remote-dvr-hash").WillReturnError(sql.ErrNoRows)

	dispatch, err := ResolveLocalDVRArtifactDispatch(context.Background(), &commodorepb.ResolveArtifactInternalNameResponse{
		Found: true, ArtifactHash: "remote-dvr-hash", InternalName: "remote-dvr-internal",
		StreamId: "parent-stream-id", ParentStreamInternalName: "parent-routing-name",
		ContentType: "dvr", TenantId: "tenant-remote",
	}, "remote-playback", false)
	if err != nil {
		t.Fatalf("ResolveLocalDVRArtifactDispatch: %v", err)
	}
	if dispatch == nil || dispatch.StreamInternalName != "parent-routing-name" || dispatch.StreamID != "parent-stream-id" {
		t.Fatalf("signed cross-cluster DVR dispatch = %+v", dispatch)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestResolveLocalDVRArtifactDispatchRejectsDurableIdentityConflict(t *testing.T) {
	localDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	previousDB := db
	SetDB(localDB)
	t.Cleanup(func() {
		SetDB(previousDB)
		_ = localDB.Close()
	})
	mock.ExpectQuery(regexp.QuoteMeta("SELECT COALESCE(artifact_type, '')::text AS artifact_type")).
		WithArgs("dvr-hash-conflict").
		WillReturnRows(sqlmock.NewRows([]string{"artifact_type", "stream_id", "stream_internal_name"}).
			AddRow("dvr", "different-stream-id", "stream-internal"))

	dispatch, err := ResolveLocalDVRArtifactDispatch(context.Background(), &commodorepb.ResolveArtifactInternalNameResponse{
		Found: true, ArtifactHash: "dvr-hash-conflict", InternalName: "dvr-internal",
		StreamId: "signed-stream-id", ContentType: "dvr", TenantId: "tenant-local",
	}, "playback-local", false)
	if err == nil || dispatch != nil {
		t.Fatalf("conflicting durable identity = dispatch:%+v err:%v", dispatch, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

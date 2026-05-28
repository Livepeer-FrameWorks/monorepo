package control

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

const sampleHash = "abcdef0123456789abcdef0123456789"
const sampleVODInternal = "vodartifact01"

func TestArtifactRuntimeName(t *testing.T) {
	cases := []struct {
		kind ArtifactKind
		intl string
		hash string
		want string
	}{
		{ArtifactKindVOD, "abc", "", "vod+abc"},
		{ArtifactKindClip, "abc", "", "vod+abc"},
		{ArtifactKindDVR, "abc", "", "dvr+abc"},
		{ArtifactKindProcessing, "", "h1", "processing+h1"},
		{ArtifactKindVOD, "", "h1", ""},      // VOD with no internal_name → empty
		{ArtifactKindProcessing, "", "", ""}, // processing with no hash → empty
	}
	for _, tc := range cases {
		if got := artifactRuntimeName(tc.kind, tc.intl, tc.hash); got != tc.want {
			t.Errorf("artifactRuntimeName(%v, %q, %q) = %q, want %q", tc.kind, tc.intl, tc.hash, got, tc.want)
		}
	}
}

func TestResolveArtifactByHash_HitsSQL(t *testing.T) {
	tdb, mock, _, _ := setupArtifactTestDepsWithDB(t)
	defer tdb.Close()

	mock.ExpectQuery(`SELECT artifact_hash, artifact_type`).
		WithArgs(sampleHash).
		WillReturnRows(sqlmock.NewRows([]string{
			"artifact_hash", "artifact_type", "internal_name", "stream_internal_name",
			"stream_id", "tenant_id", "status", "format",
			"origin_cluster_id", "storage_cluster_id", "has_thumbnails",
		}).AddRow(sampleHash, "vod", sampleVODInternal, "src_internal", "", "", "ready", "mp4", "", "", true))

	r := NewStreamRegistry(nil, "cluster-A", time.Minute)
	e, err := r.ResolveArtifactByHash(context.Background(), tdb, sampleHash)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if e.Kind != ArtifactKindVOD {
		t.Errorf("Kind = %v, want VOD", e.Kind)
	}
	if e.RuntimeName != "vod+"+sampleVODInternal {
		t.Errorf("RuntimeName = %q, want vod+%s", e.RuntimeName, sampleVODInternal)
	}
	if !e.HasThumbnails {
		t.Error("HasThumbnails = false, want true")
	}
}

func TestResolveArtifactByHash_DVRKind(t *testing.T) {
	tdb, mock, _, _ := setupArtifactTestDepsWithDB(t)
	defer tdb.Close()

	mock.ExpectQuery(`SELECT artifact_hash, artifact_type`).
		WithArgs(sampleHash).
		WillReturnRows(sqlmock.NewRows([]string{
			"artifact_hash", "artifact_type", "internal_name", "stream_internal_name",
			"stream_id", "tenant_id", "status", "format",
			"origin_cluster_id", "storage_cluster_id", "has_thumbnails",
		}).AddRow(sampleHash, "dvr", "dvrintl01", "src_internal", "", "", "recording", "m3u8", "", "", false))

	r := NewStreamRegistry(nil, "cluster-A", time.Minute)
	e, err := r.ResolveArtifactByHash(context.Background(), tdb, sampleHash)
	if err != nil {
		t.Fatal(err)
	}
	if e.Kind != ArtifactKindDVR {
		t.Errorf("Kind = %v, want DVR", e.Kind)
	}
	if e.RuntimeName != "dvr+dvrintl01" {
		t.Errorf("RuntimeName = %q", e.RuntimeName)
	}
}

func TestResolveArtifactByHash_MissingFailsClosed(t *testing.T) {
	tdb, mock, _, _ := setupArtifactTestDepsWithDB(t)
	defer tdb.Close()

	mock.ExpectQuery(`SELECT artifact_hash, artifact_type`).
		WithArgs("ghost").
		WillReturnRows(sqlmock.NewRows([]string{}))

	r := NewStreamRegistry(nil, "cluster-A", time.Minute)
	_, err := r.ResolveArtifactByHash(context.Background(), tdb, "ghost")
	if !errors.Is(err, ErrUnknownArtifact) {
		t.Errorf("err = %v, want ErrUnknownArtifact", err)
	}
}

func TestResolveByProcessingHash_PrefersFinalizedArtifact(t *testing.T) {
	tdb, mock, _, _ := setupArtifactTestDepsWithDB(t)
	defer tdb.Close()

	// Artifact row exists (job already finalized) — must short-circuit
	// before querying processing_jobs. We only set up one expectation.
	mock.ExpectQuery(`SELECT artifact_hash, artifact_type`).
		WithArgs(sampleHash).
		WillReturnRows(sqlmock.NewRows([]string{
			"artifact_hash", "artifact_type", "internal_name", "stream_internal_name",
			"stream_id", "tenant_id", "status", "format",
			"origin_cluster_id", "storage_cluster_id", "has_thumbnails",
		}).AddRow(sampleHash, "vod", sampleVODInternal, "src_internal", "", "", "ready", "mp4", "", "", true))

	r := NewStreamRegistry(nil, "cluster-A", time.Minute)
	e, err := r.ResolveByProcessingHash(context.Background(), tdb, sampleHash)
	if err != nil {
		t.Fatal(err)
	}
	if e.Kind != ArtifactKindVOD {
		t.Errorf("Kind = %v, want VOD (finalized wins)", e.Kind)
	}
	if e.RuntimeName != "vod+"+sampleVODInternal {
		t.Errorf("RuntimeName = %q, want vod+%s", e.RuntimeName, sampleVODInternal)
	}
}

func TestResolveByProcessingHash_FallsBackToJob(t *testing.T) {
	tdb, mock, _, _ := setupArtifactTestDepsWithDB(t)
	defer tdb.Close()

	// Artifact row missing → fall through to processing_jobs.
	mock.ExpectQuery(`SELECT artifact_hash, artifact_type`).
		WithArgs(sampleHash).
		WillReturnRows(sqlmock.NewRows([]string{}))
	mock.ExpectQuery(`SELECT job_id::text`).
		WithArgs(sampleHash).
		WillReturnRows(sqlmock.NewRows([]string{"job_id", "tenant_id", "status"}).
			AddRow("job-uuid-1", "tenant-1", "processing"))

	r := NewStreamRegistry(nil, "cluster-A", time.Minute)
	e, err := r.ResolveByProcessingHash(context.Background(), tdb, sampleHash)
	if err != nil {
		t.Fatal(err)
	}
	if e.Kind != ArtifactKindProcessing {
		t.Errorf("Kind = %v, want Processing", e.Kind)
	}
	if e.RuntimeName != "processing+"+sampleHash {
		t.Errorf("RuntimeName = %q", e.RuntimeName)
	}
}

func TestOnProcessingFinalize_EvictsAndFiresHook(t *testing.T) {
	r := NewStreamRegistry(nil, "cluster-A", time.Minute)

	// Pre-populate as if the job had been resolved.
	r.storeArtifact(ArtifactEntry{
		Kind:         ArtifactKindProcessing,
		ArtifactHash: sampleHash,
		RuntimeName:  "processing+" + sampleHash,
	})
	if _, ok := r.lookupArtifact(r.artifacts.byProcessingKey, sampleHash); !ok {
		t.Fatal("preload missing from byProcessingKey")
	}

	var hookFired string
	r.RegisterFinalizeHook(func(hash string) { hookFired = hash })

	r.OnProcessingFinalize(sampleHash)

	if _, ok := r.lookupArtifact(r.artifacts.byProcessingKey, sampleHash); ok {
		t.Error("processing entry not evicted")
	}
	if _, ok := r.lookupArtifact(r.artifacts.byHash, sampleHash); ok {
		t.Error("hash entry not evicted")
	}
	if hookFired != sampleHash {
		t.Errorf("hook fired with %q, want %q", hookFired, sampleHash)
	}
}

func TestUpsertFederatedSource_AppearsInSourceCache(t *testing.T) {
	r := NewStreamRegistry(nil, "cluster-A", time.Minute)
	r.UpsertFederatedSource(
		"cluster-B",
		StreamEntry{
			StreamID:     "peer-stream-1",
			InternalName: "peer_internal_native",
			PlaybackID:   "peer-playback",
			TenantID:     "tenant-X",
		},
		Location{
			IsLiveNow:   true,
			AdTimestamp: 1700000000,
		},
	)

	e, ok := r.lookup(r.byInt, "peer_internal_native")
	if !ok {
		t.Fatal("federated source not visible by internal_name")
	}
	if e.OriginClusterID != "cluster-B" {
		t.Errorf("OriginClusterID = %q (origin defaults to peer cluster)", e.OriginClusterID)
	}
	if !e.IsLiveAnywhere() {
		t.Error("IsLiveAnywhere = false, want true (peer ad said live)")
	}
	if e.IsLocallyOwned("cluster-A") {
		t.Error("IsLocallyOwned(cluster-A) = true; federated entries should not appear locally-owned")
	}
	peerLoc, ok := e.Locations["cluster-B"]
	if !ok {
		t.Fatal("no Location for cluster-B")
	}
	if !peerLoc.IsOrigin {
		t.Error("peer Location.IsOrigin = false, want true")
	}
}

// TestUpsertFederatedSource_RelayLocationIsNotOrigin guards multi-hop
// federation: when A originates and B re-advertises after replicating
// from A, the receiver (C) must record B's Location as IsOrigin=false
// so origin-pull cascades terminate at A, not at the nearest relay.
// Previously every peer Location was hard-coded IsOrigin=true.
func TestUpsertFederatedSource_RelayLocationIsNotOrigin(t *testing.T) {
	r := NewStreamRegistry(nil, "cluster-C", time.Minute)

	// Peer B advertises a stream whose actual origin is cluster A.
	r.UpsertFederatedSource("cluster-B", StreamEntry{
		InternalName:    "stream-relayed",
		OriginClusterID: "cluster-A",
	}, Location{IsLiveNow: true})

	e, ok := r.lookup(r.byInt, "stream-relayed")
	if !ok {
		t.Fatal("entry missing")
	}
	if e.OriginClusterID != "cluster-A" {
		t.Errorf("OriginClusterID = %q, want cluster-A (preserved from ad, not overwritten with peer)", e.OriginClusterID)
	}
	if loc := e.Locations["cluster-B"]; loc.IsOrigin {
		t.Error("Location[cluster-B].IsOrigin = true; want false (cluster-B is a relay)")
	}

	// Now A itself advertises the stream as its own origin. A's Location
	// should be marked IsOrigin=true alongside B's relay Location.
	r.UpsertFederatedSource("cluster-A", StreamEntry{
		InternalName:    "stream-relayed",
		OriginClusterID: "cluster-A",
	}, Location{IsLiveNow: true})

	e, _ = r.lookup(r.byInt, "stream-relayed")
	if loc := e.Locations["cluster-A"]; !loc.IsOrigin {
		t.Error("Location[cluster-A].IsOrigin = false; want true (cluster-A is the origin per the ad)")
	}
	if loc := e.Locations["cluster-B"]; loc.IsOrigin {
		t.Error("Location[cluster-B].IsOrigin flipped to true; should stay false")
	}
}

// TestSnapshotIncludesFederatedEntriesWithoutStreamID guards the
// /debug/stream-registry contract. StreamAdvertisement carries no
// stream_id field (foghorn_federation.proto), so federated entries
// land only in byInt. A Snapshot that ranges only byID would miss
// them entirely. Locks in the fix: Snapshot ranges both byID and byInt
// with internal_name-based dedup.
func TestSnapshotIncludesFederatedEntriesWithoutStreamID(t *testing.T) {
	r := NewStreamRegistry(nil, "cluster-C", time.Minute)

	// Federated entry from a peer with no StreamID (the ad proto omits it).
	r.UpsertFederatedSource("cluster-B", StreamEntry{
		InternalName:    "peer-stream-no-id",
		OriginClusterID: "cluster-A",
	}, Location{IsLiveNow: true})

	snaps := r.Snapshot()
	var found bool
	for _, s := range snaps {
		if s.InternalName == "peer-stream-no-id" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("Snapshot omitted federated entry with no StreamID; /debug/stream-registry would hide it")
	}
}

func TestUpsertFederatedSource_MultiplePeers(t *testing.T) {
	r := NewStreamRegistry(nil, "cluster-A", time.Minute)
	// Two peer clusters both advertise the same stream live.
	r.UpsertFederatedSource("cluster-B", StreamEntry{InternalName: "shared", PlaybackID: "p1"},
		Location{IsLiveNow: true, AdTimestamp: 100})
	r.UpsertFederatedSource("cluster-C", StreamEntry{InternalName: "shared"},
		Location{IsLiveNow: true, AdTimestamp: 200})

	e, ok := r.lookup(r.byInt, "shared")
	if !ok {
		t.Fatal("missing")
	}
	if len(e.Locations) != 2 {
		t.Fatalf("Locations len = %d, want 2", len(e.Locations))
	}
	feds := e.FederatedLocations("cluster-A")
	if len(feds) != 2 {
		t.Errorf("FederatedLocations len = %d", len(feds))
	}
	if !e.IsLiveAnywhere() {
		t.Error("IsLiveAnywhere = false, want true (both peers reported live)")
	}
}

func TestUpsertFederatedSource_WithdrawalDropsLocation(t *testing.T) {
	r := NewStreamRegistry(nil, "cluster-A", time.Minute)
	// Two peers advertise live, then one withdraws.
	r.UpsertFederatedSource("cluster-B", StreamEntry{InternalName: "shared", PlaybackID: "p1"},
		Location{IsLiveNow: true, AdTimestamp: 100})
	r.UpsertFederatedSource("cluster-C", StreamEntry{InternalName: "shared"},
		Location{IsLiveNow: true, AdTimestamp: 200})
	// Cluster-B withdraws.
	r.UpsertFederatedSource("cluster-B", StreamEntry{InternalName: "shared"},
		Location{IsLiveNow: false, AdTimestamp: 300})

	e, ok := r.lookup(r.byInt, "shared")
	if !ok {
		t.Fatal("expected entry to persist since cluster-C still live")
	}
	if _, ok := e.Locations["cluster-B"]; ok {
		t.Error("cluster-B Location should be dropped after withdrawal")
	}
	if _, ok := e.Locations["cluster-C"]; !ok {
		t.Error("cluster-C Location should remain")
	}

	// Now cluster-C withdraws too — entry itself should drop, including
	// PlaybackID reverse index.
	r.UpsertFederatedSource("cluster-C", StreamEntry{InternalName: "shared"},
		Location{IsLiveNow: false, AdTimestamp: 400})
	if _, ok := r.lookup(r.byInt, "shared"); ok {
		t.Error("entry should be dropped after last Location withdrawn")
	}
	if _, ok := r.lookup(r.byPlay, "p1"); ok {
		t.Error("playback-id reverse index should be cleared")
	}
}

func TestInvalidateArtifact_DropsCache(t *testing.T) {
	tdb, mock, _, _ := setupArtifactTestDepsWithDB(t)
	defer tdb.Close()

	rows := func() *sqlmock.Rows {
		return sqlmock.NewRows([]string{
			"artifact_hash", "artifact_type", "internal_name", "stream_internal_name",
			"stream_id", "tenant_id", "status", "format",
			"origin_cluster_id", "storage_cluster_id", "has_thumbnails",
		}).AddRow(sampleHash, "vod", sampleVODInternal, "src", "", "", "ready", "mp4", "", "", false)
	}
	mock.ExpectQuery(`SELECT artifact_hash, artifact_type`).WithArgs(sampleHash).WillReturnRows(rows())
	mock.ExpectQuery(`SELECT artifact_hash, artifact_type`).WithArgs(sampleHash).WillReturnRows(rows())

	r := NewStreamRegistry(nil, "cluster-A", time.Minute)
	if _, err := r.ResolveArtifactByHash(context.Background(), tdb, sampleHash); err != nil {
		t.Fatal(err)
	}
	r.InvalidateArtifact(sampleHash, sampleVODInternal)
	// Second call must hit SQL again, not cache.
	if _, err := r.ResolveArtifactByHash(context.Background(), tdb, sampleHash); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

package artifacts

import (
	"context"
	"errors"
	"fmt"
	foghornfederationpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn_federation"
	"strings"
	"testing"
)

type fakeS3 struct {
	deleteCalls       []string
	deletePrefixCalls []string
	deleteErr         error
	deletePrefixErr   error
}

func (f *fakeS3) Delete(_ context.Context, key string) error {
	f.deleteCalls = append(f.deleteCalls, key)
	return f.deleteErr
}
func (f *fakeS3) DeletePrefix(_ context.Context, prefix string) (int, error) {
	f.deletePrefixCalls = append(f.deletePrefixCalls, prefix)
	return 0, f.deletePrefixErr
}
func (f *fakeS3) ParseS3URL(s3URL string) (string, error) {
	rest, ok := strings.CutPrefix(s3URL, "s3://")
	if !ok {
		return "", fmt.Errorf("not an s3:// URL: %s", s3URL)
	}
	_, key, ok := strings.Cut(rest, "/")
	if !ok {
		return "", fmt.Errorf("no key in URL")
	}
	return key, nil
}

func TestCleaner_LocalClipUsesFormatColumn(t *testing.T) {
	s3 := &fakeS3{}
	c := &Cleaner{LocalCluster: "eu-west", S3: s3, LocalBackendID: "local-backend"}

	err := c.Delete(context.Background(), ArtifactRef{
		Hash:           "clip-1",
		Type:           "clip",
		TenantID:       "tenant-a",
		StreamInternal: "stream-x",
		Format:         "webm",
		BackendID:      "local-backend",
	})
	if err != nil {
		t.Fatalf("Delete err = %v", err)
	}
	// The main object AND its co-located .dtsh index are deleted.
	if len(s3.deleteCalls) != 2 {
		t.Fatalf("Delete calls = %d, want 2", len(s3.deleteCalls))
	}
	if got, want := s3.deleteCalls[0], "clips/tenant-a/stream-x/clip-1.webm"; got != want {
		t.Errorf("key = %q, want %q", got, want)
	}
	if got, want := s3.deleteCalls[1], "clips/tenant-a/stream-x/clip-1.webm.dtsh"; got != want {
		t.Errorf("dtsh key = %q, want %q", got, want)
	}
}

// Under the immutable-single-backend model, a local delete whose recorded backend_id EXACTLY matches the cell's
// current store sweeps that store. This is the common production case: the backend never changes and every local row
// carries the fingerprint (attributed at write, or once at boot for legacy rows), so the recorded id matches.
func TestCleaner_LocalDelete_MatchingBackendSweepsCurrentStore(t *testing.T) {
	current := &fakeS3{}
	c := &Cleaner{LocalCluster: "eu-west", S3: current, LocalBackendID: "the-backend"}

	err := c.Delete(context.Background(), ArtifactRef{
		Hash:           "clip-1",
		Type:           "clip",
		TenantID:       "tenant-a",
		StreamInternal: "stream-x",
		Format:         "webm",
		BackendID:      "the-backend", // the immutable current store
	})
	if err != nil {
		t.Fatalf("Delete err = %v", err)
	}
	// The current store got the media object + its .dtsh.
	if len(current.deleteCalls) != 2 {
		t.Fatalf("current store delete calls = %d, want 2 (media + dtsh)", len(current.deleteCalls))
	}
	if got, want := current.deleteCalls[0], "clips/tenant-a/stream-x/clip-1.webm"; got != want {
		t.Errorf("key = %q, want %q", got, want)
	}
}

// A recorded backend_id that does NOT match the cell's current store must fail closed with ErrRecordedBackendMismatch
// — never silently sweep the wrong (current) store. Under the immutable-backend invariant this cannot arise for a live
// cell; it is the defensive guard against a stale/foreign recorded id.
func TestCleaner_LocalDelete_MismatchedBackendFailsClosed(t *testing.T) {
	current := &fakeS3{}
	c := &Cleaner{LocalCluster: "eu-west", S3: current, LocalBackendID: "NEW-backend"}

	err := c.Delete(context.Background(), ArtifactRef{
		Hash:           "clip-1",
		Type:           "clip",
		TenantID:       "tenant-a",
		StreamInternal: "stream-x",
		Format:         "webm",
		BackendID:      "OLD-backend", // not this cell's current store
	})
	if !errors.Is(err, ErrRecordedBackendMismatch) {
		t.Fatalf("Delete err = %v, want ErrRecordedBackendMismatch", err)
	}
	if len(current.deleteCalls) != 0 {
		t.Fatalf("fail-closed must not sweep the current store, got %v", current.deleteCalls)
	}
}

// A recorded non-empty backend_id with an EMPTY LocalBackendID (an unwired local fingerprint) must ALSO fail closed:
// a missing local identity is not proof of a match, and must never license deleting from the current store.
func TestCleaner_LocalDelete_RecordedBackendWithoutLocalIdentityFailsClosed(t *testing.T) {
	current := &fakeS3{}
	c := &Cleaner{LocalCluster: "eu-west", S3: current} // LocalBackendID deliberately unset

	err := c.Delete(context.Background(), ArtifactRef{
		Hash:           "clip-2",
		Type:           "clip",
		TenantID:       "tenant-a",
		StreamInternal: "stream-x",
		Format:         "webm",
		BackendID:      "SOME-backend", // recorded, but this cell has no local fingerprint to match it against
	})
	if !errors.Is(err, ErrRecordedBackendMismatch) {
		t.Fatalf("Delete err = %v, want ErrRecordedBackendMismatch (missing local identity must fail closed)", err)
	}
	if len(current.deleteCalls) != 0 {
		t.Fatalf("an unmatched recorded backend must not sweep the current store, got %v", current.deleteCalls)
	}
}

func TestCleaner_LocalDVRUsesPrefix(t *testing.T) {
	s3 := &fakeS3{}
	c := &Cleaner{LocalCluster: "eu-west", S3: s3, LocalBackendID: "local-backend"}

	err := c.Delete(context.Background(), ArtifactRef{
		Hash:           "dvr-1",
		Type:           "dvr",
		TenantID:       "tenant-a",
		StreamInternal: "stream-x",
		BackendID:      "local-backend",
	})
	if err != nil {
		t.Fatalf("Delete err = %v", err)
	}
	if len(s3.deletePrefixCalls) != 1 {
		t.Fatalf("DeletePrefix calls = %d, want 1", len(s3.deletePrefixCalls))
	}
	if got, want := s3.deletePrefixCalls[0], "dvr/tenant-a/stream-x/dvr-1"; got != want {
		t.Errorf("prefix = %q, want %q", got, want)
	}
}

func TestCleaner_LocalVODUsesS3Key(t *testing.T) {
	s3 := &fakeS3{}
	c := &Cleaner{LocalCluster: "eu-west", S3: s3, LocalBackendID: "local-backend"}

	err := c.Delete(context.Background(), ArtifactRef{
		Hash:      "vod-1",
		Type:      "vod",
		TenantID:  "tenant-a",
		VODS3Key:  "vod/tenant-a/vod-1/movie.mp4",
		BackendID: "local-backend",
	})
	if err != nil {
		t.Fatalf("Delete err = %v", err)
	}
	// Main object + co-located .dtsh.
	if len(s3.deleteCalls) != 2 {
		t.Fatalf("Delete calls = %d, want 2", len(s3.deleteCalls))
	}
	if got, want := s3.deleteCalls[0], "vod/tenant-a/vod-1/movie.mp4"; got != want {
		t.Errorf("key = %q, want %q", got, want)
	}
	if got, want := s3.deleteCalls[1], "vod/tenant-a/vod-1/movie.mp4.dtsh"; got != want {
		t.Errorf("dtsh key = %q, want %q", got, want)
	}
}

// A freeze may authorize a PUT that lands AFTER the artifact is deleted. The deleted row retains the
// bound sync_object_key (PendingObjectKey); Delete must free THAT object too, in addition to the main
// deletion target, so an authorized-but-late upload can't leak.
func TestCleaner_DeletesPendingObjectKey(t *testing.T) {
	s3 := &fakeS3{}
	c := &Cleaner{LocalCluster: "eu-west", S3: s3, LocalBackendID: "local-backend"}

	err := c.Delete(context.Background(), ArtifactRef{
		Hash:             "vod-1",
		Type:             "vod",
		TenantID:         "tenant-a",
		VODS3Key:         "vod/tenant-a/vod-1/movie.mp4",
		PendingObjectKey: "vod/tenant-a/vod-1/vod-1.mkv", // a differently-formatted pending freeze target
		BackendID:        "local-backend",
	})
	if err != nil {
		t.Fatalf("Delete err = %v", err)
	}
	// Main object, its co-located .dtsh, then the pending freeze target.
	if len(s3.deleteCalls) != 3 {
		t.Fatalf("expected main + .dtsh + pending delete, got %v", s3.deleteCalls)
	}
	if s3.deleteCalls[0] != "vod/tenant-a/vod-1/movie.mp4" || s3.deleteCalls[1] != "vod/tenant-a/vod-1/movie.mp4.dtsh" || s3.deleteCalls[2] != "vod/tenant-a/vod-1/vod-1.mkv" {
		t.Fatalf("unexpected delete keys: %v", s3.deleteCalls)
	}
}

// When the row can no longer derive a main deletion target (e.g. a clip missing stream_internal_name)
// but a pending freeze object was bound, Delete must still free the pending object rather than surface
// ErrMissingTarget and leak it.
func TestCleaner_MissingTargetStillDeletesPendingObject(t *testing.T) {
	s3 := &fakeS3{}
	c := &Cleaner{LocalCluster: "eu-west", S3: s3, LocalBackendID: "local-backend"}

	err := c.Delete(context.Background(), ArtifactRef{
		Hash:     "clip-1",
		Type:     "clip",
		TenantID: "tenant-a",
		// StreamInternal + Format absent → resolveTarget returns ErrMissingTarget
		PendingObjectKey: "clips/tenant-a/stream-x/clip-1.mp4",
		BackendID:        "local-backend",
	})
	if err != nil {
		t.Fatalf("Delete err = %v (pending object must be freed despite missing main target)", err)
	}
	if len(s3.deleteCalls) != 1 || s3.deleteCalls[0] != "clips/tenant-a/stream-x/clip-1.mp4" {
		t.Fatalf("expected the pending object to be deleted, got %v", s3.deleteCalls)
	}
}

func TestCleaner_VODFallsBackToS3URL(t *testing.T) {
	// vod_metadata.s3_key absent, but foghorn.artifacts.s3_url is set
	// (legacy / non-upload paths). We must derive the key from the URL
	// and clean the bytes; never silently soft-delete + drop.
	s3 := &fakeS3{}
	c := &Cleaner{LocalCluster: "eu-west", S3: s3, LocalBackendID: "local-backend"}

	err := c.Delete(context.Background(), ArtifactRef{
		Hash:      "vod-1",
		Type:      "vod",
		TenantID:  "tenant-a",
		S3URL:     "s3://bucket/vod/tenant-a/vod-1/movie.mp4",
		BackendID: "local-backend",
	})
	if err != nil {
		t.Fatalf("Delete err = %v", err)
	}
	if len(s3.deleteCalls) != 2 || s3.deleteCalls[0] != "vod/tenant-a/vod-1/movie.mp4" || s3.deleteCalls[1] != "vod/tenant-a/vod-1/movie.mp4.dtsh" {
		t.Errorf("deleteCalls = %v", s3.deleteCalls)
	}
}

func TestCleaner_VODWithoutRecordedKeyFailsClosed(t *testing.T) {
	// Deletion consumes a RECORDED key only (vod_metadata.s3_key / s3_url / the freeze descriptor). A VOD
	// with just tenant+hash+format and no recorded key is NOT deleted at a reconstructed key — it fails
	// closed with ErrMissingTarget so the purge job never frees a guessed object.
	s3 := &fakeS3{}
	c := &Cleaner{LocalCluster: "eu-west", S3: s3, LocalBackendID: "local-backend"}

	err := c.Delete(context.Background(), ArtifactRef{
		Hash:     "vod-1",
		Type:     "vod",
		TenantID: "tenant-a",
		Format:   "mp4",
	})
	if !errors.Is(err, ErrMissingTarget) {
		t.Fatalf("expected ErrMissingTarget, got %v", err)
	}
	if len(s3.deleteCalls) != 0 {
		t.Fatalf("must not delete a reconstructed key, got %v", s3.deleteCalls)
	}
}

func TestCleaner_RemoteOnlyDeploymentNoLocalS3(t *testing.T) {
	// Storage-via-federation: this Foghorn has no local S3, but the
	// delegate is wired. Remote rows must still get cleaned.
	called := false
	delegate := func(_ context.Context, _ string, req *foghornfederationpb.DeleteStorageObjectsRequest) (*foghornfederationpb.DeleteStorageObjectsResponse, error) {
		if !called && req.GetS3Key() != "vod/tenant-a/vod-1/movie.mp4" { // first call = main object (a second call cleans .dtsh)
			t.Errorf("delegate received key = %q", req.GetS3Key())
		}
		called = true
		return &foghornfederationpb.DeleteStorageObjectsResponse{Accepted: true}, nil
	}
	c := &Cleaner{LocalCluster: "eu-west", S3: nil, Delegate: delegate}

	err := c.Delete(context.Background(), ArtifactRef{
		Hash:             "vod-1",
		Type:             "vod",
		TenantID:         "tenant-a",
		VODS3Key:         "vod/tenant-a/vod-1/movie.mp4",
		StorageClusterID: "us-east",
	})
	if err != nil {
		t.Fatalf("Delete err = %v", err)
	}
	if !called {
		t.Errorf("delegate not called for remote-only deployment")
	}
}

func TestCleaner_LocalDeleteWithNilS3ReturnsTypedErr(t *testing.T) {
	// Storage-via-federation deployment receives a request whose row
	// resolves to "local" (no storage_cluster_id, no origin_cluster_id).
	// We can't free anything without local S3; surface a typed error so
	// the purge job keeps the row and the gRPC handler reports cleanup
	// pending.
	c := &Cleaner{LocalCluster: "eu-west", S3: nil, LocalBackendID: "local-backend"}
	err := c.Delete(context.Background(), ArtifactRef{
		Hash:      "vod-1",
		Type:      "vod",
		TenantID:  "tenant-a",
		VODS3Key:  "vod/tenant-a/vod-1/movie.mp4",
		BackendID: "local-backend",
	})
	if !errors.Is(err, ErrLocalS3Missing) {
		t.Fatalf("err = %v, want ErrLocalS3Missing", err)
	}
}

func TestCleaner_MissingFieldsReturnTypedError(t *testing.T) {
	s3 := &fakeS3{}
	c := &Cleaner{LocalCluster: "eu-west", S3: s3, LocalBackendID: "local-backend"}

	cases := []struct {
		name string
		ref  ArtifactRef
	}{
		{"clip without format", ArtifactRef{Hash: "h", Type: "clip", TenantID: "t", StreamInternal: "s"}},
		{"clip without stream", ArtifactRef{Hash: "h", Type: "clip", TenantID: "t", Format: "mp4"}},
		{"dvr without stream", ArtifactRef{Hash: "h", Type: "dvr", TenantID: "t"}},
		{"vod without s3_key", ArtifactRef{Hash: "h", Type: "vod", TenantID: "t"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := c.Delete(context.Background(), tc.ref)
			if !errors.Is(err, ErrMissingTarget) {
				t.Fatalf("err = %v, want ErrMissingTarget", err)
			}
		})
	}
	if len(s3.deleteCalls)+len(s3.deletePrefixCalls) != 0 {
		t.Errorf("S3 should not be called when target is missing")
	}
}

func TestCleaner_UnsupportedTypeReturnsTypedError(t *testing.T) {
	c := &Cleaner{LocalCluster: "eu-west", S3: &fakeS3{}}
	err := c.Delete(context.Background(), ArtifactRef{Hash: "h", Type: "thumbnail", TenantID: "t"})
	if !errors.Is(err, ErrUnsupportedType) {
		t.Fatalf("err = %v, want ErrUnsupportedType", err)
	}
}

func TestCleaner_RemoteUsesDelegateNotLocalS3(t *testing.T) {
	s3 := &fakeS3{}
	var got *foghornfederationpb.DeleteStorageObjectsRequest
	delegate := func(_ context.Context, target string, req *foghornfederationpb.DeleteStorageObjectsRequest) (*foghornfederationpb.DeleteStorageObjectsResponse, error) {
		if got == nil { // capture the MAIN object delete (the co-located .dtsh is a second delegate call)
			got = req
		}
		if target != "us-east" {
			t.Errorf("delegate target = %q, want us-east", target)
		}
		return &foghornfederationpb.DeleteStorageObjectsResponse{Accepted: true}, nil
	}
	c := &Cleaner{LocalCluster: "eu-west", S3: s3, Delegate: delegate}

	err := c.Delete(context.Background(), ArtifactRef{
		Hash:             "vod-2",
		Type:             "vod",
		TenantID:         "tenant-a",
		VODS3Key:         "vod/tenant-a/vod-2/movie.mp4",
		StorageClusterID: "us-east",
	})
	if err != nil {
		t.Fatalf("Delete err = %v", err)
	}
	if got == nil {
		t.Fatalf("delegate not called")
	}
	if got.GetTargetClusterId() != "us-east" {
		t.Errorf("TargetClusterId = %q", got.GetTargetClusterId())
	}
	if got.GetRequestingCluster() != "eu-west" {
		t.Errorf("RequestingCluster = %q", got.GetRequestingCluster())
	}
	if got.GetS3Key() != "vod/tenant-a/vod-2/movie.mp4" {
		t.Errorf("S3Key = %q", got.GetS3Key())
	}
	if len(s3.deleteCalls)+len(s3.deletePrefixCalls) != 0 {
		t.Errorf("local S3 must not be called for remote storage")
	}
}

func TestCleaner_RemoteDVRSendsPrefix(t *testing.T) {
	var got *foghornfederationpb.DeleteStorageObjectsRequest
	delegate := func(_ context.Context, _ string, req *foghornfederationpb.DeleteStorageObjectsRequest) (*foghornfederationpb.DeleteStorageObjectsResponse, error) {
		got = req
		return &foghornfederationpb.DeleteStorageObjectsResponse{Accepted: true}, nil
	}
	c := &Cleaner{LocalCluster: "eu-west", S3: &fakeS3{}, Delegate: delegate}

	err := c.Delete(context.Background(), ArtifactRef{
		Hash:             "dvr-2",
		Type:             "dvr",
		TenantID:         "tenant-a",
		StreamInternal:   "stream-x",
		StorageClusterID: "us-east",
	})
	if err != nil {
		t.Fatalf("Delete err = %v", err)
	}
	if got.GetS3Prefix() != "dvr/tenant-a/stream-x/dvr-2" {
		t.Errorf("S3Prefix = %q", got.GetS3Prefix())
	}
	if got.GetS3Key() != "" {
		t.Errorf("S3Key should be empty for dvr, got %q", got.GetS3Key())
	}
}

func TestCleaner_RemoteWithoutDelegateReturnsErr(t *testing.T) {
	c := &Cleaner{LocalCluster: "eu-west", S3: &fakeS3{}, Delegate: nil}
	err := c.Delete(context.Background(), ArtifactRef{
		Hash:             "vod-3",
		Type:             "vod",
		TenantID:         "tenant-a",
		VODS3Key:         "vod/tenant-a/vod-3/x.mp4",
		StorageClusterID: "us-east",
	})
	if !errors.Is(err, ErrDelegateMissing) {
		t.Fatalf("err = %v, want ErrDelegateMissing", err)
	}
}

// I2 acceptance: a locally-backed OFFICIAL ALIAS — storage_cluster_id names a cluster other than this cell, but
// the STABLE write-time fact durable_backend_local=true records that the bytes landed on THIS cell's S3 — must be
// deleted LOCALLY. Delete routing reads the recorded evidence, so the cluster-id mismatch does NOT misroute the
// delete to a federation peer (which would leave the local bytes leaked and delegate a delete of nothing).
func TestCleaner_LocallyBackedOfficialAliasDeletesLocally(t *testing.T) {
	s3 := &fakeS3{}
	delegateCalls := 0
	delegate := func(_ context.Context, _ string, _ *foghornfederationpb.DeleteStorageObjectsRequest) (*foghornfederationpb.DeleteStorageObjectsResponse, error) {
		delegateCalls++
		return &foghornfederationpb.DeleteStorageObjectsResponse{Accepted: true}, nil
	}
	c := &Cleaner{LocalCluster: "eu-west", S3: s3, Delegate: delegate, LocalBackendID: "local-backend"}

	err := c.Delete(context.Background(), ArtifactRef{
		Hash:                "vod-alias",
		Type:                "vod",
		TenantID:            "tenant-a",
		VODS3Key:            "vod/tenant-a/vod-alias/movie.mp4",
		StorageClusterID:    "official-alias", // attribution cluster id != this cell...
		DurableBackendLocal: true,             // ...but the bytes are on THIS cell's backend.
		BackendID:           "local-backend",  // recorded on this cell's store
	})
	if err != nil {
		t.Fatalf("Delete err = %v", err)
	}
	if delegateCalls != 0 {
		t.Fatalf("locally-backed alias must not delegate to a peer; delegate called %d times", delegateCalls)
	}
	// The main object is freed locally (the co-located .dtsh index is deleted alongside it).
	foundMain := false
	for _, k := range s3.deleteCalls {
		if k == "vod/tenant-a/vod-alias/movie.mp4" {
			foundMain = true
		}
	}
	if !foundMain {
		t.Fatalf("expected local delete of the recorded key; got %v", s3.deleteCalls)
	}
}

// The converse guard: without the durable_backend_local fact, the SAME cluster-id mismatch DOES route remotely —
// the override is what flips routing, not a coincidence of the key/type. Proves the two tests are distinguishing
// on the recorded evidence and nothing else.
func TestCleaner_RemoteAliasWithoutLocalBackingDelegates(t *testing.T) {
	s3 := &fakeS3{}
	delegateCalls := 0
	delegate := func(_ context.Context, _ string, _ *foghornfederationpb.DeleteStorageObjectsRequest) (*foghornfederationpb.DeleteStorageObjectsResponse, error) {
		delegateCalls++
		return &foghornfederationpb.DeleteStorageObjectsResponse{Accepted: true}, nil
	}
	c := &Cleaner{LocalCluster: "eu-west", S3: s3, Delegate: delegate}

	err := c.Delete(context.Background(), ArtifactRef{
		Hash:                "vod-remote",
		Type:                "vod",
		TenantID:            "tenant-a",
		VODS3Key:            "vod/tenant-a/vod-remote/movie.mp4",
		StorageClusterID:    "official-alias",
		DurableBackendLocal: false, // no recorded local backing → route by cluster id → remote.
	})
	if err != nil {
		t.Fatalf("Delete err = %v", err)
	}
	if delegateCalls == 0 {
		t.Fatalf("remote alias without local backing must delegate to the owning peer")
	}
	if len(s3.deleteCalls) != 0 {
		t.Fatalf("remote alias must not touch local S3; got %v", s3.deleteCalls)
	}
}

func TestCleaner_RemoteRejectionPropagatesReason(t *testing.T) {
	delegate := func(_ context.Context, _ string, _ *foghornfederationpb.DeleteStorageObjectsRequest) (*foghornfederationpb.DeleteStorageObjectsResponse, error) {
		return &foghornfederationpb.DeleteStorageObjectsResponse{Accepted: false, Reason: "tenant_mismatch"}, nil
	}
	c := &Cleaner{LocalCluster: "eu-west", S3: &fakeS3{}, Delegate: delegate}
	err := c.Delete(context.Background(), ArtifactRef{
		Hash:             "vod-4",
		Type:             "vod",
		TenantID:         "tenant-a",
		VODS3Key:         "vod/tenant-a/vod-4/x.mp4",
		StorageClusterID: "us-east",
	})
	if !errors.Is(err, ErrRemoteRejected) {
		t.Fatalf("err = %v, want ErrRemoteRejected", err)
	}
	if !strings.Contains(err.Error(), "tenant_mismatch") {
		t.Errorf("error doesn't carry reason: %v", err)
	}
}

func TestCleaner_OriginClusterFallbackForRemoteCheck(t *testing.T) {
	// storage_cluster_id empty, origin_cluster_id != local → remote
	called := false
	delegate := func(_ context.Context, _ string, _ *foghornfederationpb.DeleteStorageObjectsRequest) (*foghornfederationpb.DeleteStorageObjectsResponse, error) {
		called = true
		return &foghornfederationpb.DeleteStorageObjectsResponse{Accepted: true}, nil
	}
	c := &Cleaner{LocalCluster: "eu-west", S3: &fakeS3{}, Delegate: delegate}

	err := c.Delete(context.Background(), ArtifactRef{
		Hash:            "clip-3",
		Type:            "clip",
		TenantID:        "tenant-a",
		StreamInternal:  "s",
		Format:          "mp4",
		OriginClusterID: "us-east",
	})
	if err != nil {
		t.Fatalf("Delete err = %v", err)
	}
	if !called {
		t.Errorf("delegate not called for origin-cluster fallback")
	}
}

func TestCleaner_LocalClusterMatchUsesLocalS3(t *testing.T) {
	// storage_cluster_id == local → local S3
	s3 := &fakeS3{}
	delegate := func(_ context.Context, _ string, _ *foghornfederationpb.DeleteStorageObjectsRequest) (*foghornfederationpb.DeleteStorageObjectsResponse, error) {
		t.Fatal("delegate should not be called when storage_cluster_id == local")
		return nil, nil
	}
	c := &Cleaner{LocalCluster: "eu-west", S3: s3, Delegate: delegate, LocalBackendID: "local-backend"}

	err := c.Delete(context.Background(), ArtifactRef{
		Hash:             "clip-4",
		Type:             "clip",
		TenantID:         "tenant-a",
		StreamInternal:   "s",
		Format:           "mp4",
		StorageClusterID: "eu-west",
		BackendID:        "local-backend",
	})
	if err != nil {
		t.Fatalf("Delete err = %v", err)
	}
	if len(s3.deleteCalls) != 2 { // main object + co-located .dtsh
		t.Errorf("local Delete calls = %d, want 2", len(s3.deleteCalls))
	}
}

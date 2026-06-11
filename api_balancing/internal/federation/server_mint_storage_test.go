package federation

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"frameworks/api_balancing/internal/control"
	"frameworks/api_balancing/internal/identity"
	"frameworks/api_balancing/internal/state"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	foghornfederationpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn_federation"
)

// fakeMintS3Client captures presigned PUT calls so the tests can assert
// the exact key shapes the federated mint produced — these MUST match
// what the local-mint code paths would have built for the same inputs.
type fakeMintS3Client struct {
	putCalls []putCall

	// Optional override: when non-nil, GeneratePresignedPUT returns the
	// (url, err) per key from this map; missing keys fall through to the
	// default deterministic URL.
	putByKey map[string]struct {
		url string
		err error
	}
}

type putCall struct {
	key string
}

func (f *fakeMintS3Client) GeneratePresignedGET(string, time.Duration) (string, error) {
	return "", nil
}
func (f *fakeMintS3Client) GeneratePresignedPUT(key string, _ time.Duration) (string, error) {
	f.putCalls = append(f.putCalls, putCall{key: key})
	if entry, ok := f.putByKey[key]; ok {
		return entry.url, entry.err
	}
	return "https://s3.example.com/" + key + "?X-Amz-Signature=abc", nil
}
func (f *fakeMintS3Client) BuildClipS3Key(tenantID, streamName, clipHash, format string) string {
	return "clips/" + tenantID + "/" + streamName + "/" + clipHash + "." + format
}
func (f *fakeMintS3Client) BuildDVRS3Key(tenantID, internalName, dvrHash string) string {
	return "dvr/" + tenantID + "/" + internalName + "/" + dvrHash
}
func (f *fakeMintS3Client) BuildVodS3Key(tenantID, artifactHash, filename string) string {
	return "vod/" + tenantID + "/" + artifactHash + "/" + filename
}
func (f *fakeMintS3Client) Delete(_ context.Context, _ string) error {
	return nil
}
func (f *fakeMintS3Client) DeletePrefix(_ context.Context, _ string) (int, error) {
	return 0, nil
}

// mintTestServer builds a FederationServer that owns the named target
// cluster's storage by default.
func mintTestServer(t *testing.T, fake *fakeMintS3Client, db *sqlmock.Sqlmock) *FederationServer {
	t.Helper()
	cfg := FederationServerConfig{
		Logger:    logging.NewLogger(),
		ClusterID: "platform-eu",
		S3Client:  fake,
		LocalS3Backing: S3Backing{
			Bucket:   "frameworks",
			Endpoint: "https://s3.example.com",
			Region:   "us-east-1",
		},
		IsServedCluster: func(id string) bool { return id == "platform-eu" },
		AdvertisedBacking: func(_ context.Context, _, clusterID string) (S3Backing, bool) {
			if clusterID == "platform-eu" {
				return S3Backing{Bucket: "frameworks", Endpoint: "https://s3.example.com", Region: "us-east-1"}, true
			}
			return S3Backing{}, false
		},
	}
	return NewFederationServer(cfg)
}

func TestMintStorageURLs_RequiresAuth(t *testing.T) {
	srv := mintTestServer(t, &fakeMintS3Client{}, nil)
	_, err := srv.MintStorageURLs(context.Background(), &foghornfederationpb.MintStorageURLsRequest{
		TenantId:        "tenant-a",
		TargetClusterId: "platform-eu",
		ArtifactType:    "thumbnail",
		ArtifactKey:     "stream-uuid/poster.jpg",
		Op:              foghornfederationpb.MintStorageURLsRequest_OPERATION_PUT_SINGLE,
	})
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("expected PermissionDenied without service auth, got %v", err)
	}
}

func TestMintStorageURLs_RejectsTargetWeDoNotOwn(t *testing.T) {
	srv := mintTestServer(t, &fakeMintS3Client{}, nil)
	resp, err := srv.MintStorageURLs(serviceAuthContext(), &foghornfederationpb.MintStorageURLsRequest{
		TenantId:        "tenant-a",
		TargetClusterId: "selfhost-x", // not served + not advertised here
		ArtifactType:    "thumbnail",
		ArtifactKey:     "stream-uuid/poster.jpg",
		Op:              foghornfederationpb.MintStorageURLsRequest_OPERATION_PUT_SINGLE,
	})
	if err != nil {
		t.Fatalf("unexpected RPC error: %v", err)
	}
	if resp.GetAccepted() {
		t.Fatal("must reject when target_cluster_id is not locally owned")
	}
	if resp.GetReason() != "storage_not_owned_here" {
		t.Fatalf("expected reason=storage_not_owned_here, got %q", resp.GetReason())
	}
}

func TestMintStorageURLs_LiveThumbnail_HappyPath(t *testing.T) {
	state.ResetDefaultManagerForTests()
	t.Cleanup(func() { state.ResetDefaultManagerForTests() })

	sm := state.DefaultManager()
	if err := sm.UpdateStreamFromBuffer("stream-a", "stream-a", "node-a", "tenant-a", "FULL", ""); err != nil {
		t.Fatalf("seed stream: %v", err)
	}
	sm.SetStreamStreamID("stream-a", "stream-uuid")
	installMintIdentityResolver(t, nil, nil)

	fake := &fakeMintS3Client{}
	srv := mintTestServer(t, fake, nil)

	resp, err := srv.MintStorageURLs(serviceAuthContext(), &foghornfederationpb.MintStorageURLsRequest{
		TenantId:           "tenant-a",
		TargetClusterId:    "platform-eu",
		ArtifactType:       "thumbnail",
		ArtifactKey:        "stream-uuid/poster.jpg",
		Op:                 foghornfederationpb.MintStorageURLsRequest_OPERATION_PUT_SINGLE,
		StreamInternalName: "stream-a",
		ContentType:        "image/jpeg",
	})
	if err != nil {
		t.Fatalf("unexpected RPC error: %v", err)
	}
	if !resp.GetAccepted() {
		t.Fatalf("expected acceptance; reason=%q", resp.GetReason())
	}
	wantKey := "thumbnails/stream-uuid/poster.jpg"
	if resp.GetS3Key() != wantKey {
		t.Fatalf("S3 key shape mismatch: got %q want %q", resp.GetS3Key(), wantKey)
	}
	if len(fake.putCalls) != 1 || fake.putCalls[0].key != wantKey {
		t.Fatalf("expected single PUT against %q, got %+v", wantKey, fake.putCalls)
	}
}

// installMintIdentityResolver wires an identity facade over the test's
// fakes, mirroring the production wiring in cmd/foghorn/main.go: the state
// manager for the fast path, a registry leg backed by the fake's
// ResolveInternalName (sources) and the sqlmock DB (artifacts), and the
// fake's Resolve*Hash tables as the Commodore leg.
func installMintIdentityResolver(t *testing.T, db *sql.DB, resolver *fakeMintResolver) {
	t.Helper()
	registry := control.NewStreamRegistry(nil, "platform-eu", time.Minute)
	cfg := identity.Config{
		StreamState: func(internalName string) (identity.StreamStateView, bool) {
			ss := state.DefaultManager().GetStreamState(internalName)
			if ss == nil {
				return identity.StreamStateView{}, false
			}
			return identity.StreamStateView{
				StreamID:   ss.StreamID,
				PlaybackID: ss.PlaybackID,
				TenantID:   ss.TenantID,
				NodeID:     ss.NodeID,
			}, true
		},
	}
	if db != nil {
		cfg.RegistryArtifact = func(ctx context.Context, hash string) (identity.ArtifactIdentity, error) {
			entry, err := registry.ResolveArtifactByHash(ctx, db, hash)
			if err != nil {
				if errors.Is(err, control.ErrUnknownArtifact) {
					return identity.ArtifactIdentity{}, identity.ErrNotFound
				}
				return identity.ArtifactIdentity{}, err
			}
			return identity.ArtifactIdentity{
				ArtifactHash:       entry.ArtifactHash,
				Kind:               entry.Kind.String(),
				InternalName:       entry.InternalName,
				StreamInternalName: entry.StreamInternal,
				StreamID:           entry.StreamID,
				TenantID:           entry.TenantID,
				OriginClusterID:    entry.OriginClusterID,
				StorageClusterID:   entry.StorageCluster,
			}, nil
		}
	}
	if resolver != nil {
		cfg.RegistrySource = func(ctx context.Context, internalName string) (identity.StreamIdentity, error) {
			resp, err := resolver.ResolveInternalName(ctx, internalName)
			if err != nil || resp == nil || resp.GetStreamId() == "" {
				return identity.StreamIdentity{}, identity.ErrNotFound
			}
			return identity.StreamIdentity{
				InternalName:    internalName,
				StreamID:        resp.GetStreamId(),
				TenantID:        resp.GetTenantId(),
				OriginClusterID: resp.GetOriginClusterId(),
			}, nil
		}
		cfg.CommodoreArtifact = func(ctx context.Context, kind, hash string) (identity.ArtifactIdentity, error) {
			switch kind {
			case "clip":
				resp, err := resolver.ResolveClipHash(ctx, hash)
				if err != nil || resp == nil || !resp.GetFound() {
					return identity.ArtifactIdentity{}, err
				}
				return identity.ArtifactIdentity{
					ArtifactHash:       hash,
					Kind:               kind,
					InternalName:       resp.GetInternalName(),
					StreamInternalName: resp.GetStreamInternalName(),
					StreamID:           resp.GetStreamId(),
					TenantID:           resp.GetTenantId(),
					OriginClusterID:    resp.GetOriginClusterId(),
				}, nil
			case "vod":
				resp, err := resolver.ResolveVodHash(ctx, hash)
				if err != nil || resp == nil || !resp.GetFound() {
					return identity.ArtifactIdentity{}, err
				}
				return identity.ArtifactIdentity{
					ArtifactHash:       hash,
					Kind:               kind,
					InternalName:       resp.GetInternalName(),
					StreamInternalName: resp.GetInternalName(),
					TenantID:           resp.GetTenantId(),
					OriginClusterID:    resp.GetOriginClusterId(),
				}, nil
			case "dvr":
				resp, err := resolver.ResolveDVRHash(ctx, hash)
				if err != nil || resp == nil || !resp.GetFound() {
					return identity.ArtifactIdentity{}, err
				}
				return identity.ArtifactIdentity{
					ArtifactHash:       hash,
					Kind:               kind,
					InternalName:       resp.GetInternalName(),
					StreamInternalName: resp.GetStreamInternalName(),
					StreamID:           resp.GetStreamId(),
					TenantID:           resp.GetTenantId(),
					OriginClusterID:    resp.GetOriginClusterId(),
				}, nil
			default:
				return identity.ArtifactIdentity{}, nil
			}
		}
	}
	identity.SetDefault(identity.NewResolver(cfg))
	t.Cleanup(func() { identity.SetDefault(nil) })
}

// fakeMintResolver answers the four Commodore Resolve* calls deterministically
// from in-memory tables so MintStorageURLs' Commodore-fallback branch can be
// exercised without a real Commodore process.
type fakeMintResolver struct {
	internalNames map[string]*commodorepb.ResolveInternalNameResponse
	clipHashes    map[string]*commodorepb.ResolveClipHashResponse
	dvrHashes     map[string]*commodorepb.ResolveDVRHashResponse
	vodHashes     map[string]*commodorepb.ResolveVodHashResponse
}

func (f *fakeMintResolver) ResolveInternalName(_ context.Context, n string) (*commodorepb.ResolveInternalNameResponse, error) {
	if r, ok := f.internalNames[n]; ok {
		return r, nil
	}
	return nil, nil
}
func (f *fakeMintResolver) ResolveClipHash(_ context.Context, h string) (*commodorepb.ResolveClipHashResponse, error) {
	if r, ok := f.clipHashes[h]; ok {
		return r, nil
	}
	return &commodorepb.ResolveClipHashResponse{Found: false}, nil
}
func (f *fakeMintResolver) ResolveDVRHash(_ context.Context, h string) (*commodorepb.ResolveDVRHashResponse, error) {
	if r, ok := f.dvrHashes[h]; ok {
		return r, nil
	}
	return &commodorepb.ResolveDVRHashResponse{Found: false}, nil
}
func (f *fakeMintResolver) ResolveVodHash(_ context.Context, h string) (*commodorepb.ResolveVodHashResponse, error) {
	if r, ok := f.vodHashes[h]; ok {
		return r, nil
	}
	return &commodorepb.ResolveVodHashResponse{Found: false}, nil
}

// TestMintStorageURLs_LiveThumbnail_CommodoreFallback covers the cross-pool
// delegation path: the storage Foghorn has no local stream state for the
// ingest stream (it lives on a peer pool), but Commodore returns the
// authoritative tenant binding + stream_id. Mint must succeed and use
// Commodore's stream_id rather than any caller-supplied value.
func TestMintStorageURLs_LiveThumbnail_CommodoreFallback(t *testing.T) {
	state.ResetDefaultManagerForTests()
	t.Cleanup(func() { state.ResetDefaultManagerForTests() })

	resolver := &fakeMintResolver{
		internalNames: map[string]*commodorepb.ResolveInternalNameResponse{
			"stream-a": {
				TenantId: "tenant-a",
				StreamId: "stream-uuid",
			},
		},
	}

	installMintIdentityResolver(t, nil, resolver)

	fake := &fakeMintS3Client{}
	srv := mintTestServer(t, fake, nil)

	resp, err := srv.MintStorageURLs(serviceAuthContext(), &foghornfederationpb.MintStorageURLsRequest{
		TenantId:           "tenant-a",
		TargetClusterId:    "platform-eu",
		ArtifactType:       "thumbnail",
		ArtifactKey:        "stream-uuid/poster.jpg",
		Op:                 foghornfederationpb.MintStorageURLsRequest_OPERATION_PUT_SINGLE,
		StreamInternalName: "stream-a",
		ContentType:        "image/jpeg",
	})
	if err != nil {
		t.Fatalf("unexpected RPC error: %v", err)
	}
	if !resp.GetAccepted() {
		t.Fatalf("expected acceptance via Commodore fallback; reason=%q", resp.GetReason())
	}
	wantKey := "thumbnails/stream-uuid/poster.jpg"
	if resp.GetS3Key() != wantKey {
		t.Fatalf("S3 key shape mismatch: got %q want %q", resp.GetS3Key(), wantKey)
	}
	if len(fake.putCalls) != 1 || fake.putCalls[0].key != wantKey {
		t.Fatalf("expected single PUT against %q, got %+v", wantKey, fake.putCalls)
	}
}

// Without local state OR Commodore confirmation, the callee must reject —
// it cannot prove tenant ownership of the stream.
func TestMintStorageURLs_LiveThumbnail_RejectsWithoutCommodore(t *testing.T) {
	state.ResetDefaultManagerForTests()
	t.Cleanup(func() { state.ResetDefaultManagerForTests() })

	installMintIdentityResolver(t, nil, nil)
	srv := mintTestServer(t, &fakeMintS3Client{}, nil)
	// No registry/Commodore legs wired — resolution fails closed.

	resp, err := srv.MintStorageURLs(serviceAuthContext(), &foghornfederationpb.MintStorageURLsRequest{
		TenantId:           "tenant-a",
		TargetClusterId:    "platform-eu",
		ArtifactType:       "thumbnail",
		ArtifactKey:        "stream-uuid/poster.jpg",
		Op:                 foghornfederationpb.MintStorageURLsRequest_OPERATION_PUT_SINGLE,
		StreamInternalName: "stream-a",
	})
	if err != nil {
		t.Fatalf("unexpected RPC error: %v", err)
	}
	if resp.GetAccepted() {
		t.Fatal("must reject when neither local state nor Commodore can confirm tenant")
	}
	if resp.GetReason() != "tenant_mismatch" {
		t.Fatalf("expected tenant_mismatch, got %q", resp.GetReason())
	}
}

// Commodore confirms a different stream_id than the artifact_key prefix —
// the prefix mismatch must reject to prevent cross-stream key forgery.
func TestMintStorageURLs_LiveThumbnail_RejectsArtifactKeyStreamIDMismatch(t *testing.T) {
	state.ResetDefaultManagerForTests()
	t.Cleanup(func() { state.ResetDefaultManagerForTests() })

	resolver := &fakeMintResolver{
		internalNames: map[string]*commodorepb.ResolveInternalNameResponse{
			"stream-a": {
				TenantId: "tenant-a",
				StreamId: "actual-stream-uuid",
			},
		},
	}

	installMintIdentityResolver(t, nil, resolver)
	srv := mintTestServer(t, &fakeMintS3Client{}, nil)

	resp, err := srv.MintStorageURLs(serviceAuthContext(), &foghornfederationpb.MintStorageURLsRequest{
		TenantId:           "tenant-a",
		TargetClusterId:    "platform-eu",
		ArtifactType:       "thumbnail",
		ArtifactKey:        "other-stream-uuid/poster.jpg", // forged
		Op:                 foghornfederationpb.MintStorageURLsRequest_OPERATION_PUT_SINGLE,
		StreamInternalName: "stream-a",
	})
	if err != nil {
		t.Fatalf("unexpected RPC error: %v", err)
	}
	if resp.GetAccepted() {
		t.Fatal("must reject when artifact_key streamID prefix doesn't match Commodore's stream_id")
	}
	if resp.GetReason() != "tenant_mismatch" {
		t.Fatalf("expected tenant_mismatch, got %q", resp.GetReason())
	}
}

func TestMintStorageURLs_LiveThumbnail_TenantMismatch(t *testing.T) {
	state.ResetDefaultManagerForTests()
	t.Cleanup(func() { state.ResetDefaultManagerForTests() })

	sm := state.DefaultManager()
	if err := sm.UpdateStreamFromBuffer("stream-a", "stream-a", "node-a", "tenant-other", "FULL", ""); err != nil {
		t.Fatalf("seed stream: %v", err)
	}
	installMintIdentityResolver(t, nil, nil)

	srv := mintTestServer(t, &fakeMintS3Client{}, nil)
	resp, err := srv.MintStorageURLs(serviceAuthContext(), &foghornfederationpb.MintStorageURLsRequest{
		TenantId:           "tenant-a", // doesn't match the stream's owner
		TargetClusterId:    "platform-eu",
		ArtifactType:       "thumbnail",
		ArtifactKey:        "stream-uuid/poster.jpg",
		Op:                 foghornfederationpb.MintStorageURLsRequest_OPERATION_PUT_SINGLE,
		StreamInternalName: "stream-a",
	})
	if err != nil {
		t.Fatalf("unexpected RPC error: %v", err)
	}
	if resp.GetAccepted() {
		t.Fatal("must reject on tenant mismatch")
	}
	if resp.GetReason() != "tenant_mismatch" {
		t.Fatalf("expected tenant_mismatch, got %q", resp.GetReason())
	}
}

func TestMintStorageURLs_VodRejectsMultipart(t *testing.T) {
	srv := mintTestServer(t, &fakeMintS3Client{}, nil)
	resp, err := srv.MintStorageURLs(serviceAuthContext(), &foghornfederationpb.MintStorageURLsRequest{
		TenantId:        "tenant-a",
		TargetClusterId: "platform-eu",
		ArtifactType:    "vod",
		ArtifactKey:     "abcd1234",
		Op:              foghornfederationpb.MintStorageURLsRequest_OPERATION_PUT_DVR_SET, // wrong op for vod
	})
	if err != nil {
		t.Fatalf("unexpected RPC error: %v", err)
	}
	if resp.GetAccepted() {
		t.Fatal("must reject vod with non-single op")
	}
	if resp.GetReason() != "unsupported_operation" {
		t.Fatalf("expected unsupported_operation, got %q", resp.GetReason())
	}
}

// TestMintStorageURLs_DvrSet_KeyShape locks in the segment-key shape against
// a fake S3 client whose BuildDVRS3Key mirrors the real one (no trailing
// slash). Without an explicit "/" in the federation handler's concat, segment
// keys end up as ".../{hash}{filename}" instead of ".../{hash}/{filename}".
func TestMintStorageURLs_DvrSet_KeyShape(t *testing.T) {
	resolver := &fakeMintResolver{
		dvrHashes: map[string]*commodorepb.ResolveDVRHashResponse{
			"dvr-abcd": {
				Found:              true,
				TenantId:           "tenant-a",
				StreamInternalName: "stream-a",
				InternalName:       "dvr-abcd",
			},
		},
	}

	installMintIdentityResolver(t, nil, resolver)

	fake := &fakeMintS3Client{}
	srv := mintTestServer(t, fake, nil)

	resp, err := srv.MintStorageURLs(serviceAuthContext(), &foghornfederationpb.MintStorageURLsRequest{
		TenantId:         "tenant-a",
		TargetClusterId:  "platform-eu",
		ArtifactType:     "dvr",
		ArtifactKey:      "dvr-abcd",
		Op:               foghornfederationpb.MintStorageURLsRequest_OPERATION_PUT_DVR_SET,
		SegmentFilenames: []string{"segments/0.ts", "playlist.m3u8"},
	})
	if err != nil {
		t.Fatalf("unexpected RPC error: %v", err)
	}
	if !resp.GetAccepted() {
		t.Fatalf("expected acceptance; reason=%q", resp.GetReason())
	}
	wantPrefix := "dvr/tenant-a/stream-a/dvr-abcd"
	if resp.GetS3Key() != wantPrefix {
		t.Fatalf("dvr prefix mismatch: got %q want %q", resp.GetS3Key(), wantPrefix)
	}
	for _, fn := range []string{"segments/0.ts", "playlist.m3u8"} {
		wantKey := wantPrefix + "/" + fn
		hit := false
		for _, c := range fake.putCalls {
			if c.key == wantKey {
				hit = true
				break
			}
		}
		if !hit {
			t.Fatalf("expected presigned PUT against %q; got calls=%+v", wantKey, fake.putCalls)
		}
	}
}

// TestMintStorageURLs_DvrSegment_KeyShape covers the dvr_segment / dvr_manifest
// branch (incremental segment uploads). Same slash-separator invariant.
func TestMintStorageURLs_DvrSegment_KeyShape(t *testing.T) {
	resolver := &fakeMintResolver{
		dvrHashes: map[string]*commodorepb.ResolveDVRHashResponse{
			"dvr-abcd": {
				Found:              true,
				TenantId:           "tenant-a",
				StreamInternalName: "stream-a",
				InternalName:       "dvr-abcd",
			},
		},
	}

	installMintIdentityResolver(t, nil, resolver)

	fake := &fakeMintS3Client{}
	srv := mintTestServer(t, fake, nil)

	resp, err := srv.MintStorageURLs(serviceAuthContext(), &foghornfederationpb.MintStorageURLsRequest{
		TenantId:        "tenant-a",
		TargetClusterId: "platform-eu",
		ArtifactType:    "dvr_segment",
		ArtifactKey:     "dvr-abcd/segments/42.ts",
		Op:              foghornfederationpb.MintStorageURLsRequest_OPERATION_PUT_SINGLE,
	})
	if err != nil {
		t.Fatalf("unexpected RPC error: %v", err)
	}
	if !resp.GetAccepted() {
		t.Fatalf("expected acceptance; reason=%q", resp.GetReason())
	}
	wantKey := "dvr/tenant-a/stream-a/dvr-abcd/segments/42.ts"
	if resp.GetS3Key() != wantKey {
		t.Fatalf("dvr_segment key mismatch: got %q want %q", resp.GetS3Key(), wantKey)
	}
	if len(fake.putCalls) != 1 || fake.putCalls[0].key != wantKey {
		t.Fatalf("expected single PUT against %q, got %+v", wantKey, fake.putCalls)
	}
}

// TestMintStorageURLs_FastPath_RejectsCrossTypeHash asserts the resolved
// artifact kind is validated against the requested mint type. A same-tenant
// DVR hash requested as a clip mint must be rejected; otherwise the handler
// would build a clip-shape S3 key against an asset that downstream consumers
// expect at the dvr-shape path.
func TestMintStorageURLs_FastPath_RejectsCrossTypeHash(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	rows := sqlmock.NewRows([]string{
		"artifact_hash", "artifact_type", "internal_name", "stream_internal_name",
		"stream_id", "tenant_id", "status", "format",
		"origin_cluster_id", "storage_cluster_id", "has_thumbnails",
	}).AddRow("dvr-abcd", "dvr", "dvr-abcd", "stream-a", "", "tenant-a", "ready", "", "platform-eu", "", false)
	mock.ExpectQuery("FROM foghorn.artifacts").
		WithArgs("dvr-abcd").
		WillReturnRows(rows)

	cfg := FederationServerConfig{
		Logger:    logging.NewLogger(),
		ClusterID: "platform-eu",
		DB:        db,
		S3Client:  &fakeMintS3Client{},
		LocalS3Backing: S3Backing{
			Bucket: "frameworks", Endpoint: "https://s3.example.com", Region: "us-east-1",
		},
		IsServedCluster: func(id string) bool { return id == "platform-eu" },
		AdvertisedBacking: func(_ context.Context, _, clusterID string) (S3Backing, bool) {
			if clusterID == "platform-eu" {
				return S3Backing{Bucket: "frameworks", Endpoint: "https://s3.example.com", Region: "us-east-1"}, true
			}
			return S3Backing{}, false
		},
	}
	srv := NewFederationServer(cfg)
	installMintIdentityResolver(t, db, nil)

	resp, err := srv.MintStorageURLs(serviceAuthContext(), &foghornfederationpb.MintStorageURLsRequest{
		TenantId:        "tenant-a",
		TargetClusterId: "platform-eu",
		ArtifactType:    "clip", // wrong: row is dvr
		ArtifactKey:     "dvr-abcd",
		Op:              foghornfederationpb.MintStorageURLsRequest_OPERATION_PUT_SINGLE,
	})
	if err != nil {
		t.Fatalf("unexpected RPC error: %v", err)
	}
	if resp.GetAccepted() {
		t.Fatal("must reject: row.artifact_type=dvr but request asked for clip mint")
	}
	if resp.GetReason() != "tenant_mismatch" {
		t.Fatalf("expected tenant_mismatch, got %q", resp.GetReason())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}

// TestMintStorageURLs_FastPath_FallsThroughOnEmptyStreamName covers the case
// where the local artifact row matches by tenant and type but has no
// stream_internal_name (incomplete cache from a prior delegation that
// couldn't fill it in). The local row must NOT satisfy a clip/dvr mint on
// its own — BuildClipS3Key / BuildDVRS3Key would emit
// "clips/<tenant>//<hash>.<fmt>". Commodore is the authoritative source for
// stream_internal_name and is always wired in production, so the resolver
// fills the gap from it.
func TestMintStorageURLs_FastPath_FallsThroughOnEmptyStreamName(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	rows := sqlmock.NewRows([]string{
		"artifact_hash", "artifact_type", "internal_name", "stream_internal_name",
		"stream_id", "tenant_id", "status", "format",
		"origin_cluster_id", "storage_cluster_id", "has_thumbnails",
	}).AddRow("clip-abcd", "clip", "clip-abcd", "", "", "tenant-a", "ready", "mp4", "platform-eu", "", false) // empty stream_internal_name
	mock.ExpectQuery("FROM foghorn.artifacts").
		WithArgs("clip-abcd").
		WillReturnRows(rows)

	resolver := &fakeMintResolver{
		clipHashes: map[string]*commodorepb.ResolveClipHashResponse{
			"clip-abcd": {
				Found:              true,
				TenantId:           "tenant-a",
				StreamInternalName: "stream-a", // Commodore fills the gap
				InternalName:       "clip-abcd",
				OriginClusterId:    "platform-eu",
			},
		},
	}

	cfg := FederationServerConfig{
		Logger:    logging.NewLogger(),
		ClusterID: "platform-eu",
		DB:        db,
		S3Client:  &fakeMintS3Client{},
		LocalS3Backing: S3Backing{
			Bucket: "frameworks", Endpoint: "https://s3.example.com", Region: "us-east-1",
		},
		IsServedCluster: func(id string) bool { return id == "platform-eu" },
		AdvertisedBacking: func(_ context.Context, _, clusterID string) (S3Backing, bool) {
			if clusterID == "platform-eu" {
				return S3Backing{Bucket: "frameworks", Endpoint: "https://s3.example.com", Region: "us-east-1"}, true
			}
			return S3Backing{}, false
		},
	}
	srv := NewFederationServer(cfg)
	installMintIdentityResolver(t, db, resolver)

	resp, err := srv.MintStorageURLs(serviceAuthContext(), &foghornfederationpb.MintStorageURLsRequest{
		TenantId:        "tenant-a",
		TargetClusterId: "platform-eu",
		ArtifactType:    "clip",
		ArtifactKey:     "clip-abcd",
		Op:              foghornfederationpb.MintStorageURLsRequest_OPERATION_PUT_SINGLE,
		ContentType:     "video/mp4",
	})
	if err != nil {
		t.Fatalf("unexpected RPC error: %v", err)
	}
	if !resp.GetAccepted() {
		t.Fatalf("expected acceptance via Commodore fallback; reason=%q", resp.GetReason())
	}
	wantKey := "clips/tenant-a/stream-a/clip-abcd.mp4"
	if resp.GetS3Key() != wantKey {
		t.Fatalf("S3 key mismatch: got %q want %q", resp.GetS3Key(), wantKey)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}

func TestMintStorageURLs_DvrSet_RequiresFilenames(t *testing.T) {
	srv := mintTestServer(t, &fakeMintS3Client{}, nil)
	resp, err := srv.MintStorageURLs(serviceAuthContext(), &foghornfederationpb.MintStorageURLsRequest{
		TenantId:        "tenant-a",
		TargetClusterId: "platform-eu",
		ArtifactType:    "dvr",
		ArtifactKey:     "abcd1234",
		Op:              foghornfederationpb.MintStorageURLsRequest_OPERATION_PUT_DVR_SET,
		// SegmentFilenames intentionally empty
	})
	if err != nil {
		t.Fatalf("unexpected RPC error: %v", err)
	}
	if resp.GetAccepted() {
		t.Fatal("must reject dvr set without filenames")
	}
	if resp.GetReason() != "unsupported_operation" {
		t.Fatalf("expected unsupported_operation, got %q", resp.GetReason())
	}
}

func TestPrepareArtifact_RedirectsWhenStorageOwnedElsewhere(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	rows := sqlmock.NewRows([]string{"internal_name", "stream_internal_name", "artifact_type", "format", "storage_location", "sync_status", "size_bytes", "authoritative_cluster"}).
		AddRow("clip-x", "stream-x", "clip", "mp4", "s3", "synced", 1024, "selfhost-foreign")
	mock.ExpectQuery("FROM foghorn.artifacts").WillReturnRows(rows)

	cfg := FederationServerConfig{
		Logger:          logging.NewLogger(),
		ClusterID:       "platform-eu",
		DB:              db,
		S3Client:        &fakeMintS3Client{},
		LocalS3Backing:  S3Backing{Bucket: "frameworks", Region: "us-east-1"},
		IsServedCluster: func(id string) bool { return id == "platform-eu" },
		AdvertisedBacking: func(_ context.Context, _, clusterID string) (S3Backing, bool) {
			if clusterID == "platform-eu" {
				return S3Backing{Bucket: "frameworks", Region: "us-east-1"}, true
			}
			return S3Backing{}, false
		},
	}
	srv := NewFederationServer(cfg)

	resp, err := srv.PrepareArtifact(serviceAuthContext(), &foghornfederationpb.PrepareArtifactRequest{
		ArtifactId: "hash-x",
		TenantId:   "tenant-a",
	})
	if err != nil {
		t.Fatalf("unexpected RPC error: %v", err)
	}
	// The artifact's authoritative cluster (selfhost-foreign) is NOT
	// served locally, so PrepareArtifact must emit redirect_cluster_id
	// instead of attempting a presigned mint. Other response fields are
	// ignored when redirect is set.
	if got := resp.GetRedirectClusterId(); got != "selfhost-foreign" {
		t.Fatalf("expected redirect to selfhost-foreign, got %q", got)
	}
	if resp.GetUrl() != "" || resp.GetReady() {
		t.Fatalf("redirect response must not carry a presigned URL or ready=true: %+v", resp)
	}
}

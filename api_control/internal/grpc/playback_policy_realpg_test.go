//go:build schema_verify

package grpc

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	"github.com/sirupsen/logrus"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestPlaybackPolicyRepository_RealPG(t *testing.T) {
	db := startCommodoreRealPG(t)
	const (
		tenantID = "10000000-0000-4000-8000-000000000001"
		otherID  = "10000000-0000-4000-8000-000000000002"
		userID   = "20000000-0000-4000-8000-000000000001"
		streamID = "30000000-0000-4000-8000-000000000001"
		vodHash  = "vodhash00000000000000000000001"
		clipHash = "cliphash000000000000000000001"
	)
	ctx := context.WithValue(context.Background(), ctxkeys.KeyUserID, userID)
	ctx = context.WithValue(ctx, ctxkeys.KeyTenantID, tenantID)

	if _, err := db.ExecContext(ctx, `
		INSERT INTO commodore.streams
		    (id, tenant_id, user_id, stream_key, playback_id, internal_name, title)
		VALUES ($1::uuid, $2::uuid, $3::uuid, 'stream-key', 'StreamPlayback', 'stream-internal', 'title')
	`, streamID, tenantID, userID); err != nil {
		t.Fatal(err)
	}
	var vodID, clipID string
	if err := db.QueryRowContext(ctx, `
		INSERT INTO commodore.vod_assets
		    (tenant_id, user_id, vod_hash, internal_name, playback_id, filename)
		VALUES ($1::uuid, $2::uuid, $3, 'vod-internal', 'VodPlayback', 'video.mp4')
		RETURNING id::text
	`, tenantID, userID, vodHash).Scan(&vodID); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `
		INSERT INTO commodore.clips
		    (tenant_id, user_id, stream_id, clip_hash, internal_name, playback_id, start_time, duration)
		VALUES ($1::uuid, $2::uuid, $3::uuid, $4, 'clip-internal', 'ClipPlayback', 0, 1000)
		RETURNING id::text
	`, tenantID, userID, streamID, clipHash).Scan(&clipID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO commodore.dvr_recordings
		    (tenant_id, user_id, stream_id, dvr_hash, internal_name, playback_id, stream_internal_name)
		VALUES ($1::uuid, $2::uuid, $3::uuid, 'dvrhash00000000000000000000001',
		        'dvr-internal', 'DVRPlayback', 'stream-internal')
	`, tenantID, userID, streamID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO commodore.signing_keys (tenant_id, kid, name, public_key_pem, status)
		VALUES ($1::uuid, 'kid-active', 'active', 'public-pem', 'active')
	`, tenantID); err != nil {
		t.Fatal(err)
	}

	server := &CommodoreServer{db: db, logger: logrus.New()}
	if _, err := server.SetPlaybackPolicy(ctx, &commodorepb.SetPlaybackPolicyRequest{
		StreamId: streamID,
		Type:     "jwt",
		Jwt:      &commodorepb.PlaybackJwtPolicy{AllowedKids: []string{"kid-active"}},
	}); err != nil {
		t.Fatalf("set stream JWT policy: %v", err)
	}
	if _, err := server.SetPlaybackPolicy(ctx, &commodorepb.SetPlaybackPolicyRequest{VodAssetId: vodHash, Type: "public"}); err != nil {
		t.Fatalf("set VOD policy by hash: %v", err)
	}
	if _, err := server.SetPlaybackPolicy(ctx, &commodorepb.SetPlaybackPolicyRequest{ClipId: clipID, Type: "public"}); err != nil {
		t.Fatalf("set clip policy by UUID: %v", err)
	}

	streamPolicy, err := server.ResolvePlaybackPolicy(ctx, &commodorepb.ResolvePlaybackPolicyRequest{PlaybackId: "streamplayback"})
	if err != nil || streamPolicy.GetType() != "jwt" || streamPolicy.GetTenantId() != tenantID || len(streamPolicy.GetJwtPolicy().GetActiveKeys()) != 1 {
		t.Fatalf("stream playback policy = %#v, err = %v", streamPolicy, err)
	}
	internalPolicy, err := server.ResolvePlaybackPolicy(ctx, &commodorepb.ResolvePlaybackPolicyRequest{InternalName: "stream-internal"})
	if err != nil || internalPolicy.GetType() != "jwt" {
		t.Fatalf("stream internal policy = %#v, err = %v", internalPolicy, err)
	}
	dvrPolicy, err := server.ResolvePlaybackPolicy(ctx, &commodorepb.ResolvePlaybackPolicyRequest{PlaybackId: "dvrplayback"})
	if err != nil || dvrPolicy.GetType() != "jwt" {
		t.Fatalf("inherited DVR policy = %#v, err = %v", dvrPolicy, err)
	}
	vodPolicy, err := server.ResolvePlaybackPolicy(ctx, &commodorepb.ResolvePlaybackPolicyRequest{InternalName: "vod-internal"})
	if err != nil || vodPolicy.GetType() != "public" {
		t.Fatalf("VOD internal policy = %#v, err = %v", vodPolicy, err)
	}
	clipPolicy, err := server.ResolvePlaybackPolicy(ctx, &commodorepb.ResolvePlaybackPolicyRequest{PlaybackId: "clipplayback"})
	if err != nil || clipPolicy.GetType() != "public" {
		t.Fatalf("clip playback policy = %#v, err = %v", clipPolicy, err)
	}

	if _, err := db.ExecContext(ctx, `
		INSERT INTO commodore.artifact_catalog_tombstones
		    (tenant_id, kind, artifact_hash, origin_cluster_id, deletion_revision)
		VALUES ($1::uuid, 'vod', $2, 'media-1', 1)
	`, tenantID, vodHash); err != nil {
		t.Fatal(err)
	}
	if _, err := server.SetPlaybackPolicy(ctx, &commodorepb.SetPlaybackPolicyRequest{VodAssetId: vodID, Type: "jwt"}); status.Code(err) != codes.NotFound {
		t.Fatalf("tombstoned VOD update code = %v, err = %v", status.Code(err), err)
	}

	var invalidations int
	if err := db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM commodore.playback_policy_invalidation_outbox
		WHERE tenant_id = $1::uuid AND reason = 'policy_change'
	`, tenantID).Scan(&invalidations); err != nil {
		t.Fatal(err)
	}
	if invalidations != 3 {
		t.Fatalf("policy invalidations = %d, want 3", invalidations)
	}

	t.Setenv("POLICY_BUNDLE_SIGNING_SECRET", "real-pg-contract-secret")
	server.qmEntitlements = stubEntitlements{resp: &quartermasterpb.GetTenantEntitlementResponse{
		AllowedClusterIds: []string{"media-1", "media-2"},
		PlanClass:         "paid",
	}}
	firstBundle, err := server.GetSignedPolicyBundle(svcCtx(), &commodorepb.GetSignedPolicyBundleRequest{TenantId: tenantID, StreamId: streamID})
	if err != nil || firstBundle.GetBundle().GetBundleVersion() != 1 || firstBundle.GetBundle().GetBundleJwt() == "" {
		t.Fatalf("first policy bundle = %#v, err = %v", firstBundle, err)
	}
	secondBundle, err := server.GetSignedPolicyBundle(svcCtx(), &commodorepb.GetSignedPolicyBundleRequest{TenantId: tenantID, StreamId: streamID})
	if err != nil || secondBundle.GetBundle().GetBundleVersion() != 2 {
		t.Fatalf("second policy bundle = %#v, err = %v", secondBundle, err)
	}
	versionsSeen := make(chan int64, 8)
	errs := make(chan error, 8)
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			bundle, err := server.GetSignedPolicyBundle(svcCtx(), &commodorepb.GetSignedPolicyBundleRequest{TenantId: tenantID, StreamId: streamID})
			if err != nil {
				errs <- err
				return
			}
			versionsSeen <- bundle.GetBundle().GetBundleVersion()
		}()
	}
	wg.Wait()
	close(errs)
	close(versionsSeen)
	for err := range errs {
		t.Fatalf("concurrent policy bundle: %v", err)
	}
	uniqueVersions := map[int64]bool{}
	for version := range versionsSeen {
		uniqueVersions[version] = true
	}
	if len(uniqueVersions) != 8 {
		t.Fatalf("concurrent bundle versions = %#v, want 8 unique values", uniqueVersions)
	}
	for version := int64(3); version <= 10; version++ {
		if !uniqueVersions[version] {
			t.Fatalf("concurrent bundle versions = %#v, missing %d", uniqueVersions, version)
		}
	}
	if _, _, err := server.lookupPolicyForStream(ctx, otherID, streamID); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("bundle tenant mismatch code = %v, err = %v", status.Code(err), err)
	}
	var versions string
	if err := db.QueryRowContext(ctx, `
		SELECT string_agg(bundle_version::text, ',' ORDER BY bundle_version)
		FROM commodore.policy_bundle_versions
		WHERE tenant_id = $1::uuid AND stream_id = $2::uuid
	`, tenantID, streamID).Scan(&versions); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(versions) != "1,2,3,4,5,6,7,8,9,10" {
		t.Fatalf("persisted bundle versions = %q", versions)
	}
}

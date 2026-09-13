//go:build schema_verify

package grpc

import (
	"context"
	"testing"

	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	"github.com/sirupsen/logrus"
)

func TestUpdateArtifactCatalogSnapshot_ClipWithoutMeasuredDuration_RealPG(t *testing.T) {
	conn := startCommodoreRealPG(t)
	ctx := context.Background()
	server := &CommodoreServer{db: conn, logger: logrus.New()}
	const tenant = "11111111-1111-1111-1111-111111111111"
	const hash = "abcdef0123456789abcdef0123456701"
	origin := "media-us-1"
	if _, err := conn.ExecContext(ctx, `
		INSERT INTO commodore.clips
		(tenant_id, user_id, stream_id, clip_hash, internal_name, playback_id, start_time, duration, origin_cluster_id)
		VALUES ($1::uuid, $1::uuid, $1::uuid, $2::varchar, $3::varchar, $4::citext, 1000, 5000, $5)`,
		tenant, hash, hash, hash, origin); err != nil {
		t.Fatalf("seed clip: %v", err)
	}
	measured := int64(6033)
	for _, tc := range []struct {
		name         string
		revision     int64
		lifecycle    string
		duration     *int64
		wantDuration int64
		wantRevision int64
		wantStatus   string
	}{
		{"processing without measurement", 1, "processing", nil, 5000, 1, "processing"},
		{"failed without measurement", 2, "failed", nil, 5000, 2, "failed"},
		{"measured completion", 3, "ready", &measured, 6033, 3, "ready"},
		{"stale failure", 2, "failed", nil, 6033, 3, "ready"},
		{"absent measurement preserves duration", 4, "ready", nil, 6033, 4, "ready"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp, err := server.UpdateArtifactCatalogSnapshot(ctx, &commodorepb.UpdateArtifactCatalogSnapshotRequest{
				TenantId: tenant, AssetType: commodorepb.ArtifactAssetType_ARTIFACT_ASSET_TYPE_CLIP,
				AssetKey: hash, SourceClusterId: &origin, SourceRevision: tc.revision,
				LifecycleStatus: &tc.lifecycle, DurationMs: tc.duration,
			})
			if err != nil {
				t.Fatalf("project snapshot: %v", err)
			}
			if !resp.GetFound() {
				t.Fatal("clip was not found")
			}
			var duration, revision int64
			var lifecycle string
			if err := conn.QueryRowContext(ctx, `
				SELECT duration, catalog_revision, lifecycle_status FROM commodore.clips
				WHERE tenant_id = $1::uuid AND clip_hash = $2`, tenant, hash).Scan(&duration, &revision, &lifecycle); err != nil {
				t.Fatalf("read projected clip: %v", err)
			}
			if duration != tc.wantDuration || revision != tc.wantRevision || lifecycle != tc.wantStatus {
				t.Fatalf("got duration=%d revision=%d lifecycle=%s, want %d/%d/%s",
					duration, revision, lifecycle, tc.wantDuration, tc.wantRevision, tc.wantStatus)
			}
		})
	}
}

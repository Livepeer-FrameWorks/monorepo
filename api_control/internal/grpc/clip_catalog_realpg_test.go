//go:build schema_verify

package grpc

import (
	"context"
	sharedpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/shared"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"testing"
)

func TestGetClipCatalogLifecycle_RealPG(t *testing.T) {
	db := startCommodoreRealPG(t)
	const tenant = "10000000-0000-4000-8000-000000000081"
	const user = "20000000-0000-4000-8000-000000000081"
	const stream = "30000000-0000-4000-8000-000000000081"
	_, err := db.ExecContext(context.Background(), `INSERT INTO commodore.streams(id,tenant_id,user_id,stream_key,playback_id,internal_name,title) VALUES($1,$2,$3,'catalog-key','catalog-play','live+catalog','Catalog')`, stream, tenant, user)
	if err != nil {
		t.Fatal(err)
	}
	for _, state := range []string{"ready", "failed"} {
		t.Run(state, func(t *testing.T) {
			hash := "catalog-" + state
			_, err := db.ExecContext(context.Background(), `INSERT INTO commodore.clips(tenant_id,user_id,stream_id,clip_hash,internal_name,playback_id,start_time,duration,lifecycle_status,error_message,size_bytes,storage_location,sync_status,is_synced,is_finalized) VALUES($1,$2,$3,$4::text,$4::text,$4::text,1000,60000,$5,'encoder failed',2048,'s3','synced',true,true)`, tenant, user, stream, hash, state)
			if err != nil {
				t.Fatal(err)
			}
			server := &CommodoreServer{db: db}
			clip, err := server.GetClip(ctxAs(user, tenant, "owner"), &sharedpb.GetClipRequest{ClipHash: hash})
			if err != nil {
				t.Fatal(err)
			}
			if clip.GetErrorMessage() != "encoder failed" {
				t.Errorf("missing catalog error: %v", clip)
			}
			if clip.GetStatus() != state {
				t.Errorf("catalog %s returned status %q", state, clip.GetStatus())
			}
			if clip.GetStorageLocation() != "s3" || !clip.GetIsSynced() || !clip.GetIsFinalized() {
				t.Errorf("catalog storage projection missing: %v", clip)
			}
			_, err = server.GetClip(ctxAs(user, "10000000-0000-4000-8000-000000000082", "owner"), &sharedpb.GetClipRequest{ClipHash: hash})
			if status.Code(err) != codes.NotFound {
				t.Errorf("cross-tenant clip error: %v", err)
			}
		})
	}
}

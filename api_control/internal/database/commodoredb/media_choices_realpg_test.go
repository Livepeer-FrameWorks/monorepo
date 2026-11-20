//go:build schema_verify

package commodoredb

import (
	"context"
	"database/sql"
	"errors"
	"testing"
)

func TestMediaChoicesStayTenantScoped_RealPG(t *testing.T) {
	db := startCommodoreQueryCatalogRealPG(t)
	ctx := context.Background()
	q := New(db)
	const tenant = "11000000-0000-0000-0000-000000000091"
	const other = "11000000-0000-0000-0000-000000000092"
	const user = "22000000-0000-0000-0000-000000000091"
	const stream = "33000000-0000-0000-0000-000000000091"
	if _, err := db.Exec(`INSERT INTO commodore.users(id,tenant_id,email,password_hash) VALUES($1,$2,'choices@example.test','x')`, user, tenant); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO commodore.streams(id,tenant_id,user_id,stream_key,playback_id,internal_name,title) VALUES($1,$2,$3,'choices-key','choices-play','choices-stream','Choices')`, stream, tenant, user); err != nil {
		t.Fatal(err)
	}
	const overrideJSON = `[{"process":"Thumbs","track_select":"video=maxbps"}]`
	if _, err := db.Exec(`INSERT INTO commodore.stream_processing_config(stream_id,processes_live) VALUES($1,$2::jsonb)`, stream, overrideJSON); err != nil {
		t.Fatal(err)
	}
	overrides, err := q.GetStreamProcessingOverrides(ctx, GetStreamProcessingOverridesParams{StreamID: stream, TenantID: tenant})
	if err != nil || overrides.ProcessesLive == "" || overrides.ProcessesDvr != "" || overrides.ProcessesVod != "" {
		t.Fatalf("nullable override projection: %+v / %v", overrides, err)
	}
	if _, err := q.GetStreamProcessingOverrides(ctx, GetStreamProcessingOverridesParams{StreamID: stream, TenantID: other}); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("foreign override read: %v", err)
	}
	if err := q.SetTenantTranscodeLadder(ctx, SetTenantTranscodeLadderParams{TenantID: tenant, TranscodeLadder: sql.NullString{String: "qhd", Valid: true}}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE commodore.tenant_processing_config SET processes_live=$2::jsonb WHERE tenant_id=$1`, tenant, overrideJSON); err != nil {
		t.Fatal(err)
	}
	tenantOverrides, err := q.GetTenantProcessingOverrides(ctx, tenant)
	if err != nil || tenantOverrides.ProcessesLive == "" || tenantOverrides.ProcessesDvr != "" || tenantOverrides.ProcessesVod != "" {
		t.Fatalf("nullable tenant override projection: %+v / %v", tenantOverrides, err)
	}
	if err := q.SetCreatedStreamTranscodeLadder(ctx, SetCreatedStreamTranscodeLadderParams{TenantID: tenant, StreamID: stream, TranscodeLadder: "uhd"}); err != nil {
		t.Fatal(err)
	}
	row, err := q.GetStreamProcessingChoices(ctx, GetStreamProcessingChoicesParams{ID: stream, TenantID: tenant})
	if err != nil || row.TranscodeLadder != "uhd" {
		t.Fatalf("stream choice %+v: %v", row, err)
	}
	if _, err := q.GetStreamProcessingChoices(ctx, GetStreamProcessingChoicesParams{ID: stream, TenantID: other}); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("foreign stream read: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO commodore.vod_assets(tenant_id,user_id,vod_hash,internal_name,playback_id,filename,transcode_ladder,size_bytes,original_size_bytes) VALUES($1,$2,'choices-vod','choices-vod-runtime','choices-vod-play','source.mov','uhd',30,20)`, tenant, user); err != nil {
		t.Fatal(err)
	}
	choice, err := q.GetVODTranscodeLadder(ctx, GetVODTranscodeLadderParams{TenantID: tenant, VodHash: "choices-vod"})
	if err != nil || choice != "uhd" {
		t.Fatalf("upload choice %q: %v", choice, err)
	}
	if _, err := q.GetVODTranscodeLadder(ctx, GetVODTranscodeLadderParams{TenantID: other, VodHash: "choices-vod"}); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("foreign upload read: %v", err)
	}
	if err := q.UpsertVodOriginalPolicy(ctx, UpsertVodOriginalPolicyParams{TenantID: tenant, UpdatedBy: user, KeepVodOriginals: sql.NullBool{Bool: false, Valid: true}}); err != nil {
		t.Fatal(err)
	}
	policy, err := q.GetTenantMediaRetentionPolicy(ctx, tenant)
	if err != nil || !policy.KeepVodOriginals.Valid || policy.KeepVodOriginals.Bool {
		t.Fatalf("original opt-out %+v: %v", policy, err)
	}
	if _, err := q.GetVodOriginalPolicy(ctx, other); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("foreign policy read: %v", err)
	}
	catalog, err := q.ListStorageArtifactCatalog(ctx, StorageArtifactFilter{TenantID: tenant, SortField: "created_at", SortDirection: "DESC", Limit: 25})
	if err != nil {
		t.Fatal(err)
	}
	if len(catalog.Rows) != 1 || catalog.Rows[0].OriginalSizeBytes.Int64 != 20 || catalog.Rows[0].SizeBytes.Int64 != 30 {
		t.Fatalf("catalog sizes %+v", catalog.Rows)
	}
}

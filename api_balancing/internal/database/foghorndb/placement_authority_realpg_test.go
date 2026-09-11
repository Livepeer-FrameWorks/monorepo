//go:build schema_verify

package foghorndb

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"testing"
	"time"
)

func TestPlacementAuthorityPairTenantAndVersionFences_RealPG(t *testing.T) {
	verifyPlacementAuthorityPair(t, startFoghornCatalogPostgres(t))
}

func TestPlacementAuthorityPairTenantAndVersionFences_RealYugabyte(t *testing.T) {
	verifyPlacementAuthorityPair(t, startFoghornCatalogYugabyte(t))
}

func verifyPlacementAuthorityPair(t *testing.T, db *sql.DB) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	const tenantID = "10000000-0000-0000-0000-000000000001"
	const authorityID = "live_stream:20000000-0000-0000-0000-000000000001"
	for _, identity := range []struct{ kind, id string }{{"tenant", tenantID}, {"media_object", authorityID}} {
		if _, err := db.ExecContext(ctx, `INSERT INTO foghorn.media_authorities (
authority_kind, authority_id, authority_version, signer_key_id, audience_cell_id,
issued_at, refresh_after, valid_until, payload_sha256, signed_envelope, payload
) VALUES ($1, $2, 7, 'test-key', 'test-cell', NOW(), NOW() + INTERVAL '1 minute', NOW() + INTERVAL '2 minutes',
decode(repeat('00', 32), 'hex'), '\x00'::bytea, '\x01'::bytea)`, identity.kind, identity.id); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO foghorn.tenant_authority_projection (
tenant_id, authority_version, lifecycle, billing_decision, billing_model, valid_until, local_read_ready, local_ingest_ready
) VALUES ($1::uuid, 7, 'active', 'allow', 'postpaid', NOW() + INTERVAL '2 minutes', TRUE, FALSE)`, tenantID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO foghorn.media_object_authority_projection (
authority_id, authority_version, object_kind, tenant_id, internal_name, playback_id, lifecycle,
playback_policy_kind, playback_policy, stream_id, ingest_mode, valid_until, local_read_ready, local_ingest_ready
) VALUES ($1, 7, 'live_stream', $2::uuid, 'placement-internal', 'placement-playback', 'active',
'public', '\x'::bytea, '20000000-0000-0000-0000-000000000001'::uuid, 'push', NOW() + INTERVAL '2 minutes', FALSE, TRUE)`, authorityID, tenantID); err != nil {
		t.Fatal(err)
	}
	q := New(db)
	verifyObjectReadinessTenantFence(t, ctx, db, tenantID, authorityID)
	params := GetLocalPlacementAuthorityPairParams{TenantID: tenantID, AuthorityID: authorityID, InternalName: "placement-internal"}
	got, err := q.GetLocalPlacementAuthorityPair(ctx, params)
	if err != nil {
		t.Fatal(err)
	}
	if got.ObjectAuthorityID != authorityID || got.ObjectAuthorityVersion != 7 || got.TenantAuthorityVersion != 7 ||
		!got.TenantReadReady || got.TenantIngestReady || got.ObjectReadReady || !got.ObjectIngestReady ||
		!got.ObjectValidUntil.After(got.ObjectRefreshAfter) || !got.TenantValidUntil.After(got.TenantRefreshAfter) {
		t.Fatalf("lost authority identity, readiness or lifetime: %+v", got)
	}
	named := GetLocalPlacementAuthorityPairByInternalNameParams{TenantID: tenantID, InternalName: params.InternalName}
	byName, err := q.GetLocalPlacementAuthorityPairByInternalName(ctx, named)
	if err != nil || !reflect.DeepEqual(GetLocalPlacementAuthorityPairRow(byName), got) {
		t.Fatalf("named lookup lost exact joined snapshot: %+v %v", byName, err)
	}
	for _, foreign := range []GetLocalPlacementAuthorityPairByInternalNameParams{
		{TenantID: "10000000-0000-0000-0000-000000000002", InternalName: named.InternalName},
		{TenantID: tenantID, InternalName: "another-name"},
	} {
		if _, err := q.GetLocalPlacementAuthorityPairByInternalName(ctx, foreign); !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("named lookup crossed tenant/name boundary: %v", err)
		}
	}
	for _, mutate := range []func(*GetLocalPlacementAuthorityPairParams){
		func(p *GetLocalPlacementAuthorityPairParams) { p.TenantID = "10000000-0000-0000-0000-000000000002" },
		func(p *GetLocalPlacementAuthorityPairParams) { p.AuthorityID = "another-authority" },
		func(p *GetLocalPlacementAuthorityPairParams) { p.InternalName = "another-name" },
	} {
		foreign := params
		mutate(&foreign)
		if _, err := q.GetLocalPlacementAuthorityPair(ctx, foreign); !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("cross-identity authority read: %v", err)
		}
	}
	if _, err := db.ExecContext(ctx, `UPDATE foghorn.tenant_authority_projection SET authority_version=8 WHERE tenant_id=$1::uuid`, tenantID); err != nil {
		t.Fatal(err)
	}
	if _, err := q.GetLocalPlacementAuthorityPair(ctx, params); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("mismatched tenant projection accepted: %v", err)
	}
	if _, err := q.GetLocalPlacementAuthorityPairByInternalName(ctx, named); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("named lookup accepted mismatched tenant version: %v", err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE foghorn.tenant_authority_projection SET authority_version=7 WHERE tenant_id=$1::uuid`, tenantID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE foghorn.media_object_authority_projection SET authority_version=8 WHERE tenant_id=$1::uuid AND authority_id=$2`, tenantID, authorityID); err != nil {
		t.Fatal(err)
	}
	if _, err := q.GetLocalPlacementAuthorityPair(ctx, params); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("mismatched object projection accepted: %v", err)
	}
	if _, err := q.GetLocalPlacementAuthorityPairByInternalName(ctx, named); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("named lookup accepted mismatched object version: %v", err)
	}
}

func verifyObjectReadinessTenantFence(t *testing.T, ctx context.Context, db *sql.DB, tenantID, authorityID string) {
	t.Helper()
	q := New(db)
	for _, surface := range []string{"playback", "ingest", "source"} {
		for _, match := range []string{"foreign tenant", "stale version", "current"} {
			if _, err := db.ExecContext(ctx, `UPDATE foghorn.media_object_authority_projection
SET local_read_ready=FALSE, local_ingest_ready=FALSE, local_source_ready=FALSE
WHERE tenant_id=$1::uuid AND authority_id=$2`, tenantID, authorityID); err != nil {
				t.Fatal(err)
			}
			id, version := tenantID, int64(7)
			switch match {
			case "foreign tenant":
				id = "10000000-0000-0000-0000-000000000002"
			case "stale version":
				version = 6
			}
			var rows int64
			var err error
			switch surface {
			case "playback":
				rows, err = q.MarkMediaObjectAuthorityLocalReadReady(ctx, MarkMediaObjectAuthorityLocalReadReadyParams{TenantID: id, AuthorityID: authorityID, AuthorityVersion: version})
			case "ingest":
				rows, err = q.MarkMediaObjectAuthorityLocalIngestReady(ctx, MarkMediaObjectAuthorityLocalIngestReadyParams{TenantID: id, AuthorityID: authorityID, AuthorityVersion: version})
			case "source":
				rows, err = q.MarkMediaObjectAuthorityLocalSourceReady(ctx, MarkMediaObjectAuthorityLocalSourceReadyParams{TenantID: id, AuthorityID: authorityID, AuthorityVersion: version})
			}
			want := int64(0)
			if match == "current" {
				want = 1
			}
			if err != nil || rows != want {
				t.Fatalf("%s %s promotion rows=%d err=%v", surface, match, rows, err)
			}
			var read, ingest, source bool
			if err := db.QueryRowContext(ctx, `SELECT local_read_ready, local_ingest_ready, local_source_ready
FROM foghorn.media_object_authority_projection WHERE tenant_id=$1::uuid AND authority_id=$2`, tenantID, authorityID).Scan(&read, &ingest, &source); err != nil {
				t.Fatal(err)
			}
			if read != (want == 1 && surface == "playback") || ingest != (want == 1 && surface == "ingest") || source != (want == 1 && surface == "source") {
				t.Fatalf("%s %s changed unrelated readiness: read=%v ingest=%v source=%v", surface, match, read, ingest, source)
			}
		}
	}
	if _, err := db.ExecContext(ctx, `UPDATE foghorn.media_object_authority_projection
SET local_read_ready=FALSE, local_ingest_ready=TRUE, local_source_ready=FALSE
WHERE tenant_id=$1::uuid AND authority_id=$2`, tenantID, authorityID); err != nil {
		t.Fatal(err)
	}
}

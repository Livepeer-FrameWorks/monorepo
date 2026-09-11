package mediaauthority

import (
	"context"
	"database/sql/driver"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	sharedauthority "github.com/Livepeer-FrameWorks/monorepo/pkg/mediaauthority"
	mediaauthoritypb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_authority"
	placementpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_placement"
	"google.golang.org/protobuf/proto"
)

func TestPlacementReadJoinsTenantAndExactObjectWithoutSecrets(t *testing.T) {
	t.Run("exact", func(t *testing.T) { testPlacementReadJoinsTenant(t, false) })
	t.Run("internal_name", func(t *testing.T) { testPlacementReadJoinsTenant(t, true) })
}

func testPlacementReadJoinsTenant(t *testing.T, byName bool) {
	const tenantID = "10000000-0000-0000-0000-000000000001"
	const streamID = "20000000-0000-0000-0000-000000000001"
	authorityID := sharedauthority.LiveStreamAuthorityID(streamID)
	for _, scenario := range []string{"valid", "tenant_payload_mismatch", "object_tenant_mismatch", "object_name_mismatch", "object_id_mismatch", "object_kind_mismatch", "object_corrupt", "tenant_corrupt"} {
		t.Run(scenario, func(t *testing.T) {
			store, mock, closeDB := newFixtureStore(t, "cell-a")
			defer closeDB()
			store.now = func() time.Time { return storeFixtureNow }
			tenant := &mediaauthoritypb.TenantAuthority{SchemaVersion: 2, TenantId: tenantID, MediaPlacement: &placementpb.PolicySet{Revision: 9}}
			object := &mediaauthoritypb.MediaObjectAuthority{SchemaVersion: 2, TenantId: tenantID, InternalName: "internal",
				ObjectKind: mediaauthoritypb.MediaObjectKind_MEDIA_OBJECT_KIND_LIVE_STREAM, PlacementTenantRevision: 9, MediaPlacement: &placementpb.PolicySet{Revision: 2},
				Object: &mediaauthoritypb.MediaObjectAuthority_LiveStream{LiveStream: &mediaauthoritypb.LiveStreamAuthority{StreamId: streamID, IngestMode: "push"}},
			}
			switch scenario {
			case "tenant_payload_mismatch":
				tenant.TenantId = "other"
			case "object_tenant_mismatch":
				object.TenantId = "other"
			case "object_name_mismatch":
				object.InternalName = "other"
			case "object_id_mismatch":
				object.GetLiveStream().StreamId = "other"
			case "object_kind_mismatch":
				object.ObjectKind = mediaauthoritypb.MediaObjectKind_MEDIA_OBJECT_KIND_UNSPECIFIED
			}
			tenantBytes, err := proto.Marshal(tenant)
			if err != nil {
				t.Fatal(err)
			}
			objectBytes, err := proto.Marshal(object)
			if err != nil {
				t.Fatal(err)
			}
			tenantHash, objectHash := testPayloadDigest(tenantBytes), testPayloadDigest(objectBytes)
			if scenario == "tenant_corrupt" {
				tenantHash[0] ^= 1
			}
			if scenario == "object_corrupt" {
				objectHash[0] ^= 1
			}
			refresh, expiry := storeFixtureNow.Add(-time.Second), storeFixtureNow.Add(time.Minute)
			columns := []string{"object_payload", "object_payload_sha256", "object_refresh_after", "object_valid_until", "object_authority_id", "object_authority_version", "object_read_ready", "object_ingest_ready", "object_source_ready", "tenant_payload", "tenant_payload_sha256", "tenant_refresh_after", "tenant_valid_until", "tenant_authority_version", "tenant_read_ready", "tenant_ingest_ready", "tenant_source_ready"}
			values := []driver.Value{objectBytes, objectHash, refresh, expiry, authorityID, int64(11), true, false, true, tenantBytes, tenantHash, refresh, expiry.Add(time.Minute), int64(12), false, true, false}
			var got PlacementPair
			if byName {
				mock.ExpectQuery("SELECT object_authority.payload AS object_payload").WithArgs(tenantID, "internal").WillReturnRows(sqlmock.NewRows(columns).AddRow(values...))
				got, err = store.PlacementForInternalName(context.Background(), tenantID, "internal")
			} else {
				mock.ExpectQuery("SELECT object_authority.payload AS object_payload").WithArgs(tenantID, authorityID, "internal").WillReturnRows(sqlmock.NewRows(columns).AddRow(values...))
				got, err = store.Placement(context.Background(), tenantID, authorityID, "internal")
			}
			if scenario == "valid" {
				if err != nil {
					t.Fatal(err)
				}
				if !proto.Equal(got.Object.Authority, object) || !proto.Equal(got.Tenant.Authority, tenant) || !got.Object.ValidUntil.Equal(expiry) || !got.Tenant.ValidUntil.Equal(expiry.Add(time.Minute)) || !got.Object.RefreshAfter.Equal(refresh) {
					t.Fatalf("lost signed placement pair: %+v", got)
				}
				if !got.Object.Ready || got.Object.IngestReady || !got.Object.SourceReady || got.Tenant.Ready || !got.Tenant.IngestReady || got.Tenant.SourceReady || got.Object.Freshness != FreshnessSoftExpired {
					t.Fatal("reader merged independent readiness or freshness")
				}
			} else if err == nil {
				t.Fatal("inconsistent or corrupt placement pair accepted")
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestPlacementReadRejectsIncompleteIdentityBeforeQuery(t *testing.T) {
	store, mock, closeDB := newFixtureStore(t, "cell-a")
	defer closeDB()
	for _, args := range [][3]string{{"", "object", "name"}, {"tenant", "", "name"}, {"tenant", "object", ""}, {" tenant", "object", "name"}, {"tenant", "object", "name "}} {
		if _, err := store.Placement(context.Background(), args[0], args[1], args[2]); err == nil {
			t.Fatal("unscoped placement query accepted")
		}
	}
	for _, args := range [][2]string{{"", "name"}, {"tenant", ""}, {" tenant", "name"}, {"tenant", "name "}} {
		if _, err := store.PlacementForInternalName(context.Background(), args[0], args[1]); err == nil {
			t.Fatal("unscoped named placement query accepted")
		}
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

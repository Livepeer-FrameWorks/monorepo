package mediaauthority

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	sharedauthority "github.com/Livepeer-FrameWorks/monorepo/pkg/mediaauthority"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	mediapb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_authority"
	"google.golang.org/protobuf/proto"
)

func TestManagedSourceMissingTenantIsIncompleteAndFetched(t *testing.T) {
	store, mock, closeDB := newFixtureStore(t, "cell-a")
	defer closeDB()
	object := &mediapb.MediaObjectAuthority{TenantId: "tenant", InternalName: "internal",
		Object: &mediapb.MediaObjectAuthority_LiveStream{LiveStream: &mediapb.LiveStreamAuthority{StreamId: "stream", IngestMode: "mist_native"}}}
	payload, err := proto.Marshal(object)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(payload)
	until := store.now().Add(time.Hour)
	mock.ExpectQuery("ListLocalManagedStreamAuthorities").WillReturnRows(sqlmock.NewRows([]string{
		"authority_id", "authority_version", "payload", "payload_sha256", "refresh_after", "valid_until",
		"local_source_ready", "withheld_by_tenant_revival", "tenant_authority_version",
	}).AddRow("stream", 2, payload, digest[:], until, until, true, false, 0))
	mock.ExpectQuery("GetLocalTenantSourceAuthority").WithArgs("tenant").WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery("BeginMediaAuthorityConfirmation").WillReturnRows(sqlmock.NewRows([]string{"started_at"}).AddRow(store.now()))
	fetched := make(chan AuthorityLookup, 1)
	store.SetAuthorityFetcher(func(_ context.Context, lookup AuthorityLookup) ([][]byte, error) {
		fetched <- lookup
		return nil, nil
	})
	set, err := store.ManagedStreams(context.Background(), "cluster")
	if err != nil || !set.Marked || set.Complete {
		t.Fatalf("missing parent must allow connected fallback without retract: %+v, %v", set, err)
	}
	select {
	case lookup := <-fetched:
		if lookup.AuthorityID != "stream" {
			t.Fatalf("fetched %+v, want the object and parent pair", lookup)
		}
	case <-time.After(time.Second):
		t.Fatal("missing managed-stream parent was not fetched")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestManagedSourceExpiredTombstoneIsCompleteWithoutParentOrFetch(t *testing.T) {
	store, mock, closeDB := newFixtureStore(t, "cell-a")
	defer closeDB()
	object := &mediapb.MediaObjectAuthority{
		TenantId: "tenant", Lifecycle: mediapb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_TOMBSTONE,
		Object: &mediapb.MediaObjectAuthority_LiveStream{LiveStream: &mediapb.LiveStreamAuthority{StreamId: "stream", IngestMode: "mist_native"}},
	}
	payload, err := proto.Marshal(object)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(payload)
	until := store.now().Add(-time.Hour)
	mock.ExpectQuery("ListLocalManagedStreamAuthorities").WillReturnRows(sqlmock.NewRows([]string{
		"authority_id", "authority_version", "payload", "payload_sha256", "refresh_after", "valid_until",
		"local_source_ready", "withheld_by_tenant_revival", "tenant_authority_version",
	}).AddRow("stream", 2, payload, digest[:], until, until, true, true, 0))
	set, err := store.ManagedStreams(context.Background(), "cluster")
	if err != nil || !set.Marked || !set.Complete || len(set.Rows) != 0 {
		t.Fatalf("expired tombstone must be a complete empty desired set: %+v, %v", set, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestManagedSourceShadowRejectsPlacementSchemaBeforeSecrets(t *testing.T) {
	for _, schemas := range [][2]uint32{{1, 2}, {2, 1}, {2, 2}} {
		store, mock, closeDB := newFixtureStore(t, "cell-a")
		t.Cleanup(closeDB)
		tenant := &mediapb.TenantAuthority{SchemaVersion: schemas[0], TenantId: "tenant"}
		object := &mediapb.MediaObjectAuthority{SchemaVersion: schemas[1], TenantId: "tenant", InternalName: "internal", ObjectKind: mediapb.MediaObjectKind_MEDIA_OBJECT_KIND_LIVE_STREAM,
			Object: &mediapb.MediaObjectAuthority_LiveStream{LiveStream: &mediapb.LiveStreamAuthority{StreamId: "stream", IngestMode: "mist_native"}}}
		objectBytes, err := proto.Marshal(object)
		if err != nil {
			t.Fatal(err)
		}
		tenantBytes, err := proto.Marshal(tenant)
		if err != nil {
			t.Fatal(err)
		}
		objectHash, tenantHash := sha256.Sum256(objectBytes), sha256.Sum256(tenantBytes)
		until := store.now().Add(time.Hour)
		mock.ExpectQuery("SELECT authority.payload").WillReturnRows(sqlmock.NewRows([]string{"payload", "payload_sha256", "refresh_after", "valid_until", "authority_id", "authority_version", "local_source_ready", "withheld_by_tenant_revival", "tenant_authority_version"}).
			AddRow(objectBytes, objectHash[:], until, until, sharedauthority.LiveStreamAuthorityID("stream"), 2, false, false, int64(2)))
		mock.ExpectQuery("SELECT authority.payload").WillReturnRows(sqlmock.NewRows([]string{"payload", "payload_sha256", "refresh_after", "valid_until", "authority_version", "local_source_ready"}).
			AddRow(tenantBytes, tenantHash[:], until, until, 2, false))
		got, err := store.PromoteManagedStreamIfMatching(context.Background(), "cluster", &commodorepb.ManagedStreamRow{InternalName: "internal"}, &commodorepb.ResolveStreamContextResponse{Admitted: true})
		if err != nil || got != ManagedStreamPromotionMismatch {
			t.Fatalf("schema %v reached secret access or promotion: %s, %v", schemas, got, err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	}
}

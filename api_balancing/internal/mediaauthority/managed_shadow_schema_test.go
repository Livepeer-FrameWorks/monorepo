package mediaauthority

import (
	"context"
	"crypto/sha256"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	sharedauthority "github.com/Livepeer-FrameWorks/monorepo/pkg/mediaauthority"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	mediapb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_authority"
	"google.golang.org/protobuf/proto"
)

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
		mock.ExpectQuery("SELECT authority.payload").WillReturnRows(sqlmock.NewRows([]string{"payload", "payload_sha256", "refresh_after", "valid_until", "authority_id", "authority_version", "local_source_ready"}).
			AddRow(objectBytes, objectHash[:], until, until, sharedauthority.LiveStreamAuthorityID("stream"), 2, false))
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

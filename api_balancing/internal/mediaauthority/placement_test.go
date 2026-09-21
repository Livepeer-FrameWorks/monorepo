package mediaauthority

import (
	"context"
	"errors"
	"regexp"
	"testing"

	"frameworks/api_balancing/internal/database/foghorndb"
	"github.com/DATA-DOG/go-sqlmock"
	sharedauthority "github.com/Livepeer-FrameWorks/monorepo/pkg/mediaauthority"
	mediaauthoritypb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_authority"
	placementpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_placement"
	"google.golang.org/protobuf/proto"
)

func TestPlacementReadinessIsSchemaSpecific(t *testing.T) {
	store, _, closeDB := newFixtureStore(t, "cell-a")
	defer closeDB()
	for _, tenant := range []bool{false, true} {
		for _, previousSchema := range []uint32{1, 2} {
			for _, nextSchema := range []uint32{1, 2} {
				var previous proto.Message
				verified := &sharedauthority.Verified{}
				if tenant {
					previous = &mediaauthoritypb.TenantAuthority{SchemaVersion: previousSchema}
					verified.Tenant = &mediaauthoritypb.TenantAuthority{SchemaVersion: nextSchema}
				} else {
					previous = &mediaauthoritypb.MediaObjectAuthority{SchemaVersion: previousSchema}
					verified.MediaObject = &mediaauthoritypb.MediaObjectAuthority{SchemaVersion: nextSchema}
				}
				encoded, err := proto.Marshal(previous)
				if err != nil {
					t.Fatal(err)
				}
				preserved := store.readinessPreservation(foghorndb.GetMediaAuthorityForUpdateRow{Payload: encoded}, nil, verified)
				want := previousSchema == nextSchema
				if preserved.read != want || preserved.ingest != want || preserved.source != want {
					t.Fatalf("schema %d -> %d tenant=%t preservation=%+v", previousSchema, nextSchema, tenant, preserved)
				}
			}
		}
	}
}

func TestPlacementRevisionRollbackFences(t *testing.T) {
	for _, tenant := range []bool{false, true} {
		for _, test := range []struct {
			name             string
			schema           uint32
			revision, parent uint64
			want             bool
		}{
			{"newer", 2, 9, 8, false},
			{"same", 2, 7, 8, false},
			{"cleared without revision", 2, 0, 8, true},
			{"old intent", 2, 6, 8, true},
			{"lossy schema", 1, 7, 8, true},
			{"old parent", 2, 7, 6, !tenant},
		} {
			t.Run(test.name, func(t *testing.T) {
				var previous proto.Message
				verified := &sharedauthority.Verified{}
				if tenant {
					previous = &mediaauthoritypb.TenantAuthority{SchemaVersion: 2, MediaPlacement: &placementpb.PolicySet{Revision: 7}}
					verified.Tenant = &mediaauthoritypb.TenantAuthority{SchemaVersion: test.schema, MediaPlacement: &placementpb.PolicySet{Revision: test.revision}}
				} else {
					previous = &mediaauthoritypb.MediaObjectAuthority{SchemaVersion: 2, MediaPlacement: &placementpb.PolicySet{Revision: 7}, PlacementTenantRevision: 8}
					verified.MediaObject = &mediaauthoritypb.MediaObjectAuthority{SchemaVersion: test.schema, MediaPlacement: &placementpb.PolicySet{Revision: test.revision}, PlacementTenantRevision: test.parent}
				}
				encoded, err := proto.Marshal(previous)
				if err != nil {
					t.Fatal(err)
				}
				got, err := rejectsPlacementRollback(encoded, verified)
				if err != nil || got != test.want {
					t.Fatalf("rollback tenant=%t: %t %v", tenant, got, err)
				}
			})
		}
	}
}

func TestStoreRejectsLegacySchemaEvenWithNewerAuthorityVersion(t *testing.T) {
	encoded, _, signed := storeFixture(t, "cell-a")
	previous := &mediaauthoritypb.TenantAuthority{}
	if err := proto.Unmarshal(signed.GetEnvelope().GetPayload(), previous); err != nil {
		t.Fatal(err)
	}
	previous.SchemaVersion = sharedauthority.PlacementSchemaVersion
	previous.MediaPlacement = &placementpb.PolicySet{Revision: 1}
	previousBytes, err := proto.Marshal(previous)
	if err != nil {
		t.Fatal(err)
	}
	store, mock, closeDB := newFixtureStore(t, "cell-a")
	defer closeDB()
	mock.ExpectBegin()
	expectMediaAuthorityLockTimeout(mock)
	mock.ExpectExec(regexp.QuoteMeta("SELECT pg_advisory_xact_lock(")).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT authority_version, payload_sha256, payload")).WillReturnRows(
		sqlmock.NewRows([]string{"authority_version", "payload_sha256", "payload", "valid_until"}).AddRow(int64(6), testPayloadDigest(previousBytes), previousBytes, storeFixtureNow.AddDate(0, 0, 1)),
	)
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO foghorn.media_authority_apply_audit")).
		WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), "rollback_rejected", ErrRollback.Error()).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()
	if _, err := store.Apply(context.Background(), encoded); !errors.Is(err, ErrRollback) {
		t.Fatalf("lossy legacy refresh accepted: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

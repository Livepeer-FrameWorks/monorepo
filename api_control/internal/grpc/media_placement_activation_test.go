package grpc

import (
	"context"
	"regexp"
	"testing"
	"time"

	"frameworks/api_control/internal/database/commodoredb"
	"frameworks/api_control/internal/placementpolicy"
	"github.com/DATA-DOG/go-sqlmock"
	mediapb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_authority"
	placementpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_placement"
	"github.com/sirupsen/logrus"
	"google.golang.org/protobuf/proto"
)

func rolloutRows(t *testing.T, payload proto.Message, schema int32, cells ...struct {
	id, status string
	ready      bool
}) *sqlmock.Rows {
	t.Helper()
	encoded, err := proto.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	rows := sqlmock.NewRows([]string{"authority_version", "payload", "payload_schema_version", "valid_until", "cell_id", "delivery_status", "acknowledged_at", "cell_ready", "cell_schema_version"})
	for _, cell := range cells {
		rows.AddRow(int64(7), encoded, schema, time.Now().Add(time.Hour), cell.id, cell.status, nil, cell.ready, int32(2))
	}
	return rows
}

type rolloutCell = struct {
	id, status string
	ready      bool
}

func TestReconcilePlacementActivationFollowsAcknowledgementsAndAttestation(t *testing.T) {
	tenant := &mediapb.TenantAuthority{SchemaVersion: 2, TenantId: "5eed517e-ba5e-da7a-517e-ba5eda7a0001", MediaPlacement: &placementpb.PolicySet{Revision: 3}}
	object := &mediapb.MediaObjectAuthority{SchemaVersion: 2, TenantId: tenant.TenantId, ObjectKind: mediapb.MediaObjectKind_MEDIA_OBJECT_KIND_LIVE_STREAM,
		MediaPlacement: &placementpb.PolicySet{Revision: 2}, PlacementTenantRevision: 3,
		Object: &mediapb.MediaObjectAuthority_LiveStream{LiveStream: &mediapb.LiveStreamAuthority{StreamId: "5eedfeed-11fe-ca57-feed-11feca570001", IngestMode: "push"}}}
	for _, test := range []struct {
		name     string
		kind     string
		payload  proto.Message
		schema   int32
		cells    []rolloutCell
		expect   func(sqlmock.Sqlmock)
		status   placementpb.RolloutStatus
		required uint32
		applied  uint32
	}{
		{"every cell acknowledged and attested activates the tenant revision", "tenant", tenant, 2,
			[]rolloutCell{{"cell-a", "acknowledged", true}, {"cell-b", "acknowledged", true}},
			func(mock sqlmock.Sqlmock) {
				mock.ExpectExec(regexp.QuoteMeta("UPDATE commodore.media_placement_policies")).WithArgs(int64(3), int64(0), sqlmock.AnyArg(), tenant.TenantId, "tenant", tenant.TenantId).WillReturnResult(sqlmock.NewResult(0, 1))
				mock.ExpectExec(regexp.QuoteMeta("SET rollout_status = 'effective'")).WithArgs(tenant.TenantId, "tenant", tenant.TenantId, int64(3)).WillReturnResult(sqlmock.NewResult(0, 1))
			}, placementpb.RolloutStatus_ROLLOUT_STATUS_EFFECTIVE, 2, 2},
		{"a pending delivery keeps the change pending", "tenant", tenant, 2,
			[]rolloutCell{{"cell-a", "acknowledged", true}, {"cell-b", "pending", true}},
			func(mock sqlmock.Sqlmock) {
				mock.ExpectExec(regexp.QuoteMeta("UPDATE commodore.media_placement_changes")).WithArgs("pending", "", tenant.TenantId, "tenant", tenant.TenantId, int64(3)).WillReturnResult(sqlmock.NewResult(0, 0))
			}, placementpb.RolloutStatus_ROLLOUT_STATUS_PENDING, 2, 1},
		{"an unattested cell blocks the change with a reason", "tenant", tenant, 2,
			[]rolloutCell{{"cell-a", "acknowledged", true}, {"cell-b", "acknowledged", false}},
			func(mock sqlmock.Sqlmock) {
				mock.ExpectExec(regexp.QuoteMeta("UPDATE commodore.media_placement_changes")).WithArgs("blocked", placementRolloutReasonUnattested, tenant.TenantId, "tenant", tenant.TenantId, int64(3)).WillReturnResult(sqlmock.NewResult(0, 1))
			}, placementpb.RolloutStatus_ROLLOUT_STATUS_BLOCKED, 2, 1},
		{"a legacy authority never activates", "tenant", &mediapb.TenantAuthority{SchemaVersion: 1, TenantId: tenant.TenantId}, 1,
			[]rolloutCell{{"cell-a", "acknowledged", true}}, func(sqlmock.Sqlmock) {}, placementpb.RolloutStatus_ROLLOUT_STATUS_PENDING, 1, 0},
		{"a live stream activates its own revision under the tenant parent", "media_object", object, 2,
			[]rolloutCell{{"cell-a", "acknowledged", true}},
			func(mock sqlmock.Sqlmock) {
				mock.ExpectExec(regexp.QuoteMeta("UPDATE commodore.media_placement_policies")).WithArgs(int64(2), int64(3), sqlmock.AnyArg(), tenant.TenantId, "stream", object.GetLiveStream().GetStreamId()).WillReturnResult(sqlmock.NewResult(0, 1))
				mock.ExpectExec(regexp.QuoteMeta("SET rollout_status = 'effective'")).WithArgs(tenant.TenantId, "stream", object.GetLiveStream().GetStreamId(), int64(2)).WillReturnResult(sqlmock.NewResult(0, 1))
			}, placementpb.RolloutStatus_ROLLOUT_STATUS_EFFECTIVE, 1, 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			authorityID := tenant.TenantId
			if test.kind == "media_object" {
				authorityID = "live_stream:" + object.GetLiveStream().GetStreamId()
			}
			mock.ExpectQuery(regexp.QuoteMeta("FROM commodore.media_authority_current AS current")).WithArgs(test.kind, authorityID).WillReturnRows(rolloutRows(t, test.payload, test.schema, test.cells...))
			view, err := placementRolloutFor(context.Background(), commodoredb.New(db), test.kind, authorityID)
			if err != nil || view == nil || view.Rollout.GetStatus() != test.status || view.Rollout.GetRequiredRecipients() != test.required || view.Rollout.GetAppliedRecipients() != test.applied {
				t.Fatalf("rollout view = %+v, %v", view, err)
			}
			// Derivation serializes on the authority before reading, so that
			// concurrent acknowledgements cannot each miss the others.
			mock.ExpectExec(regexp.QuoteMeta("pg_advisory_xact_lock")).WithArgs(test.kind, authorityID).WillReturnResult(sqlmock.NewResult(0, 0))
			mock.ExpectQuery(regexp.QuoteMeta("FROM commodore.media_authority_current AS current")).WithArgs(test.kind, authorityID).WillReturnRows(rolloutRows(t, test.payload, test.schema, test.cells...))
			test.expect(mock)
			server := &CommodoreServer{logger: logrus.New()}
			if err := server.reconcilePlacementActivation(context.Background(), commodoredb.New(db), test.kind, authorityID); err != nil {
				t.Fatal(err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestAttachPlacementRolloutDemotesStaleEffectiveState(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	tenant := &mediapb.TenantAuthority{SchemaVersion: 2, TenantId: "5eed517e-ba5e-da7a-517e-ba5eda7a0001", MediaPlacement: &placementpb.PolicySet{Revision: 1}}
	mock.ExpectQuery(regexp.QuoteMeta("FROM commodore.media_authority_current AS current")).WithArgs("tenant", tenant.TenantId).
		WillReturnRows(rolloutRows(t, tenant, 2, rolloutCell{"cell-a", "acknowledged", true}, rolloutCell{"cell-new", "pending", true}))
	server := &CommodoreServer{db: db, logger: logrus.New()}
	rollout := &placementpb.Rollout{Status: placementpb.RolloutStatus_ROLLOUT_STATUS_EFFECTIVE}
	server.attachPlacementRollout(context.Background(), placementpolicy.Scope{TenantID: tenant.TenantId, Kind: "tenant", ID: tenant.TenantId}, rollout)
	if rollout.GetStatus() != placementpb.RolloutStatus_ROLLOUT_STATUS_PENDING || rollout.GetRequiredRecipients() != 2 || rollout.GetAppliedRecipients() != 1 || len(rollout.GetPendingRecipients()) != 1 || rollout.GetPendingRecipients()[0].GetId() != "cell-new" {
		t.Fatalf("stale effective rollout was not demoted: %+v", rollout)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

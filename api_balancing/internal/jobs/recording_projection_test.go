package jobs

import (
	"context"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
)

func TestRecordingCaptureProjectionWaitsForConsumerAcknowledgement(t *testing.T) {
	for _, ack := range []bool{false, true} {
		t.Run(map[bool]string{false: "legacy_consumer", true: "upgraded_consumer"}[ack], func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			start := time.Unix(1800000000, 0).UTC()
			end := start.Add(time.Hour)
			client := &mockCommodoreClient{snapshotRespFn: func(request *pb.UpdateArtifactCatalogSnapshotRequest) (*pb.UpdateArtifactCatalogSnapshotResponse, error) {
				if !request.GetRecordingTimesPresent() || !request.GetStartedAt().AsTime().Equal(start) || !request.GetEndedAt().AsTime().Equal(end) {
					t.Errorf("recording times absent from projection: %v", request)
				}
				return &pb.UpdateArtifactCatalogSnapshotResponse{Found: true, CurrentRevision: request.GetSourceRevision(), RecordingTimesPresent: ack}, nil
			}}
			reconciler := newTestReconciler(t, db, nil, client, nil)
			reconciler.clusterID = "origin"
			mock.ExpectQuery("FROM foghorn.artifacts").WillReturnRows(sqlmock.NewRows(projectionRowCols).AddRow("recording", "dvr", "tenant", "origin", false, nil, nil, nil, "synced", true, "s3", int64(7), "completed", nil, nil, nil, nil, start, end))
			if ack {
				mock.ExpectExec("AdvanceCatalogWatermark").WillReturnResult(sqlmock.NewResult(0, 1))
			} else {
				mock.ExpectExec("BackoffCatalogProjection").WillReturnResult(sqlmock.NewResult(0, 1))
			}
			advanced, scanned := reconciler.projectCommodoreArtifactState(context.Background())
			want := 0
			if ack {
				want = 1
			}
			if advanced != want || scanned != 1 {
				t.Fatalf("recording acknowledged too early or never retried: advanced=%d scanned=%d want=%d/1", advanced, scanned, want)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestRecordingCaptureProjectionRepairsPreviouslyAcknowledgedRevision(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	client := &mockCommodoreClient{}
	reconciler := newTestReconciler(t, db, nil, client, nil)
	reconciler.clusterID = "origin"
	mock.ExpectQuery("catalog_capture_times_synced_rev").WillReturnRows(sqlmock.NewRows(projectionRowCols).AddRow("recording", "dvr", "tenant", "origin", false, nil, nil, nil, "synced", true, "s3", int64(7), "completed", nil, nil, nil, nil, nil, nil))
	mock.ExpectExec("catalog_capture_times_synced_rev").WillReturnResult(sqlmock.NewResult(0, 1))
	advanced, scanned := reconciler.projectCommodoreArtifactState(context.Background())
	if advanced != 1 || scanned != 1 || len(client.snapshotCalls) != 1 {
		t.Fatalf("old consumer's acknowledged revision never reprojected: advanced=%d scanned=%d calls=%d", advanced, scanned, len(client.snapshotCalls))
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

package handlers

import (
	"database/sql"
	"errors"
	"testing"

	"frameworks/api_balancing/internal/control"

	"github.com/DATA-DOG/go-sqlmock"
	sharedpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/shared"
)

func stoppedDVRResolution() *control.ContentResolution {
	return &control.ContentResolution{ContentType: "dvr", ContentId: "dvr-pid", InternalName: "dvr+recording-name",
		ArtifactHash: "dvrhash1", ArtifactID: "recording-id", TenantId: "tenant", StreamId: "parent-stream",
		ParentStreamInternalName: "parent-name", LocalAuthority: true}
}

func expectStoppedDVRDispatch(mock sqlmock.Sqlmock) {
	mock.ExpectQuery("SELECT COALESCE\\(artifact_type").
		WithArgs("dvrhash1").WillReturnRows(sqlmock.NewRows([]string{"artifact_type", "stream_id", "stream_internal_name"}).
		AddRow("dvr", "parent-stream", "parent-name"))
	mock.ExpectQuery("SELECT status").WithArgs("dvrhash1").
		WillReturnRows(sqlmock.NewRows([]string{"status"}).AddRow("completed"))
}

// A stopped recording's own playbackId looks up its most recent finalized
// chapter. With none yet it reports errDVRChaptersPending, which the HTTP
// handler answers with 409 DVR_CHAPTERS_PENDING.
func TestResolveDVRViewerEndpoint_StoppedWithoutChapterIsPending(t *testing.T) {
	mock := withMockDBSourceRes(t)
	prevControlDB := control.GetDB()
	control.SetDB(db)
	t.Cleanup(func() { control.SetDB(prevControlDB) })

	expectStoppedDVRDispatch(mock)
	mock.ExpectQuery("SELECT playback_id\\s+FROM foghorn.dvr_chapters").WithArgs("dvrhash1").
		WillReturnError(sql.ErrNoRows)

	_, err := resolveDVRViewerEndpoint(t.Context(), &sharedpb.ViewerEndpointRequest{ContentId: "dvr-pid", Protocol: "hls"}, 0, 0, stoppedDVRResolution())
	if !errors.Is(err, errDVRChaptersPending) {
		t.Fatalf("stopped DVR without chapters: want errDVRChaptersPending, got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// A chapter lookup failure is an error of its own, not "no chapters".
func TestResolveDVRViewerEndpoint_StoppedChapterLookupError(t *testing.T) {
	mock := withMockDBSourceRes(t)
	prevControlDB := control.GetDB()
	control.SetDB(db)
	t.Cleanup(func() { control.SetDB(prevControlDB) })

	expectStoppedDVRDispatch(mock)
	mock.ExpectQuery("SELECT playback_id\\s+FROM foghorn.dvr_chapters").WithArgs("dvrhash1").
		WillReturnError(errors.New("db down"))

	_, err := resolveDVRViewerEndpoint(t.Context(), &sharedpb.ViewerEndpointRequest{ContentId: "dvr-pid", Protocol: "hls"}, 0, 0, stoppedDVRResolution())
	if err == nil || errors.Is(err, errDVRChaptersPending) {
		t.Fatalf("chapter lookup failure: want a lookup error, got %v", err)
	}
}

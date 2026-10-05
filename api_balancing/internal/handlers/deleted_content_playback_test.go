package handlers

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"frameworks/api_balancing/internal/control"

	"github.com/DATA-DOG/go-sqlmock"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	sharedpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/shared"
)

// Deleting an artifact commits in Foghorn before the catalog projection removes
// its playback ID, so a viewer can still resolve the ID while the artifact row
// is already deleted. That is missing content: 404, not a 500 resolution failure.
func TestGenericViewerPlayback_DeletedArtifactIsNotFound(t *testing.T) {
	setupPreparedViewerHTTP(t, false)
	mock := withMockDBSourceRes(t)
	mock.MatchExpectationsInOrder(false)
	startCommodoreFakeArms(t, &commodoreArmsFake{artifactPlaybackID: func(context.Context, *commodorepb.ResolveArtifactPlaybackIDRequest) (*commodorepb.ResolveArtifactPlaybackIDResponse, error) {
		return &commodorepb.ResolveArtifactPlaybackIDResponse{Found: true, ContentType: "clip", ArtifactHash: "deleted-clip", InternalName: "clip-internal", TenantId: "owner", StreamId: "parent"}, nil
	}})
	mock.ExpectQuery(`FROM foghorn.artifacts\s+WHERE artifact_hash = \$1 AND artifact_type = \$2 AND status != 'deleted'`).
		WillReturnRows(sqlmock.NewRows([]string{"internal_name"}))

	c, w := playbackCtxArms(t, "clip-public/hls")
	HandleGenericViewerPlayback(c)
	if w.Code != http.StatusNotFound {
		t.Fatalf("deleted clip: status=%d body=%s, want 404", w.Code, w.Body.String())
	}
}

// A stopped recording that has been deleted has no chapters to wait for; it is
// gone, not pending.
func TestResolveDVRViewerEndpoint_DeletedDVRIsNotFound(t *testing.T) {
	mock := withMockDBSourceRes(t)
	prevControlDB := control.GetDB()
	control.SetDB(db)
	t.Cleanup(func() { control.SetDB(prevControlDB) })

	mock.ExpectQuery("SELECT COALESCE\\(artifact_type").
		WithArgs("dvrhash1").WillReturnRows(sqlmock.NewRows([]string{"artifact_type", "stream_id", "stream_internal_name"}).
		AddRow("dvr", "parent-stream", "parent-name"))
	mock.ExpectQuery("SELECT status").WithArgs("dvrhash1").
		WillReturnRows(sqlmock.NewRows([]string{"status"}).AddRow("deleted"))
	mock.ExpectQuery("SELECT playback_id\\s+FROM foghorn.dvr_chapters").WithArgs("dvrhash1").
		WillReturnRows(sqlmock.NewRows([]string{"playback_id"}))

	_, err := resolveDVRViewerEndpoint(t.Context(), &sharedpb.ViewerEndpointRequest{ContentId: "dvr-pid", Protocol: "hls"}, 0, 0, stoppedDVRResolution())
	if !errors.Is(err, control.ErrPlaybackContentNotFound) {
		t.Fatalf("deleted DVR: want ErrPlaybackContentNotFound, got %v", err)
	}
}

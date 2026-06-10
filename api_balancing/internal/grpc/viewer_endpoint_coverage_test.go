package grpc

import (
	"context"
	"testing"

	"frameworks/api_balancing/internal/control"

	"github.com/DATA-DOG/go-sqlmock"
	sharedpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/shared"
	"github.com/sirupsen/logrus"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// These tests lock the playback endpoint-resolution decision tree's
// fail-fast guards and the active/stopped-DVR identity rewrite. The
// happy-path type dispatch (live/dvr/clip/vod winners) and the billing /
// x402 payment gate inside ResolveViewerEndpoint sit behind a concrete
// *commodore.GRPCClient (control.CommodoreClient is not an interface), so
// ResolveContent cannot be driven to a successful resolution from this
// package; those arms are exercised by the control-package resolver tests
// instead. Here we pin the branches reachable without a live Commodore.

func newViewerEndpointServer(t *testing.T) *FoghornGRPCServer {
	t.Helper()
	return &FoghornGRPCServer{logger: logrus.New()}
}

// ResolveViewerEndpoint must reject an empty content_id at the door with
// InvalidArgument before any resolution or routing work — content_id is the
// only identity the resolver has, so an empty one can never produce a
// correct endpoint.
func TestResolveViewerEndpoint_EmptyContentIDRejected(t *testing.T) {
	s := newViewerEndpointServer(t)
	_, err := s.ResolveViewerEndpoint(context.Background(), &sharedpb.ViewerEndpointRequest{ContentId: ""})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("empty content_id: want InvalidArgument, got %v", err)
	}
}

// ResolveViewerEndpoint resolves the content type from the public ID and
// never trusts a caller-provided type. When the ID resolves to nothing
// (no Commodore wired, so ResolveContent fails), the request fails NotFound
// rather than guessing a type or falling through to a default lane.
func TestResolveViewerEndpoint_UnknownContentIDIsNotFound(t *testing.T) {
	prev := control.CommodoreClient
	control.CommodoreClient = nil
	t.Cleanup(func() { control.CommodoreClient = prev })

	s := newViewerEndpointServer(t)
	_, err := s.ResolveViewerEndpoint(context.Background(), &sharedpb.ViewerEndpointRequest{ContentId: "does-not-exist"})
	if status.Code(err) != codes.NotFound {
		t.Fatalf("unknown content_id: want NotFound, got %v", err)
	}
}

// resolveLiveViewerEndpoint fails fast with NotFound when it has no internal
// stream name to route to. An empty internal name means there is no Mist
// stream identity to select an edge for, so it must reject rather than call
// the load balancer with an empty key.
func TestResolveLiveViewerEndpoint_EmptyInternalNameNotFound(t *testing.T) {
	s := newViewerEndpointServer(t)
	_, err := s.resolveLiveViewerEndpoint(context.Background(),
		&sharedpb.ViewerEndpointRequest{ContentId: "c1"},
		0, 0, "" /*internalName*/, "tenant-1", "stream-1", nil)
	if status.Code(err) != codes.NotFound {
		t.Fatalf("empty internalName: want NotFound, got %v", err)
	}
}

// resolveDVRViewerEndpoint, when the DVR is no longer active (dispatch
// resolves to nil with no Commodore) and no playable chapter has been
// dispatched yet, must return FailedPrecondition pointing the caller at
// dvrChapters — NOT silently route through the archive/warm-cache lane,
// which would land viewers on stale segments.
func TestResolveDVRViewerEndpoint_StoppedNoChapterFailsPrecondition(t *testing.T) {
	prev := control.CommodoreClient
	control.CommodoreClient = nil
	t.Cleanup(func() { control.CommodoreClient = prev })

	s := newViewerEndpointServer(t)
	_, err := s.resolveDVRViewerEndpoint(context.Background(),
		&sharedpb.ViewerEndpointRequest{ContentId: "dvr-pid"},
		0, 0,
		&control.ContentResolution{ContentId: "dvr-pid", InternalName: "dvr+abc", TenantId: "tenant-1"})
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("stopped DVR with no chapter: want FailedPrecondition, got %v", err)
	}
}

// overrideActiveDVRMetadata rewrites live-resolver metadata to the DVR
// identity that is actually being served: ContentType/Status flip to the
// recording lane while IsLive stays true (the surface remains
// live-replayable). Without this the client sees ContentType="live" for an
// active DVR and loses the rolling-window/seek distinction.
func TestOverrideActiveDVRMetadata_RewritesToDVRIdentity(t *testing.T) {
	resp := &sharedpb.ViewerEndpointResponse{
		Metadata: &sharedpb.PlaybackMetadata{
			ContentType: "live",
			Status:      "live",
			IsLive:      true,
		},
	}
	overrideActiveDVRMetadata(resp, &control.DVRArtifactDispatch{DVRHash: "h", Status: "recording"})
	if resp.Metadata.ContentType != "dvr" {
		t.Fatalf("ContentType: got %q want dvr", resp.Metadata.ContentType)
	}
	if resp.Metadata.Status != "recording" || resp.Metadata.DvrStatus != "recording" {
		t.Fatalf("status: got status=%q dvr_status=%q want recording/recording", resp.Metadata.Status, resp.Metadata.DvrStatus)
	}
	if !resp.Metadata.IsLive {
		t.Fatal("IsLive must stay true: an active DVR surface is live-replayable")
	}
}

// overrideActiveDVRMetadata is a no-op on nil inputs (nil response, nil
// metadata, nil dispatch) so the caller can invoke it unconditionally
// without a nil-panic.
func TestOverrideActiveDVRMetadata_NilSafe(t *testing.T) {
	overrideActiveDVRMetadata(nil, &control.DVRArtifactDispatch{})
	overrideActiveDVRMetadata(&sharedpb.ViewerEndpointResponse{}, &control.DVRArtifactDispatch{}) // nil Metadata
	overrideActiveDVRMetadata(&sharedpb.ViewerEndpointResponse{Metadata: &sharedpb.PlaybackMetadata{ContentType: "live"}}, nil)
	// No panic == pass.
}

// latestPlayableChapterForDVR returns ("", nil) for a nil dispatch (and one
// with an empty hash) so the DVR resolver treats "no dispatch" as "no
// chapter yet", not an error — that distinction is what routes a stopped
// DVR to FailedPrecondition rather than Unavailable.
func TestLatestPlayableChapterForDVR_NilDispatch(t *testing.T) {
	s := newViewerEndpointServer(t)
	pid, err := s.latestPlayableChapterForDVR(context.Background(), nil)
	if err != nil || pid != "" {
		t.Fatalf("nil dispatch: got (%q,%v), want (\"\",nil)", pid, err)
	}
	pid, err = s.latestPlayableChapterForDVR(context.Background(), &control.DVRArtifactDispatch{DVRHash: ""})
	if err != nil || pid != "" {
		t.Fatalf("empty hash: got (%q,%v), want (\"\",nil)", pid, err)
	}
}

// latestPlayableChapterForDVR selects only chapters in a terminal-playable
// state (finalized/frozen/reclaimed) with a minted playback_id, newest by
// end_ms. This pins the state-transition gate: a chapter still being
// finalized must not be handed to a viewer.
func TestLatestPlayableChapterForDVR_ReturnsNewestPlayablePID(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	s := &FoghornGRPCServer{db: db, logger: logrus.New()}

	mock.ExpectQuery(`SELECT playback_id`).
		WithArgs("dvr-h").
		WillReturnRows(sqlmock.NewRows([]string{"playback_id"}).AddRow("chap-latest-pid"))

	pid, qerr := s.latestPlayableChapterForDVR(context.Background(), &control.DVRArtifactDispatch{DVRHash: "dvr-h"})
	if qerr != nil {
		t.Fatalf("unexpected error: %v", qerr)
	}
	if pid != "chap-latest-pid" {
		t.Fatalf("got %q, want chap-latest-pid", pid)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sqlmock expectations: %v", err)
	}
}

// When no playable chapter row exists yet (no rows), the lookup returns
// ("", nil) — "not yet", not an error — so the resolver routes to
// FailedPrecondition/dvrChapters rather than 5xx.
func TestLatestPlayableChapterForDVR_NoRows(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	s := &FoghornGRPCServer{db: db, logger: logrus.New()}

	mock.ExpectQuery(`SELECT playback_id`).
		WithArgs("dvr-h").
		WillReturnRows(sqlmock.NewRows([]string{"playback_id"}))

	pid, qerr := s.latestPlayableChapterForDVR(context.Background(), &control.DVRArtifactDispatch{DVRHash: "dvr-h"})
	if qerr != nil || pid != "" {
		t.Fatalf("no rows: got (%q,%v), want (\"\",nil)", pid, qerr)
	}
}

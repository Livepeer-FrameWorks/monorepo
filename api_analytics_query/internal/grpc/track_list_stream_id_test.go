package grpc

import (
	"context"
	"errors"
	"testing"

	periscopepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/periscope"
	"github.com/sirupsen/logrus"
	logtest "github.com/sirupsen/logrus/hooks/test"
	grpclib "google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// A Relay global ID (or any non-UUID) is the caller's mistake; it must be
// refused before ClickHouse turns it into an opaque Internal error.
func TestGetTrackListEventsRejectsNonUUIDStreamID(t *testing.T) {
	server, mock, cleanup := newTimeSeriesServer(t)
	defer cleanup()

	_, err := server.GetTrackListEvents(context.Background(), &periscopepb.GetTrackListEventsRequest{
		TenantId:  "tenant-1",
		StreamId:  "U3RyZWFtOmIyMjEzNDgxLWVlYjgtNDkwMi05ZjdmLTIxZDQ5NDdlN2RjNg==",
		TimeRange: fixedTimeRange(),
	})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("code = %v (%v), want InvalidArgument", status.Code(err), err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("no query may run for an invalid stream id: %v", err)
	}
}

// Server-side failures are sanitized for the caller, so the interceptor is the
// only place their cause is recorded; client refusals stay at debug.
func TestUnaryInterceptorLogsServerSideCauseBeforeSanitizing(t *testing.T) {
	logger, hook := logtest.NewNullLogger()
	logger.SetLevel(logrus.InfoLevel)
	intercept := unaryInterceptor(logger)
	info := &grpclib.UnaryServerInfo{FullMethod: "/periscope.TrackAnalyticsService/GetTrackListEvents"}

	cause := status.Error(codes.Internal, "database error: cannot parse UUID")
	_, err := intercept(context.Background(), nil, info, func(context.Context, any) (any, error) { return nil, cause })
	if status.Convert(err).Message() == status.Convert(cause).Message() {
		t.Fatalf("caller must receive the sanitized error, got %q", status.Convert(err).Message())
	}
	entry := hook.LastEntry()
	if entry == nil || entry.Level != logrus.WarnLevel {
		t.Fatalf("server-side failure not logged at warn: %+v", entry)
	}
	if loggedErr, _ := entry.Data["error"].(error); !errors.Is(loggedErr, cause) {
		t.Fatalf("logged error = %v, want the unsanitized cause", entry.Data["error"])
	}

	hook.Reset()
	_, _ = intercept(context.Background(), nil, info, func(context.Context, any) (any, error) {
		return nil, status.Error(codes.InvalidArgument, "stream_id must be a UUID")
	})
	if len(hook.AllEntries()) != 0 {
		t.Fatalf("client refusal logged above debug: %+v", hook.AllEntries())
	}
}

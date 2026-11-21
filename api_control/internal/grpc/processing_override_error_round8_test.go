package grpc

import (
	"context"
	"database/sql"
	"strings"

	"errors"
	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/mist"
	"testing"

	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
)

func TestStrictProcessingPolicyPropagatesOverrideReadFailures(t *testing.T) {
	for _, scope := range []string{"stream", "tenant"} {
		t.Run(scope, func(t *testing.T) {
			server, mock, done := newMockServer(t)
			defer done()
			tier := ladderTier(true)
			tier.Features = &purserpb.BillingFeatures{ProcessingCustomizable: scope == "tenant"}
			tier.ProcessesVod = "[]"
			server.purserClient = startPurserEntitlementFake(t, ladderPurser(t, tier))
			unavailable := errors.New("processing override read unavailable")
			stream := ""
			if scope == "stream" {
				stream = "s1"
				mock.ExpectQuery("GetStreamProcessingOverrides").WithArgs(stream, testTenantID).WillReturnError(unavailable)
			} else {
				mock.ExpectQuery("GetTenantProcessingOverrides").WithArgs(testTenantID).WillReturnError(unavailable)
			}
			got, err := server.resolveProcessesJSONStrict(context.Background(), testTenantID, stream, "", "vod")
			if !errors.Is(err, unavailable) {
				t.Fatalf("%s override failure silently selected another policy: config=%s err=%v", scope, got, err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestProcessingOverrideFailureRetainsAvailableLivePolicy(t *testing.T) {
	server, mock, done := newMockServer(t)
	defer done()
	server.purserClient = startPurserEntitlementFake(t, ladderPurser(t, ladderTier(true)))
	mock.ExpectQuery("GetStreamProcessingOverrides").WithArgs("s1", testTenantID).WillReturnError(sql.ErrConnDone)
	mock.ExpectQuery("GetStreamProcessingChoices").WithArgs("s1", testTenantID).WillReturnRows(
		sqlmock.NewRows([]string{"live_video_abr", "transcode_ladder"}).AddRow("OFF", ""))
	got, err := server.resolveProcessesJSONStrict(t.Context(), testTenantID, "s1", "", "live")
	if !errors.Is(err, sql.ErrConnDone) || !strings.Contains(got, `"Thumbs"`) || !strings.Contains(got, `"opus"`) || mist.HasTranscodeProcesses(got) {
		t.Fatalf("override error lost available audio/thumbnail policy or ignored ABR off: config=%s err=%v", got, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

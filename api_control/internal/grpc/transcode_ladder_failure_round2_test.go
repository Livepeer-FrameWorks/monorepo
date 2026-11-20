package grpc

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/sirupsen/logrus"
	"github.com/sirupsen/logrus/hooks/test"
)

func TestExplicitLiveLadderDoesNotDependOnAccountRead(t *testing.T) {
	server, mock, done := newMockServer(t)
	defer done()
	server.purserClient = startPurserEntitlementFake(t, ladderPurser(t, ladderTier(true)))
	mock.ExpectQuery("GetStreamProcessingOverrides").WithArgs("s1", testTenantID).WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery("GetStreamProcessingChoices").WithArgs("s1", testTenantID).WillReturnRows(sqlmock.NewRows([]string{"live_video_abr", "transcode_ladder"}).AddRow("INHERIT", "uhd"))
	got := server.resolveProcessesJSON(context.Background(), testTenantID, "s1", "", "live")
	if strings.Join(renditionNames(t, got), ",") != "720p,1080p,2160p" {
		t.Fatalf("explicit entitled stream ladder lost to account lookup: %s", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestLiveLadderReadFallbackIsLogged(t *testing.T) {
	server, mock, done := newMockServer(t)
	defer done()
	logger, hook := test.NewNullLogger()
	server.logger = logger
	server.purserClient = startPurserEntitlementFake(t, ladderPurser(t, ladderTier(true)))
	mock.ExpectQuery("GetStreamProcessingOverrides").WithArgs("s1", testTenantID).WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery("GetStreamProcessingChoices").WithArgs("s1", testTenantID).WillReturnError(sql.ErrConnDone)
	server.resolveProcessesJSON(context.Background(), testTenantID, "s1", "", "live")
	entry := hook.LastEntry()
	if entry == nil || entry.Level != logrus.WarnLevel || entry.Data["tenant_id"] != testTenantID || entry.Data["stream_id"] != "s1" {
		t.Fatalf("fallback must identify the affected tenant and stream: %v", entry)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestLiveChoiceFailureCannotEnableVideoABR(t *testing.T) {
	server, mock, done := newMockServer(t)
	defer done()
	server.purserClient = startPurserEntitlementFake(t, ladderPurser(t, ladderTier(true)))
	mock.ExpectQuery("GetStreamProcessingOverrides").WithArgs("s1", testTenantID).WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery("GetStreamProcessingChoices").WithArgs("s1", testTenantID).WillReturnError(sql.ErrConnDone)
	got := server.resolveProcessesJSON(context.Background(), testTenantID, "s1", "", "live")
	if len(renditionNames(t, got)) != 0 {
		t.Fatalf("unknown ABR state started video renditions: %s", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

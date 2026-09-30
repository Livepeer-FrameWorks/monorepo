package grpc

import (
	"context"
	"errors"
	"testing"

	periscopepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/periscope"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/sirupsen/logrus"
	"github.com/sirupsen/logrus/hooks/test"
)

func streamStatusServer(t *testing.T) (*PeriscopeServer, sqlmock.Sqlmock, *test.Hook) {
	t.Helper()
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	logger, hook := test.NewNullLogger()
	return &PeriscopeServer{clickhouse: db, logger: logger}, mock, hook
}

func streamStatusWarnings(hook *test.Hook) []*logrus.Entry {
	var warnings []*logrus.Entry
	for _, entry := range hook.AllEntries() {
		if entry.Level == logrus.WarnLevel {
			warnings = append(warnings, entry)
		}
	}
	return warnings
}

func TestGetStreamStatusLogsClickHouseErrorAndAnswersOffline(t *testing.T) {
	server, mock, hook := streamStatusServer(t)
	readErr := errors.New("clickhouse: connection refused")
	mock.ExpectQuery(`(?s)FROM stream_state_current FINAL`).WillReturnError(readErr)

	resp, err := server.GetStreamStatus(context.Background(), &periscopepb.GetStreamStatusRequest{TenantId: "tenant-1", StreamId: "stream-1"})
	if err != nil {
		t.Fatalf("GetStreamStatus error = %v, want nil", err)
	}
	if resp.GetStatus() != "offline" {
		t.Fatalf("status = %q, want offline", resp.GetStatus())
	}
	warnings := streamStatusWarnings(hook)
	if len(warnings) != 1 {
		t.Fatalf("warnings = %v, want one", hook.AllEntries())
	}
	if !errors.Is(warnings[0].Data[logrus.ErrorKey].(error), readErr) {
		t.Fatalf("logged error = %v, want %v", warnings[0].Data[logrus.ErrorKey], readErr)
	}
	if warnings[0].Data["stream_id"] != "stream-1" || warnings[0].Data["tenant_id"] != "tenant-1" {
		t.Fatalf("log fields = %v", warnings[0].Data)
	}
}

func TestGetStreamStatusMissingRowIsOfflineWithoutWarning(t *testing.T) {
	server, mock, hook := streamStatusServer(t)
	mock.ExpectQuery(`(?s)FROM stream_state_current FINAL`).WillReturnRows(sqlmock.NewRows([]string{"status"}))

	resp, err := server.GetStreamStatus(context.Background(), &periscopepb.GetStreamStatusRequest{TenantId: "tenant-1", StreamId: "stream-1"})
	if err != nil {
		t.Fatalf("GetStreamStatus error = %v, want nil", err)
	}
	if resp.GetStatus() != "offline" {
		t.Fatalf("status = %q, want offline", resp.GetStatus())
	}
	if warnings := streamStatusWarnings(hook); len(warnings) != 0 {
		t.Fatalf("missing row logged warnings: %v", warnings)
	}
}

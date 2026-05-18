package jobs

import (
	"context"
	"database/sql/driver"
	"fmt"
	"strings"
	"testing"

	"frameworks/api_balancing/internal/control"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"

	"github.com/DATA-DOG/go-sqlmock"
)

type statusSetArg struct {
	want []string
}

func (a statusSetArg) Match(v driver.Value) bool {
	got := fmt.Sprint(v)
	for _, status := range a.want {
		if !strings.Contains(got, status) {
			return false
		}
	}
	return true
}

func TestChapterReclaimSweep_S3DeleteIncludesLostLocal(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer mockDB.Close()

	s := &ChapterReclaimSweep{db: mockDB, logger: logging.NewLogger()}
	mock.ExpectQuery(`WITH overlapping`).
		WithArgs("dvr-hash", int64(1000), int64(2000), statusSetArg{want: []string{"deleted_local", "orphan_unreachable", "lost_local"}}).
		WillReturnRows(sqlmock.NewRows([]string{"segment_name", "s3_key"}).AddRow("seg-1.ts", "s3/key"))

	rows, err := s.listSegmentsAwaitingS3Delete(context.Background(), "dvr-hash", 1000, 2000)
	if err != nil {
		t.Fatalf("listSegmentsAwaitingS3Delete() error = %v", err)
	}
	if len(rows) != 1 || rows[0].name != "seg-1.ts" || rows[0].s3Key != "s3/key" {
		t.Fatalf("unexpected rows: %+v", rows)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestChapterReclaimSweep_RangeCompleteWaitsForLostLocalS3Delete(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer mockDB.Close()

	s := &ChapterReclaimSweep{db: mockDB, logger: logging.NewLogger()}
	mock.ExpectQuery(`SELECT COUNT\(\*\).*status != 'reclaimed'`).
		WithArgs("dvr-hash", int64(1000), int64(2000)).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))

	err = s.markReclaimedIfRangeComplete(context.Background(), control.DVRChapterRow{
		ChapterID:    "chap-1",
		ArtifactHash: "dvr-hash",
		StartMs:      1000,
		EndMs:        2000,
	})
	if err != nil {
		t.Fatalf("markReclaimedIfRangeComplete() error = %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

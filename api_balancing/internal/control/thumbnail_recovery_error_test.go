package control

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestThumbnailRecoveryErrorNamesTheFailedDatabasePhase(t *testing.T) {
	for _, phase := range []string{"publishing", "expired", "superseded"} {
		t.Run(phase, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			publishing := mock.ExpectQuery("WHERE status = 'publishing'")
			if phase == "publishing" {
				publishing.WillReturnError(context.DeadlineExceeded)
			} else {
				publishing.WillReturnRows(sqlmock.NewRows([]string{"attempt_id", "token"}))
				expired := mock.ExpectQuery("SELECT attempt_id FROM foghorn.thumbnail_task_assignment WHERE status IN")
				if phase == "expired" {
					expired.WillReturnError(context.DeadlineExceeded)
				} else {
					expired.WillReturnRows(sqlmock.NewRows([]string{"attempt_id"}))
					mock.ExpectQuery("SELECT a.attempt_id FROM foghorn.thumbnail_task_assignment").WillReturnError(context.DeadlineExceeded)
				}
			}
			_, _, _, err = RecoverStuckThumbnailAttempts(t.Context(), db, time.Now(), 18)
			var detail *ThumbnailRecoveryError
			if !errors.Is(err, context.DeadlineExceeded) || !errors.As(err, &detail) || detail.Phase != phase || detail.Operation != "select" || detail.Elapsed <= 0 {
				t.Fatalf("database phase/cause lost: detail=%+v err=%v", detail, err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestThumbnailRecoveryErrorNamesTheFailedSettlementPhase(t *testing.T) {
	for _, phase := range []string{"publishing", "expired", "superseded"} {
		t.Run(phase, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			publishing := sqlmock.NewRows([]string{"attempt_id", "token"})
			if phase == "publishing" {
				publishing.AddRow("attempt", "token")
			}
			mock.ExpectQuery("WHERE status = 'publishing'").WillReturnRows(publishing)
			if phase != "publishing" {
				expired := sqlmock.NewRows([]string{"attempt_id"})
				if phase == "expired" {
					expired.AddRow("attempt")
				}
				mock.ExpectQuery("SELECT attempt_id FROM foghorn.thumbnail_task_assignment WHERE status IN").WillReturnRows(expired)
				if phase == "superseded" {
					mock.ExpectQuery("SELECT a.attempt_id FROM foghorn.thumbnail_task_assignment").WillReturnRows(sqlmock.NewRows([]string{"attempt_id"}).AddRow("attempt"))
				}
			}
			mock.ExpectBegin().WillReturnError(context.DeadlineExceeded)
			_, _, _, err = RecoverStuckThumbnailAttempts(t.Context(), db, time.Now(), 18)
			var detail *ThumbnailRecoveryError
			if !errors.Is(err, context.DeadlineExceeded) || !errors.As(err, &detail) || detail.Phase != phase || detail.Operation != "settle" {
				t.Fatalf("settlement phase/cause lost: detail=%+v err=%v", detail, err)
			}
			if checkErr := mock.ExpectationsWereMet(); checkErr != nil {
				t.Fatal(checkErr)
			}
		})
	}
}

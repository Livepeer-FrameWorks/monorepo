package grpc

import (
	"context"
	"database/sql"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"frameworks/api_control/internal/database/commodoredb"
	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
)

func TestMediaAuthorityDeadlineWorkerIsIndependent(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	done, advanced := make(chan struct{}), make(chan struct{})
	var ticks atomic.Int32
	block := func(ctx context.Context) { <-ctx.Done() }
	go func() {
		defer close(done)
		runMediaAuthorityWorkerGroup(ctx, block, block, block, func(context.Context) {
			if ticks.Add(1) == 2 {
				close(advanced)
			}
		})
	}()
	select {
	case <-advanced:
	case <-ctx.Done():
		t.Fatal("blocked ordinary workers stopped deadline renewal")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("deadline worker ignored shutdown")
	}
}

func TestMediaAuthorityDeadlineRowRejectsUnboundAndSupersededWork(t *testing.T) {
	for _, scenario := range []string{"wrong event", "wrong service", "wrong reason", "missing source", "superseded", "lookup failed"} {
		t.Run(scenario, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			row := commodoredb.ClaimMediaAuthorityRefreshInboxRow{SourceService: "commodore", SourceEventID: mediaAuthorityDeadlineEvent("media_object", "live_stream:stream", 1), TenantID: "81000000-0000-4000-8000-000000000001", Reason: "media_object:live_stream:stream:deadline_refresh", Attempts: 1}
			switch scenario {
			case "wrong event":
				row.SourceEventID = mediaAuthorityDeadlineEvent("media_object", "live_stream:other", 1)
			case "wrong service":
				row.SourceService = "purser"
			case "wrong reason":
				row.Reason = "media_object:live_stream:stream:other"
			default:
				query := mock.ExpectQuery("GetScheduledMediaAuthorityVersion").WithArgs("media_object", "live_stream:stream", row.TenantID)
				switch scenario {
				case "missing source":
					query.WillReturnError(sql.ErrNoRows)
				case "lookup failed":
					query.WillReturnError(errors.New("temporary read failure"))
				default:
					query.WillReturnRows(sqlmock.NewRows([]string{"authority_version"}).AddRow(int64(2)))
				}
			}
			if scenario == "missing source" || scenario == "superseded" {
				mock.ExpectExec("CompleteMediaAuthorityRefreshInbox").WithArgs(row.SourceService, row.SourceEventID).WillReturnResult(sqlmock.NewResult(0, 1))
			} else {
				mock.ExpectExec("FailMediaAuthorityRefreshInbox").WillReturnResult(sqlmock.NewResult(0, 1))
			}
			server := &CommodoreServer{db: db, logger: logging.NewLogger()}
			server.processMediaAuthorityDeadlineRefreshRow(context.Background(), row)
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

package grpc

import (
	"context"
	"errors"
	"testing"
	"time"

	"frameworks/api_control/internal/database/commodoredb"
	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
)

func TestTenantDeadlineRejectsCrossKindAndTenantBindings(t *testing.T) {
	for _, scenario := range []string{"foreign tenant", "object event", "wrong reason", "stale tenant"} {
		t.Run(scenario, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			const tenantID = "81000000-0000-4000-8000-000000000001"
			row := commodoredb.ClaimMediaAuthorityRefreshInboxRow{SourceService: "commodore", SourceEventID: mediaAuthorityDeadlineEvent("tenant", tenantID, 1), TenantID: tenantID, Reason: tenantMediaAuthorityDeadlineReason, Attempts: 1}
			switch scenario {
			case "foreign tenant":
				row.TenantID = "81000000-0000-4000-8000-000000000002"
			case "object event":
				row.SourceEventID = mediaAuthorityDeadlineEvent("media_object", tenantID, 1)
			case "wrong reason":
				row.Reason = "media_object:live_stream:" + tenantID + ":deadline_refresh"
			case "stale tenant":
				mock.ExpectQuery("GetScheduledMediaAuthorityVersion").WithArgs("tenant", tenantID, tenantID).
					WillReturnRows(sqlmock.NewRows([]string{"authority_version"}).AddRow(int64(2)))
			}
			if scenario == "stale tenant" {
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

func TestTenantDeadlinePublicationCannotOutliveGrant(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	tenant, _ := commercialAuthorityFixture()
	server := &CommodoreServer{db: db}
	err = server.persistTenantAuthority(context.Background(), tenant, nil, nil, time.Now().Add(-time.Minute), time.Now().Add(-time.Second))
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expired tenant publication reached transaction: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
	if tenantMediaAuthorityDeadlineLease <= tenantMediaAuthorityDeadlineTimeout+mediaAuthoritySettleTimeout {
		t.Fatal("tenant claim lease does not cover compile and settlement")
	}
}

package datamigrations

import (
	"bytes"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/database"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/datamigrate"
)

func TestViewerAttributionMetadataDoesNotOpenStores(t *testing.T) {
	opened := false
	RegisterViewerServingAttribution("metadata-test", func() (database.ClickHouseConn, error) { opened = true; return nil, errors.New("store unavailable") })
	var out bytes.Buffer
	err := datamigrate.HandleArgv(t.Context(), func() (*sql.DB, error) {
		t.Error("metadata listing opened PostgreSQL")
		return nil, errors.New("store unavailable")
	}, &out, []string{"data-migrations", "list", "--format", "json"})
	if err != nil || opened || !strings.Contains(out.String(), ViewerServingAttributionID) {
		t.Fatalf("metadata listing requires live stores: opened=%v err=%v output=%s", opened, err, out.String())
	}
}

func TestViewerAttributionDryRunDoesNotWriteClickHouse(t *testing.T) {
	ch, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer ch.Close()
	const tenant = "5eed0005-7a11-4e5e-8001-5eed00000031"
	const zero = "00000000-0000-0000-0000-000000000000"
	mock.ExpectQuery(`(?s)WITH viewer_current.*SELECT toString\(stream_id\)`).WithArgs(tenant, tenant, zero, int64(0), zero, 1).
		WillReturnRows(sqlmock.NewRows([]string{"stream", "timestamp", "event"}).AddRow("5eed0005-7a11-4e5e-8001-5eed00000032", time.Now(), "5eed0005-7a11-4e5e-8001-5eed00000033"))
	progress, err := backfillViewerServingAttribution(t.Context(), ch, datamigrate.RunOptions{Scope: datamigrate.ScopeKey{Kind: "tenant", Value: tenant}, BatchSize: 1, DryRun: true})
	if err != nil || progress.Scanned != 1 || progress.Changed != 0 || len(progress.Checkpoint) == 0 {
		t.Fatalf("dry-run progress=%+v err=%v", progress, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestViewerAttributionRequiresTenantScope(t *testing.T) {
	if _, err := backfillViewerServingAttribution(t.Context(), nil, datamigrate.RunOptions{}); err == nil {
		t.Fatal("unscoped attribution write accepted")
	}
}

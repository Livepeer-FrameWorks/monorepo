package grpc

import (
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestAccountStopKeepsUnreachableHistoricalCellsPending(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.ExpectQuery("ListAccountIngestCells").WithArgs("tenant", "").WillReturnRows(sqlmock.NewRows([]string{"cell_id"}).AddRow("historical-cell"))
	server := &CommodoreServer{db: db}
	server.foghornDiscovery.Store("historical-cell", &foghornDiscoveryEntry{at: time.Now()})
	if err = server.stopAccountIngest(t.Context(), "tenant", "", nil); err == nil {
		t.Fatal("unreachable historical cell was silently treated as retired")
	}
	if err = mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

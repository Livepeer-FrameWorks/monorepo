package grpc

import (
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/grpcutil"
	foghorncontrolpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn_control"
)

func TestAnsweringRetiredCellCannotSettleUnconfirmedStop(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	peer := &retirementRegistry{stopError: grpcutil.PendingStreamStop(&foghorncontrolpb.TerminateTenantStreamsResponse{})}
	server, addr := newRetirementServer(t, peer, db)
	server.foghornDiscovery.Store("retired-but-answering", &foghornDiscoveryEntry{addrs: []string{addr}, at: time.Now()})
	mock.ExpectQuery("ListAccountIngestCells").WithArgs("tenant", "").WillReturnRows(sqlmock.NewRows([]string{"cell_id"}).AddRow("retired-but-answering"))
	// Even if the failure path reads a retirement record, an explicit pending
	// response from the live cell must retain the stop obligation.
	mock.ExpectQuery("retired_ingest_cells").WillReturnRows(sqlmock.NewRows([]string{"cell_id"}).AddRow("retired-but-answering"))
	err = server.stopAccountIngest(t.Context(), "tenant", "", nil)
	if err == nil || !strings.Contains(err.Error(), "stream termination remains pending") {
		t.Fatalf("answering retired cell's unconfirmed stop was settled: %v", err)
	}
}

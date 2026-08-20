package grpc

import (
	"context"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/lib/pq"
	"google.golang.org/protobuf/types/known/emptypb"
)

func TestListMeterDefinitionsMapsCanonicalCatalog(t *testing.T) {
	server, mock := newReadServer(t, true)
	mock.ExpectQuery(`FROM purser\.meter_definitions`).WillReturnRows(
		sqlmock.NewRows([]string{
			"meter", "unit", "aggregation", "display_name", "allowed_dimensions", "default_priceable",
		}).AddRow(
			"transcode_rendition_seconds", "second", "sum", "Transcode renditions",
			pq.Array([]string{"execution_backend", "output_codec"}), true,
		),
	)

	resp, err := server.ListMeterDefinitions(context.Background(), &emptypb.Empty{})
	if err != nil {
		t.Fatalf("ListMeterDefinitions: %v", err)
	}
	if len(resp.GetMeters()) != 1 {
		t.Fatalf("meters = %d, want 1", len(resp.GetMeters()))
	}
	meter := resp.GetMeters()[0]
	if meter.GetMeter() != "transcode_rendition_seconds" || meter.GetUnit() != "second" || meter.GetAggregation() != "sum" {
		t.Fatalf("meter identity = %+v", meter)
	}
	if !meter.GetDefaultPriceable() || len(meter.GetAllowedDimensions()) != 2 || meter.GetAllowedDimensions()[1] != "output_codec" {
		t.Fatalf("meter contract = %+v", meter)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

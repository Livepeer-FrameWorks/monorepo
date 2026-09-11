package grpc

import (
	"context"
	"regexp"
	"testing"
	"time"

	"frameworks/api_control/internal/database/commodoredb"
	"github.com/DATA-DOG/go-sqlmock"
	foghornpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn"
)

func TestAttestedCellPlacementCapabilityNormalizesAcknowledgements(t *testing.T) {
	for _, test := range []struct {
		name       string
		capability *foghornpb.MediaCellPlacementCapability
		wantSchema int32
		wantReady  bool
	}{
		{"absent", nil, 1, false},
		{"legacy only", &foghornpb.MediaCellPlacementCapability{SupportedSchemaVersions: []uint32{1}, EnforcementReady: true, LiveReplicas: 1}, 1, false},
		{"schema 2 not enforcing", &foghornpb.MediaCellPlacementCapability{SupportedSchemaVersions: []uint32{1, 2}, LiveReplicas: 1}, 2, false},
		{"schema 2 without replicas", &foghornpb.MediaCellPlacementCapability{SupportedSchemaVersions: []uint32{1, 2}, EnforcementReady: true}, 2, false},
		{"unknown schema", &foghornpb.MediaCellPlacementCapability{SupportedSchemaVersions: []uint32{2, 3}, EnforcementReady: true, LiveReplicas: 1}, 1, false},
		{"implausible replicas", &foghornpb.MediaCellPlacementCapability{SupportedSchemaVersions: []uint32{2}, EnforcementReady: true, LiveReplicas: 1 << 21}, 1, false},
		{"ready", &foghornpb.MediaCellPlacementCapability{SupportedSchemaVersions: []uint32{2, 1}, EnforcementReady: true, LiveReplicas: 2}, 2, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			schema, ready, _ := attestedCellPlacementCapability(test.capability)
			if schema != test.wantSchema || ready != test.wantReady || cellPlacementCapabilityReady(schema, ready) != test.wantReady {
				t.Fatalf("schema=%d ready=%t", schema, ready)
			}
		})
	}
}

func TestPlacementCellsReadyRequiresEveryTargetCell(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	queries := commodoredb.New(db)
	if ready, err := placementCellsReady(context.Background(), queries, []string{" ", ""}); err != nil || !ready {
		t.Fatalf("empty target set is not vacuously ready: %t %v", ready, err)
	}
	columns := []string{"cell_id", "max_schema_version", "enforcement_ready", "live_replicas", "attested_at"}
	mock.ExpectQuery(regexp.QuoteMeta("FROM commodore.media_cell_placement_capabilities")).WillReturnRows(sqlmock.NewRows(columns).
		AddRow("cell-a", int32(2), true, int32(1), time.Now()).AddRow("cell-b", int32(2), false, int32(1), time.Now()))
	if ready, err := placementCellsReady(context.Background(), queries, []string{"cell-a", "cell-b", "cell-a"}); err != nil || ready {
		t.Fatalf("non-ready cell passed the barrier: %t %v", ready, err)
	}
	mock.ExpectQuery(regexp.QuoteMeta("FROM commodore.media_cell_placement_capabilities")).WillReturnRows(sqlmock.NewRows(columns).
		AddRow("cell-a", int32(2), true, int32(1), time.Now()))
	if ready, err := placementCellsReady(context.Background(), queries, []string{"cell-a", "cell-c"}); err != nil || ready {
		t.Fatalf("unattested cell passed the barrier: %t %v", ready, err)
	}
	mock.ExpectQuery(regexp.QuoteMeta("FROM commodore.media_cell_placement_capabilities")).WillReturnRows(sqlmock.NewRows(columns).
		AddRow("cell-a", int32(2), true, int32(1), time.Now()))
	if ready, err := placementCellsReady(context.Background(), queries, []string{"cell-a"}); err != nil || !ready {
		t.Fatalf("attested cell failed the barrier: %t %v", ready, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

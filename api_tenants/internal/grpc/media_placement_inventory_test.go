package grpc

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	"github.com/lib/pq"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const inventoryTenant = "11111111-1111-4111-8111-111111111111"

func inventoryRequest() *quartermasterpb.GetMediaPlacementInventoryRequest {
	return &quartermasterpb.GetMediaPlacementInventoryRequest{TenantId: inventoryTenant, ControlCellId: "cell", ClusterIds: []string{"empty", "own"}}
}

func inventoryRows() *sqlmock.Rows {
	return sqlmock.NewRows([]string{"cluster_id", "node_id", "admission_enabled", "observed_at"})
}

func TestMediaPlacementInventoryAuthAndInput(t *testing.T) {
	server := &QuartermasterServer{}
	if _, err := server.GetMediaPlacementInventory(context.Background(), inventoryRequest()); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("public inventory access = %v", err)
	}
	for _, req := range []*quartermasterpb.GetMediaPlacementInventoryRequest{
		nil, {}, {TenantId: inventoryTenant, ControlCellId: "cell"},
		{TenantId: "not-a-tenant", ControlCellId: "cell", ClusterIds: []string{"own"}},
		{TenantId: inventoryTenant, ControlCellId: "cell", ClusterIds: []string{"own", "own"}},
		{TenantId: inventoryTenant, ControlCellId: "cell\n", ClusterIds: []string{"own"}},
	} {
		if _, err := server.GetMediaPlacementInventory(serviceCtx(), req); status.Code(err) != codes.InvalidArgument {
			t.Fatalf("invalid inventory request = %v", err)
		}
	}
}

func TestMediaPlacementInventoryPreservesOfflineNodesAndEmptyClusters(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	now := time.Now().UTC()
	mock.ExpectQuery(`(?s)LEFT JOIN quartermaster.infrastructure_nodes.*tenant_id = \$1::uuid.*cluster_id = ANY\(\$2::text\[\]\).*\$3::text.*subscription_status = 'active'.*expires_at.*ic.is_active = true.*LIMIT 8193`).
		WithArgs(inventoryTenant, pq.Array([]string{"empty", "own"}), "cell").
		WillReturnRows(inventoryRows().AddRow("empty", "", false, now).AddRow("own", "offline", false, now).AddRow("own", "live", true, now))
	result, err := (&QuartermasterServer{db: db}).GetMediaPlacementInventory(serviceCtx(), inventoryRequest())
	if err != nil || !result.GetComplete() || len(result.GetClusterIds()) != 2 || len(result.GetNodes()) != 2 || result.GetNodes()[0].GetAdmissionEnabled() || !result.GetNodes()[1].GetAdmissionEnabled() || !result.GetObservedAt().AsTime().Equal(now) {
		t.Fatalf("inventory = %+v, %v", result, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestMediaPlacementInventoryCannotCertifyPartialOrAmbiguousResults(t *testing.T) {
	now := time.Now().UTC()
	for _, tc := range []struct {
		name     string
		rows     *sqlmock.Rows
		queryErr error
		code     codes.Code
	}{
		{"missing_cluster", inventoryRows().AddRow("own", "node", true, now), nil, codes.PermissionDenied},
		{"no_access", inventoryRows(), nil, codes.PermissionDenied},
		{"duplicate_node", inventoryRows().AddRow("empty", "node", true, now).AddRow("own", "node", true, now), nil, codes.Internal},
		{"snapshot_skew", inventoryRows().AddRow("empty", "", false, now).AddRow("own", "node", true, now.Add(time.Second)), nil, codes.Internal},
		{"foreign_cluster", inventoryRows().AddRow("foreign", "node", true, now), nil, codes.Internal},
		{"owner_unavailable", nil, errors.New("private database details"), codes.Unavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = db.Close() })
			expected := mock.ExpectQuery("GetMediaPlacementInventory")
			if tc.queryErr != nil {
				expected.WillReturnError(tc.queryErr)
			} else {
				expected.WillReturnRows(tc.rows)
			}
			result, err := (&QuartermasterServer{db: db}).GetMediaPlacementInventory(serviceCtx(), inventoryRequest())
			if result != nil || status.Code(err) != tc.code {
				t.Fatalf("partial inventory = %+v, %v", result, err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestMediaPlacementInventoryNodeBound(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	rows, now := inventoryRows(), time.Now().UTC()
	rows.AddRow("empty", "", false, now)
	for index := range 4097 {
		rows.AddRow("own", fmt.Sprintf("node-%d", index), true, now)
	}
	mock.ExpectQuery("GetMediaPlacementInventory").WillReturnRows(rows)
	result, err := (&QuartermasterServer{db: db}).GetMediaPlacementInventory(serviceCtx(), inventoryRequest())
	if result != nil || status.Code(err) != codes.ResourceExhausted {
		t.Fatalf("overflow = %+v, %v", result, err)
	}
}

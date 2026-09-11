//go:build schema_verify

package quartermasterdb

import (
	"context"
	"database/sql"
	"testing"
)

func TestMediaPlacementInventory_RealPG(t *testing.T) {
	verifyMediaPlacementInventory(t, startQuartermasterQueryCatalogRealPG(t))
}

func TestMediaPlacementInventory_RealYugabyte(t *testing.T) {
	verifyMediaPlacementInventory(t, startQuartermasterQueryCatalogRealYugabyte(t))
}

func verifyMediaPlacementInventory(t *testing.T, db *sql.DB) {
	t.Helper()
	ctx := context.Background()
	const tenantID = "11111111-1111-4111-8111-111111111141"
	const otherTenantID = "11111111-1111-4111-8111-111111111142"
	if _, err := db.ExecContext(ctx, `INSERT INTO quartermaster.tenants (id, name)
		VALUES ($1::uuid, 'Placement inventory tenant'), ($2::uuid, 'Unrelated inventory tenant')`, tenantID, otherTenantID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO quartermaster.infrastructure_clusters
		(cluster_id, cluster_name, cluster_type, base_url, cluster_class, owner_tenant_id, control_cell_id, cell_id, is_active)
		VALUES
		('inventory-own', 'Own', 'edge', 'https://own.example', 'tenant_private', $1::uuid, 'inventory-cell', 'old-cell', true),
		('inventory-empty', 'Empty', 'edge', 'https://empty.example', 'platform_official', NULL, 'inventory-cell', NULL, true),
		('inventory-legacy', 'Legacy', 'edge', 'https://legacy.example', 'platform_official', NULL, '', 'inventory-cell', true),
		('inventory-self', 'Self', 'edge', 'https://self.example', 'platform_official', NULL, NULL, NULL, true),
		('inventory-other-cell', 'Other cell', 'edge', 'https://other.example', 'platform_official', NULL, 'other-cell', NULL, true),
		('inventory-other-tenant', 'Other tenant', 'edge', 'https://other-tenant.example', 'tenant_private', $2::uuid, 'inventory-cell', NULL, true),
		('inventory-pending', 'Pending', 'edge', 'https://pending.example', 'third_party_marketplace', NULL, 'inventory-cell', NULL, true),
		('inventory-expired', 'Expired', 'edge', 'https://expired.example', 'platform_official', NULL, 'inventory-cell', NULL, true),
		('inventory-unknown', 'Unknown', 'edge', 'https://unknown.example', 'platform_official', NULL, 'inventory-cell', NULL, true),
		('inventory-inactive-grant', 'Inactive grant', 'edge', 'https://inactive-grant.example', 'platform_official', NULL, 'inventory-cell', NULL, true),
		('inventory-inactive-cluster', 'Inactive cluster', 'edge', 'https://inactive-cluster.example', 'platform_official', NULL, 'inventory-cell', NULL, false)
	`, tenantID, otherTenantID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO quartermaster.tenant_cluster_access
		(tenant_id, cluster_id, access_source, subscription_status, is_active, expires_at)
		VALUES
		($1::uuid, 'inventory-own', 'owner', 'active', true, NULL),
		($1::uuid, 'inventory-empty', 'operator_override', 'active', true, NULL),
		($1::uuid, 'inventory-legacy', 'operator_override', 'active', true, NULL),
		($1::uuid, 'inventory-self', 'operator_override', 'active', true, NULL),
		($1::uuid, 'inventory-other-cell', 'operator_override', 'active', true, NULL),
		($2::uuid, 'inventory-other-tenant', 'owner', 'active', true, NULL),
		($1::uuid, 'inventory-pending', 'marketplace_subscription', 'pending_approval', true, NULL),
		($1::uuid, 'inventory-expired', 'operator_override', 'active', true, NOW() - INTERVAL '1 minute'),
		($1::uuid, 'inventory-unknown', 'unknown', 'active', true, NULL),
		($1::uuid, 'inventory-inactive-grant', 'operator_override', 'active', false, NULL),
		($1::uuid, 'inventory-inactive-cluster', 'operator_override', 'active', true, NULL)
	`, tenantID, otherTenantID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO quartermaster.infrastructure_nodes (node_id, cluster_id, node_name, node_type, status)
		VALUES ('inventory-live', 'inventory-own', 'Live', 'edge', 'active'),
		       ('inventory-offline', 'inventory-own', 'Offline', 'edge', 'offline'),
		       ('inventory-control', 'inventory-own', 'Control', 'control', 'active')`); err != nil {
		t.Fatal(err)
	}
	request := GetMediaPlacementInventoryParams{
		TenantID: tenantID, ControlCellID: "inventory-cell",
		ClusterIds: []string{"inventory-own", "inventory-empty", "inventory-legacy", "inventory-self", "inventory-other-cell", "inventory-other-tenant", "inventory-pending", "inventory-expired", "inventory-unknown", "inventory-inactive-grant", "inventory-inactive-cluster"},
	}
	rows, err := New(db).GetMediaPlacementInventory(ctx, request)
	if err != nil || len(rows) != 4 {
		t.Fatalf("inventory must include two edges and two empty clusters only: %+v, %v", rows, err)
	}
	want := map[string]bool{"inventory-empty/": false, "inventory-legacy/": false, "inventory-own/inventory-live": true, "inventory-own/inventory-offline": false}
	for _, row := range rows {
		key := row.ClusterID + "/" + row.NodeID
		enabled, exists := want[key]
		if !exists || row.AdmissionEnabled != enabled || row.ObservedAt.IsZero() || !row.ObservedAt.Equal(rows[0].ObservedAt) {
			t.Fatalf("incorrect inventory row: %+v", row)
		}
		delete(want, key)
	}
	if len(want) != 0 {
		t.Fatalf("missing members: %+v", want)
	}
	request.ControlCellID = "inventory-self"
	rows, err = New(db).GetMediaPlacementInventory(ctx, request)
	if err != nil || len(rows) != 1 || rows[0].ClusterID != "inventory-self" || rows[0].NodeID != "" {
		t.Fatalf("legacy cluster-id cell fallback: %+v, %v", rows, err)
	}
	request.ControlCellID, request.TenantID = "inventory-cell", otherTenantID
	rows, err = New(db).GetMediaPlacementInventory(ctx, request)
	if err != nil || len(rows) != 1 || rows[0].ClusterID != "inventory-other-tenant" {
		t.Fatalf("tenant inventory isolation: %+v, %v", rows, err)
	}
}

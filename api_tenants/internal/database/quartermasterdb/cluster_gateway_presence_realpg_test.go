//go:build schema_verify

package quartermasterdb

import (
	"context"
	"database/sql"
	"errors"
	"testing"
)

// Gateway presence answers only callers that own the cluster or hold an
// active, subscribed, unexpired grant of known provenance, and reports the
// owner and gateway presence without any other inventory.
func TestVisibleClusterGatewayPresence_RealPG(t *testing.T) {
	db := startQuartermasterQueryCatalogRealPG(t)
	ctx := context.Background()
	const (
		owner      = "22222222-2222-4222-8222-000000000001"
		subscriber = "22222222-2222-4222-8222-000000000002"
		pending    = "22222222-2222-4222-8222-000000000003"
		expired    = "22222222-2222-4222-8222-000000000004"
		unknown    = "22222222-2222-4222-8222-000000000005"
		inactive   = "22222222-2222-4222-8222-000000000006"
		stranger   = "22222222-2222-4222-8222-000000000007"
	)
	for _, statement := range []string{
		`INSERT INTO quartermaster.tenants (id, name, subdomain) VALUES
			('` + owner + `', 'Owner', 'gw-owner'),
			('` + subscriber + `', 'Subscriber', 'gw-subscriber'),
			('` + pending + `', 'Pending', 'gw-pending'),
			('` + expired + `', 'Expired', 'gw-expired'),
			('` + unknown + `', 'Unknown', 'gw-unknown'),
			('` + inactive + `', 'Inactive', 'gw-inactive'),
			('` + stranger + `', 'Stranger', 'gw-stranger')`,
		`INSERT INTO quartermaster.infrastructure_clusters
			(cluster_id, cluster_name, cluster_type, base_url, owner_tenant_id, public_topology, is_active)
		VALUES
			('gw-private', 'Private gateway', 'edge', 'https://gw-private.example.com', '` + owner + `', false, true),
			('gw-none', 'No gateway', 'edge', 'https://gw-none.example.com', '` + owner + `', false, true)`,
		`INSERT INTO quartermaster.services (service_id, name, plane, type, protocol)
		VALUES ('livepeer-gateway', 'Livepeer gateway', 'data', 'livepeer-gateway', 'http')
		ON CONFLICT (service_id) DO NOTHING`,
		`INSERT INTO quartermaster.service_instances (instance_id, cluster_id, service_id, status, port)
		VALUES ('gw-private-1', 'gw-private', 'livepeer-gateway', 'running', 8935)`,
		`INSERT INTO quartermaster.tenant_cluster_access
			(tenant_id, cluster_id, access_source, subscription_status, is_active, expires_at)
		VALUES
			('` + subscriber + `', 'gw-private', 'private_invite', 'active', true, NULL),
			('` + subscriber + `', 'gw-none', 'private_invite', 'active', true, NULL),
			('` + pending + `', 'gw-private', 'marketplace_subscription', 'pending_approval', true, NULL),
			('` + expired + `', 'gw-private', 'operator_override', 'active', true, NOW() - INTERVAL '1 minute'),
			('` + unknown + `', 'gw-private', 'unknown', 'active', true, NULL),
			('` + inactive + `', 'gw-private', 'private_invite', 'active', false, NULL)`,
	} {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			t.Fatalf("seed: %v\n%s", err, statement)
		}
	}

	q := New(db)
	presence := func(clusterID, tenantID string, anyTenant bool) (GetVisibleClusterGatewayPresenceRow, error) {
		return q.GetVisibleClusterGatewayPresence(ctx, GetVisibleClusterGatewayPresenceParams{
			ClusterID: clusterID,
			AnyTenant: anyTenant,
			TenantID:  sql.NullString{String: tenantID, Valid: tenantID != ""},
		})
	}

	for name, tenantID := range map[string]string{"owner": owner, "subscriber": subscriber} {
		row, err := presence("gw-private", tenantID, false)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !row.HasLivepeerGateway || row.OwnerTenantID.String != owner {
			t.Fatalf("%s: presence = %+v, want gateway owned by %s", name, row, owner)
		}
	}
	row, err := presence("gw-none", subscriber, false)
	if err != nil || row.HasLivepeerGateway {
		t.Fatalf("subscriber of a cluster without a gateway: row=%+v err=%v, want no gateway", row, err)
	}
	for name, tenantID := range map[string]string{
		"pending": pending, "expired": expired, "unknown provenance": unknown,
		"inactive grant": inactive, "stranger": stranger,
	} {
		if _, err := presence("gw-private", tenantID, false); !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("%s: err = %v, want no row", name, err)
		}
	}
	if _, err := presence("gw-missing", subscriber, false); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("missing cluster: err = %v, want no row", err)
	}
	row, err = presence("gw-private", "", true)
	if err != nil || !row.HasLivepeerGateway {
		t.Fatalf("any-tenant caller: row=%+v err=%v, want gateway", row, err)
	}
}

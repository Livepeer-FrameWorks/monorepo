//go:build schema_verify

package grpc

import (
	"database/sql"
	"testing"

	dbsql "github.com/Livepeer-FrameWorks/monorepo/pkg/database/sql"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/testutil/dockerpg"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/structpb"
)

func TestClusterBandwidthLimitsRejected_RealPG(t *testing.T) {
	verifyClusterBandwidthLimitsRemoved(t, openQuartermasterRealPG(t, "cluster-bandwidth-removal"))
}

func TestClusterBandwidthLimitsRejected_RealYugabyte(t *testing.T) {
	db, ok := dockerpg.OpenSharedYugabyteBaseline(t, "quartermaster_bandwidth_removal", "quartermaster")
	if !ok {
		t.Skip("requires shared Yugabyte contract fixture")
	}
	verifyClusterBandwidthLimitsRemoved(t, db)
}

func verifyClusterBandwidthLimitsRemoved(t *testing.T, db *sql.DB) {
	t.Helper()
	const owner = "11111111-1111-4111-8111-111111111191"
	const guest = "11111111-1111-4111-8111-111111111192"
	if _, err := db.Exec(`INSERT INTO quartermaster.tenants (id,name) VALUES ($1::uuid,'Owner'),($2::uuid,'Guest')`, owner, guest); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO quartermaster.infrastructure_clusters
		(cluster_id,cluster_name,cluster_type,base_url,owner_tenant_id,cluster_class,visibility)
		VALUES ('bandwidth-removal','Owned','edge','https://edge.example',$1::uuid,'tenant_private','private')`, owner); err != nil {
		t.Fatal(err)
	}
	s := NewQuartermasterServer(db, logging.NewLogger(), nil, nil, nil, nil, nil)
	limits, err := structpb.NewStruct(map[string]any{"max_bandwidth_mbps": 1000})
	if err != nil {
		t.Fatal(err)
	}
	t.Run("grant", func(t *testing.T) {
		_, err := s.GrantClusterAccess(subscriptionJWTContext(owner, true), &quartermasterpb.GrantClusterAccessRequest{
			TenantId: guest, ClusterId: "bandwidth-removal", ResourceLimits: limits,
		})
		if status.Code(err) != codes.InvalidArgument {
			t.Fatalf("bandwidth grant = %v; want InvalidArgument", err)
		}
	})
	t.Run("invite", func(t *testing.T) {
		_, err := s.CreateClusterInvite(subscriptionJWTContext(owner, false), &quartermasterpb.CreateClusterInviteRequest{
			OwnerTenantId: owner, InvitedTenantId: guest, ClusterId: "bandwidth-removal", ResourceLimits: limits,
		})
		if status.Code(err) != codes.InvalidArgument {
			t.Fatalf("bandwidth invite = %v; want InvalidArgument", err)
		}
	})
	t.Run("migration preserves other limits and can repeat", func(t *testing.T) {
		if _, err := db.Exec(`INSERT INTO quartermaster.tenant_cluster_access (tenant_id,cluster_id,resource_limits)
			VALUES ($1::uuid,'bandwidth-removal','{"max_bandwidth_mbps":1000,"max_streams":3,"max_viewers":7}')`, guest); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO quartermaster.cluster_invites (cluster_id,invited_tenant_id,created_by,resource_limits)
			VALUES ('bandwidth-removal',$1::uuid,$2::uuid,'{"max_bandwidth_mbps":1000,"max_streams":3,"max_viewers":7}')`, guest, owner); err != nil {
			t.Fatal(err)
		}
		migration, err := dbsql.Content.ReadFile("migrations/quartermaster/v0.3.16/postdeploy/001_strip_tenant_bandwidth_limit.sql")
		if err != nil {
			t.Fatal(err)
		}
		for range 2 {
			if _, err := db.Exec(string(migration)); err != nil {
				t.Fatal(err)
			}
		}
		var retained int
		if err := db.QueryRow(`SELECT count(*) FROM (
			SELECT resource_limits FROM quartermaster.tenant_cluster_access WHERE tenant_id=$1::uuid AND cluster_id='bandwidth-removal'
			UNION ALL SELECT resource_limits FROM quartermaster.cluster_invites WHERE invited_tenant_id=$1::uuid AND cluster_id='bandwidth-removal'
		) limits WHERE resource_limits='{"max_streams":3,"max_viewers":7}'::jsonb`, guest).Scan(&retained); err != nil || retained != 2 {
			t.Fatalf("retained limits=%d, err=%v; want two unchanged stream/viewer limits", retained, err)
		}
	})
}

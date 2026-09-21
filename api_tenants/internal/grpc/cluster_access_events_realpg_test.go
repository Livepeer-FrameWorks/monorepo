//go:build schema_verify

package grpc

import (
	"context"
	"database/sql"
	"strconv"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/events"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	clusterpeerpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/cluster_peer"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
)

type accessEventRow struct {
	eventID, eventType, authType, userID, tokenHash string
}

// tenant.cluster_assigned and tenant.cluster_unassigned commit with the access
// change that justifies them, under one ID with their legacy service event,
// attributed to the principal the calling service named (or the caller
// itself). A failed event insert rolls the access change back, and a write
// that leaves access as it was records nothing.
func TestClusterAccessEventsCommitWithAccess_RealPG(t *testing.T) { //nolint:funlen // One engine fixture walks grant, replay, re-grant, and unsubscribe.
	db := startQuartermasterDomainEventsRealPG(t)
	ctx := context.Background()
	const (
		tenantID = "55555555-5555-4555-8555-555555555555"
		official = "access-official"
		owned    = "access-owned"
		secret   = "access-materialization-secret"
	)
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.Exec(query, args...); err != nil {
			t.Fatalf("%s: %v", query, err)
		}
	}
	exec(`INSERT INTO quartermaster.tenants (id, name) VALUES ($1::uuid, 'Access events')`, tenantID)
	exec(`INSERT INTO quartermaster.infrastructure_clusters (cluster_id, cluster_name, cluster_type, base_url, is_platform_official)
		VALUES ($1, 'Official', 'edge', 'https://official.example', true)`, official)
	exec(`INSERT INTO quartermaster.infrastructure_clusters (cluster_id, cluster_name, cluster_type, base_url, cluster_class, owner_tenant_id)
		VALUES ($1, 'Owned', 'edge', 'https://owned.example', 'tenant_private', $2::uuid)`, owned, tenantID)

	hasher, err := events.NewTokenHasher("qm-access-usage-secret")
	if err != nil {
		t.Fatal(err)
	}
	server := NewQuartermasterServer(db, logging.NewLogger(), nil, nil, nil, nil, nil)
	server.SetEventTokenHasher(hasher)
	server.SetClusterAccessMaterializationSecret(secret)
	svcCtx := context.WithValue(ctx, ctxkeys.KeyAuthType, "service")

	// The principal Purser names is the one events.ActorFromContext computed
	// in Purser, with the shared usage hash secret.
	namedHash := events.HashIdentifier([]byte("qm-access-usage-secret"), "token-record-5")
	named := events.RequestActor(events.Actor{AuthType: "api_token", UserID: "user-5", TokenHash: namedHash})

	accessEvents := func(eventType, clusterID string) []accessEventRow {
		t.Helper()
		rows, err := db.Query(`
			SELECT d.event_id::text, d.event_type, d.actor_auth_type, d.actor_user_id, d.actor_token_hash
			FROM quartermaster.domain_event_outbox d
			JOIN quartermaster.service_event_outbox s ON s.event_id::text = d.event_id::text
			WHERE d.event_type = $1 AND d.tenant_id = $2::uuid AND s.resource_id = $3
			ORDER BY d.enqueued_at, d.event_id`, eventType, tenantID, clusterID)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var out []accessEventRow
		for rows.Next() {
			var r accessEventRow
			if err := rows.Scan(&r.eventID, &r.eventType, &r.authType, &r.userID, &r.tokenHash); err != nil {
				t.Fatal(err)
			}
			out = append(out, r)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		return out
	}
	accessActive := func(clusterID string) bool {
		t.Helper()
		var active sql.NullBool
		err := db.QueryRow(`SELECT is_active FROM quartermaster.tenant_cluster_access WHERE tenant_id = $1::uuid AND cluster_id = $2`, tenantID, clusterID).Scan(&active)
		if err == sql.ErrNoRows {
			return false
		}
		if err != nil {
			t.Fatal(err)
		}
		return active.Bool
	}
	offline := func() func() {
		exec(`ALTER TABLE quartermaster.domain_event_outbox RENAME TO domain_event_outbox_unavailable`)
		return func() {
			exec(`ALTER TABLE quartermaster.domain_event_outbox_unavailable RENAME TO domain_event_outbox`)
		}
	}
	bootstrap := func() error {
		_, err := server.BootstrapClusterAccess(svcCtx, &quartermasterpb.BootstrapClusterAccessRequest{
			TenantId: tenantID, ClusterId: official, Actor: named,
		})
		return err
	}

	t.Run("a failed tenant.cluster_assigned insert leaves no access", func(t *testing.T) {
		restore := offline()
		err := bootstrap()
		restore()
		if err == nil {
			t.Fatal("access was granted without its event")
		}
		if accessActive(official) {
			t.Fatal("access survived the failed event insert")
		}
	})

	t.Run("the grant records tenant.cluster_assigned for the named principal, once", func(t *testing.T) {
		if err := bootstrap(); err != nil {
			t.Fatal(err)
		}
		rows := accessEvents("tenant.cluster_assigned", official)
		if len(rows) != 1 {
			t.Fatalf("assigned rows = %+v, want one", rows)
		}
		if r := rows[0]; r.authType != "api_token" || r.userID != "user-5" || r.tokenHash != strconv.FormatUint(namedHash, 10) {
			t.Fatalf("assigned actor = %+v, want the named api token", r)
		}
		if err := bootstrap(); err != nil {
			t.Fatal(err)
		}
		if rows := accessEvents("tenant.cluster_assigned", official); len(rows) != 1 {
			t.Fatalf("assigned rows after the replay = %+v, want still one", rows)
		}
	})

	t.Run("access that lapsed and is granted again is assigned again", func(t *testing.T) {
		if _, err := server.DeactivateClusterAccess(svcCtx, &quartermasterpb.DeactivateClusterAccessRequest{TenantId: tenantID, ClusterId: official}); err != nil {
			t.Fatal(err)
		}
		if err := bootstrap(); err != nil {
			t.Fatal(err)
		}
		if rows := accessEvents("tenant.cluster_assigned", official); len(rows) != 2 {
			t.Fatalf("assigned rows after the re-grant = %+v, want two", rows)
		}
	})

	t.Run("materialized owner access without a named principal records the calling service", func(t *testing.T) {
		req := materializationRequest(t, secret, tenantID, owned, clusterpeerpb.TenantClusterAccessSource_TENANT_CLUSTER_ACCESS_SOURCE_OWNER, "purser:tenant_private", time.Now().UTC().Truncate(time.Second))
		if _, err := server.MaterializeClusterAccess(svcCtx, req); err != nil {
			t.Fatal(err)
		}
		rows := accessEvents("tenant.cluster_assigned", owned)
		if len(rows) != 1 || rows[0].authType != "service" || rows[0].userID != "" || rows[0].tokenHash != "" {
			t.Fatalf("materialized assigned rows = %+v, want one by the service", rows)
		}
		replay := materializationRequest(t, secret, tenantID, owned, clusterpeerpb.TenantClusterAccessSource_TENANT_CLUSTER_ACCESS_SOURCE_OWNER, "purser:tenant_private", time.Now().UTC().Truncate(time.Second))
		if _, err := server.MaterializeClusterAccess(svcCtx, replay); err != nil {
			t.Fatal(err)
		}
		if rows := accessEvents("tenant.cluster_assigned", owned); len(rows) != 1 {
			t.Fatalf("materialized assigned rows after the replay = %+v, want still one", rows)
		}
	})

	userCtx := context.WithValue(ctx, ctxkeys.KeyAuthType, "jwt")
	userCtx = context.WithValue(userCtx, ctxkeys.KeyTenantID, tenantID)
	userCtx = context.WithValue(userCtx, ctxkeys.KeyUserID, "user-9")
	userCtx = context.WithValue(userCtx, ctxkeys.KeyRole, "owner")
	unsubscribe := func() error {
		_, err := server.UnsubscribeFromCluster(userCtx, &quartermasterpb.UnsubscribeFromClusterRequest{ClusterId: owned})
		return err
	}

	t.Run("a failed tenant.cluster_unassigned insert keeps the access", func(t *testing.T) {
		restore := offline()
		err := unsubscribe()
		restore()
		if err == nil {
			t.Fatal("unsubscribe committed without its event")
		}
		if !accessActive(owned) {
			t.Fatal("access was removed despite the failed event insert")
		}
	})

	t.Run("unsubscribing records tenant.cluster_unassigned for the caller, once", func(t *testing.T) {
		if err := unsubscribe(); err != nil {
			t.Fatal(err)
		}
		if accessActive(owned) {
			t.Fatal("access is still active after unsubscribing")
		}
		rows := accessEvents("tenant.cluster_unassigned", owned)
		if len(rows) != 1 || rows[0].authType != "jwt" || rows[0].userID != "user-9" || rows[0].tokenHash != "" {
			t.Fatalf("unassigned rows = %+v, want one by the jwt caller", rows)
		}
		if err := unsubscribe(); err != nil {
			t.Fatal(err)
		}
		if rows := accessEvents("tenant.cluster_unassigned", owned); len(rows) != 1 {
			t.Fatalf("unassigned rows after the replay = %+v, want still one", rows)
		}
	})
}

// Two first grants of the same (tenant, cluster) run concurrently: both would
// find no access row, and whichever inserts second updates the first one's
// row. Exactly one tenant.cluster_assigned is recorded. A blocker holds an
// uncommitted insert of the row, so both grants are inside their transactions
// before either can write it.
func TestConcurrentFirstGrantsRecordOneAssignment_RealPG(t *testing.T) {
	db := startQuartermasterDomainEventsRealPG(t)
	ctx := context.Background()
	const (
		tenantID = "66666666-6666-4666-8666-666666666666"
		official = "access-concurrent"
	)
	for _, q := range []string{
		`INSERT INTO quartermaster.tenants (id, name) VALUES ('` + tenantID + `'::uuid, 'Concurrent grants')`,
		`INSERT INTO quartermaster.infrastructure_clusters (cluster_id, cluster_name, cluster_type, base_url, is_platform_official)
			VALUES ('` + official + `', 'Official', 'edge', 'https://concurrent.example', true)`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	hasher, err := events.NewTokenHasher("qm-concurrent-usage-secret")
	if err != nil {
		t.Fatal(err)
	}
	server := NewQuartermasterServer(db, logging.NewLogger(), nil, nil, nil, nil, nil)
	server.SetEventTokenHasher(hasher)
	svcCtx := context.WithValue(ctx, ctxkeys.KeyAuthType, "service")

	blocker, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = blocker.Rollback() }()
	if _, err := blocker.ExecContext(ctx, `
		INSERT INTO quartermaster.tenant_cluster_access (tenant_id, cluster_id, access_level, access_source, subscription_status, is_active)
		VALUES ($1::uuid, $2, 'shared', 'platform_tier', 'active', true)`, tenantID, official); err != nil {
		t.Fatal(err)
	}

	const grants = 2
	errs := make(chan error, grants)
	for range grants {
		go func() {
			_, grantErr := server.BootstrapClusterAccess(svcCtx, &quartermasterpb.BootstrapClusterAccessRequest{TenantId: tenantID, ClusterId: official})
			errs <- grantErr
		}()
	}
	deadline := time.Now().Add(20 * time.Second)
	for {
		var waiting int
		if err := db.QueryRow(`SELECT count(*) FROM pg_stat_activity WHERE datname = current_database() AND wait_event_type = 'Lock'`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting >= grants {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d grants waiting, want %d", waiting, grants)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err := blocker.Rollback(); err != nil {
		t.Fatal(err)
	}
	for range grants {
		if err := <-errs; err != nil {
			t.Fatalf("grant: %v", err)
		}
	}

	var assigned int
	if err := db.QueryRow(`
		SELECT count(*) FROM quartermaster.domain_event_outbox
		WHERE event_type = 'tenant.cluster_assigned' AND tenant_id = $1::uuid`, tenantID).Scan(&assigned); err != nil {
		t.Fatal(err)
	}
	if assigned != 1 {
		t.Fatalf("tenant.cluster_assigned rows = %d, want exactly 1", assigned)
	}
}

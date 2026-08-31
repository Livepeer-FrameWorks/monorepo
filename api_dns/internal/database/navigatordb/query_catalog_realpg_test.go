//go:build schema_verify

package navigatordb

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/database"
	dbsql "github.com/Livepeer-FrameWorks/monorepo/pkg/database/sql"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/testutil/dockerpg"
	"github.com/lib/pq"
)

func TestGeneratedQueryCatalogPrepares_RealPG(t *testing.T) {
	prepareNavigatorQueryCatalog(t, startNavigatorQueryCatalogRealPG(t))
}

func TestGeneratedQueryCatalogPrepares_RealYugabyte(t *testing.T) {
	prepareNavigatorQueryCatalog(t, startNavigatorQueryCatalogRealYugabyte(t))
}

func TestTenantEdgeApplyAckDeliveryFence_RealPG(t *testing.T) {
	verifyTenantEdgeApplyAckDeliveryFence(t, startNavigatorQueryCatalogRealPG(t))
}

func TestTenantEdgeApplyAckDeliveryFence_RealYugabyte(t *testing.T) {
	verifyTenantEdgeApplyAckDeliveryFence(t, startNavigatorQueryCatalogRealYugabyte(t))
}

func TestTenantEdgeApplyAckTeardownSerialization_RealPG(t *testing.T) {
	verifyTenantEdgeApplyAckTeardownSerialization(t, startNavigatorQueryCatalogRealPG(t))
}

func TestTenantEdgeApplyAckTeardownSerialization_RealYugabyte(t *testing.T) {
	verifyTenantEdgeApplyAckTeardownSerialization(t, startNavigatorQueryCatalogRealYugabyte(t))
}

func TestTenantEdgeApplyAckClusterRevocationSerialization_RealPG(t *testing.T) {
	verifyTenantEdgeApplyAckClusterRevocationSerialization(t, startNavigatorQueryCatalogRealPG(t))
}

func TestTenantEdgeApplyAckClusterRevocationSerialization_RealYugabyte(t *testing.T) {
	verifyTenantEdgeApplyAckClusterRevocationSerialization(t, startNavigatorQueryCatalogRealYugabyte(t))
}

func TestTenantAliasReactivationTeardownSerialization_RealPG(t *testing.T) {
	verifyTenantAliasReactivationTeardownSerialization(t, startNavigatorQueryCatalogRealPG(t))
}

func TestTenantAliasReactivationTeardownSerialization_RealYugabyte(t *testing.T) {
	verifyTenantAliasReactivationTeardownSerialization(t, startNavigatorQueryCatalogRealYugabyte(t))
}

func TestTenantBundleAuthoritySerialization_RealPG(t *testing.T) {
	verifyTenantBundleAuthoritySerialization(t, startNavigatorQueryCatalogRealPG(t))
}

func TestTenantBundleAuthoritySerialization_RealYugabyte(t *testing.T) {
	verifyTenantBundleAuthoritySerialization(t, startNavigatorQueryCatalogRealYugabyte(t))
}

func TestTenantEdgeApplyAliasFKMigrationCleansOrphans_RealPG(t *testing.T) {
	verifyTenantEdgeApplyAliasFKMigrationCleansOrphans(t, startNavigatorQueryCatalogRealPG(t))
}

func TestTenantEdgeApplyAliasFKMigrationCleansOrphans_RealYugabyte(t *testing.T) {
	verifyTenantEdgeApplyAliasFKMigrationCleansOrphans(t, startNavigatorQueryCatalogRealYugabyte(t))
}

func TestNavigatorAutomaticMigrationPhasesConverge_RealPG(t *testing.T) {
	verifyNavigatorAutomaticMigrationPhasesConverge(t, startNavigatorQueryCatalogRealPG(t))
}

func TestNavigatorAutomaticMigrationPhasesConverge_RealYugabyte(t *testing.T) {
	verifyNavigatorAutomaticMigrationPhasesConverge(t, startNavigatorQueryCatalogRealYugabyte(t))
}

func TestTenantTLSBundleRevisionExpandReadsLegacyRows_RealPG(t *testing.T) {
	verifyTenantTLSBundleRevisionExpandReadsLegacyRows(t, startNavigatorQueryCatalogRealPG(t))
}

func TestTenantTLSBundleRevisionExpandReadsLegacyRows_RealYugabyte(t *testing.T) {
	verifyTenantTLSBundleRevisionExpandReadsLegacyRows(t, startNavigatorQueryCatalogRealYugabyte(t))
}

func verifyTenantTLSBundleRevisionExpandReadsLegacyRows(t *testing.T, db *sql.DB) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := db.ExecContext(ctx, `ALTER TABLE navigator.tls_bundles DROP COLUMN version`); err != nil {
		t.Fatalf("restore pre-revision bundle schema: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
INSERT INTO navigator.tls_bundles (bundle_id, domains, cert_pem, key_pem, expires_at)
VALUES ('platform-legacy-expand', '["legacy.example.test"]'::jsonb, 'legacy-cert', 'legacy-key', NOW() + INTERVAL '1 day')
`); err != nil {
		t.Fatalf("seed pre-revision bundle: %v", err)
	}
	expand, err := dbsql.Content.ReadFile("migrations/navigator/v0.3.0/expand/004_tenant_tls_bundle_revision.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, string(expand)); err != nil {
		t.Fatalf("apply bundle revision expand migration: %v", err)
	}
	row, err := New(db).GetTLSBundle(ctx, "platform-legacy-expand")
	if err != nil {
		t.Fatalf("new binary read pre-existing expanded bundle: %v", err)
	}
	if row.Version != "" {
		t.Fatalf("expand compatibility version=%q, want empty revision pending postdeploy backfill", row.Version)
	}
}

func verifyNavigatorAutomaticMigrationPhasesConverge(t *testing.T, db *sql.DB) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	seedNavigatorCredentialCleanupProof(t, ctx, db)
	for _, phase := range []string{"expand", "postdeploy"} {
		dir := "migrations/navigator/v0.3.0/" + phase
		entries, err := fs.ReadDir(dbsql.Content, dir)
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
				continue
			}
			migration, err := dbsql.Content.ReadFile(dir + "/" + entry.Name())
			if err != nil {
				t.Fatal(err)
			}
			if _, err := db.ExecContext(ctx, string(migration)); err != nil {
				t.Fatalf("apply automatic %s migration %s: %v", phase, entry.Name(), err)
			}
		}
		if phase == "expand" {
			assertNoUnvalidatedNavigatorConstraints(t, ctx, db, "fresh baseline plus expand")
		}
	}
	assertNoUnvalidatedNavigatorConstraints(t, ctx, db, "fresh baseline plus automatic postdeploy")
	assertNavigatorCredentialCleanupProof(t, ctx, db)
}

func seedNavigatorCredentialCleanupProof(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()
	const activeTenant = "10000000-0000-0000-0000-000000000006"
	const orphanTenant = "10000000-0000-0000-0000-000000000007"
	const customDomainTenant = "10000000-0000-0000-0000-000000000008"
	const tearingDomainTenant = "10000000-0000-0000-0000-000000000010"
	seeds := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO navigator.tenant_aliases (tenant_id, subdomain, status) VALUES ($1::uuid, 'cleanup-active', 'cert_issued')`, []any{activeTenant}},
		{`INSERT INTO navigator.tls_bundles (bundle_id, domains, cert_pem, key_pem, expires_at)
VALUES ('tenant:' || $1::text, '[]'::jsonb, 'active-cert', 'active-key', NOW() + INTERVAL '1 day'),
       ('tenant:' || $2::text, '[]'::jsonb, 'orphan-cert', 'orphan-key', NOW() + INTERVAL '1 day'),
       ('platform:test', '[]'::jsonb, 'platform-cert', 'platform-key', NOW() + INTERVAL '1 day')`, []any{activeTenant, orphanTenant}},
		{`INSERT INTO navigator.certificates (tenant_id, domain, cert_pem, key_pem, expires_at)
VALUES ($1::uuid, 'orphan.example.test', 'orphan-cert', 'orphan-key', NOW() + INTERVAL '1 day'),
       ($2::uuid, 'custom.example.test', 'custom-cert', 'custom-key', NOW() + INTERVAL '1 day'),
       ($3::uuid, 'alias-only.example.test', 'alias-cert', 'alias-key', NOW() + INTERVAL '1 day'),
       ($4::uuid, 'tearing.example.test', 'tearing-cert', 'tearing-key', NOW() + INTERVAL '1 day'),
       (NULL, 'platform.example.test', 'platform-cert', 'platform-key', NOW() + INTERVAL '1 day')`, []any{orphanTenant, customDomainTenant, activeTenant, tearingDomainTenant}},
		{`INSERT INTO navigator.tenant_custom_domains (tenant_id, domain, acme_dns_subdomain, status)
VALUES ($1::uuid, 'custom.example.test', 'cleanup-proof', 'cert_failed'),
       ($2::uuid, 'tearing.example.test', 'cleanup-tearing', 'tearing_down')`, []any{customDomainTenant, tearingDomainTenant}},
		{`INSERT INTO navigator.acme_accounts (tenant_id, email, registration_json, private_key_pem, ca)
VALUES ($1::uuid, 'orphan@example.test', '{}', 'orphan-key', 'letsencrypt'),
       ($2::uuid, 'custom@example.test', '{}', 'custom-key', 'letsencrypt'),
       ($3::uuid, 'tearing@example.test', '{}', 'tearing-key', 'letsencrypt')`, []any{orphanTenant, customDomainTenant, tearingDomainTenant}},
	}
	for _, seed := range seeds {
		if _, err := db.ExecContext(ctx, seed.query, seed.args...); err != nil {
			t.Fatalf("seed Navigator credential cleanup proof: %v", err)
		}
	}
}

func assertNavigatorCredentialCleanupProof(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()
	var orphanBundles, retainedBundles, orphanCerts, retainedCerts, orphanAccounts, retainedAccounts int
	if err := db.QueryRowContext(ctx, `
SELECT
  COUNT(*) FILTER (WHERE bundle_id = 'tenant:10000000-0000-0000-0000-000000000007'),
  COUNT(*) FILTER (WHERE bundle_id IN ('tenant:10000000-0000-0000-0000-000000000006', 'platform:test'))
FROM navigator.tls_bundles`).Scan(&orphanBundles, &retainedBundles); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `
SELECT
  COUNT(*) FILTER (WHERE domain IN ('orphan.example.test', 'alias-only.example.test', 'tearing.example.test')),
  COUNT(*) FILTER (WHERE domain IN ('custom.example.test', 'platform.example.test'))
FROM navigator.certificates`).Scan(&orphanCerts, &retainedCerts); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `
SELECT
  COUNT(*) FILTER (WHERE email IN ('orphan@example.test', 'tearing@example.test')),
  COUNT(*) FILTER (WHERE email = 'custom@example.test')
FROM navigator.acme_accounts`).Scan(&orphanAccounts, &retainedAccounts); err != nil {
		t.Fatal(err)
	}
	if orphanBundles != 0 || orphanCerts != 0 || orphanAccounts != 0 || retainedBundles != 2 || retainedCerts != 2 || retainedAccounts != 1 {
		t.Fatalf("credential cleanup orphan_bundles=%d orphan_certs=%d orphan_accounts=%d retained_bundles=%d retained_certs=%d retained_accounts=%d", orphanBundles, orphanCerts, orphanAccounts, retainedBundles, retainedCerts, retainedAccounts)
	}
}

func assertNoUnvalidatedNavigatorConstraints(t *testing.T, ctx context.Context, db *sql.DB, stage string) {
	t.Helper()
	var unvalidated int
	if err := db.QueryRowContext(ctx, `
SELECT COUNT(*)
FROM pg_constraint
WHERE connamespace = 'navigator'::regnamespace
  AND NOT convalidated`).Scan(&unvalidated); err != nil {
		t.Fatal(err)
	}
	if unvalidated != 0 {
		t.Fatalf("%s left %d Navigator constraints unvalidated", stage, unvalidated)
	}
}

func verifyTenantEdgeApplyAliasFKMigrationCleansOrphans(t *testing.T, db *sql.DB) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `
ALTER TABLE navigator.tenant_edge_apply_state
DROP CONSTRAINT IF EXISTS fk_navigator_tenant_edge_apply_alias`); err != nil {
		t.Fatalf("drop baseline alias foreign key: %v", err)
	}
	const orphanTenantID = "10000000-0000-0000-0000-000000000003"
	if _, err := conn.ExecContext(ctx, `
INSERT INTO navigator.tenant_edge_apply_state (
    tenant_id, cluster_id, node_id, bundle_id, state, last_delivery_sequence
) VALUES ($1::uuid, 'orphan-cluster', 'orphan-node', 'tenant:' || $1::text, 'applied', 1)`, orphanTenantID); err != nil {
		t.Fatalf("insert pre-migration orphan: %v", err)
	}
	expand, err := dbsql.Content.ReadFile("migrations/navigator/v0.3.0/expand/003_tenant_edge_apply_alias_fk.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(ctx, string(expand)); err != nil {
		t.Fatalf("apply alias foreign-key expand migration: %v", err)
	}
	var orphanCount int
	if err := conn.QueryRowContext(ctx, `
SELECT COUNT(*) FROM navigator.tenant_edge_apply_state WHERE tenant_id=$1::uuid`, orphanTenantID).Scan(&orphanCount); err != nil {
		t.Fatal(err)
	}
	if orphanCount != 1 {
		t.Fatalf("expand changed %d historical orphan rows, want cleanup deferred to postdeploy", orphanCount)
	}
	var validated bool
	if err := conn.QueryRowContext(ctx, `
SELECT convalidated
FROM pg_constraint
WHERE conname = 'fk_navigator_tenant_edge_apply_alias'
  AND conrelid = 'navigator.tenant_edge_apply_state'::regclass`).Scan(&validated); err != nil {
		t.Fatal(err)
	}
	if validated {
		t.Fatal("expand unexpectedly validated alias foreign key with an existing orphan")
	}
	const validTenantID = "10000000-0000-0000-0000-000000000011"
	if _, err := conn.ExecContext(ctx, `
INSERT INTO navigator.tenant_aliases (tenant_id, subdomain, status)
VALUES ($1::uuid, 'authority-backfill-valid', 'cert_issued')`, validTenantID); err != nil {
		t.Fatalf("insert valid alias for authority backfill: %v", err)
	}
	if _, err := conn.ExecContext(ctx, `
INSERT INTO navigator.tenant_edge_apply_state (
    tenant_id, cluster_id, node_id, bundle_id, state, last_delivery_sequence
) VALUES ($1::uuid, 'valid-cluster', 'valid-node', 'tenant:' || $1::text, 'applied', 1)`, validTenantID); err != nil {
		t.Fatalf("insert valid pre-migration edge state: %v", err)
	}
	authorityExpand, err := dbsql.Content.ReadFile("migrations/navigator/v0.3.0/expand/007_tenant_alias_cluster_authority.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(ctx, string(authorityExpand)); err != nil {
		t.Fatalf("apply cluster-authority expand migration with historical orphan: %v", err)
	}
	var validAuthority, orphanAuthority int
	if err := conn.QueryRowContext(ctx, `
SELECT
  COUNT(*) FILTER (WHERE tenant_id = $1::uuid AND cluster_id = 'valid-cluster'),
  COUNT(*) FILTER (WHERE tenant_id = $2::uuid AND cluster_id = 'orphan-cluster')
FROM navigator.tenant_alias_cluster_authority`, validTenantID, orphanTenantID).Scan(&validAuthority, &orphanAuthority); err != nil {
		t.Fatal(err)
	}
	if validAuthority != 1 || orphanAuthority != 0 {
		t.Fatalf("authority backfill valid=%d orphan=%d, want 1/0", validAuthority, orphanAuthority)
	}
	const newOrphanTenantID = "10000000-0000-0000-0000-000000000004"
	_, insertErr := conn.ExecContext(ctx, `
INSERT INTO navigator.tenant_edge_apply_state (
    tenant_id, cluster_id, node_id, bundle_id, state, last_delivery_sequence
) VALUES ($1::uuid, 'new-orphan-cluster', 'new-orphan-node', 'tenant:' || $1::text, 'applied', 1)`, newOrphanTenantID)
	var pqErr *pq.Error
	if !errors.As(insertErr, &pqErr) || string(pqErr.Code) != "23503" {
		t.Fatalf("new orphan after NOT VALID FK error = %v, want SQLSTATE 23503", insertErr)
	}
	cleanup, err := dbsql.Content.ReadFile("migrations/navigator/v0.3.0/postdeploy/003_tenant_edge_apply_alias_orphan_cleanup.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(ctx, string(cleanup)); err != nil {
		t.Fatalf("apply alias orphan-cleanup postdeploy migration: %v", err)
	}
	if err := conn.QueryRowContext(ctx, `
SELECT COUNT(*) FROM navigator.tenant_edge_apply_state WHERE tenant_id=$1::uuid`, orphanTenantID).Scan(&orphanCount); err != nil {
		t.Fatal(err)
	}
	if orphanCount != 0 {
		t.Fatalf("postdeploy retained %d orphan edge rows", orphanCount)
	}
	validate, err := dbsql.Content.ReadFile("migrations/navigator/v0.3.0/postdeploy/004_validate_tenant_edge_apply_alias_fk.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(ctx, string(validate)); err != nil {
		t.Fatalf("apply alias foreign-key validation postdeploy migration: %v", err)
	}
	if err := conn.QueryRowContext(ctx, `
SELECT convalidated
FROM pg_constraint
WHERE conname = 'fk_navigator_tenant_edge_apply_alias'
  AND conrelid = 'navigator.tenant_edge_apply_state'::regclass`).Scan(&validated); err != nil {
		t.Fatal(err)
	}
	if !validated {
		t.Fatal("postdeploy did not validate alias foreign key")
	}
}

func verifyTenantEdgeApplyAckDeliveryFence(t *testing.T, db *sql.DB) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	q := New(db)
	base := UpsertTenantEdgeApplyAckParams{
		TenantID: "10000000-0000-0000-0000-000000000001", ClusterID: "cluster-1", NodeID: "node-1",
		BundleID: "tenant:10000000-0000-0000-0000-000000000001", BundleVersion: "bundle-v1", State: "applied",
		LastSeedVersion: sql.NullInt64{Int64: 7, Valid: true}, LastDeliverySequence: 11,
		LastAckAt: sql.NullTime{Time: time.Now().UTC(), Valid: true},
	}
	if _, err := db.ExecContext(ctx, `
INSERT INTO navigator.tenant_aliases (tenant_id, subdomain, status)
VALUES ($1::uuid, 'contract-alias', 'cert_issued')`, base.TenantID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
INSERT INTO navigator.tls_bundles (bundle_id, domains, cert_pem, key_pem, expires_at, version)
VALUES ($1, '[]'::jsonb, 'cert', 'key', NOW() + INTERVAL '1 day', $2)`, base.BundleID, base.BundleVersion); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
INSERT INTO navigator.tenant_alias_cluster_authority (tenant_id, cluster_id, state, authority_sequence)
VALUES ($1::uuid, $2, 'active', 1)`, base.TenantID, base.ClusterID); err != nil {
		t.Fatal(err)
	}
	if outcome, err := q.UpsertTenantEdgeApplyAck(ctx, base); err != nil || outcome != "accepted" {
		t.Fatalf("apply ordered ACK outcome=%q err=%v", outcome, err)
	}
	late := base
	late.State = "pending_apply"
	late.LastDeliverySequence = 10
	if outcome, err := q.UpsertTenantEdgeApplyAck(ctx, late); err != nil || outcome != "stale" {
		t.Fatalf("late equal-seed ACK outcome=%q err=%v, want stale", outcome, err)
	}
	var state string
	var deliverySequence int64
	if err := db.QueryRowContext(ctx, `
SELECT state, last_delivery_sequence
FROM navigator.tenant_edge_apply_state
WHERE tenant_id = $1::uuid AND node_id = $2 AND bundle_id = $3`,
		base.TenantID, base.NodeID, base.BundleID).Scan(&state, &deliverySequence); err != nil {
		t.Fatal(err)
	}
	if state != "applied" || deliverySequence != 11 {
		t.Fatalf("state=%q delivery_sequence=%d, want applied/11", state, deliverySequence)
	}
	if _, err := db.ExecContext(ctx, `
UPDATE navigator.tenant_edge_apply_state
SET state = 'in_dns', in_dns_at = NOW()
WHERE tenant_id = $1::uuid AND node_id = $2 AND bundle_id = $3`,
		base.TenantID, base.NodeID, base.BundleID); err != nil {
		t.Fatal(err)
	}
	replay := base
	replay.LastDeliverySequence = 12
	if outcome, err := q.UpsertTenantEdgeApplyAck(ctx, replay); err != nil || outcome != "accepted" {
		t.Fatalf("sequenced success replay outcome=%q err=%v, want fence advance", outcome, err)
	}
	var inDNSAt sql.NullTime
	if err := db.QueryRowContext(ctx, `
SELECT state, last_delivery_sequence, in_dns_at
FROM navigator.tenant_edge_apply_state
WHERE tenant_id = $1::uuid AND node_id = $2 AND bundle_id = $3`,
		base.TenantID, base.NodeID, base.BundleID).Scan(&state, &deliverySequence, &inDNSAt); err != nil {
		t.Fatal(err)
	}
	if state != "in_dns" || deliverySequence != 12 || !inDNSAt.Valid {
		t.Fatalf("sequenced replay state=%q delivery_sequence=%d in_dns_at=%v, want in_dns/12/preserved", state, deliverySequence, inDNSAt)
	}
	failure := base
	failure.State = "pending_apply"
	failure.LastDeliverySequence = 13
	if outcome, err := q.UpsertTenantEdgeApplyAck(ctx, failure); err != nil || outcome != "accepted" {
		t.Fatalf("sequenced failure outcome=%q err=%v", outcome, err)
	}
	recovery := base
	recovery.LastDeliverySequence = 14
	if outcome, err := q.UpsertTenantEdgeApplyAck(ctx, recovery); err != nil || outcome != "accepted" {
		t.Fatalf("sequenced recovery outcome=%q err=%v", outcome, err)
	}
	legacyNewer := failure
	legacyNewer.LastSeedVersion = sql.NullInt64{Int64: 8, Valid: true}
	legacyNewer.LastDeliverySequence = 0
	if outcome, err := q.UpsertTenantEdgeApplyAck(ctx, legacyNewer); err != nil || outcome != "accepted" {
		t.Fatalf("newer legacy failure outcome=%q err=%v", outcome, err)
	}
	legacyRecovery := recovery
	legacyRecovery.LastSeedVersion = legacyNewer.LastSeedVersion
	legacyRecovery.LastDeliverySequence = 0
	if outcome, err := q.UpsertTenantEdgeApplyAck(ctx, legacyRecovery); err != nil || outcome != "stale" {
		t.Fatalf("equal-seed legacy recovery crossed sequenced fence outcome=%q err=%v", outcome, err)
	}
	if err := db.QueryRowContext(ctx, `
SELECT state, last_delivery_sequence
FROM navigator.tenant_edge_apply_state
WHERE tenant_id = $1::uuid AND node_id = $2 AND bundle_id = $3`,
		base.TenantID, base.NodeID, base.BundleID).Scan(&state, &deliverySequence); err != nil {
		t.Fatal(err)
	}
	if state != "pending_apply" || deliverySequence != 14 {
		t.Fatalf("legacy reset state=%q delivery_sequence=%d, want pending_apply/14", state, deliverySequence)
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM navigator.tenant_edge_apply_state WHERE tenant_id=$1::uuid`, base.TenantID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM navigator.tenant_aliases WHERE tenant_id=$1::uuid`, base.TenantID); err != nil {
		t.Fatal(err)
	}
	lateAfterTeardown := base
	lateAfterTeardown.LastSeedVersion = sql.NullInt64{Int64: 9, Valid: true}
	lateAfterTeardown.LastDeliverySequence = 15
	if outcome, err := q.UpsertTenantEdgeApplyAck(ctx, lateAfterTeardown); err != nil || outcome != "missing_parent" {
		t.Fatalf("late ACK after alias teardown outcome=%q err=%v, want missing_parent", outcome, err)
	}
	var resurrected int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM navigator.tenant_edge_apply_state WHERE tenant_id=$1::uuid`, base.TenantID).Scan(&resurrected); err != nil || resurrected != 0 {
		t.Fatalf("orphaned apply state after teardown count=%d err=%v", resurrected, err)
	}
}

func verifyTenantAliasReactivationTeardownSerialization(t *testing.T, db *sql.DB) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	const tenantID = "10000000-0000-0000-0000-000000000005"
	if _, err := db.ExecContext(ctx, `
INSERT INTO navigator.tenant_aliases (tenant_id, subdomain, status)
VALUES ($1::uuid, 'reactivation-race', 'tearing_down')`, tenantID); err != nil {
		t.Fatal(err)
	}

	deleteTx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	deleted, err := New(deleteTx).DeleteTenantAlias(ctx, tenantID)
	if err != nil || deleted != 1 {
		_ = deleteTx.Rollback()
		t.Fatalf("stage delete-first teardown rows=%d err=%v", deleted, err)
	}
	type ensureResult struct {
		row EnsureTenantAliasRow
		err error
	}
	ensureDone := make(chan ensureResult, 1)
	go func() {
		var row EnsureTenantAliasRow
		retryErr := database.RetryPostgres(ctx, database.DefaultRetryAttempts, 25*time.Millisecond, func() error {
			var queryErr error
			row, queryErr = New(db).EnsureTenantAlias(ctx, EnsureTenantAliasParams{TenantID: tenantID, Subdomain: "reactivation-race"})
			return queryErr
		})
		ensureDone <- ensureResult{row: row, err: retryErr}
	}()
	select {
	case result := <-ensureDone:
		_ = deleteTx.Rollback()
		t.Fatalf("reactivation did not serialize behind teardown: row=%#v err=%v", result.row, result.err)
	case <-time.After(150 * time.Millisecond):
	}
	if err := deleteTx.Commit(); err != nil {
		t.Fatal(err)
	}
	select {
	case result := <-ensureDone:
		if result.err != nil || result.row.Status != "cert_issuing" {
			t.Fatalf("delete-first reactivation row=%#v err=%v", result.row, result.err)
		}
	case <-ctx.Done():
		t.Fatal("reactivation did not resume after teardown")
	}

	if _, err := db.ExecContext(ctx, `UPDATE navigator.tenant_aliases SET status='tearing_down' WHERE tenant_id=$1::uuid`, tenantID); err != nil {
		t.Fatal(err)
	}
	ensureTx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	reactivated, err := New(ensureTx).EnsureTenantAlias(ctx, EnsureTenantAliasParams{TenantID: tenantID, Subdomain: "reactivation-race"})
	if err != nil || reactivated.Status != "cert_issuing" {
		_ = ensureTx.Rollback()
		t.Fatalf("stage reactivation-first row=%#v err=%v", reactivated, err)
	}
	type deleteResult struct {
		rows int64
		err  error
	}
	deleteDone := make(chan deleteResult, 1)
	go func() {
		var rows int64
		retryErr := database.RetryPostgres(ctx, database.DefaultRetryAttempts, 25*time.Millisecond, func() error {
			var queryErr error
			rows, queryErr = New(db).DeleteTenantAlias(ctx, tenantID)
			return queryErr
		})
		deleteDone <- deleteResult{rows: rows, err: retryErr}
	}()
	select {
	case result := <-deleteDone:
		_ = ensureTx.Rollback()
		t.Fatalf("teardown did not serialize behind reactivation: rows=%d err=%v", result.rows, result.err)
	case <-time.After(150 * time.Millisecond):
	}
	if err := ensureTx.Commit(); err != nil {
		t.Fatal(err)
	}
	select {
	case result := <-deleteDone:
		if result.err != nil || result.rows != 0 {
			t.Fatalf("reactivation-first teardown rows=%d err=%v, want fenced no-op", result.rows, result.err)
		}
	case <-ctx.Done():
		t.Fatal("teardown did not resume after reactivation")
	}
	var status string
	if err := db.QueryRowContext(ctx, `SELECT status FROM navigator.tenant_aliases WHERE tenant_id=$1::uuid`, tenantID).Scan(&status); err != nil || status != "cert_issuing" {
		t.Fatalf("reactivated alias status=%q err=%v", status, err)
	}
}

func verifyTenantBundleAuthoritySerialization(t *testing.T, db *sql.DB) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	const tenantID = "10000000-0000-0000-0000-000000000009"
	const bundleID = "tenant:" + tenantID
	if _, err := db.ExecContext(ctx, `
INSERT INTO navigator.tenant_aliases (tenant_id, subdomain, status)
VALUES ($1::uuid, 'bundle-race', 'cert_issuing')`, tenantID); err != nil {
		t.Fatal(err)
	}
	var authorityVersion int64
	if err := db.QueryRowContext(ctx, `SELECT authority_version FROM navigator.tenant_aliases WHERE tenant_id=$1::uuid`, tenantID).Scan(&authorityVersion); err != nil {
		t.Fatal(err)
	}
	bundle := SaveTLSBundleParams{
		BundleID: bundleID, Domains: json.RawMessage(`[]`), CertPem: "cert", KeyPem: "key",
		ExpiresAt: time.Now().UTC().Add(24 * time.Hour), IssuerCa: "letsencrypt", ExpectedSubdomain: "bundle-race",
		ExpectedAuthorityVersion: authorityVersion, Version: "bundle-v1",
	}

	bundleTx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := New(bundleTx).SaveTLSBundle(ctx, bundle); err != nil {
		_ = bundleTx.Rollback()
		t.Fatal(err)
	}
	teardownDone := make(chan error, 1)
	go func() {
		teardownDone <- database.RetryPostgres(ctx, database.DefaultRetryAttempts, 25*time.Millisecond, func() error {
			_, statusErr := New(db).SetTenantAliasStatus(ctx, SetTenantAliasStatusParams{
				TenantID: tenantID, ExpectedSubdomain: "", Status: "tearing_down", ErrMsg: "",
			})
			return statusErr
		})
	}()
	select {
	case teardownErr := <-teardownDone:
		_ = bundleTx.Rollback()
		t.Fatalf("teardown did not serialize behind bundle persistence: %v", teardownErr)
	case <-time.After(150 * time.Millisecond):
	}
	if err := bundleTx.Commit(); err != nil {
		t.Fatal(err)
	}
	select {
	case teardownErr := <-teardownDone:
		if teardownErr != nil {
			t.Fatal(teardownErr)
		}
	case <-ctx.Done():
		t.Fatal("teardown did not resume after bundle persistence")
	}

	if _, err := db.ExecContext(ctx, `DELETE FROM navigator.tls_bundles WHERE bundle_id=$1`, bundleID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE navigator.tenant_aliases SET status='cert_issuing', updated_at=NOW() WHERE tenant_id=$1::uuid`, tenantID); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT authority_version FROM navigator.tenant_aliases WHERE tenant_id=$1::uuid`, tenantID).Scan(&bundle.ExpectedAuthorityVersion); err != nil {
		t.Fatal(err)
	}
	teardownTx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if rows, err := New(teardownTx).SetTenantAliasStatus(ctx, SetTenantAliasStatusParams{
		TenantID: tenantID, ExpectedSubdomain: "", Status: "tearing_down", ErrMsg: "",
	}); err != nil || rows != 1 {
		_ = teardownTx.Rollback()
		t.Fatalf("stage teardown-first rows=%d err=%v", rows, err)
	}
	type bundleResult struct{ err error }
	bundleDone := make(chan bundleResult, 1)
	go func() {
		saveErr := database.RetryPostgres(ctx, database.DefaultRetryAttempts, 25*time.Millisecond, func() error {
			_, queryErr := New(db).SaveTLSBundle(ctx, bundle)
			return queryErr
		})
		bundleDone <- bundleResult{err: saveErr}
	}()
	select {
	case result := <-bundleDone:
		_ = teardownTx.Rollback()
		t.Fatalf("bundle persistence did not serialize behind teardown: %v", result.err)
	case <-time.After(150 * time.Millisecond):
	}
	if err := teardownTx.Commit(); err != nil {
		t.Fatal(err)
	}
	select {
	case result := <-bundleDone:
		if !errors.Is(result.err, sql.ErrNoRows) {
			t.Fatalf("teardown-first bundle persistence error=%v, want sql.ErrNoRows", result.err)
		}
	case <-ctx.Done():
		t.Fatal("bundle persistence did not resume after teardown")
	}
	var bundles int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM navigator.tls_bundles WHERE bundle_id=$1`, bundleID).Scan(&bundles); err != nil || bundles != 0 {
		t.Fatalf("teardown-first bundle count=%d err=%v", bundles, err)
	}
	renamed, err := New(db).EnsureTenantAlias(ctx, EnsureTenantAliasParams{TenantID: tenantID, Subdomain: "bundle-race-renamed"})
	if err != nil {
		t.Fatal(err)
	}
	bundle.ExpectedAuthorityVersion = renamed.AuthorityVersion
	if _, err := New(db).SaveTLSBundle(ctx, bundle); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("old-label bundle write after rename error=%v, want sql.ErrNoRows", err)
	}
}

func verifyTenantEdgeApplyAckTeardownSerialization(t *testing.T, db *sql.DB) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	const tenantID = "10000000-0000-0000-0000-000000000002"
	if _, err := db.ExecContext(ctx, `
INSERT INTO navigator.tenant_aliases (tenant_id, subdomain, status)
VALUES ($1::uuid, 'teardown-race', 'cert_issued')`, tenantID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
INSERT INTO navigator.tls_bundles (bundle_id, domains, cert_pem, key_pem, expires_at, version)
VALUES ('tenant:' || $1::text, '[]'::jsonb, 'cert', 'key', NOW() + INTERVAL '1 day', 'bundle-v1')`, tenantID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
INSERT INTO navigator.tenant_alias_cluster_authority (tenant_id, cluster_id, state, authority_sequence)
VALUES ($1::uuid, 'cluster-1', 'active', 1)`, tenantID); err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	ack := UpsertTenantEdgeApplyAckParams{
		TenantID: tenantID, ClusterID: "cluster-1", NodeID: "node-1", BundleID: "tenant:" + tenantID,
		BundleVersion: "bundle-v1", State: "applied", LastSeedVersion: sql.NullInt64{Int64: 1, Valid: true}, LastDeliverySequence: 1,
		LastAckAt: sql.NullTime{Time: time.Now().UTC(), Valid: true},
	}
	if outcome, err := New(tx).UpsertTenantEdgeApplyAck(ctx, ack); err != nil || outcome != "accepted" {
		t.Fatalf("insert in-flight ACK outcome=%q err=%v", outcome, err)
	}
	if rows, err := New(tx).SetTenantAliasStatus(ctx, SetTenantAliasStatusParams{TenantID: tenantID, Status: "tearing_down"}); err != nil || rows != 1 {
		t.Fatalf("stage teardown after ACK rows=%d err=%v", rows, err)
	}
	deleteDone := make(chan error, 1)
	go func() {
		deleteDone <- database.RetryPostgres(ctx, database.DefaultRetryAttempts, 25*time.Millisecond, func() error {
			rows, statusErr := New(db).SetTenantAliasStatus(ctx, SetTenantAliasStatusParams{
				TenantID: tenantID, Status: "tearing_down",
			})
			if statusErr != nil {
				return statusErr
			}
			if rows != 1 {
				return fmt.Errorf("stage teardown rows=%d", rows)
			}
			_, deleteErr := New(db).DeleteTenantAlias(ctx, tenantID)
			return deleteErr
		})
	}()
	select {
	case deleteErr := <-deleteDone:
		t.Fatalf("teardown did not serialize behind in-flight ACK: %v", deleteErr)
	case <-time.After(150 * time.Millisecond):
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	committed = true
	select {
	case deleteErr := <-deleteDone:
		if deleteErr != nil {
			t.Fatalf("delete alias after ACK commit: %v", deleteErr)
		}
	case <-ctx.Done():
		t.Fatal("teardown did not resume after ACK transaction")
	}
	var aliases, edges int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM navigator.tenant_aliases WHERE tenant_id=$1::uuid`, tenantID).Scan(&aliases); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM navigator.tenant_edge_apply_state WHERE tenant_id=$1::uuid`, tenantID).Scan(&edges); err != nil {
		t.Fatal(err)
	}
	if aliases != 0 || edges != 0 {
		t.Fatalf("teardown left aliases=%d edge_rows=%d, want 0/0", aliases, edges)
	}

	// Exercise the inverse ordering too: teardown owns the parent row before
	// the ACK statement starts. FOR KEY SHARE makes PostgreSQL recheck the
	// deleted parent after waiting; Yugabyte may instead request a statement
	// retry. Both must converge to the explicit missing-parent outcome.
	if _, err := db.ExecContext(ctx, `
INSERT INTO navigator.tenant_aliases (tenant_id, subdomain, status)
VALUES ($1::uuid, 'teardown-race-delete-first', 'cert_issued')`, tenantID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
INSERT INTO navigator.tls_bundles (bundle_id, domains, cert_pem, key_pem, expires_at, version)
VALUES ('tenant:' || $1::text, '[]'::jsonb, 'cert', 'key', NOW() + INTERVAL '1 day', 'bundle-v1')`, tenantID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
INSERT INTO navigator.tenant_alias_cluster_authority (tenant_id, cluster_id, state, authority_sequence)
VALUES ($1::uuid, 'cluster-1', 'active', 1)`, tenantID); err != nil {
		t.Fatal(err)
	}
	deleteTx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	deleteCommitted := false
	defer func() {
		if !deleteCommitted {
			_ = deleteTx.Rollback()
		}
	}()
	if rows, err := New(deleteTx).SetTenantAliasStatus(ctx, SetTenantAliasStatusParams{TenantID: tenantID, Status: "tearing_down"}); err != nil || rows != 1 {
		t.Fatal(err)
	}
	if _, err := New(deleteTx).DeleteTenantAlias(ctx, tenantID); err != nil {
		t.Fatal(err)
	}
	type ackResult struct {
		outcome string
		err     error
	}
	ackDone := make(chan ackResult, 1)
	go func() {
		var outcome string
		err := database.RetryPostgres(ctx, database.DefaultRetryAttempts, 25*time.Millisecond, func() error {
			var upsertErr error
			outcome, upsertErr = New(db).UpsertTenantEdgeApplyAck(ctx, ack)
			return upsertErr
		})
		ackDone <- ackResult{outcome: outcome, err: err}
	}()
	select {
	case result := <-ackDone:
		t.Fatalf("delete-first ACK did not serialize: outcome=%q err=%v", result.outcome, result.err)
	case <-time.After(150 * time.Millisecond):
	}
	if err := deleteTx.Commit(); err != nil {
		t.Fatal(err)
	}
	deleteCommitted = true
	select {
	case result := <-ackDone:
		if result.err != nil || result.outcome != "missing_parent" {
			t.Fatalf("delete-first ACK outcome=%q err=%v, want missing_parent without error", result.outcome, result.err)
		}
	case <-ctx.Done():
		t.Fatal("delete-first ACK did not resume after teardown")
	}
}

func verifyTenantEdgeApplyAckClusterRevocationSerialization(t *testing.T, db *sql.DB) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	const tenantID = "10000000-0000-0000-0000-000000000012"
	const clusterID = "authority-race-cluster"
	const bundleVersion = "authority-race-v1"
	if _, err := db.ExecContext(ctx, `
INSERT INTO navigator.tenant_aliases (tenant_id, subdomain, status)
VALUES ($1::uuid, 'authority-race', 'cert_issued')`, tenantID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
INSERT INTO navigator.tls_bundles (bundle_id, domains, cert_pem, key_pem, expires_at, version)
VALUES ('tenant:' || $1::text, '[]'::jsonb, 'cert', 'key', NOW() + INTERVAL '1 day', $2)`, tenantID, bundleVersion); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
INSERT INTO navigator.tenant_alias_cluster_authority (tenant_id, cluster_id, state, authority_sequence)
VALUES ($1::uuid, $2, 'active', 1)`, tenantID, clusterID); err != nil {
		t.Fatal(err)
	}

	ack := UpsertTenantEdgeApplyAckParams{
		TenantID: tenantID, ClusterID: clusterID, NodeID: "authority-race-node", BundleID: "tenant:" + tenantID,
		BundleVersion: bundleVersion, State: "applied", LastSeedVersion: sql.NullInt64{Int64: 1, Valid: true},
		LastDeliverySequence: 1, LastAckAt: sql.NullTime{Time: time.Now().UTC(), Valid: true},
	}

	// ACK-first: its shared authority lock must hold the tombstone until the
	// ACK commits; the separately fenced delete statement then removes the row
	// under a snapshot that postdates the tombstone's lock.
	ackTx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	ackCommitted := false
	defer func() {
		if !ackCommitted {
			_ = ackTx.Rollback()
		}
	}()
	if outcome, err := New(ackTx).UpsertTenantEdgeApplyAck(ctx, ack); err != nil || outcome != "accepted" {
		t.Fatalf("stage ACK-first outcome=%q err=%v", outcome, err)
	}
	type revokeResult struct {
		applied bool
		err     error
	}
	revokeDone := make(chan revokeResult, 1)
	go func() {
		applied, err := revokeTenantAliasClusterAuthorityTx(ctx, db, tenantID, clusterID, 2)
		revokeDone <- revokeResult{applied: applied, err: err}
	}()
	select {
	case result := <-revokeDone:
		t.Fatalf("revocation did not serialize behind in-flight ACK: applied=%v err=%v", result.applied, result.err)
	case <-time.After(150 * time.Millisecond):
	}
	if err := ackTx.Commit(); err != nil {
		t.Fatal(err)
	}
	ackCommitted = true
	select {
	case result := <-revokeDone:
		if result.err != nil || !result.applied {
			t.Fatalf("ACK-first revocation applied=%v err=%v", result.applied, result.err)
		}
	case <-ctx.Done():
		t.Fatal("revocation did not resume after ACK commit")
	}
	assertNoTenantEdgeRows(t, ctx, db, tenantID, "ACK-first revocation")

	// A corrupt or legacy residual row beneath a tombstone must not be visible
	// to readiness or the DNS publisher even before a repair removes it.
	if _, err := db.ExecContext(ctx, `
INSERT INTO navigator.tenant_edge_apply_state (
    tenant_id, cluster_id, node_id, bundle_id, bundle_version, state, last_seed_version, last_delivery_sequence
) VALUES ($1::uuid, $2, 'residual-node', 'tenant:' || $1::text, $3, 'in_dns', 2, 2)`, tenantID, clusterID, bundleVersion); err != nil {
		t.Fatal(err)
	}
	if rows, err := New(db).ListTenantEdgeApplyState(ctx, tenantID); err != nil || len(rows) != 0 {
		t.Fatalf("revoked residual reached publisher rows=%#v err=%v", rows, err)
	}
	if ready, err := New(db).TenantAliasHasDNS(ctx, tenantID); err != nil || ready {
		t.Fatalf("revoked residual satisfied readiness ready=%v err=%v", ready, err)
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM navigator.tenant_edge_apply_state WHERE tenant_id=$1::uuid`, tenantID); err != nil {
		t.Fatal(err)
	}
	if applied, err := New(db).GrantTenantAliasClusterAuthority(ctx, GrantTenantAliasClusterAuthorityParams{
		TenantID: tenantID, ClusterID: clusterID, AuthoritySequence: 3,
	}); err != nil || !applied {
		t.Fatalf("reactivate authority applied=%v err=%v", applied, err)
	}

	// Revoke-first: the ACK blocks on the same authority row, then rechecks the
	// tombstone and classifies itself stale without inserting an edge row.
	revokeTx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	revokeCommitted := false
	defer func() {
		if !revokeCommitted {
			_ = revokeTx.Rollback()
		}
	}()
	if applied, err := New(revokeTx).RevokeTenantAliasClusterAuthority(ctx, RevokeTenantAliasClusterAuthorityParams{
		TenantID: tenantID, ClusterID: clusterID, AuthoritySequence: 4,
	}); err != nil || !applied {
		t.Fatalf("stage revoke-first applied=%v err=%v", applied, err)
	}
	type ackResult struct {
		outcome string
		err     error
	}
	ackDone := make(chan ackResult, 1)
	go func() {
		var outcome string
		err := database.RetryPostgres(ctx, database.DefaultRetryAttempts, 25*time.Millisecond, func() error {
			var ackErr error
			outcome, ackErr = New(db).UpsertTenantEdgeApplyAck(ctx, ack)
			return ackErr
		})
		ackDone <- ackResult{outcome: outcome, err: err}
	}()
	select {
	case result := <-ackDone:
		t.Fatalf("ACK did not serialize behind in-flight revocation: outcome=%q err=%v", result.outcome, result.err)
	case <-time.After(150 * time.Millisecond):
	}
	if err := revokeTx.Commit(); err != nil {
		t.Fatal(err)
	}
	revokeCommitted = true
	select {
	case result := <-ackDone:
		if result.err != nil || result.outcome != "revoked" {
			t.Fatalf("revoke-first ACK outcome=%q err=%v, want revoked", result.outcome, result.err)
		}
	case <-ctx.Done():
		t.Fatal("ACK did not resume after revocation commit")
	}
	assertNoTenantEdgeRows(t, ctx, db, tenantID, "revoke-first ACK")

	// Absent-row branch: the tombstone itself creates the serialization row.
	// A legacy first admission must wait and then classify itself stale; there
	// is no unprotected pre-tombstone gap in which it can authorize an ACK.
	const absentClusterID = "authority-race-cluster-absent"
	// A residual edge row (e.g. from an interrupted earlier rollout) makes the
	// post-commit zero-rows assertion prove the tombstone-fenced delete ran.
	if _, err := db.ExecContext(ctx, `
INSERT INTO navigator.tenant_edge_apply_state (
    tenant_id, cluster_id, node_id, bundle_id, bundle_version, state, last_seed_version, last_delivery_sequence
) VALUES ($1::uuid, $2, 'absent-residual-node', 'tenant:' || $1::text, $3, 'applied', 5, 5)`, tenantID, absentClusterID, bundleVersion); err != nil {
		t.Fatal(err)
	}
	absentRevokeTx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if applied, revokeErr := New(absentRevokeTx).RevokeTenantAliasClusterAuthority(ctx, RevokeTenantAliasClusterAuthorityParams{
		TenantID: tenantID, ClusterID: absentClusterID, AuthoritySequence: 5,
	}); revokeErr != nil || !applied {
		_ = absentRevokeTx.Rollback()
		t.Fatalf("stage absent-row revocation applied=%v err=%v", applied, revokeErr)
	}
	if _, deleteErr := New(absentRevokeTx).DeleteTenantEdgeApplyStateForRevokedCluster(ctx, DeleteTenantEdgeApplyStateForRevokedClusterParams{
		TenantID: tenantID, ClusterID: absentClusterID,
	}); deleteErr != nil {
		_ = absentRevokeTx.Rollback()
		t.Fatalf("stage absent-row edge delete: %v", deleteErr)
	}
	grantDone := make(chan revokeResult, 1)
	go func() {
		var granted bool
		grantErr := database.RetryPostgres(ctx, database.DefaultRetryAttempts, 25*time.Millisecond, func() error {
			var queryErr error
			granted, queryErr = New(db).GrantTenantAliasClusterAuthority(ctx, GrantTenantAliasClusterAuthorityParams{
				TenantID: tenantID, ClusterID: absentClusterID, AuthoritySequence: 0,
			})
			return queryErr
		})
		grantDone <- revokeResult{applied: granted, err: grantErr}
	}()
	select {
	case result := <-grantDone:
		_ = absentRevokeTx.Rollback()
		t.Fatalf("absent-row grant did not serialize: applied=%v err=%v", result.applied, result.err)
	case <-time.After(150 * time.Millisecond):
	}
	if err := absentRevokeTx.Commit(); err != nil {
		t.Fatal(err)
	}
	select {
	case result := <-grantDone:
		if (result.err != nil && !errors.Is(result.err, sql.ErrNoRows)) || result.applied {
			t.Fatalf("absent-row legacy grant applied=%v err=%v, want stale", result.applied, result.err)
		}
	case <-ctx.Done():
		t.Fatal("absent-row grant did not resume after revocation")
	}
	var absentState string
	if err := db.QueryRowContext(ctx, `
SELECT state FROM navigator.tenant_alias_cluster_authority
WHERE tenant_id=$1::uuid AND cluster_id=$2`, tenantID, absentClusterID).Scan(&absentState); err != nil || absentState != "revoked" {
		t.Fatalf("absent-row revocation state=%q err=%v", absentState, err)
	}
	var absentRows int
	if err := db.QueryRowContext(ctx, `
SELECT COUNT(*) FROM navigator.tenant_edge_apply_state
WHERE tenant_id=$1::uuid AND cluster_id=$2`, tenantID, absentClusterID).Scan(&absentRows); err != nil {
		t.Fatal(err)
	}
	if absentRows != 0 {
		t.Fatalf("absent-row revocation left %d edge rows beneath the tombstone", absentRows)
	}
}

func revokeTenantAliasClusterAuthorityTx(ctx context.Context, db *sql.DB, tenantID, clusterID string, sequence int64) (bool, error) {
	var applied bool
	err := database.WithRetryablePostgresTx(ctx, db, nil, func(tx *sql.Tx) error {
		queries := New(tx)
		applied = false
		if _, tombstoneErr := queries.RevokeTenantAliasClusterAuthority(ctx, RevokeTenantAliasClusterAuthorityParams{
			TenantID: tenantID, ClusterID: clusterID, AuthoritySequence: sequence,
		}); tombstoneErr != nil {
			if errors.Is(tombstoneErr, sql.ErrNoRows) {
				return nil
			}
			return tombstoneErr
		}
		applied = true
		_, deleteErr := queries.DeleteTenantEdgeApplyStateForRevokedCluster(ctx, DeleteTenantEdgeApplyStateForRevokedClusterParams{
			TenantID: tenantID, ClusterID: clusterID,
		})
		return deleteErr
	})
	return applied, err
}

func assertNoTenantEdgeRows(t *testing.T, ctx context.Context, db *sql.DB, tenantID, stage string) {
	t.Helper()
	var rows int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM navigator.tenant_edge_apply_state WHERE tenant_id=$1::uuid`, tenantID).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 0 {
		t.Fatalf("%s left %d tenant edge rows", stage, rows)
	}
}

func prepareNavigatorQueryCatalog(t *testing.T, db *sql.DB) {
	t.Helper()
	queries := navigatorGeneratedQueries(t)
	if len(queries) != 55 {
		t.Fatalf("found %d generated Navigator queries, want 55", len(queries))
	}
	ctx := context.Background()
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	for index, query := range queries {
		name := fmt.Sprintf("navigator_contract_%d", index)
		if _, err := conn.ExecContext(ctx, "PREPARE "+name+" AS "+query.sql); err != nil {
			t.Fatalf("prepare %s from %s: %v\n%s", query.name, query.file, err, query.sql)
		}
		if _, err := conn.ExecContext(ctx, "DEALLOCATE "+name); err != nil {
			t.Fatalf("deallocate %s: %v", query.name, err)
		}
	}
}

type navigatorGeneratedQuery struct {
	file string
	name string
	sql  string
}

func navigatorGeneratedQueries(t *testing.T) []navigatorGeneratedQuery {
	t.Helper()
	paths, err := filepath.Glob("*.sql.go")
	if err != nil {
		t.Fatal(err)
	}
	var queries []navigatorGeneratedQuery
	for _, path := range paths {
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		for _, declaration := range file.Decls {
			general, ok := declaration.(*ast.GenDecl)
			if !ok || general.Tok != token.CONST {
				continue
			}
			for _, specification := range general.Specs {
				value, ok := specification.(*ast.ValueSpec)
				if !ok {
					continue
				}
				for index, expression := range value.Values {
					literal, ok := expression.(*ast.BasicLit)
					if !ok || literal.Kind != token.STRING {
						continue
					}
					querySQL, err := strconv.Unquote(literal.Value)
					if err != nil {
						t.Fatal(err)
					}
					if !strings.HasPrefix(querySQL, "-- name:") {
						continue
					}
					queryName := "unknown"
					if index < len(value.Names) {
						queryName = value.Names[index].Name
					}
					queries = append(queries, navigatorGeneratedQuery{file: path, name: queryName, sql: querySQL})
				}
			}
		}
	}
	return queries
}

func startNavigatorQueryCatalogRealPG(t *testing.T) *sql.DB {
	t.Helper()
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not available")
	}
	name := fmt.Sprintf("fw-navigator-query-catalog-realpg-%d", time.Now().UnixNano())
	t.Cleanup(func() { _, _ = dockerpg.CLI("rm", "-fv", name) })
	image, err := dockerpg.PostgresImage()
	if err != nil {
		t.Fatal(err)
	}
	if output, err := dockerpg.Run("run", "-d", "--name", name, "-P", "-e", "POSTGRES_PASSWORD=harness", image); err != nil {
		t.Fatalf("docker run: %v\n%s", err, output)
	}
	port, err := dockerpg.DiscoverPublishedHostPort(name, "5432/tcp")
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("postgres", fmt.Sprintf("postgres://postgres:harness@127.0.0.1:%s/postgres?sslmode=disable", port))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := dockerpg.WaitReady(db, name); err != nil {
		t.Fatal(err)
	}
	schema, err := dbsql.Content.ReadFile("schema/navigator.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(string(schema)); err != nil {
		t.Fatal(err)
	}
	return db
}

func startNavigatorQueryCatalogRealYugabyte(t *testing.T) *sql.DB {
	t.Helper()
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not available")
	}
	name := fmt.Sprintf("fw-navigator-query-catalog-yb-%d", time.Now().UnixNano())
	t.Cleanup(func() { _, _ = dockerpg.CLI("rm", "-fv", name) })
	image, err := dockerpg.YugabyteImage()
	if err != nil {
		t.Fatal(err)
	}
	if output, err := dockerpg.Run("run", "-d", "--name", name, "-P", "--hostname", name, image, "bash", "-c", `exec bin/yugabyted start --background=false --advertise_address="$(hostname -i)" --tserver_flags=yb_enable_read_committed_isolation=false`); err != nil {
		t.Fatalf("docker run: %v\n%s", err, output)
	}
	port, err := dockerpg.DiscoverPublishedHostPort(name, "5433/tcp")
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("postgres", fmt.Sprintf("postgres://yugabyte@127.0.0.1:%s/yugabyte?sslmode=disable", port))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := dockerpg.WaitReadyFor(db, name, 3*time.Minute); err != nil {
		t.Fatal(err)
	}
	schema, err := dbsql.Content.ReadFile("schema/navigator.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(string(schema)); err != nil {
		t.Fatal(err)
	}
	return db
}

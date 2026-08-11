package control

import (
	"context"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	clusterpeerpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/cluster_peer"
)

func storagePeerSet(ids ...string) []*clusterpeerpb.TenantClusterPeer {
	peers := make([]*clusterpeerpb.TenantClusterPeer, 0, len(ids))
	for _, id := range ids {
		peers = append(peers, &clusterpeerpb.TenantClusterPeer{ClusterId: id})
	}
	return peers
}

// Storage authority: the ONLY authorized durable destination is the tenant's platform-OFFICIAL cluster with
// active/unexpired access (peer membership). Subscribed/generic peers are NOT storage destinations, and
// possession (checked elsewhere) is self-attested so the SOURCE must also be tenant-owned / origin / official.
func TestAuthorizeStorageReplication(t *testing.T) {
	// Tenant-A's entitlement: official cluster "official-a" (active, unexpired). "subscribed-remote" is an
	// active peer but NOT the official cluster, so it is not a storage destination. An EXPIRED grant to the
	// official cluster is modeled by that cluster being absent from the peer set (the QM query filters it).
	rA := tenantStorageRouting{officialCluster: "official-a", peers: storagePeerSet("official-a", "subscribed-remote")}
	// rExpired: official cluster is set in routing but the tenant no longer holds active/unexpired access to
	// it (not in the filtered peer set).
	rExpired := tenantStorageRouting{officialCluster: "official-a", peers: storagePeerSet("subscribed-remote")}
	cases := []struct {
		name                                                 string
		nodeTenant, nodeCluster, artifactTenant, destCluster string
		routing                                              tenantStorageRouting
		want                                                 bool
	}{
		{"byoc stores own media to platform official (billable)", "tenant-a", "byoc-a", "tenant-a", "official-a", rA, true},
		{"platform node on the tenant's official cluster", "", "official-a", "tenant-a", "official-a", rA, true},
		// Origin-cluster equality is NOT source authority: a tenant-B node in tenant-A's origin cluster
		// self-reporting A's hash must be denied.
		{"same-origin cross-tenant node denied", "tenant-b", "byoc-a", "tenant-a", "official-a", rA, false},
		// Cross-tenant: a tenant-B node self-reports possession of tenant-A's hash → denied.
		{"cross-tenant source denied", "tenant-b", "byoc-b", "tenant-a", "official-a", rA, false},
		// A subscribed remote (active peer) is NOT a valid storage destination.
		{"subscribed-remote destination denied", "tenant-a", "byoc-a", "tenant-a", "subscribed-remote", rA, false},
		// Destination is the official cluster but the tenant's access to it is expired (absent from peers).
		{"expired official access denied", "tenant-a", "byoc-a", "tenant-a", "official-a", rExpired, false},
		{"unresolved node fails closed", "", "", "tenant-a", "official-a", rA, false},
		{"empty destination fails closed", "tenant-a", "byoc-a", "tenant-a", "", rA, false},
		{"unknown artifact tenant fails closed", "tenant-a", "byoc-a", "", "official-a", rA, false},
		{"unresolved official cluster fails closed", "tenant-a", "byoc-a", "tenant-a", "official-a", tenantStorageRouting{}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := authorizeStorageReplication(tc.nodeTenant, tc.nodeCluster, tc.artifactTenant, tc.destCluster, tc.routing); got != tc.want {
				t.Fatalf("authorizeStorageReplication(nodeT=%q,nodeC=%q,artT=%q,dest=%q) = %v, want %v",
					tc.nodeTenant, tc.nodeCluster, tc.artifactTenant, tc.destCluster, got, tc.want)
			}
		})
	}
}

// A freeze claim must fail closed when the identity is incomplete (no tenant): permission can never be
// granted without an attempt scoped to a tenant, and no UPDATE must be issued.
func TestClaimFreezeAttempt_DeniesIncompleteIdentity(t *testing.T) {
	mock, _, _ := setupArtifactTestDeps(t)

	for _, tc := range []struct{ tenant, req, node string }{
		{"", "req-1", "node-1"},
		{"tenant-1", "", "node-1"},
		{"tenant-1", "req-1", ""},
	} {
		claimed, err := claimFreezeAttempt(context.Background(), "hash-1", tc.req, tc.node, tc.tenant, "", "key-1")
		if err != nil || claimed {
			t.Fatalf("expected deny (claimed=false, err=nil) for %+v, got claimed=%v err=%v", tc, claimed, err)
		}
	}
	// No DB expectations were registered → asserts no UPDATE was attempted.
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// A freeze claim must also fail closed when no server-derived object key is bound: without the exact
// descriptor, completion could not promote the same object, so the claim is denied and no UPDATE runs.
func TestClaimFreezeAttempt_DeniesMissingObjectKey(t *testing.T) {
	mock, _, _ := setupArtifactTestDeps(t)
	claimed, err := claimFreezeAttempt(context.Background(), "hash-1", "req-1", "node-1", "tenant-1", "", "")
	if err != nil || claimed {
		t.Fatalf("expected deny for empty object key, got claimed=%v err=%v", claimed, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// A claim is tenant-scoped and only fires for the explicit claimable states or the same request+node
// idempotent case. The predicate must carry the mandatory tenant match and NOT the old permissive
// "any non-in_progress" clause.
func TestClaimFreezeAttempt_TenantScopedClaimableStates(t *testing.T) {
	mock, _, _ := setupArtifactTestDeps(t)

	// The claim + its publication-ledger rows commit in one transaction.
	mock.ExpectBegin()
	mock.ExpectExec("UPDATE foghorn.artifacts.*sync_object_key = \\$6.*tenant_id::text = \\$4.*status = 'ready'.*is_complete = true AND an.is_orphaned = false.*sync_status IN \\('pending', 'failed', 'synced'\\).*sync_status = 'in_progress' AND sync_request_id = \\$2 AND sync_node_id = \\$3.*sync_object_key = \\$6.*storage_cluster_id IS NOT DISTINCT FROM NULLIF\\(\\$5, ''\\)").
		WithArgs("hash-1", "req-1", "node-1", "tenant-1", "", "key-1", sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("INSERT INTO foghorn.freeze_publication_ledger").WillReturnResult(sqlmock.NewResult(0, 4))
	mock.ExpectCommit()

	claimed, err := claimFreezeAttempt(context.Background(), "hash-1", "req-1", "node-1", "tenant-1", "", "key-1")
	if err != nil || !claimed {
		t.Fatalf("expected claim to succeed, got claimed=%v err=%v", claimed, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// An unclaimable row (e.g. in_progress under a different request, or terminal lost_local) matches zero
// rows → deny.
func TestClaimFreezeAttempt_UnclaimableDenies(t *testing.T) {
	mock, _, _ := setupArtifactTestDeps(t)

	// Zero rows claimed → the transaction rolls back with no ledger rows.
	mock.ExpectBegin()
	mock.ExpectExec("UPDATE foghorn.artifacts.*sync_status IN \\('pending', 'failed', 'synced'\\)").
		WithArgs("hash-1", "req-1", "node-1", "tenant-1", "", "key-1", sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectRollback()

	claimed, err := claimFreezeAttempt(context.Background(), "hash-1", "req-1", "node-1", "tenant-1", "", "key-1")
	if err != nil || claimed {
		t.Fatalf("expected deny on 0 rows, got claimed=%v err=%v", claimed, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// A .dtsh attempt is claimable only on a synced, not-yet-dtsh-synced row of the right tenant,
// recording request+node, and its retry predicate reads dtsh_failure_count for backoff.
func TestClaimDtshAttempt_ClaimsOnSyncedRow(t *testing.T) {
	mock, _, _ := setupArtifactTestDeps(t)

	// One transaction: the CTE snapshots the previous attempt before the guarded UPDATE. A fresh claim (no
	// prior attempt) returns empty prev fields, so nothing is enqueued.
	mock.ExpectBegin()
	mock.ExpectQuery("UPDATE foghorn.artifacts.*dtsh_status = 'in_progress'.*tenant_id::text = \\$4.*artifact_type IN \\('clip', 'vod'\\).*status = 'ready'.*sync_status = 'synced'.*dtsh_synced = false.*RETURNING").
		WithArgs("hash-d", "dtsh-req", "node-1", "tenant-1").
		WillReturnRows(sqlmock.NewRows([]string{"ok", "old_req"}).AddRow("", ""))
	mock.ExpectCommit()

	claimed, err := claimDtshAttempt(context.Background(), "hash-d", "dtsh-req", "node-1", "tenant-1")
	if err != nil || !claimed {
		t.Fatalf("expected dtsh claim to succeed, got claimed=%v err=%v", claimed, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// A claim with no tenant fails closed without touching the DB.
func TestClaimDtshAttempt_DeniesEmptyTenant(t *testing.T) {
	mock, _, _ := setupArtifactTestDeps(t)
	claimed, err := claimDtshAttempt(context.Background(), "hash-d", "dtsh-req", "node-1", "")
	if err != nil || claimed {
		t.Fatalf("expected deny on empty tenant, got claimed=%v err=%v", claimed, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// A failure that matches the persisted dtsh attempt is recorded (retryable) and reported handled, so
// the caller does NOT also run the main-upload failure guard.
func TestApplyDtshCompletionFailure_MatchesAttempt(t *testing.T) {
	mock, _, _ := setupArtifactTestDeps(t)

	// The DTSH failure settlement runs in a transaction: clear the attempt (RETURNING the descriptor), then
	// durably enqueue BOTH the .dtsh staging object (the node may have uploaded it despite reporting failure)
	// AND its versioned candidate (a completion may have promoted it before losing the CAS). The terminal-status
	// exclusion still applies so a late failure never writes onto a deleted/expired/aborted row.
	mock.ExpectBegin()
	mock.ExpectQuery("UPDATE foghorn.artifacts.*dtsh_status = 'failed'.*dtsh_sync_request_id = \\$2 AND dtsh_sync_node_id = \\$3.*status NOT IN \\('deleted', 'expired', 'aborted'\\).*tenant_id::text = \\$5.*RETURNING").
		WithArgs("hash-d", "dtsh-req", "node-1", "boom", "tenant-1").
		WillReturnRows(sqlmock.NewRows([]string{"sync_object_key"}).AddRow("obj/hash-d"))
	mock.ExpectExec("INSERT INTO foghorn.staging_cleanup_queue").
		WithArgs(FreezeStagingKey("obj/hash-d.dtsh", "dtsh-req"), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("INSERT INTO foghorn.staging_cleanup_queue").
		WithArgs(FreezePublishDtshKey("obj/hash-d", "dtsh-req"), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	if handled := applyDtshCompletionFailure(context.Background(), "hash-d", "node-1", "dtsh-req", "boom", "tenant-1", logging.NewLogger()); !handled {
		t.Fatal("expected dtsh failure to be handled")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// A failure that is NOT a known dtsh attempt matches zero rows → not handled, so the main-upload guard
// takes over.
func TestApplyDtshCompletionFailure_NotADtshAttempt(t *testing.T) {
	mock, _, _ := setupArtifactTestDeps(t)

	// No matching dtsh attempt → the guarded UPDATE affects zero rows → RETURNING yields no row → rollback.
	mock.ExpectBegin()
	mock.ExpectQuery("UPDATE foghorn.artifacts.*dtsh_status = 'failed'.*RETURNING").
		WithArgs("hash-x", "main-req", "node-1", "boom", "tenant-1").
		WillReturnRows(sqlmock.NewRows([]string{"sync_object_key"}))
	mock.ExpectRollback()

	if handled := applyDtshCompletionFailure(context.Background(), "hash-x", "node-1", "main-req", "boom", "tenant-1", logging.NewLogger()); handled {
		t.Fatal("expected a non-dtsh failure to be left for the main-upload guard")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// Every DTSH mutation fails closed without a tenant: no DB statement is issued.
func TestDtshMutations_DenyEmptyTenant(t *testing.T) {
	mock, _, _ := setupArtifactTestDeps(t)

	if handled := applyDtshCompletionFailure(context.Background(), "hash-d", "node-1", "dtsh-req", "boom", "", logging.NewLogger()); handled {
		t.Fatal("empty-tenant dtsh failure must not be handled")
	}
	clearDtshAttempt(context.Background(), "hash-d", "dtsh-req", "node-1", "") // must be a no-op
	if claimed, err := claimDtshAttempt(context.Background(), "hash-d", "dtsh-req", "node-1", ""); err != nil || claimed {
		t.Fatalf("empty-tenant claim must deny, got claimed=%v err=%v", claimed, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

//go:build schema_verify

package grpc

import (
	"context"
	"crypto/ed25519"
	"database/sql"
	"fmt"
	"testing"
	"time"

	"frameworks/api_control/internal/database/commodoredb"
	sharedauthority "github.com/Livepeer-FrameWorks/monorepo/pkg/mediaauthority"
	mediapb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_authority"
	placementpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_placement"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	"github.com/sirupsen/logrus"
	"google.golang.org/protobuf/proto"
)

const (
	activationTenant   = "8f5d2c11-0000-4000-8000-00000000ab01"
	activationRevision = 7
)

// seedPlacementActivationFixture puts one tenant authority version in front of
// two attested cells, with a saved placement change still awaiting rollout —
// the state the system is in immediately after an operator applies a policy and
// both cells have been sent the signed authority.
func seedPlacementActivationFixture(t *testing.T, db *sql.DB, cells []string) {
	t.Helper()
	ctx := context.Background()

	policy := &placementpb.PolicySet{Revision: activationRevision}
	tenant := &mediapb.TenantAuthority{
		TenantId:        activationTenant,
		Lifecycle:       mediapb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_ACTIVE,
		BillingDecision: mediapb.TenantBillingDecision_TENANT_BILLING_DECISION_ALLOW,
		MediaPlacement:  policy,
	}
	payload, err := proto.Marshal(tenant)
	if err != nil {
		t.Fatalf("marshal tenant authority: %v", err)
	}
	policyPayload, err := proto.Marshal(policy)
	if err != nil {
		t.Fatalf("marshal policy: %v", err)
	}

	if _, err := db.ExecContext(ctx, `
		INSERT INTO commodore.media_authority_versions
		  (authority_kind, authority_id, authority_version, payload_schema_version, payload,
		   payload_sha256, source_revisions, issued_at, refresh_after, valid_until)
		SELECT 'tenant', $1, 1, $2, $3::bytea, sha256($3::bytea), '[]'::jsonb, NOW(), NOW() + INTERVAL '1 hour', NOW() + INTERVAL '2 hours'`,
		activationTenant, sharedauthority.PlacementSchemaVersion, payload); err != nil {
		t.Fatalf("seed authority version: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO commodore.media_authority_current (authority_kind, authority_id, authority_version)
		VALUES ('tenant', $1, 1)`, activationTenant); err != nil {
		t.Fatalf("seed current authority: %v", err)
	}
	for _, cell := range cells {
		if _, err := db.ExecContext(ctx, `
			INSERT INTO commodore.media_authority_deliveries
			  (authority_kind, authority_id, authority_version, cell_id, signed_envelope, status)
			VALUES ('tenant', $1, 1, $2, '\x00'::bytea, 'delivering')`, activationTenant, cell); err != nil {
			t.Fatalf("seed delivery for %s: %v", cell, err)
		}
		if _, err := db.ExecContext(ctx, `
			INSERT INTO commodore.media_cell_placement_capabilities (cell_id, max_schema_version, enforcement_ready)
			VALUES ($1, $2, TRUE)`, cell, sharedauthority.PlacementSchemaVersion); err != nil {
			t.Fatalf("seed cell capability for %s: %v", cell, err)
		}
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO commodore.media_placement_policies (tenant_id, scope_kind, scope_id, revision, policy_payload)
		VALUES ($1::uuid, 'tenant', $1::uuid, $2, $3)`, activationTenant, activationRevision, policyPayload); err != nil {
		t.Fatalf("seed policy row: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO commodore.media_placement_changes
		  (tenant_id, scope_kind, scope_id, idempotency_key, request_sha256, revision, parent_revision,
		   policy_digest, review_digest, previous_policy_payload, policy_payload, actor_id, rollout_status)
		SELECT $1::uuid, 'tenant', $1::uuid, 'seed-key', sha256($3::bytea), $2, 0,
		       repeat('b', 64), repeat('c', 64), '\x'::bytea, $3::bytea, 'operator', 'pending'`,
		activationTenant, activationRevision, policyPayload); err != nil {
		t.Fatalf("seed placement change: %v", err)
	}
}

func placementChangeStatus(t *testing.T, db *sql.DB) (status string, activeRevision int64) {
	t.Helper()
	if err := db.QueryRow(`SELECT rollout_status FROM commodore.media_placement_changes WHERE tenant_id=$1::uuid AND revision=$2`,
		activationTenant, activationRevision).Scan(&status); err != nil {
		t.Fatalf("read change status: %v", err)
	}
	if err := db.QueryRow(`SELECT active_revision FROM commodore.media_placement_policies WHERE tenant_id=$1::uuid AND scope_kind='tenant'`,
		activationTenant).Scan(&activeRevision); err != nil {
		t.Fatalf("read active revision: %v", err)
	}
	return status, activeRevision
}

// ackInTx performs the acknowledgement half of acknowledgeMediaAuthorityDelivery
// for one cell, then runs the same activation derivation, in one transaction —
// the real shape, with the commit point under the test's control so the two
// cells' transactions can be made to overlap deterministically.
func ackInTx(t *testing.T, srv *CommodoreServer, db *sql.DB, cell string) (*sql.Tx, func() error) {
	t.Helper()
	ctx := context.Background()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin tx for %s: %v", cell, err)
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE commodore.media_authority_deliveries
		SET status='acknowledged', acknowledged_at=NOW(), updated_at=NOW()
		WHERE authority_kind='tenant' AND authority_id=$1 AND authority_version=1 AND cell_id=$2`,
		activationTenant, cell); err != nil {
		_ = tx.Rollback()
		t.Fatalf("mark %s acknowledged: %v", cell, err)
	}
	return tx, func() error {
		if err := srv.reconcilePlacementActivation(ctx, commodoredb.New(tx), "tenant", activationTenant); err != nil {
			_ = tx.Rollback()
			return err
		}
		return tx.Commit()
	}
}

// Both cells acknowledge the same authority version, and BOTH derive activation
// before either commits. That is the production interleaving: deliveries for one
// version are dispatched concurrently, and each acknowledgement derives inside
// its own transaction.
//
// Unserialized, each transaction reads the other's pre-acknowledgement state,
// both compute applied<required, both write "pending", both commit, and the
// change is stranded with every cell already acknowledged and nothing left to
// wait for. Serialized, the second derivation blocks until the first commits,
// sees a complete set, and activates. This asserts the outcome; the mechanism is
// pinned separately below.
func TestPlacementActivationSurvivesConcurrentAcknowledgements_RealPG(t *testing.T) {
	db := startCommodoreRealPG(t)
	seedPlacementActivationFixture(t, db, []string{"cell-a", "cell-b"})
	srv := &CommodoreServer{db: db}
	ctx := context.Background()

	txA, finishA := ackInTx(t, srv, db, "cell-a")
	txB, finishB := ackInTx(t, srv, db, "cell-b")

	// A derives first and keeps its transaction open, holding the lock.
	if err := srv.reconcilePlacementActivation(ctx, commodoredb.New(txA), "tenant", activationTenant); err != nil {
		t.Fatalf("first derivation: %v", err)
	}

	// B derives while A is still open. Serialized, this blocks; unserialized it
	// returns immediately having seen A as unacknowledged.
	derivedB := make(chan error, 1)
	go func() {
		derivedB <- srv.reconcilePlacementActivation(ctx, commodoredb.New(txB), "tenant", activationTenant)
	}()
	select {
	case err := <-derivedB:
		t.Fatalf("second derivation ran concurrently with the first (err=%v); both would read a partial picture", err)
	case <-time.After(500 * time.Millisecond):
	}

	// Releasing A lets B proceed against a complete set. A never waits on B, so
	// there is no cycle here — this is the real shape, not a contrived one.
	if err := finishA(); err != nil {
		t.Fatalf("commit first acknowledgement: %v", err)
	}
	select {
	case err := <-derivedB:
		if err != nil {
			t.Fatalf("second derivation: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("second derivation never unblocked after the first committed")
	}
	if err := finishB(); err != nil {
		t.Fatalf("commit second acknowledgement: %v", err)
	}

	status, active := placementChangeStatus(t, db)
	if status != "effective" {
		t.Fatalf("change rollout = %q after every cell acknowledged; the operator sees a delivered policy as not yet live", status)
	}
	if active != activationRevision {
		t.Fatalf("active revision = %d, want %d", active, activationRevision)
	}
}

// The serial case must keep working: one cell acknowledges, the change stays
// pending because the other has not, and it activates when the second lands.
func TestPlacementActivationWaitsForEveryCell_RealPG(t *testing.T) {
	db := startCommodoreRealPG(t)
	cells := []string{"cell-a", "cell-b"}
	seedPlacementActivationFixture(t, db, cells)
	srv := &CommodoreServer{db: db}

	_, finishA := ackInTx(t, srv, db, "cell-a")
	if err := finishA(); err != nil {
		t.Fatalf("first acknowledgement: %v", err)
	}
	if status, active := placementChangeStatus(t, db); status != "pending" || active != 0 {
		t.Fatalf("one acknowledgement activated the change: status=%q active=%d", status, active)
	}

	_, finishB := ackInTx(t, srv, db, "cell-b")
	if err := finishB(); err != nil {
		t.Fatalf("second acknowledgement: %v", err)
	}
	if status, active := placementChangeStatus(t, db); status != "effective" || active != activationRevision {
		t.Fatalf("the last acknowledgement did not activate: status=%q active=%d", status, active)
	}
}

// A change stranded by an acknowledgement that never reconciled it — a crashed
// process, a retried delivery — has no other retry path. The backstop converges
// it without inventing an activation the acknowledgement path would refuse.
func TestPlacementActivationBacklogConvergesStrandedChange_RealPG(t *testing.T) {
	db := startCommodoreRealPG(t)
	cells := []string{"cell-a", "cell-b"}
	seedPlacementActivationFixture(t, db, cells)
	srv := activationBacklogServer(db)

	// Every delivery acknowledged, nothing reconciled: the stranded state.
	if _, err := db.Exec(`
		UPDATE commodore.media_authority_deliveries
		SET status='acknowledged', acknowledged_at=NOW()
		WHERE authority_kind='tenant' AND authority_id=$1`, activationTenant); err != nil {
		t.Fatalf("acknowledge deliveries: %v", err)
	}
	if _, err := db.Exec(`
		UPDATE commodore.media_placement_changes SET updated_at = NOW() - INTERVAL '10 minutes'
		WHERE tenant_id=$1::uuid`, activationTenant); err != nil {
		t.Fatalf("age the change: %v", err)
	}
	if status, _ := placementChangeStatus(t, db); status != "pending" {
		t.Fatalf("fixture is not stranded: status=%q", status)
	}

	srv.processPlacementActivationBacklog(context.Background())

	if status, active := placementChangeStatus(t, db); status != "effective" || active != activationRevision {
		t.Fatalf("backlog sweep did not converge the change: status=%q active=%d", status, active)
	}
}

// The sweep must not activate a change whose cells have not all acknowledged.
// It supplies a missing trigger, never a missing acknowledgement.
func TestPlacementActivationBacklogRespectsOutstandingCells_RealPG(t *testing.T) {
	db := startCommodoreRealPG(t)
	cells := []string{"cell-a", "cell-b"}
	seedPlacementActivationFixture(t, db, cells)
	srv := activationBacklogServer(db)

	if _, err := db.Exec(`
		UPDATE commodore.media_authority_deliveries
		SET status='acknowledged', acknowledged_at=NOW()
		WHERE authority_kind='tenant' AND authority_id=$1 AND cell_id='cell-a'`, activationTenant); err != nil {
		t.Fatalf("acknowledge one delivery: %v", err)
	}
	if _, err := db.Exec(`
		UPDATE commodore.media_placement_changes SET updated_at = NOW() - INTERVAL '10 minutes'
		WHERE tenant_id=$1::uuid`, activationTenant); err != nil {
		t.Fatalf("age the change: %v", err)
	}

	srv.processPlacementActivationBacklog(context.Background())

	if status, active := placementChangeStatus(t, db); status != "pending" || active != 0 {
		t.Fatalf("backlog sweep activated a change one cell has not acknowledged: status=%q active=%d", status, active)
	}
}

// activationBacklogServer satisfies mediaAuthorityEnabled so the backlog worker
// runs. The sources are never called: the sweep only re-derives activation from
// rows that already exist.
type inertAuthoritySources struct{}

func (inertAuthoritySources) GetTenant(context.Context, string) (*quartermasterpb.GetTenantResponse, error) {
	return nil, nil
}

func (inertAuthoritySources) GetTenantEntitlement(context.Context, string) (*quartermasterpb.GetTenantEntitlementResponse, error) {
	return nil, nil
}

func (inertAuthoritySources) ListActiveTenants(context.Context) ([]string, error) { return nil, nil }

func (inertAuthoritySources) GetTenantBillingStatus(context.Context, string) (*purserpb.GetTenantBillingStatusResponse, error) {
	return nil, nil
}

func (inertAuthoritySources) GetTenantAdmissionStatus(context.Context, string) (*purserpb.GetTenantAdmissionStatusResponse, error) {
	return nil, nil
}

func activationBacklogServer(db *sql.DB) *CommodoreServer {
	sources := inertAuthoritySources{}
	_, key, err := ed25519.GenerateKey(nil)
	if err != nil {
		panic(err)
	}
	return &CommodoreServer{
		db: db, logger: logrus.New(),
		authorityTenantSource: sources, authorityBillingSource: sources,
		mediaAuthorityKeyID: "test-key", mediaAuthorityPrivateKey: key,
	}
}

// The outcome test above asserts that concurrency does not lose an activation,
// but it cannot by itself prove the serialization is present: with a different
// interleaving the unserialized code reaches the same result by luck.
//
// This pins the mechanism instead. While one acknowledgement transaction is
// mid-derivation, a second must not be able to derive concurrently — that is
// exactly the window in which both would read the other's pre-acknowledgement
// state, both conclude the rollout is incomplete, and a fully delivered change
// would be left pending with nothing left to wait for.
func TestPlacementActivationSerializesConcurrentDerivation_RealPG(t *testing.T) {
	db := startCommodoreRealPG(t)
	seedPlacementActivationFixture(t, db, []string{"cell-a", "cell-b"})
	srv := &CommodoreServer{db: db}
	ctx := context.Background()
	// Must match LockMediaAuthorityActivation exactly, namespace included:
	// the namespace exists so the key cannot collide with Commodore's
	// artifact-catalog locks, which share the single-bigint advisory space.
	lockKey := "media_authority_activation:tenant:" + activationTenant

	// contended reports whether a second transaction can take the activation
	// lock for this authority right now.
	contended := func() bool {
		probe, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatalf("begin probe tx: %v", err)
		}
		defer func() { _ = probe.Rollback() }()
		var acquired bool
		if err := probe.QueryRowContext(ctx, `SELECT pg_try_advisory_xact_lock(hashtext($1::text))`, lockKey).Scan(&acquired); err != nil {
			t.Fatalf("probe advisory lock: %v", err)
		}
		return !acquired
	}

	if contended() {
		t.Fatal("activation lock was already held before any derivation ran")
	}

	txA, finishA := ackInTx(t, srv, db, "cell-a")
	if err := srv.reconcilePlacementActivation(ctx, commodoredb.New(txA), "tenant", activationTenant); err != nil {
		t.Fatalf("derive activation: %v", err)
	}
	if !contended() {
		t.Fatal("a second transaction could derive activation concurrently; both would read the other's pre-acknowledgement state and strand the change")
	}

	if err := finishA(); err != nil {
		t.Fatalf("commit first acknowledgement: %v", err)
	}
	if contended() {
		t.Fatal("activation lock outlived its transaction")
	}
}

// seedBlockedScopes creates `count` scopes whose changes are permanently
// blocked: their cell never attests enforcement, and re-writing the same
// rollout status is a no-op that does not bump updated_at, so they never leave
// the backlog's result set.
func seedBlockedScopes(t *testing.T, db *sql.DB, count int) {
	t.Helper()
	for i := range count {
		tenant := fmt.Sprintf("00000000-0000-4000-8000-%012d", i)
		// The change references its policy row, which is the scope's identity.
		if _, err := db.Exec(`
			INSERT INTO commodore.media_placement_policies (tenant_id, scope_kind, scope_id, revision, policy_payload)
			VALUES ($1::uuid, 'tenant', $1::uuid, 1, '\x01'::bytea)`, tenant); err != nil {
			t.Fatalf("seed blocked policy %d: %v", i, err)
		}
		if _, err := db.Exec(`
			INSERT INTO commodore.media_placement_changes
			  (tenant_id, scope_kind, scope_id, idempotency_key, request_sha256, revision, parent_revision,
			   policy_digest, review_digest, previous_policy_payload, policy_payload, actor_id, rollout_status, created_at, updated_at)
			SELECT $1::uuid, 'tenant', $1::uuid, $2::text, sha256($2::text::bytea), 1, 0,
			       repeat('b', 64), repeat('c', 64), '\x'::bytea, '\x01'::bytea, 'operator', 'blocked',
			       NOW() - INTERVAL '1 hour', NOW() - INTERVAL '1 hour'`,
			tenant, fmt.Sprintf("blocked-%d", i)); err != nil {
			t.Fatalf("seed blocked scope %d: %v", i, err)
		}
	}
}

// A page of permanently blocked scopes must not hide the scopes behind it.
//
// Blocked changes never leave the backlog: re-writing the same rollout status
// matches no rows, so updated_at is never bumped. With a fixed ordering and a
// fixed page, a bounded prefix of them would starve every scope that sorts
// after — which is precisely the stranded change the backstop exists to rescue,
// in precisely the deployment condition (a cell that stopped attesting) that
// makes stranding likely.
func TestPlacementActivationBacklogPagesPastBlockedScopes_RealPG(t *testing.T) {
	db := startCommodoreRealPG(t)
	// The stranded tenant's UUID sorts after every blocked one, so it is only
	// ever reached by advancing the cursor.
	seedBlockedScopes(t, db, placementActivationBacklogBatch+16)
	seedPlacementActivationFixture(t, db, []string{"cell-a", "cell-b"})
	if _, err := db.Exec(`
		UPDATE commodore.media_authority_deliveries SET status='acknowledged', acknowledged_at=NOW()
		WHERE authority_kind='tenant' AND authority_id=$1`, activationTenant); err != nil {
		t.Fatalf("acknowledge deliveries: %v", err)
	}
	if _, err := db.Exec(`
		UPDATE commodore.media_placement_changes SET updated_at = NOW() - INTERVAL '10 minutes'
		WHERE tenant_id=$1::uuid`, activationTenant); err != nil {
		t.Fatalf("age the change: %v", err)
	}

	srv := activationBacklogServer(db)
	// One page is not enough by construction; the cursor must carry across ticks.
	for i := 0; i < 8; i++ {
		srv.processPlacementActivationBacklog(context.Background())
		if status, _ := placementChangeStatus(t, db); status == "effective" {
			return
		}
	}
	status, active := placementChangeStatus(t, db)
	t.Fatalf("a stranded change behind %d blocked scopes was never swept: status=%q active=%d",
		placementActivationBacklogBatch+16, status, active)
}

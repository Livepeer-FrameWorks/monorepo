//go:build schema_verify

package mediaauthority

import (
	"context"
	"crypto/ed25519"
	"errors"
	"testing"
	"time"

	"frameworks/api_balancing/internal/database/foghorndb"
	sharedauthority "github.com/Livepeer-FrameWorks/monorepo/pkg/mediaauthority"
	mediaauthoritypb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_authority"
	"google.golang.org/protobuf/proto"
)

// signedTenantAt re-signs the store fixture's tenant authority at another
// version and validity, with the fixture's key.
func signedTenantAt(t *testing.T, version uint64, issuedAt, validUntil time.Time) []byte {
	t.Helper()
	_, _, fixture := storeFixture(t, "cell-a")
	payload := &mediaauthoritypb.TenantAuthority{}
	if err := proto.Unmarshal(fixture.GetEnvelope().GetPayload(), payload); err != nil {
		t.Fatal(err)
	}
	envelope, err := sharedauthority.NewEnvelope(mediaauthoritypb.AuthorityKind_AUTHORITY_KIND_TENANT, payload.GetTenantId(), version,
		issuedAt, issuedAt.Add(validUntil.Sub(issuedAt)/2), validUntil, "signer-1", "cell-a", payload, fixture.GetEnvelope().GetSourceRevisions())
	if err != nil {
		t.Fatal(err)
	}
	seed := make([]byte, ed25519.SeedSize)
	for i := range seed {
		seed[i] = byte(i)
	}
	signed, err := sharedauthority.Sign(envelope, ed25519.NewKeyFromSeed(seed))
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := proto.MarshalOptions{Deterministic: true}.Marshal(signed)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

// A tenant whose authority lapsed in the cell comes back. The object copies the
// cell still holds were not corrected while the tenant was out, and nothing
// orders their corrections before the tenant's return (delivery runs in
// parallel; another object's fetch brings the tenant too). So the tenant's
// return withholds them until the control plane has sent them again. A
// renewal of a tenant that never lapsed withholds nothing.
func TestMediaAuthorityTenantRevivalWithholdsObjects_RealPG(t *testing.T) {
	conn := startMediaAuthorityRealPG(t)
	_, trust, _ := storeFixture(t, "cell-a")
	ctx := context.Background()
	store, err := NewStore(conn, "cell-a", trust)
	if err != nil {
		t.Fatal(err)
	}
	clock := storeFixtureNow.Add(time.Minute)
	store.now = func() time.Time { return clock }
	active := mediaauthoritypb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_ACTIVE
	object, _ := signedArtifactFixture(t, active, 2)
	for _, envelope := range [][]byte{signedTenantAt(t, 7, storeFixtureNow, storeFixtureNow.Add(24*time.Hour)), object} {
		if _, err = store.Apply(ctx, envelope); err != nil {
			t.Fatal(err)
		}
	}
	read := func() Freshness {
		t.Helper()
		snapshot, readErr := store.MediaObjectByPlaybackID(ctx, "artifact-playback")
		if readErr != nil {
			t.Fatal(readErr)
		}
		return snapshot.Freshness
	}
	if got := read(); got != FreshnessValid {
		t.Fatalf("before anything lapsed: %s", got)
	}

	// A renewal of a tenant still valid here withholds nothing.
	beforeRenewal, err := store.MediaObjectByPlaybackID(ctx, "artifact-playback")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.Apply(ctx, signedTenantAt(t, 8, storeFixtureNow, storeFixtureNow.Add(24*time.Hour))); err != nil {
		t.Fatal(err)
	}
	if got := read(); got != FreshnessValid {
		t.Fatalf("after an ordinary tenant renewal: %s, want valid", got)
	}
	if tenant, err := store.TenantForObject(ctx, beforeRenewal); err != nil || tenant.Freshness != FreshnessValid {
		t.Fatalf("routine parent renewal must recheck locally without a fetch: %+v, %v", tenant, err)
	}
	beforeRevival, err := store.MediaObjectByPlaybackID(ctx, "artifact-playback")
	if err != nil {
		t.Fatal(err)
	}
	beforeRevivalAt, err := foghorndb.New(conn).BeginMediaAuthorityConfirmation(ctx)
	if err != nil {
		t.Fatal(err)
	}

	// The tenant lapses here; a newer tenant version brings it back.
	if _, err = conn.ExecContext(ctx, `
		UPDATE foghorn.media_authorities
		SET issued_at = $1, refresh_after = $2, valid_until = $3
		WHERE authority_kind = 'tenant'`, storeFixtureNow.Add(-48*time.Hour), storeFixtureNow.Add(-36*time.Hour), storeFixtureNow.Add(-24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err = store.Apply(ctx, signedTenantAt(t, 9, storeFixtureNow, storeFixtureNow.Add(24*time.Hour))); err != nil {
		t.Fatal(err)
	}
	if got := read(); got != FreshnessHardExpired {
		t.Fatalf("object copy after its tenant came back: %s, want withheld", got)
	}
	if tenant, err := store.TenantForObject(ctx, beforeRevival); err != nil || tenant.Freshness != FreshnessHardExpired {
		t.Fatalf("object read before revival paired with revived tenant: %+v, %v", tenant, err)
	}
	if tenant, err := store.TenantSourceForObject(ctx, beforeRevival); err != nil || tenant.Freshness != FreshnessHardExpired {
		t.Fatalf("source read before revival paired with revived tenant: %+v, %v", tenant, err)
	}
	// Neither delayed ordinary delivery nor a fetch begun before revival is
	// proof that this object is current after the tenant's return.
	if _, err = store.Apply(ctx, object); !errors.Is(err, ErrAuthorityConfirmationRequired) {
		t.Fatalf("delayed ordinary delivery must request confirmation: %v", err)
	}
	if got := read(); got != FreshnessHardExpired {
		t.Fatalf("delayed duplicate cleared the revival barrier: %s", got)
	}
	if _, err = store.apply(ctx, object, beforeRevivalAt, 0); err != nil {
		t.Fatal(err)
	}
	if got := read(); got != FreshnessHardExpired {
		t.Fatalf("fetch begun before revival cleared the barrier: %s", got)
	}
	delayedNewVersion, _ := signedArtifactFixture(t, active, 3)
	if _, err = store.Apply(ctx, delayedNewVersion); !errors.Is(err, ErrAuthorityConfirmationRequired) {
		t.Fatalf("unconfirmed successor must request confirmation: %v", err)
	}
	if got := read(); got != FreshnessHardExpired {
		t.Fatalf("previously unseen delayed delivery cleared the barrier: %s", got)
	}
	// Without the control plane the pair stays withheld.
	store.SetAuthorityFetcher(func(context.Context, AuthorityLookup) ([][]byte, error) {
		return nil, errors.New("control plane unreachable")
	})
	pair, err := store.PlacementForInternalName(ctx, "10000000-0000-0000-0000-000000000001", "artifact-internal")
	if err != nil || pair.Object.Freshness != FreshnessHardExpired {
		t.Fatalf("pair without the control plane: object=%s err=%v, want withheld", pair.Object.Freshness, err)
	}
	// The control plane sends the version the cell holds; that lifts it.
	placementStore, err := NewStore(conn, "cell-a", trust)
	if err != nil {
		t.Fatal(err)
	}
	placementStore.now = store.now
	current, _ := signedArtifactFixture(t, active, 4)
	placementStore.SetAuthorityFetcher(func(context.Context, AuthorityLookup) ([][]byte, error) {
		return [][]byte{current}, nil
	})
	pair, err = placementStore.PlacementForInternalName(ctx, "10000000-0000-0000-0000-000000000001", "artifact-internal")
	if err != nil || pair.Object.Version != 4 || pair.Object.Freshness != FreshnessValid {
		t.Fatalf("pair after a current fetch: version=%d object=%s err=%v, want version 4 valid", pair.Object.Version, pair.Object.Freshness, err)
	}
}

// A restored cell database can hold a version the cell had already replaced,
// still valid, while the replacement has expired and can no longer be sent.
// The restore fence withholds everything the control plane has not sent again
// since, and only a matching summary lowers it. An ordinary restart is
// withheld only until the first answer, and an unreachable control plane ends
// that: the cell then decides on what it holds.
func TestMediaAuthorityRestoreFence_RealPG(t *testing.T) {
	conn := startMediaAuthorityRealPG(t)
	_, trust, _ := storeFixture(t, "cell-a")
	ctx := context.Background()
	clock := storeFixtureNow.Add(time.Minute)
	restart := func(t *testing.T) *Store {
		t.Helper()
		store, err := NewStore(conn, "cell-a", trust)
		if err != nil {
			t.Fatal(err)
		}
		store.now = func() time.Time { return clock }
		if err = store.RaiseStartupFence(ctx); err != nil {
			t.Fatal(err)
		}
		return store
	}
	freshness := func(t *testing.T, store *Store) Freshness {
		t.Helper()
		object, err := store.MediaObjectByPlaybackID(ctx, "artifact-playback")
		if err != nil {
			t.Fatal(err)
		}
		return object.Freshness
	}
	placement := func(store *Store) (PlacementPair, error) {
		return store.PlacementForInternalName(ctx, "10000000-0000-0000-0000-000000000001", "artifact-internal")
	}
	active := mediaauthoritypb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_ACTIVE

	// The state a restore brings back: the tenant and an older object version,
	// both still valid.
	seed, err := NewStore(conn, "cell-a", trust)
	if err != nil {
		t.Fatal(err)
	}
	seed.now = func() time.Time { return clock }
	tenant, _, _ := storeFixture(t, "cell-a")
	restored, _ := signedArtifactFixture(t, active, 2)
	startedAt, err := foghorndb.New(conn).BeginMediaAuthorityConfirmation(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, envelope := range [][]byte{tenant, restored} {
		if _, err = seed.apply(ctx, envelope, startedAt, 0); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("an ordinary restart decides locally before core answers", func(t *testing.T) {
		store := restart(t)
		if got := freshness(t, store); got != FreshnessValid {
			t.Fatalf("before the first answer: %s, want valid", got)
		}
		if err := store.SettleFence(ctx, FenceCheck{Reachable: true, Checked: true, Matches: true, AsOf: clock}); err != nil {
			t.Fatal(err)
		}
		if got := freshness(t, store); got != FreshnessValid {
			t.Fatalf("after a matching summary: %s, want valid", got)
		}
	})

	t.Run("an unreachable control plane at start does not stop the cell", func(t *testing.T) {
		store := restart(t)
		if err := store.SettleFence(ctx, FenceCheck{}); err != nil {
			t.Fatal(err)
		}
		if got := freshness(t, store); got != FreshnessValid {
			t.Fatalf("control plane unreachable, no restore: %s, want valid", got)
		}
		// The summary is still owed: when the control plane comes back on the
		// same connection, it is sent again, and a mismatch raises the fence.
		if !store.SummaryOwed() {
			t.Fatal("a summary nobody answered is no longer owed")
		}
		if err := store.SettleFence(ctx, FenceCheck{Reachable: true, Checked: true, Matches: false, AsOf: clock}); err != nil {
			t.Fatal(err)
		}
		if !store.DurablyFenced() || freshness(t, store) != FreshnessHardExpired || !store.SummaryOwed() {
			t.Fatal("a mismatch answered after the control plane came back did not fence the cell")
		}
		if err := store.SettleFence(ctx, FenceCheck{Reachable: true, Checked: true, Matches: true, AsOf: time.Now().Add(time.Minute), InventoryComplete: true}); err != nil {
			t.Fatal(err)
		}
		if store.SummaryOwed() {
			t.Fatal("a summary is still owed after a match lowered the fence")
		}
	})

	t.Run("a reachable but unchecked summary is still owed", func(t *testing.T) {
		store := restart(t)
		if err := store.SettleFence(ctx, FenceCheck{}); err != nil {
			t.Fatal(err)
		}
		if err := store.SettleFence(ctx, FenceCheck{Reachable: true}); err != nil {
			t.Fatal(err)
		}
		if !store.SummaryOwed() || store.DurablyFenced() {
			t.Fatal("unchecked summary stopped retries or raised a restore fence")
		}
		if freshness(t, store) != FreshnessValid {
			t.Fatal("unchecked response withheld valid authority without a restore marker")
		}
	})

	// The restore raised the durable fence.
	if err = foghorndb.New(conn).RaiseMediaAuthorityRestoreFence(ctx); err != nil {
		t.Fatal(err)
	}

	t.Run("a restored cell refuses what it holds while the control plane is unreachable", func(t *testing.T) {
		store := restart(t)
		if result, err := store.Apply(ctx, restored); err != nil || result.Confirmed {
			t.Fatalf("ordinary delivery confirmed restored authority: %+v, %v", result, err)
		}
		if err := store.SettleFence(ctx, FenceCheck{}); err != nil {
			t.Fatal(err)
		}
		if got := freshness(t, store); got != FreshnessHardExpired {
			t.Fatalf("restored, control plane unreachable: %s, want withheld", got)
		}
		store.SetAuthorityFetcher(func(context.Context, AuthorityLookup) ([][]byte, error) {
			return nil, errors.New("control plane unreachable")
		})
		// The pair still reads, withheld; the placement evaluator refuses a
		// withheld pair like an expired one.
		if pair, err := placement(store); err != nil || pair.Object.Freshness != FreshnessHardExpired || pair.Tenant.Freshness != FreshnessHardExpired {
			t.Fatalf("restored pair without the control plane: object=%s tenant=%s err=%v, want withheld", pair.Object.Freshness, pair.Tenant.Freshness, err)
		}
	})

	t.Run("a restored cell decides again once the control plane sends it", func(t *testing.T) {
		store := restart(t)
		// The control plane answers that the cell holds something else; the
		// fence stays up.
		if err := store.SettleFence(ctx, FenceCheck{Reachable: true, Checked: true, Matches: false, AsOf: clock}); err != nil {
			t.Fatal(err)
		}
		if !store.DurablyFenced() {
			t.Fatal("a mismatch lowered the fence")
		}
		// The placement read asks for the pair and gets a newer object version.
		current, _ := signedArtifactFixture(t, active, 3)
		store.SetAuthorityFetcher(func(context.Context, AuthorityLookup) ([][]byte, error) {
			return [][]byte{tenant, current}, nil
		})
		pair, err := placement(store)
		if err != nil || pair.Object.Version != 3 || pair.Object.Freshness != FreshnessValid || pair.Tenant.Freshness != FreshnessValid {
			t.Fatalf("after the control plane sent the pair: version=%d object=%s tenant=%s err=%v", pair.Object.Version, pair.Object.Freshness, pair.Tenant.Freshness, err)
		}
	})

	t.Run("the version the cell already holds is confirmed by being sent again", func(t *testing.T) {
		store := restart(t)
		current, _ := signedArtifactFixture(t, active, 3)
		store.SetAuthorityFetcher(func(context.Context, AuthorityLookup) ([][]byte, error) {
			return [][]byte{tenant, current}, nil
		})
		pair, err := placement(store)
		if err != nil || pair.Object.Version != 3 || pair.Object.Freshness != FreshnessValid {
			t.Fatalf("resending held versions: version=%d freshness=%s err=%v", pair.Object.Version, pair.Object.Freshness, err)
		}
	})

	t.Run("a matching summary lowers the fence for every replica", func(t *testing.T) {
		store, other := restart(t), restart(t)
		// The fence carries the database's clock; the summary is taken after it.
		if err := store.SettleFence(ctx, FenceCheck{Reachable: true, Checked: true, Matches: true, AsOf: time.Now().Add(time.Minute)}); err != nil {
			t.Fatal(err)
		}
		if store.DurablyFenced() || freshness(t, store) != FreshnessValid {
			t.Fatal("a matching summary left the fence up")
		}
		if err := other.SettleFence(ctx, FenceCheck{}); err != nil {
			t.Fatal(err)
		}
		if err := other.ReloadFence(ctx); err != nil {
			t.Fatal(err)
		}
		if other.DurablyFenced() || freshness(t, other) != FreshnessValid {
			t.Fatal("another replica kept the fence after it was lowered")
		}
	})

	t.Run("a summary taken before the fence was raised does not lower it", func(t *testing.T) {
		store := restart(t)
		if err := foghorndb.New(conn).RaiseMediaAuthorityRestoreFence(ctx); err != nil {
			t.Fatal(err)
		}
		if err := store.SettleFence(ctx, FenceCheck{Reachable: true, Checked: true, Matches: true, AsOf: time.Unix(1, 0)}); err != nil {
			t.Fatal(err)
		}
		if !store.DurablyFenced() {
			t.Fatal("a stale summary lowered a fence raised after it")
		}
	})

	t.Run("a second restore invalidates proof begun after the first", func(t *testing.T) {
		queries := foghorndb.New(conn)
		if err := queries.RaiseMediaAuthorityRestoreFence(ctx); err != nil {
			t.Fatal(err)
		}
		started, err := queries.BeginMediaAuthorityConfirmation(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if err := queries.RaiseMediaAuthorityRestoreFence(ctx); err != nil {
			t.Fatal(err)
		}
		store := restart(t)
		if err := store.SettleFence(ctx, FenceCheck{Checked: true, Matches: true, AsOf: started, InventoryComplete: true}); err != nil {
			t.Fatal(err)
		}
		if !store.DurablyFenced() {
			t.Fatal("proof begun before the second restore cleared its fence")
		}
	})
}

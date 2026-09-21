//go:build schema_verify

package mediaauthority

import (
	"context"
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/rand"
	"database/sql"
	"errors"
	"testing"
	"time"

	sharedauthority "github.com/Livepeer-FrameWorks/monorepo/pkg/mediaauthority"
	mediaauthoritypb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_authority"
	"google.golang.org/protobuf/proto"
)

// A cell holds the authorities in use. One nobody uses runs out and is
// forgotten; asking for it brings it back. The three rules that make forgetting
// safe are asserted against the real schema: nothing is forgotten while an older
// signed version could still verify, an old envelope cannot come back once the
// version fence is gone, and a tombstone is never forgotten.
func TestMediaAuthorityCollectionAndFetch_RealPG(t *testing.T) {
	conn := startMediaAuthorityRealPG(t)
	_, trust, _ := storeFixture(t, "cell-a")
	store, err := NewStore(conn, "cell-a", trust)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	ctx := context.Background()
	issued := storeFixtureNow.Add(time.Minute)
	longAfter := storeFixtureNow.Add(40 * 24 * time.Hour)
	clock := issued
	store.now = func() time.Time { return clock }

	active := mediaauthoritypb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_ACTIVE
	held := func() (version int64, projected bool) {
		t.Helper()
		scanErr := conn.QueryRowContext(ctx, `
			SELECT held.authority_version, projection.authority_id IS NOT NULL
			FROM foghorn.media_authorities AS held
			LEFT JOIN foghorn.media_object_authority_projection AS projection ON projection.authority_id = held.authority_id
			WHERE held.authority_kind = 'media_object'`).Scan(&version, &projected)
		if errors.Is(scanErr, sql.ErrNoRows) {
			return 0, false
		}
		if scanErr != nil {
			t.Fatal(scanErr)
		}
		return version, projected
	}

	second, _ := signedArtifactFixture(t, active, 2)
	if _, err = store.Apply(ctx, second); err != nil {
		t.Fatalf("apply version 2: %v", err)
	}
	summary, err := store.HeldSummary(ctx, issued)
	if err != nil || summary.Count != 1 {
		t.Fatalf("held summary = %+v (err %v), want one authority", summary, err)
	}

	// Past its validity, but issued recently enough that an older signed version
	// could still verify: forgetting it now would forget what refuses that one.
	clock = storeFixtureNow.Add(2 * time.Hour)
	if collected, collectErr := store.collectExpired(ctx); collectErr != nil || collected != 0 {
		t.Fatalf("forgot %d authorities two hours after issue (err %v)", collected, collectErr)
	}
	clock = longAfter
	if collected, collectErr := store.collectExpired(ctx); collectErr != nil || collected != 1 {
		t.Fatalf("forgot %d authorities forty days after issue (err %v), want 1", collected, collectErr)
	}
	if version, _ := held(); version != 0 {
		t.Fatalf("authority version %d still held after it was forgotten", version)
	}
	var projections int
	if err = conn.QueryRowContext(ctx, `SELECT count(*) FROM foghorn.media_object_authority_projection`).Scan(&projections); err != nil || projections != 0 {
		t.Fatalf("%d projections left behind (err %v)", projections, err)
	}

	// The version fence is gone, and the old envelope still cannot come back: it
	// is past its validity, which is why it was safe to forget.
	store.SetAuthorityFetcher(func(context.Context, AuthorityLookup) ([][]byte, error) { return [][]byte{second}, nil })
	if applied, fetchErr := store.Fetch(ctx, AuthorityLookup{PlaybackID: "artifact-playback"}); applied || fetchErr == nil {
		t.Fatalf("an expired envelope was applied after the authority was forgotten (applied=%v err=%v)", applied, fetchErr)
	}
	if version, _ := held(); version != 0 {
		t.Fatalf("expired envelope left version %d behind", version)
	}

	// Placement admission decides on the tenant and object pair alone. Reading a
	// pair whose object was forgotten asks for it, applies what comes back, and
	// decides on the reread, so the cell decides locally again.
	clock = issued
	third, _ := signedArtifactFixture(t, active, 3)
	refetch, err := NewStore(conn, "cell-a", trust)
	if err != nil {
		t.Fatal(err)
	}
	refetch.now = func() time.Time { return clock }
	tenant, _, _ := storeFixture(t, "cell-a")
	if _, err = refetch.Apply(ctx, tenant); err != nil {
		t.Fatalf("apply tenant: %v", err)
	}
	var asked []AuthorityLookup
	refetch.SetAuthorityFetcher(func(_ context.Context, lookup AuthorityLookup) ([][]byte, error) {
		asked = append(asked, lookup)
		return [][]byte{third}, nil
	})
	pair, err := refetch.PlacementForInternalName(ctx, "10000000-0000-0000-0000-000000000001", "artifact-internal")
	if err != nil || pair.Object.Version != 3 || len(asked) != 1 {
		t.Fatalf("placement read of a forgotten object: version=%d err=%v asked=%v, want version 3 after one fetch", pair.Object.Version, err, asked)
	}
	if version, projected := held(); version != 3 || !projected {
		t.Fatalf("after fetch: version=%d projected=%v, want 3 and a projection", version, projected)
	}
	if object, readErr := refetch.MediaObjectByPlaybackID(ctx, "artifact-playback"); readErr != nil || object.Version != 3 {
		t.Fatalf("local read after fetch: version=%d err=%v", object.Version, readErr)
	}

	// A tombstone is what refuses the object's return. It is never forgotten,
	// however long ago it was issued; the tenant beside it, long expired, is.
	tombstone, _ := signedArtifactFixture(t, mediaauthoritypb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_TOMBSTONE, 4)
	if _, err = refetch.Apply(ctx, tombstone); err != nil {
		t.Fatalf("apply tombstone: %v", err)
	}
	clock = longAfter
	if collected, collectErr := refetch.collectExpired(ctx); collectErr != nil || collected != 1 {
		t.Fatalf("forgot %d authorities (err %v), want only the expired tenant", collected, collectErr)
	}
	if version, _ := held(); version != 4 {
		t.Fatalf("tombstone gone: held version %d", version)
	}

	t.Run("ordinary offline restart retains the always-on desired source", func(t *testing.T) {
		clock = issued
		privateKey, err := ecdh.X25519().GenerateKey(rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		id := sharedauthority.LiveStreamAuthorityID("20000000-0000-0000-0000-000000000001")
		secret := &mediaauthoritypb.LiveStreamSecret{
			AuthorityId: id, TenantId: "10000000-0000-0000-0000-000000000001",
			NativeSourceSpec: "/media/marketing.mp4", NativeSourceKind: "file", NativeAlwaysOn: true,
			NativePlacementCount: 1, NativeAllowedClusterIds: []string{"cell-a"},
		}
		object := sealedObjectFixture(t, privateKey, id, secret, nil)
		object.UserId = "30000000-0000-0000-0000-000000000001"
		object.GetLiveStream().IngestMode = "mist_native"
		envelope, err := sharedauthority.NewEnvelope(mediaauthoritypb.AuthorityKind_AUTHORITY_KIND_MEDIA_OBJECT, id, 1,
			storeFixtureNow, storeFixtureNow.Add(time.Hour), storeFixtureNow.Add(24*time.Hour), "signer-1", "cell-a", object,
			[]*mediaauthoritypb.AuthoritySourceRevision{{Service: "commodore", Revision: "managed-offline"}})
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
		encoded, err := proto.Marshal(signed)
		if err != nil {
			t.Fatal(err)
		}
		for _, value := range [][]byte{tenant, encoded} {
			if _, err := refetch.Apply(ctx, value); err != nil {
				t.Fatal(err)
			}
		}
		// These persisted markers represent the completed source cutover, not
		// something an ordinary restart must ask the core to repeat.
		if _, err := conn.ExecContext(ctx, `UPDATE foghorn.tenant_authority_projection SET local_source_ready = TRUE;
			UPDATE foghorn.media_object_authority_projection SET local_source_ready = TRUE`); err != nil {
			t.Fatal(err)
		}
		restarted, err := NewStore(conn, "cell-a", trust)
		if err != nil {
			t.Fatal(err)
		}
		restarted.now = func() time.Time { return clock }
		if err := restarted.SetSealPrivateKey(sharedauthority.SealRecipientKeyID("cell-a", privateKey.PublicKey()), privateKey); err != nil {
			t.Fatal(err)
		}
		if err := restarted.RaiseStartupFence(ctx); err != nil {
			t.Fatal(err)
		}
		set, err := restarted.ManagedStreams(ctx, "cell-a")
		if err != nil || !set.Marked || !set.Complete || len(set.Rows) != 1 || !set.Rows[0].GetAlwaysOn() || set.Rows[0].GetSourceSpec() != secret.GetNativeSourceSpec() {
			t.Fatalf("offline restart lost managed source: %+v, %v", set, err)
		}
	})
}

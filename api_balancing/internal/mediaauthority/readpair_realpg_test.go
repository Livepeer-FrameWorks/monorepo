//go:build schema_verify

package mediaauthority

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	mediaauthoritypb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_authority"
	"google.golang.org/protobuf/proto"
)

// The one-statement read returns what the object read followed by the tenant read returns, in
// every state the pair can be in: both valid, the object withheld by a tenant revival, the tenant
// lapsed, and no object at all.
func TestMediaAuthorityReadPairMatchesSeparateReads_RealPG(t *testing.T) {
	conn := startMediaAuthorityRealPG(t)
	_, trust, _ := storeFixture(t, "cell-a")
	ctx := context.Background()
	store, err := NewStore(conn, "cell-a", trust)
	if err != nil {
		t.Fatal(err)
	}
	clock := storeFixtureNow.Add(time.Minute)
	store.now = func() time.Time { return clock }
	object, _ := signedArtifactFixture(t, mediaauthoritypb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_ACTIVE, 2)
	for _, envelope := range [][]byte{signedTenantAt(t, 7, storeFixtureNow, storeFixtureNow.Add(24*time.Hour)), object} {
		if _, err = store.Apply(ctx, envelope); err != nil {
			t.Fatal(err)
		}
	}

	compare := func(state string) {
		t.Helper()
		wantObject, wantObjectErr := store.MediaObjectByInternalName(ctx, "artifact-internal")
		pair, pairErr := store.ReadPairByInternalName(ctx, "artifact-internal")
		if (wantObjectErr == nil) != (pairErr == nil) {
			t.Fatalf("%s: object error %v, pair error %v", state, wantObjectErr, pairErr)
		}
		if wantObjectErr != nil {
			return
		}
		wantTenant, wantTenantErr := store.TenantForObject(ctx, wantObject)
		if (wantTenantErr == nil) != (pair.TenantErr == nil) {
			t.Fatalf("%s: tenant error %v, pair tenant error %v", state, wantTenantErr, pair.TenantErr)
		}
		gotObject, gotTenant := pair.Object, pair.Tenant
		if gotObject.AuthorityID != wantObject.AuthorityID || gotObject.Version != wantObject.Version ||
			gotObject.ParentVersion != wantObject.ParentVersion || gotObject.Ready != wantObject.Ready ||
			gotObject.Freshness != wantObject.Freshness || !gotObject.ValidUntil.Equal(wantObject.ValidUntil) ||
			!proto.Equal(gotObject.Authority, wantObject.Authority) {
			t.Fatalf("%s: pair object %+v, separate read %+v", state, gotObject, wantObject)
		}
		if gotTenant.Version != wantTenant.Version || gotTenant.Ready != wantTenant.Ready ||
			gotTenant.IngestReady != wantTenant.IngestReady || gotTenant.SourceReady != wantTenant.SourceReady ||
			gotTenant.Freshness != wantTenant.Freshness || !gotTenant.ValidUntil.Equal(wantTenant.ValidUntil) ||
			!proto.Equal(gotTenant.Authority, wantTenant.Authority) {
			t.Fatalf("%s: pair tenant %+v, separate read %+v", state, gotTenant, wantTenant)
		}
	}

	compare("both valid")

	if _, err = conn.ExecContext(ctx, `
		UPDATE foghorn.media_authorities
		SET issued_at = $1, refresh_after = $2, valid_until = $3
		WHERE authority_kind = 'tenant'`, storeFixtureNow.Add(-48*time.Hour), storeFixtureNow.Add(-36*time.Hour), storeFixtureNow.Add(-24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	compare("tenant lapsed")

	if _, err = store.Apply(ctx, signedTenantAt(t, 9, storeFixtureNow, storeFixtureNow.Add(24*time.Hour))); err != nil {
		t.Fatal(err)
	}
	compare("object withheld by tenant revival")

	if _, err = store.ReadPairByInternalName(ctx, "no-such-internal"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("absent object: %v, want sql.ErrNoRows", err)
	}
}

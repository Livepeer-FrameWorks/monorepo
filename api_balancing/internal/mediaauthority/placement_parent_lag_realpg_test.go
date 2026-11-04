//go:build schema_verify

package mediaauthority

import (
	"context"
	"crypto/ed25519"
	"testing"
	"time"

	sharedauthority "github.com/Livepeer-FrameWorks/monorepo/pkg/mediaauthority"
	mediaauthoritypb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_authority"
	placementpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_placement"
	"google.golang.org/protobuf/proto"
)

func signedPlacementFixture(t *testing.T, kind mediaauthoritypb.AuthorityKind, authorityID string, version uint64, payload proto.Message) []byte {
	t.Helper()
	envelope, err := sharedauthority.NewEnvelope(kind, authorityID, version, storeFixtureNow, storeFixtureNow.Add(10*time.Minute), storeFixtureNow.Add(time.Hour),
		"signer-1", "cell-a", payload, []*mediaauthoritypb.AuthoritySourceRevision{{Service: "commodore", Revision: "1"}})
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

func placementTenantAt(t *testing.T, version uint64, set *placementpb.PolicySet, reason string) []byte {
	t.Helper()
	_, _, fixture := storeFixture(t, "cell-a")
	tenant := &mediaauthoritypb.TenantAuthority{}
	if err := proto.Unmarshal(fixture.GetEnvelope().GetPayload(), tenant); err != nil {
		t.Fatal(err)
	}
	tenant.SchemaVersion, tenant.MediaPlacement, tenant.DecisionReason = sharedauthority.PlacementSchemaVersion, set, reason
	for _, grant := range tenant.GetEffectiveClusterGrants() {
		grant.MediaConsent = &placementpb.CapacityConsent{AllowIngest: true, AllowServe: true}
	}
	return signedPlacementFixture(t, mediaauthoritypb.AuthorityKind_AUTHORITY_KIND_TENANT, tenant.GetTenantId(), version, tenant)
}

func placementArtifactAt(t *testing.T, version, parent uint64) []byte {
	t.Helper()
	object := &mediaauthoritypb.MediaObjectAuthority{
		SchemaVersion: sharedauthority.PlacementSchemaVersion, ObjectKind: mediaauthoritypb.MediaObjectKind_MEDIA_OBJECT_KIND_ARTIFACT,
		TenantId: "10000000-0000-0000-0000-000000000001", InternalName: "artifact-internal", PlaybackId: "artifact-playback",
		Lifecycle:      mediaauthoritypb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_ACTIVE,
		PlaybackPolicy: &mediaauthoritypb.PlaybackPolicy{Kind: mediaauthoritypb.PlaybackPolicyKind_PLAYBACK_POLICY_KIND_PUBLIC},
		MediaPlacement: &placementpb.PolicySet{Revision: 1}, PlacementTenantRevision: parent,
		Object: &mediaauthoritypb.MediaObjectAuthority_Artifact{Artifact: &mediaauthoritypb.ArtifactAuthority{
			ArtifactId: "20000000-0000-0000-0000-000000000001", ArtifactHash: "artifact-hash",
			ArtifactKind: mediaauthoritypb.ArtifactKind_ARTIFACT_KIND_VOD,
		}},
	}
	return signedPlacementFixture(t, mediaauthoritypb.AuthorityKind_AUTHORITY_KIND_MEDIA_OBJECT, sharedauthority.ArtifactAuthorityID("20000000-0000-0000-0000-000000000001"), version, object)
}

// A tenant authority that raises the placement revision keeps the policy set
// it replaced next to it, a renewal at the same revision keeps that record, and
// a placement pair read hands it to the evaluator, so an object still compiled
// against the replaced revision is evaluated against it while its own
// replacement is on the way.
func TestMediaAuthorityTenantKeepsReplacedPlacementForLaggingObjects_RealPG(t *testing.T) {
	conn := startMediaAuthorityRealPG(t)
	_, trust, _ := storeFixture(t, "cell-a")
	ctx := context.Background()
	store, err := NewStore(conn, "cell-a", trust)
	if err != nil {
		t.Fatal(err)
	}
	clock := storeFixtureNow.Add(time.Minute)
	store.now = func() time.Time { return clock }
	tenantID, artifactID := "10000000-0000-0000-0000-000000000001", sharedauthority.ArtifactAuthorityID("20000000-0000-0000-0000-000000000001")
	replaced := &placementpb.PolicySet{Revision: 1}
	raised := &placementpb.PolicySet{Revision: 2, Serve: &placementpb.Rules{SchemaVersion: 1, Constraints: &placementpb.Constraints{
		Allow: &placementpb.SelectorSet{Any: []*placementpb.Selector{{ClusterIds: []string{"cell-a"}}}}}}}
	apply := func(envelope []byte) {
		t.Helper()
		if _, applyErr := store.Apply(ctx, envelope); applyErr != nil {
			t.Fatal(applyErr)
		}
	}
	read := func() PlacementPair {
		t.Helper()
		pair, readErr := store.Placement(ctx, tenantID, artifactID, "artifact-internal")
		if readErr != nil {
			t.Fatal(readErr)
		}
		return pair
	}

	apply(placementTenantAt(t, 7, replaced, ""))
	apply(placementArtifactAt(t, 2, 1))
	if pair := read(); pair.Tenant.PreviousPlacement != nil || !pair.Tenant.PlacementSupersededAt.IsZero() {
		t.Fatalf("first tenant authority recorded a previous placement: %v at %s", pair.Tenant.PreviousPlacement, pair.Tenant.PlacementSupersededAt)
	}

	apply(placementTenantAt(t, 8, raised, ""))
	lagging := read()
	if !proto.Equal(lagging.Tenant.PreviousPlacement, replaced) || lagging.Tenant.PlacementSupersededAt.IsZero() {
		t.Fatalf("raised revision kept %v at %s, want the replaced set %v", lagging.Tenant.PreviousPlacement, lagging.Tenant.PlacementSupersededAt, replaced)
	}
	supersededAt := lagging.Tenant.PlacementSupersededAt
	evaluated, until := lagging.PolicyTenant(supersededAt.Add(time.Second))
	if !proto.Equal(evaluated.GetMediaPlacement(), replaced) || !until.Equal(supersededAt.Add(PlacementParentLagWindow)) {
		t.Fatalf("lagging object evaluated against %v until %s, want %v until the lag window ends", evaluated.GetMediaPlacement(), until, replaced)
	}
	if !proto.Equal(lagging.Tenant.Authority.GetMediaPlacement(), raised) {
		t.Fatal("the held tenant authority lost its own placement revision")
	}
	if evaluated, _ = lagging.PolicyTenant(supersededAt.Add(PlacementParentLagWindow)); !proto.Equal(evaluated.GetMediaPlacement(), raised) {
		t.Fatalf("past the lag window the object is evaluated against %v, want the held revision", evaluated.GetMediaPlacement())
	}

	// A tenant change that leaves the placement revision alone keeps the record
	// and its time: the object is still catching up with the same change.
	apply(placementTenantAt(t, 9, raised, "renewed"))
	if renewed := read(); !proto.Equal(renewed.Tenant.PreviousPlacement, replaced) || !renewed.Tenant.PlacementSupersededAt.Equal(supersededAt) {
		t.Fatalf("same-revision tenant renewal kept %v at %s, want %v at %s", renewed.Tenant.PreviousPlacement, renewed.Tenant.PlacementSupersededAt, replaced, supersededAt)
	}

	// Once the object names the held revision the record no longer applies.
	apply(placementArtifactAt(t, 3, 2))
	caughtUp := read()
	if evaluated, until = caughtUp.PolicyTenant(supersededAt.Add(time.Second)); !proto.Equal(evaluated.GetMediaPlacement(), raised) || !until.IsZero() {
		t.Fatalf("caught-up object evaluated against %v until %s, want the held revision", evaluated.GetMediaPlacement(), until)
	}
}

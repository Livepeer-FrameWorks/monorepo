//go:build schema_verify

package mediaauthority

import (
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/rand"
	"testing"
	"time"

	sharedauthority "github.com/Livepeer-FrameWorks/monorepo/pkg/mediaauthority"
	mediapb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_authority"
	"google.golang.org/protobuf/proto"
)

func testManagedStreamRowFailureIsolation(t *testing.T) {
	for _, failure := range []string{"object digest", "tenant digest", "missing live payload", "retired seal key"} {
		t.Run(failure, func(t *testing.T) {
			db := startMediaAuthorityRealPG(t)
			_, trust, tenantEnvelope := storeFixture(t, "cell-a")
			store, err := NewStore(db, "cell-a", trust)
			if err != nil {
				t.Fatal(err)
			}
			store.now = func() time.Time { return storeFixtureNow }
			oldKey, err := ecdh.X25519().GenerateKey(rand.Reader)
			if err != nil {
				t.Fatal(err)
			}
			newKey, err := ecdh.X25519().GenerateKey(rand.Reader)
			if err != nil {
				t.Fatal(err)
			}
			if err = store.SetSealPrivateKey(sharedauthority.SealRecipientKeyID("cell-a", oldKey.PublicKey()), oldKey); err != nil {
				t.Fatal(err)
			}
			seed := make([]byte, ed25519.SeedSize)
			for i := range seed {
				seed[i] = byte(i)
			}
			apply := func(kind mediapb.AuthorityKind, id string, version uint64, payload proto.Message) {
				t.Helper()
				envelope, err := sharedauthority.NewEnvelope(kind, id, version, storeFixtureNow, storeFixtureNow.Add(time.Hour), storeFixtureNow.Add(24*time.Hour), "signer-1", "cell-a", payload, tenantEnvelope.GetEnvelope().GetSourceRevisions())
				if err != nil {
					t.Fatal(err)
				}
				signed, err := sharedauthority.Sign(envelope, ed25519.NewKeyFromSeed(seed))
				if err != nil {
					t.Fatal(err)
				}
				encoded, err := proto.Marshal(signed)
				if err != nil {
					t.Fatal(err)
				}
				if _, err = store.Apply(t.Context(), encoded); err != nil {
					t.Fatal(err)
				}
			}
			const badTenant = "10000000-0000-0000-0000-000000000002"
			const badStream = "20000000-0000-0000-0000-000000000001"
			const goodStream = "20000000-0000-0000-0000-000000000002"
			var badObject, goodObject *mediapb.MediaObjectAuthority
			for _, stream := range []string{badStream, goodStream} {
				tenant := &mediapb.TenantAuthority{}
				if err = proto.Unmarshal(tenantEnvelope.GetEnvelope().GetPayload(), tenant); err != nil {
					t.Fatal(err)
				}
				if stream == badStream {
					tenant.TenantId = badTenant
				}
				id := sharedauthority.LiveStreamAuthorityID(stream)
				secret := &mediapb.LiveStreamSecret{AuthorityId: id, TenantId: tenant.TenantId, NativeSourceSpec: "/media/" + stream + ".mp4", NativeSourceKind: "file", NativeAlwaysOn: true, NativePlacementCount: 1, NativeAllowedClusterIds: []string{"cell-a"}}
				object := sealedObjectFixture(t, oldKey, id, secret, nil)
				object.TenantId = tenant.TenantId
				object.UserId = "30000000-0000-0000-0000-000000000001"
				object.InternalName = "native-" + stream
				object.PlaybackId = "playback-" + stream
				object.GetLiveStream().StreamId = stream
				object.GetLiveStream().IngestMode = "mist_native"
				object.GetLiveStream().PublishingCredentialSha256 = nil
				if stream == goodStream {
					goodObject = object
				} else {
					badObject = object
				}
				apply(mediapb.AuthorityKind_AUTHORITY_KIND_TENANT, tenant.TenantId, 1, tenant)
				apply(mediapb.AuthorityKind_AUTHORITY_KIND_MEDIA_OBJECT, id, 1, object)
				if marked, err := store.MarkSourcePairLocalReady(t.Context(), tenant.TenantId, 1, id, 1); err != nil || !marked {
					t.Fatalf("source promotion=%v %v", marked, err)
				}
			}
			assertSet := func(wantComplete bool, wantRows int) {
				t.Helper()
				set, err := store.ManagedStreams(t.Context(), "cell-a")
				if err != nil || !set.Marked || set.Complete != wantComplete || len(set.Rows) != wantRows {
					t.Fatalf("one unreadable tenant blocked healthy source: complete=%v rows=%d marked=%v err=%v", set.Complete, len(set.Rows), set.Marked, err)
				}
				if wantRows == 1 && (set.Rows[0].StreamId != goodStream || set.Contexts[goodStream] == nil) {
					t.Fatalf("healthy tenant's exact source/context lost: %+v", set)
				}
			}
			assertSet(true, 2)
			switch failure {
			case "object digest":
				_, err = db.ExecContext(t.Context(), `UPDATE foghorn.media_authorities SET payload=payload || '\x00'::bytea WHERE authority_kind='media_object' AND authority_id=$1`, sharedauthority.LiveStreamAuthorityID(badStream))
			case "tenant digest":
				_, err = db.ExecContext(t.Context(), `UPDATE foghorn.media_authorities SET payload=payload || '\x00'::bytea WHERE authority_kind='tenant' AND authority_id=$1`, badTenant)
			case "missing live payload":
				// A damaged persisted payload still has its original source projection.
				badObject.Object = nil
				payload, encodeErr := proto.Marshal(badObject)
				if encodeErr != nil {
					t.Fatal(encodeErr)
				}
				_, err = db.ExecContext(t.Context(), `UPDATE foghorn.media_authorities SET payload=$2,payload_sha256=$3 WHERE authority_kind='media_object' AND authority_id=$1`, sharedauthority.LiveStreamAuthorityID(badStream), payload, testPayloadDigest(payload))
			case "retired seal key":
				// The healthy object has renewed to the new cell key; the other
				// tenant still holds an authority sealed to the retired key.
				id := sharedauthority.LiveStreamAuthorityID(goodStream)
				secret := &mediapb.LiveStreamSecret{AuthorityId: id, TenantId: goodObject.TenantId, NativeSourceSpec: "/media/" + goodStream + ".mp4", NativeSourceKind: "file", NativeAlwaysOn: true, NativePlacementCount: 1, NativeAllowedClusterIds: []string{"cell-a"}}
				rotated := sealedObjectFixture(t, newKey, id, secret, nil)
				goodObject.GetLiveStream().SealedCellSecrets = rotated.GetLiveStream().SealedCellSecrets
				apply(mediapb.AuthorityKind_AUTHORITY_KIND_MEDIA_OBJECT, id, 2, goodObject)
				err = store.SetSealPrivateKey(sharedauthority.SealRecipientKeyID("cell-a", newKey.PublicKey()), newKey)
				if marked, markErr := store.MarkSourcePairLocalReady(t.Context(), goodObject.TenantId, 1, id, 2); markErr != nil || !marked {
					t.Fatalf("renewed source promotion=%v %v", marked, markErr)
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			assertSet(false, 1)
		})
	}
}

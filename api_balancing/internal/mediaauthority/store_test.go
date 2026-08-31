package mediaauthority

import (
	"context"
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"regexp"
	"strings"
	"testing"
	"time"

	"frameworks/api_balancing/internal/database/foghorndb"
	"github.com/DATA-DOG/go-sqlmock"
	sharedauthority "github.com/Livepeer-FrameWorks/monorepo/pkg/mediaauthority"
	clusterpeerpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/cluster_peer"
	mediaauthoritypb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_authority"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

var storeFixtureNow = time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)

func testPayloadDigest(payload []byte) []byte {
	digest := sha256.Sum256(payload)
	return digest[:]
}

func storeFixture(t *testing.T, audience string) ([]byte, sharedauthority.TrustSet, *mediaauthoritypb.SignedAuthorityEnvelope) {
	t.Helper()
	seed := make([]byte, ed25519.SeedSize)
	for i := range seed {
		seed[i] = byte(i)
	}
	privateKey := ed25519.NewKeyFromSeed(seed)
	payload := &mediaauthoritypb.TenantAuthority{
		SchemaVersion:   sharedauthority.SchemaVersion,
		TenantId:        "10000000-0000-0000-0000-000000000001",
		Lifecycle:       mediaauthoritypb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_ACTIVE,
		BillingDecision: mediaauthoritypb.TenantBillingDecision_TENANT_BILLING_DECISION_ALLOW,
		BillingModel:    mediaauthoritypb.TenantBillingModel_TENANT_BILLING_MODEL_POSTPAID,
		EffectiveClusterGrants: []*mediaauthoritypb.TenantClusterGrant{{
			ClusterId:          audience,
			AccessSource:       clusterpeerpb.TenantClusterAccessSource_TENANT_CLUSTER_ACCESS_SOURCE_PLATFORM_TIER,
			SubscriptionStatus: "active",
			ClusterClass:       "platform_official",
			ControlCellId:      audience,
			EligibleServingCellIds: []string{
				audience,
			},
			ExpiresAt: timestamppb.New(storeFixtureNow.Add(48 * time.Hour)),
		}},
	}
	envelope, err := sharedauthority.NewEnvelope(
		mediaauthoritypb.AuthorityKind_AUTHORITY_KIND_TENANT,
		payload.GetTenantId(),
		7,
		storeFixtureNow,
		storeFixtureNow.Add(10*time.Minute),
		storeFixtureNow.Add(24*time.Hour),
		"signer-1",
		audience,
		payload,
		[]*mediaauthoritypb.AuthoritySourceRevision{{Service: "purser", Revision: "19"}},
	)
	if err != nil {
		t.Fatalf("NewEnvelope: %v", err)
	}
	signed, err := sharedauthority.Sign(envelope, privateKey)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	encoded, err := proto.MarshalOptions{Deterministic: true}.Marshal(signed)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	return encoded, sharedauthority.TrustSet{"signer-1": privateKey.Public().(ed25519.PublicKey)}, signed
}

func signedArtifactFixture(t *testing.T, lifecycle mediaauthoritypb.AuthorityLifecycle, version uint64) ([]byte, []byte) {
	t.Helper()
	seed := make([]byte, ed25519.SeedSize)
	for i := range seed {
		seed[i] = byte(i)
	}
	policy := mediaauthoritypb.PlaybackPolicyKind_PLAYBACK_POLICY_KIND_PUBLIC
	if lifecycle != mediaauthoritypb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_ACTIVE {
		policy = mediaauthoritypb.PlaybackPolicyKind_PLAYBACK_POLICY_KIND_DENY
	}
	payload := &mediaauthoritypb.MediaObjectAuthority{
		SchemaVersion: sharedauthority.SchemaVersion,
		ObjectKind:    mediaauthoritypb.MediaObjectKind_MEDIA_OBJECT_KIND_ARTIFACT,
		TenantId:      "10000000-0000-0000-0000-000000000001",
		InternalName:  "artifact-internal",
		PlaybackId:    "artifact-playback",
		Lifecycle:     lifecycle,
		PlaybackPolicy: &mediaauthoritypb.PlaybackPolicy{
			Kind: policy,
		},
		Object: &mediaauthoritypb.MediaObjectAuthority_Artifact{Artifact: &mediaauthoritypb.ArtifactAuthority{
			ArtifactId: "20000000-0000-0000-0000-000000000001", ArtifactHash: "artifact-hash",
			ArtifactKind: mediaauthoritypb.ArtifactKind_ARTIFACT_KIND_VOD,
		}},
	}
	envelope, err := sharedauthority.NewEnvelope(
		mediaauthoritypb.AuthorityKind_AUTHORITY_KIND_MEDIA_OBJECT,
		sharedauthority.ArtifactAuthorityID(payload.GetArtifact().GetArtifactId()), version,
		storeFixtureNow, storeFixtureNow.Add(10*time.Minute), storeFixtureNow.Add(time.Hour),
		"signer-1", "cell-a", payload,
		[]*mediaauthoritypb.AuthoritySourceRevision{{Service: "commodore", Revision: "1"}},
	)
	if err != nil {
		t.Fatalf("NewEnvelope: %v", err)
	}
	signed, err := sharedauthority.Sign(envelope, ed25519.NewKeyFromSeed(seed))
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	encoded, err := proto.MarshalOptions{Deterministic: true}.Marshal(signed)
	if err != nil {
		t.Fatalf("Marshal signed envelope: %v", err)
	}
	return encoded, envelope.GetPayload()
}

func sealedObjectFixture(t *testing.T, privateKey *ecdh.PrivateKey, authorityID string, liveSecret *mediaauthoritypb.LiveStreamSecret, webhook *mediaauthoritypb.MediaObjectSecret) *mediaauthoritypb.MediaObjectAuthority {
	t.Helper()
	sealKeyID := sharedauthority.SealRecipientKeyID("cell-a", privateKey.PublicKey())
	seal := func(message proto.Message) *mediaauthoritypb.SealedCellSecret {
		encoded, err := proto.MarshalOptions{Deterministic: true}.Marshal(message)
		if err != nil {
			t.Fatal(err)
		}
		box, err := sharedauthority.SealSecret("cell-a", authorityID, sharedauthority.SealRecipient{KeyID: sealKeyID, PublicKey: privateKey.PublicKey()}, encoded)
		if err != nil {
			t.Fatal(err)
		}
		return box
	}
	object := &mediaauthoritypb.MediaObjectAuthority{
		SchemaVersion: sharedauthority.SchemaVersion,
		ObjectKind:    mediaauthoritypb.MediaObjectKind_MEDIA_OBJECT_KIND_LIVE_STREAM,
		TenantId:      "10000000-0000-0000-0000-000000000001", UserId: "user-1",
		InternalName: "live-internal", PlaybackId: "playback-1",
		Lifecycle:       mediaauthoritypb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_ACTIVE,
		OriginClusterId: "cluster-a",
		PlaybackPolicy:  &mediaauthoritypb.PlaybackPolicy{Kind: mediaauthoritypb.PlaybackPolicyKind_PLAYBACK_POLICY_KIND_PUBLIC},
		Object: &mediaauthoritypb.MediaObjectAuthority_LiveStream{LiveStream: &mediaauthoritypb.LiveStreamAuthority{
			StreamId: "20000000-0000-0000-0000-000000000001", IngestMode: "pull",
			PublishingCredentialSha256: make([]byte, 32), OutageIngestClusterId: "cluster-a",
			ProcessesJson: "{}", DvrProcessesJson: "{}",
		}},
	}
	if liveSecret != nil {
		object.GetLiveStream().SealedCellSecrets = []*mediaauthoritypb.SealedCellSecret{seal(liveSecret)}
	}
	if webhook != nil {
		object.PlaybackPolicy = &mediaauthoritypb.PlaybackPolicy{Kind: mediaauthoritypb.PlaybackPolicyKind_PLAYBACK_POLICY_KIND_WEBHOOK}
		object.SealedPlaybackSecrets = []*mediaauthoritypb.SealedCellSecret{seal(webhook)}
	}
	return object
}

func TestReadinessPreservationKeepsOneTimeSchemaCutoverAcrossVerifiedVersions(t *testing.T) {
	privateKey, keyErr := ecdh.X25519().GenerateKey(rand.Reader)
	if keyErr != nil {
		t.Fatal(keyErr)
	}
	store, _, closeDB := newFixtureStore(t, "cell-a")
	defer closeDB()
	if sealErr := store.SetSealPrivateKey(sharedauthority.SealRecipientKeyID("cell-a", privateKey.PublicKey()), privateKey); sealErr != nil {
		t.Fatal(sealErr)
	}
	authorityID := sharedauthority.LiveStreamAuthorityID("20000000-0000-0000-0000-000000000001")
	secret := &mediaauthoritypb.LiveStreamSecret{
		AuthorityId: authorityID, TenantId: "10000000-0000-0000-0000-000000000001",
		SourceUri: "srt://source", SourceEnabled: true, AllowedClusterIds: []string{"cluster-a"},
		PushTargets: []*mediaauthoritypb.PushTargetSecret{{TargetId: "target-1", TargetUri: "rtmp://output", Platform: "custom"}},
	}
	previous := sealedObjectFixture(t, privateKey, authorityID, secret, nil)
	next := sealedObjectFixture(t, privateKey, authorityID, proto.Clone(secret).(*mediaauthoritypb.LiveStreamSecret), nil)
	if proto.Equal(previous.GetLiveStream().GetSealedCellSecrets()[0], next.GetLiveStream().GetSealedCellSecrets()[0]) {
		t.Fatal("fixture unexpectedly produced identical randomized ciphertext")
	}
	previousBytes, err := proto.MarshalOptions{Deterministic: true}.Marshal(previous)
	if err != nil {
		t.Fatal(err)
	}
	preserve := store.readinessPreservation(foghorndb.GetMediaAuthorityForUpdateRow{Payload: previousBytes, PayloadSha256: []byte{1}}, nil, &sharedauthority.Verified{
		Envelope: &mediaauthoritypb.AuthorityEnvelope{AuthorityId: authorityID, PayloadSha256: []byte{2}}, MediaObject: next,
	})
	if !preserve.read || !preserve.ingest || !preserve.source {
		t.Fatalf("equivalent re-encryption cleared readiness: %+v", preserve)
	}
	changed := proto.Clone(secret).(*mediaauthoritypb.LiveStreamSecret)
	changed.PushTargets[0].TargetUri = "rtmp://changed"
	nextChanged := sealedObjectFixture(t, privateKey, authorityID, changed, nil)
	preserve = store.readinessPreservation(foghorndb.GetMediaAuthorityForUpdateRow{Payload: previousBytes, PayloadSha256: []byte{1}}, nil, &sharedauthority.Verified{
		Envelope: &mediaauthoritypb.AuthorityEnvelope{AuthorityId: authorityID, PayloadSha256: []byte{2}}, MediaObject: nextChanged,
	})
	if !preserve.read || !preserve.ingest || !preserve.source {
		t.Fatalf("verified policy change cleared schema readiness: %+v", preserve)
	}
}

func TestSetSealPrivateKeyRejectsAnotherControlCellsBinding(t *testing.T) {
	privateKey, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	store, _, closeDB := newFixtureStore(t, "cell-a")
	defer closeDB()
	if err := store.SetSealPrivateKey(sharedauthority.SealRecipientKeyID("cell-b", privateKey.PublicKey()), privateKey); err == nil {
		t.Fatal("seal key bound to another control cell was accepted")
	}
	if err := store.SetSealPrivateKey(sharedauthority.SealRecipientKeyID("cell-a", privateKey.PublicKey()), privateKey); err != nil {
		t.Fatalf("matching control-cell seal key rejected: %v", err)
	}
}

func TestReadinessPreservationKeepsCutoverAcrossVerifiedSecretRotation(t *testing.T) {
	privateKey, keyErr := ecdh.X25519().GenerateKey(rand.Reader)
	if keyErr != nil {
		t.Fatal(keyErr)
	}
	store, _, closeDB := newFixtureStore(t, "cell-a")
	defer closeDB()
	if sealErr := store.SetSealPrivateKey(sharedauthority.SealRecipientKeyID("cell-a", privateKey.PublicKey()), privateKey); sealErr != nil {
		t.Fatal(sealErr)
	}
	authorityID := sharedauthority.LiveStreamAuthorityID("20000000-0000-0000-0000-000000000001")
	webhook := &mediaauthoritypb.MediaObjectSecret{AuthorityId: authorityID, TenantId: "10000000-0000-0000-0000-000000000001", PlaybackWebhook: &mediaauthoritypb.PlaybackWebhookSecret{Url: "https://example.test/auth", Secret: "secret", TimeoutMs: 1500}}
	previous := sealedObjectFixture(t, privateKey, authorityID, nil, webhook)
	next := sealedObjectFixture(t, privateKey, authorityID, nil, proto.Clone(webhook).(*mediaauthoritypb.MediaObjectSecret))
	previousBytes, err := proto.MarshalOptions{Deterministic: true}.Marshal(previous)
	if err != nil {
		t.Fatal(err)
	}
	preserve := store.readinessPreservation(foghorndb.GetMediaAuthorityForUpdateRow{Payload: previousBytes, PayloadSha256: []byte{1}}, nil, &sharedauthority.Verified{Envelope: &mediaauthoritypb.AuthorityEnvelope{AuthorityId: authorityID, PayloadSha256: []byte{2}}, MediaObject: next})
	if !preserve.read {
		t.Fatalf("equivalent webhook re-encryption cleared read readiness: %+v", preserve)
	}
	changed := proto.Clone(webhook).(*mediaauthoritypb.MediaObjectSecret)
	changed.PlaybackWebhook.Secret = "rotated"
	nextChanged := sealedObjectFixture(t, privateKey, authorityID, nil, changed)
	preserve = store.readinessPreservation(foghorndb.GetMediaAuthorityForUpdateRow{Payload: previousBytes, PayloadSha256: []byte{1}}, nil, &sharedauthority.Verified{Envelope: &mediaauthoritypb.AuthorityEnvelope{AuthorityId: authorityID, PayloadSha256: []byte{2}}, MediaObject: nextChanged})
	if !preserve.read || !preserve.ingest || !preserve.source {
		t.Fatalf("verified webhook rotation cleared schema readiness: %+v", preserve)
	}
}

func TestReadinessPreservationKeepsCutoverAcrossVerifiedObjectChanges(t *testing.T) {
	store, _, closeDB := newFixtureStore(t, "cell-a")
	defer closeDB()
	previous := &mediaauthoritypb.MediaObjectAuthority{
		SchemaVersion: sharedauthority.SchemaVersion, ObjectKind: mediaauthoritypb.MediaObjectKind_MEDIA_OBJECT_KIND_ARTIFACT,
		TenantId: "10000000-0000-0000-0000-000000000001", UserId: "user-1", InternalName: "artifact-internal", PlaybackId: "playback-1",
		Lifecycle: mediaauthoritypb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_ACTIVE, OriginClusterId: "cluster-a",
		PlaybackPolicy: &mediaauthoritypb.PlaybackPolicy{Kind: mediaauthoritypb.PlaybackPolicyKind_PLAYBACK_POLICY_KIND_PUBLIC},
		Object: &mediaauthoritypb.MediaObjectAuthority_Artifact{Artifact: &mediaauthoritypb.ArtifactAuthority{
			ArtifactId: "artifact-1", ArtifactHash: "hash-1", ArtifactKind: mediaauthoritypb.ArtifactKind_ARTIFACT_KIND_VOD,
		}},
	}
	previousBytes, err := proto.MarshalOptions{Deterministic: true}.Marshal(previous)
	if err != nil {
		t.Fatal(err)
	}
	next := proto.Clone(previous).(*mediaauthoritypb.MediaObjectAuthority)
	next.GetArtifact().ArtifactHash = "hash-2"
	preserve := store.readinessPreservation(foghorndb.GetMediaAuthorityForUpdateRow{Payload: previousBytes, PayloadSha256: []byte{1}}, nil, &sharedauthority.Verified{
		Envelope: &mediaauthoritypb.AuthorityEnvelope{AuthorityId: "artifact:artifact-1", PayloadSha256: []byte{2}}, MediaObject: next,
	})
	if !preserve.read || !preserve.source || !preserve.ingest {
		t.Fatalf("verified artifact change cleared schema readiness: %+v", preserve)
	}

	privateKey, keyErr := ecdh.X25519().GenerateKey(rand.Reader)
	if keyErr != nil {
		t.Fatal(keyErr)
	}
	if sealErr := store.SetSealPrivateKey(sharedauthority.SealRecipientKeyID("cell-a", privateKey.PublicKey()), privateKey); sealErr != nil {
		t.Fatal(sealErr)
	}
	liveID := sharedauthority.LiveStreamAuthorityID("20000000-0000-0000-0000-000000000001")
	live := sealedObjectFixture(t, privateKey, liveID, nil, nil)
	liveBytes, err := proto.MarshalOptions{Deterministic: true}.Marshal(live)
	if err != nil {
		t.Fatal(err)
	}
	changedProcess := proto.Clone(live).(*mediaauthoritypb.MediaObjectAuthority)
	changedProcess.GetLiveStream().ProcessesJson = `{"proc":"changed"}`
	preserve = store.readinessPreservation(foghorndb.GetMediaAuthorityForUpdateRow{Payload: liveBytes, PayloadSha256: []byte{1}}, nil, &sharedauthority.Verified{
		Envelope: &mediaauthoritypb.AuthorityEnvelope{AuthorityId: liveID, PayloadSha256: []byte{2}}, MediaObject: changedProcess,
	})
	if !preserve.read || !preserve.ingest || !preserve.source {
		t.Fatalf("verified process-policy change cleared schema readiness: %+v", preserve)
	}
}

func newFixtureStore(t *testing.T, cellID string) (*Store, sqlmock.Sqlmock, func()) {
	t.Helper()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	_, trust, _ := storeFixture(t, "cell-a")
	store, err := NewStore(db, cellID, trust)
	if err != nil {
		db.Close()
		t.Fatalf("NewStore: %v", err)
	}
	store.now = func() time.Time { return storeFixtureNow.Add(time.Minute) }
	return store, mock, func() { _ = db.Close() }
}

func expectLockAndNoCurrent(mock sqlmock.Sqlmock) {
	mock.ExpectBegin()
	expectMediaAuthorityLockTimeout(mock)
	mock.ExpectExec(regexp.QuoteMeta("SELECT pg_advisory_xact_lock(")).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT authority_version, payload_sha256")).WillReturnError(sqlmock.ErrCancelled)
}

func expectApplyPrefix(mock sqlmock.Sqlmock) {
	mock.ExpectBegin()
	expectMediaAuthorityLockTimeout(mock)
	mock.ExpectExec(regexp.QuoteMeta("SELECT pg_advisory_xact_lock(")).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT authority_version, payload_sha256, payload")).WillReturnRows(sqlmock.NewRows([]string{"authority_version", "payload_sha256", "payload"}))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO foghorn.media_authorities")).WillReturnResult(sqlmock.NewResult(1, 1))
}

func expectMediaAuthorityLockTimeout(mock sqlmock.Sqlmock) {
	mock.ExpectExec(regexp.QuoteMeta("SELECT set_config('lock_timeout'")).
		WithArgs(mediaAuthorityLockTimeout.String()).
		WillReturnResult(sqlmock.NewResult(0, 1))
}

func TestStoreApplyPersistsTenantAtomically(t *testing.T) {
	store, mock, closeDB := newFixtureStore(t, "cell-a")
	defer closeDB()
	encoded, _, _ := storeFixture(t, "cell-a")

	expectApplyPrefix(mock)
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO foghorn.tenant_authority_projection")).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec(regexp.QuoteMeta("DELETE FROM foghorn.tenant_authority_grants")).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO foghorn.tenant_authority_grants")).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO foghorn.media_authority_apply_audit")).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	result, err := store.Apply(context.Background(), encoded)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if result.Status != ApplyStatusApplied || result.Version != 7 || result.ID == "" {
		t.Fatalf("result = %+v", result)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func TestStoreApplyFailsBeforeLockWhenLockTimeoutCannotBeSet(t *testing.T) {
	store, mock, closeDB := newFixtureStore(t, "cell-a")
	defer closeDB()
	encoded, _, _ := storeFixture(t, "cell-a")
	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta("SELECT set_config('lock_timeout'")).
		WithArgs(mediaAuthorityLockTimeout.String()).
		WillReturnError(errors.New("set_config denied"))
	mock.ExpectRollback()

	if _, err := store.Apply(context.Background(), encoded); err == nil || !strings.Contains(err.Error(), "bound media authority lock wait") {
		t.Fatalf("Apply error = %v, want lock-timeout setup failure", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestStoreApplyIsIdempotentAndRejectsRollbackAndConflict(t *testing.T) {
	encoded, _, signed := storeFixture(t, "cell-a")
	tests := []struct {
		name       string
		version    int64
		digest     []byte
		wantStatus ApplyStatus
		wantErr    error
		outcome    string
	}{
		{name: "duplicate", version: 7, digest: signed.GetEnvelope().GetPayloadSha256(), wantStatus: ApplyStatusDuplicate, outcome: "duplicate"},
		{name: "rollback", version: 8, digest: signed.GetEnvelope().GetPayloadSha256(), wantErr: ErrRollback, outcome: "rollback_rejected"},
		{name: "same version conflict", version: 7, digest: make([]byte, 32), wantErr: ErrVersionConflict, outcome: "conflict_rejected"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store, mock, closeDB := newFixtureStore(t, "cell-a")
			defer closeDB()
			mock.ExpectBegin()
			expectMediaAuthorityLockTimeout(mock)
			mock.ExpectExec(regexp.QuoteMeta("SELECT pg_advisory_xact_lock(")).WillReturnResult(sqlmock.NewResult(0, 1))
			mock.ExpectQuery(regexp.QuoteMeta("SELECT authority_version, payload_sha256, payload")).WillReturnRows(
				sqlmock.NewRows([]string{"authority_version", "payload_sha256", "payload"}).AddRow(test.version, test.digest, signed.GetEnvelope().GetPayload()),
			)
			mock.ExpectExec(regexp.QuoteMeta("INSERT INTO foghorn.media_authority_apply_audit")).
				WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), test.outcome, sqlmock.AnyArg()).
				WillReturnResult(sqlmock.NewResult(1, 1))
			mock.ExpectCommit()

			result, err := store.Apply(context.Background(), encoded)
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("error = %v, want %v", err, test.wantErr)
			}
			if result.Status != test.wantStatus {
				t.Fatalf("status = %q, want %q", result.Status, test.wantStatus)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("expectations: %v", err)
			}
		})
	}
}

func TestStoreApplyRejectsNewerActiveObjectAfterTombstone(t *testing.T) {
	store, mock, closeDB := newFixtureStore(t, "cell-a")
	defer closeDB()
	_, tombstonePayload := signedArtifactFixture(t, mediaauthoritypb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_TOMBSTONE, 1)
	activeEncoded, _ := signedArtifactFixture(t, mediaauthoritypb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_ACTIVE, 2)

	mock.ExpectBegin()
	expectMediaAuthorityLockTimeout(mock)
	mock.ExpectExec(regexp.QuoteMeta("SELECT pg_advisory_xact_lock(")).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta("SELECT pg_advisory_xact_lock(")).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT authority_version, payload_sha256, payload")).WillReturnRows(
		sqlmock.NewRows([]string{"authority_version", "payload_sha256", "payload"}).AddRow(int64(1), testPayloadDigest(tombstonePayload), tombstonePayload),
	)
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO foghorn.media_authority_apply_audit")).
		WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), "terminal_lifecycle_rejected", ErrTombstoneTerminal.Error()).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	result, err := store.Apply(context.Background(), activeEncoded)
	if !errors.Is(err, ErrTombstoneTerminal) {
		t.Fatalf("Apply error = %v, want ErrTombstoneTerminal", err)
	}
	if result.Status != "" || result.Version != 2 {
		t.Fatalf("result = %+v", result)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func TestStoreApplyRejectsNewerObjectWhenCurrentLifecycleIsCorrupt(t *testing.T) {
	store, mock, closeDB := newFixtureStore(t, "cell-a")
	defer closeDB()
	activeEncoded, _ := signedArtifactFixture(t, mediaauthoritypb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_ACTIVE, 2)

	mock.ExpectBegin()
	expectMediaAuthorityLockTimeout(mock)
	mock.ExpectExec(regexp.QuoteMeta("SELECT pg_advisory_xact_lock(")).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta("SELECT pg_advisory_xact_lock(")).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT authority_version, payload_sha256, payload")).WillReturnRows(
		sqlmock.NewRows([]string{"authority_version", "payload_sha256", "payload"}).AddRow(int64(1), make([]byte, sha256.Size), []byte{0xff}),
	)
	mock.ExpectRollback()

	_, err := store.Apply(context.Background(), activeEncoded)
	if err == nil || !strings.Contains(err.Error(), "decode current media object authority lifecycle") {
		t.Fatalf("Apply error = %v, want corrupt current-lifecycle rejection", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func TestStoreRejectsUnservedAudienceBeforeMutation(t *testing.T) {
	store, mock, closeDB := newFixtureStore(t, "cell-b")
	defer closeDB()
	encoded, _, _ := storeFixture(t, "cell-a")
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO foghorn.media_authority_apply_audit")).WillReturnResult(sqlmock.NewResult(1, 1))

	_, err := store.Apply(context.Background(), encoded)
	if !errors.Is(err, sharedauthority.ErrWrongAudience) {
		t.Fatalf("error = %v, want ErrWrongAudience", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func TestStoreRollsBackEnvelopeWhenProjectionFails(t *testing.T) {
	store, mock, closeDB := newFixtureStore(t, "cell-a")
	defer closeDB()
	encoded, _, _ := storeFixture(t, "cell-a")

	expectApplyPrefix(mock)
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO foghorn.tenant_authority_projection")).WillReturnError(errors.New("projection unavailable"))
	mock.ExpectRollback()

	_, err := store.Apply(context.Background(), encoded)
	if err == nil {
		t.Fatal("Apply succeeded despite projection failure")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func TestStoreReadsDurableTenantAuthorityWithFreshness(t *testing.T) {
	store, mock, closeDB := newFixtureStore(t, "cell-a")
	defer closeDB()
	_, _, signed := storeFixture(t, "cell-a")
	payload := signed.GetEnvelope().GetPayload()
	tenantID := "10000000-0000-0000-0000-000000000001"
	mock.ExpectQuery(regexp.QuoteMeta("SELECT authority.payload, authority.payload_sha256, authority.refresh_after, authority.valid_until,")).
		WithArgs(tenantID).
		WillReturnRows(sqlmock.NewRows([]string{"payload", "payload_sha256", "refresh_after", "valid_until", "authority_version", "local_read_ready", "local_ingest_ready", "local_source_ready"}).
			AddRow(payload, testPayloadDigest(payload), storeFixtureNow.Add(30*time.Second), storeFixtureNow.Add(time.Hour), int64(7), true, false, true))

	snapshot, err := store.Tenant(context.Background(), tenantID)
	if err != nil {
		t.Fatalf("Tenant: %v", err)
	}
	if !snapshot.Ready || !snapshot.SourceReady || snapshot.Freshness != FreshnessSoftExpired || snapshot.Authority.GetTenantId() != tenantID {
		t.Fatalf("snapshot = %+v", snapshot)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func TestStoreReadsMediaObjectCaseInsensitiveAndMarksExactVersion(t *testing.T) {
	store, mock, closeDB := newFixtureStore(t, "cell-a")
	defer closeDB()
	payload := &mediaauthoritypb.MediaObjectAuthority{
		SchemaVersion:  sharedauthority.SchemaVersion,
		ObjectKind:     mediaauthoritypb.MediaObjectKind_MEDIA_OBJECT_KIND_ARTIFACT,
		TenantId:       "10000000-0000-0000-0000-000000000001",
		InternalName:   "asset-internal",
		PlaybackId:     "AbCd1234",
		Lifecycle:      mediaauthoritypb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_ACTIVE,
		PlaybackPolicy: &mediaauthoritypb.PlaybackPolicy{Kind: mediaauthoritypb.PlaybackPolicyKind_PLAYBACK_POLICY_KIND_PUBLIC},
		Object: &mediaauthoritypb.MediaObjectAuthority_Artifact{Artifact: &mediaauthoritypb.ArtifactAuthority{
			ArtifactId: "20000000-0000-0000-0000-000000000001", ArtifactHash: "hash-1",
			ArtifactKind: mediaauthoritypb.ArtifactKind_ARTIFACT_KIND_VOD,
		}},
	}
	encoded, err := proto.MarshalOptions{Deterministic: true}.Marshal(payload)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	authorityID := sharedauthority.ArtifactAuthorityID(payload.GetArtifact().GetArtifactId())
	mock.ExpectQuery(regexp.QuoteMeta("SELECT authority.payload, authority.payload_sha256, authority.refresh_after, authority.valid_until,")).
		WithArgs("abcd1234").
		WillReturnRows(sqlmock.NewRows([]string{"payload", "payload_sha256", "refresh_after", "valid_until", "authority_id", "authority_version", "local_read_ready"}).
			AddRow(encoded, testPayloadDigest(encoded), storeFixtureNow.Add(10*time.Minute), storeFixtureNow.Add(time.Hour), authorityID, int64(9), false))

	snapshot, err := store.MediaObjectByPlaybackID(context.Background(), "abcd1234")
	if err != nil {
		t.Fatalf("MediaObjectByPlaybackID: %v", err)
	}
	if snapshot.Ready || snapshot.Freshness != FreshnessValid || snapshot.Authority.GetPlaybackId() != "AbCd1234" {
		t.Fatalf("snapshot = %+v", snapshot)
	}
	mock.ExpectExec(regexp.QuoteMeta("UPDATE foghorn.media_object_authority_projection")).
		WithArgs(authorityID, int64(9)).WillReturnResult(sqlmock.NewResult(0, 1))
	marked, err := store.MarkMediaObjectLocalReadReady(context.Background(), authorityID, 9)
	if err != nil || !marked {
		t.Fatalf("MarkMediaObjectLocalReadReady = %v, %v", marked, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func TestApplyProjectionTombstonesFederatedPointerAndSettlesCatalogRevision(t *testing.T) {
	testDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer testDB.Close()
	tenantID := "10000000-0000-0000-0000-000000000001"
	artifactHash := "federated-hash"
	verified := &sharedauthority.Verified{
		Envelope: &mediaauthoritypb.AuthorityEnvelope{
			AuthorityId: "artifact-authority", AuthorityVersion: 11,
			ValidUntil: timestamppb.New(storeFixtureNow.Add(time.Hour)),
		},
		MediaObject: &mediaauthoritypb.MediaObjectAuthority{
			ObjectKind: mediaauthoritypb.MediaObjectKind_MEDIA_OBJECT_KIND_ARTIFACT,
			TenantId:   tenantID, Lifecycle: mediaauthoritypb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_TOMBSTONE,
			PlaybackPolicy: &mediaauthoritypb.PlaybackPolicy{Kind: mediaauthoritypb.PlaybackPolicyKind_PLAYBACK_POLICY_KIND_DENY},
			Object: &mediaauthoritypb.MediaObjectAuthority_Artifact{Artifact: &mediaauthoritypb.ArtifactAuthority{
				ArtifactId: "artifact-id", ArtifactHash: artifactHash,
				ArtifactKind: mediaauthoritypb.ArtifactKind_ARTIFACT_KIND_CLIP,
			}},
		},
	}
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO foghorn.media_object_authority_projection")).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta("UPDATE foghorn.artifacts\nSET status = 'deleted', federated_purge_eligible_at = NOW(), updated_at = NOW()")).
		WithArgs(artifactHash, tenantID).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta("UPDATE foghorn.artifacts\nSET catalog_synced_rev = catalog_revision")).
		WithArgs(artifactHash, tenantID).WillReturnResult(sqlmock.NewResult(0, 1))

	if err := applyProjection(context.Background(), foghorndb.New(testDB), verified, readinessPreservation{}); err != nil {
		t.Fatalf("applyProjection: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

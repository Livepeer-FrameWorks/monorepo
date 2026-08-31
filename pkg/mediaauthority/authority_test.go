package mediaauthority

import (
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	clusterpeerpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/cluster_peer"
	mediaauthoritypb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_authority"
	meteringpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/metering_contract"
	tenantlimitspb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/tenant_limits"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

var fixtureNow = time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)

func fixtureKeys() (ed25519.PublicKey, ed25519.PrivateKey) {
	seed := make([]byte, ed25519.SeedSize)
	for i := range seed {
		seed[i] = byte(i)
	}
	privateKey := ed25519.NewKeyFromSeed(seed)
	return privateKey.Public().(ed25519.PublicKey), privateKey
}

func fixtureES256PublicKey(t *testing.T) string {
	t.Helper()
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	encoded, err := x509.MarshalPKIXPublicKey(privateKey.Public())
	if err != nil {
		t.Fatalf("MarshalPKIXPublicKey: %v", err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: encoded}))
}

func fixtureTenant() *mediaauthoritypb.TenantAuthority {
	return &mediaauthoritypb.TenantAuthority{
		SchemaVersion:               SchemaVersion,
		TenantId:                    "tenant-1",
		Lifecycle:                   mediaauthoritypb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_ACTIVE,
		BillingDecision:             mediaauthoritypb.TenantBillingDecision_TENANT_BILLING_DECISION_ALLOW,
		BillingModel:                mediaauthoritypb.TenantBillingModel_TENANT_BILLING_MODEL_POSTPAID,
		OfficialClusterId:           "cell-a",
		AllowPlatformSharedPlayback: true,
		EffectiveClusterGrants: []*mediaauthoritypb.TenantClusterGrant{
			{
				ClusterId:          "cell-a",
				AccessSource:       clusterpeerpb.TenantClusterAccessSource_TENANT_CLUSTER_ACCESS_SOURCE_PLATFORM_TIER,
				AccessLevel:        "shared",
				SubscriptionStatus: "active",
				ClusterClass:       "platform_official",
				DeploymentModel:    "shared",
				ControlCellId:      "cell-a",
				EligibleServingCellIds: []string{
					"cell-a",
				},
				ExpiresAt: timestamppb.New(fixtureNow.Add(48 * time.Hour)),
			},
		},
		ResourceLimits: &tenantlimitspb.TenantResourceLimits{MaxStreams: 4, MaxViewers: 1000},
		Allowances: []*meteringpb.MeterAllowance{
			{Meter: "delivered_minutes", Included: 1000, Used: 25, Remaining: 975},
		},
	}
}

func fixtureEnvelope(t *testing.T, payload proto.Message) *mediaauthoritypb.AuthorityEnvelope {
	t.Helper()
	kind := mediaauthoritypb.AuthorityKind_AUTHORITY_KIND_TENANT
	authorityID := "tenant-1"
	if object, ok := payload.(*mediaauthoritypb.MediaObjectAuthority); ok {
		kind = mediaauthoritypb.AuthorityKind_AUTHORITY_KIND_MEDIA_OBJECT
		switch object.GetObjectKind() {
		case mediaauthoritypb.MediaObjectKind_MEDIA_OBJECT_KIND_LIVE_STREAM:
			authorityID = LiveStreamAuthorityID(object.GetLiveStream().GetStreamId())
		case mediaauthoritypb.MediaObjectKind_MEDIA_OBJECT_KIND_ARTIFACT:
			authorityID = ArtifactAuthorityID(object.GetArtifact().GetArtifactId())
		}
	}
	envelope, err := NewEnvelope(
		kind,
		authorityID,
		7,
		fixtureNow,
		fixtureNow.Add(10*time.Minute),
		fixtureNow.Add(24*time.Hour),
		"key-2026-08",
		"cell-a",
		payload,
		[]*mediaauthoritypb.AuthoritySourceRevision{
			{Service: "commodore", Revision: "stream:42"},
			{Service: "purser", Revision: "billing:19"},
			{Service: "quartermaster", Revision: "tenant:31"},
		},
	)
	if err != nil {
		t.Fatalf("NewEnvelope: %v", err)
	}
	return envelope
}

func fixtureSigned(t *testing.T, payload proto.Message) (*mediaauthoritypb.SignedAuthorityEnvelope, TrustSet) {
	t.Helper()
	publicKey, privateKey := fixtureKeys()
	signed, err := Sign(fixtureEnvelope(t, payload), privateKey)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	return signed, TrustSet{"key-2026-08": publicKey}
}

func TestTenantAuthorityGoldenVector(t *testing.T) {
	signed, trust := fixtureSigned(t, fixtureTenant())
	const wantSignature = "TWokzKjhwichdC2ZNqzbnvfndX7U3uJNG+VVo2QG7PS7++gA1Cjw796Rf8wxFrcgvEwg/rWAzw2LGLmmDlZDBw"
	gotSignature := base64.RawStdEncoding.EncodeToString(signed.GetSignature())
	if gotSignature != wantSignature {
		t.Fatalf("signature = %q", gotSignature)
	}

	verified, err := Verify(signed, trust, "cell-a", fixtureNow.Add(time.Minute))
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if verified.Tenant.GetTenantId() != "tenant-1" || verified.MediaObject != nil || verified.NeedsRefresh {
		t.Fatalf("unexpected verified result: %+v", verified)
	}
}

func TestVerifySoftAndHardExpiry(t *testing.T) {
	signed, trust := fixtureSigned(t, fixtureTenant())

	verified, err := Verify(signed, trust, "cell-a", fixtureNow.Add(10*time.Minute))
	if err != nil {
		t.Fatalf("Verify at soft expiry: %v", err)
	}
	if !verified.NeedsRefresh {
		t.Fatal("soft-expired authority did not request refresh")
	}
	if _, err := Verify(signed, trust, "cell-a", fixtureNow.Add(24*time.Hour)); !errors.Is(err, ErrExpired) {
		t.Fatalf("hard expiry error = %v, want ErrExpired", err)
	}
}

func TestVerifyRejectsAdversarialEnvelopeChanges(t *testing.T) {
	base, trust := fixtureSigned(t, fixtureTenant())
	tests := []struct {
		name string
		edit func(*mediaauthoritypb.SignedAuthorityEnvelope)
		cell string
		want error
	}{
		{name: "cross cell", cell: "cell-b", want: ErrWrongAudience},
		{name: "payload tamper", cell: "cell-a", want: ErrPayloadDigest, edit: func(s *mediaauthoritypb.SignedAuthorityEnvelope) { s.Envelope.Payload[0] ^= 1 }},
		{name: "digest tamper", cell: "cell-a", want: ErrPayloadDigest, edit: func(s *mediaauthoritypb.SignedAuthorityEnvelope) { s.Envelope.PayloadSha256[0] ^= 1 }},
		{name: "signature tamper", cell: "cell-a", want: ErrInvalidSignature, edit: func(s *mediaauthoritypb.SignedAuthorityEnvelope) { s.Signature[0] ^= 1 }},
		{name: "unknown signer", cell: "cell-a", want: ErrUnknownSigner, edit: func(s *mediaauthoritypb.SignedAuthorityEnvelope) { s.Envelope.SignerKeyId = "attacker" }},
		{name: "rollback schema", cell: "cell-a", want: ErrUnknownSchema, edit: func(s *mediaauthoritypb.SignedAuthorityEnvelope) { s.Envelope.SchemaVersion = 0 }},
		{name: "unknown envelope field", cell: "cell-a", want: ErrUnknownSchema, edit: func(s *mediaauthoritypb.SignedAuthorityEnvelope) {
			s.Envelope.ProtoReflect().SetUnknown([]byte{0xa0, 0x06, 0x01})
		}},
		{name: "zero authority version", cell: "cell-a", want: ErrMalformed, edit: func(s *mediaauthoritypb.SignedAuthorityEnvelope) { s.Envelope.AuthorityVersion = 0 }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := proto.Clone(base).(*mediaauthoritypb.SignedAuthorityEnvelope)
			if test.edit != nil {
				test.edit(candidate)
			}
			_, err := Verify(candidate, trust, test.cell, fixtureNow.Add(time.Minute))
			if !errors.Is(err, test.want) {
				t.Fatalf("Verify error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestVerifyRejectsFutureAuthority(t *testing.T) {
	publicKey, privateKey := fixtureKeys()
	envelope := fixtureEnvelope(t, fixtureTenant())
	envelope.IssuedAt = timestamppb.New(fixtureNow.Add(time.Hour))
	envelope.RefreshAfter = timestamppb.New(fixtureNow.Add(2 * time.Hour))
	envelope.ValidUntil = timestamppb.New(fixtureNow.Add(25 * time.Hour))
	signed, err := Sign(envelope, privateKey)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	if _, err := Verify(signed, TrustSet{"key-2026-08": publicKey}, "cell-a", fixtureNow); !errors.Is(err, ErrNotYetValid) {
		t.Fatalf("Verify error = %v, want ErrNotYetValid", err)
	}
}

func TestTenantAuthorityCannotOutliveGrant(t *testing.T) {
	payload := fixtureTenant()
	payload.EffectiveClusterGrants[0].ExpiresAt = timestamppb.New(fixtureNow.Add(time.Hour))
	_, err := NewEnvelope(
		mediaauthoritypb.AuthorityKind_AUTHORITY_KIND_TENANT,
		"tenant-1",
		1,
		fixtureNow,
		fixtureNow.Add(10*time.Minute),
		fixtureNow.Add(24*time.Hour),
		"key-2026-08",
		"cell-a",
		payload,
		[]*mediaauthoritypb.AuthoritySourceRevision{{Service: "purser", Revision: "1"}},
	)
	if !errors.Is(err, ErrMalformed) {
		t.Fatalf("NewEnvelope error = %v, want ErrMalformed", err)
	}
}

func TestTenantAuthorityValidatesGrantProvenanceBeforeTier(t *testing.T) {
	newEnvelope := func(payload *mediaauthoritypb.TenantAuthority) error {
		_, err := NewEnvelope(
			mediaauthoritypb.AuthorityKind_AUTHORITY_KIND_TENANT,
			"tenant-1",
			1,
			fixtureNow,
			fixtureNow.Add(10*time.Minute),
			fixtureNow.Add(24*time.Hour),
			"key-2026-08",
			"cell-a",
			payload,
			[]*mediaauthoritypb.AuthoritySourceRevision{{Service: "purser", Revision: "1"}},
		)
		return err
	}

	owner := fixtureTenant()
	owner.TierLevel = 3
	owner.EffectiveClusterGrants[0].AccessSource = clusterpeerpb.TenantClusterAccessSource_TENANT_CLUSTER_ACCESS_SOURCE_OWNER
	owner.EffectiveClusterGrants[0].ClusterClass = "tenant_private"
	if err := newEnvelope(owner); err != nil {
		t.Fatalf("owner grant rejected by unrelated tier gate: %v", err)
	}

	platform := fixtureTenant()
	platform.TierLevel = 3
	platform.EffectiveClusterGrants[0].ClusterClass = "tenant_private"
	if err := newEnvelope(platform); !errors.Is(err, ErrMalformed) {
		t.Fatalf("platform tier grant error = %v, want ErrMalformed", err)
	}

	forgedInvite := fixtureTenant()
	forgedInvite.TierLevel = 4
	forgedInvite.EffectiveClusterGrants[0].AccessSource = clusterpeerpb.TenantClusterAccessSource_TENANT_CLUSTER_ACCESS_SOURCE_PRIVATE_INVITE
	forgedInvite.EffectiveClusterGrants[0].ClusterClass = "third_party_marketplace"
	if err := newEnvelope(forgedInvite); !errors.Is(err, ErrMalformed) {
		t.Fatalf("cross-class private invite error = %v, want ErrMalformed", err)
	}
}

func TestMediaObjectJWTAuthority(t *testing.T) {
	payload := &mediaauthoritypb.MediaObjectAuthority{
		SchemaVersion:   SchemaVersion,
		ObjectKind:      mediaauthoritypb.MediaObjectKind_MEDIA_OBJECT_KIND_LIVE_STREAM,
		TenantId:        "tenant-1",
		UserId:          "user-1",
		InternalName:    "internal-1",
		PlaybackId:      "playback-1",
		Lifecycle:       mediaauthoritypb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_ACTIVE,
		OriginClusterId: "cell-a",
		PlaybackPolicy: &mediaauthoritypb.PlaybackPolicy{
			Kind: mediaauthoritypb.PlaybackPolicyKind_PLAYBACK_POLICY_KIND_JWT,
			Jwt: &mediaauthoritypb.PlaybackJwtPolicy{
				AllowedKeyIds:      []string{"jwt-key-1"},
				RequiredAudiences:  []string{"playback"},
				RequiredClaimsJson: map[string]string{"role": `"viewer"`},
				ActiveKeys:         []*mediaauthoritypb.PlaybackSigningKey{{KeyId: "jwt-key-1", Algorithm: "ES256", PublicKeyPem: fixtureES256PublicKey(t)}},
			},
		},
		Object: &mediaauthoritypb.MediaObjectAuthority_LiveStream{LiveStream: &mediaauthoritypb.LiveStreamAuthority{StreamId: "stream-1", IngestMode: "push"}},
	}
	signed, trust := fixtureSigned(t, payload)
	verified, err := Verify(signed, trust, "cell-a", fixtureNow.Add(time.Minute))
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if verified.MediaObject.GetLiveStream().GetStreamId() != "stream-1" || verified.Tenant != nil {
		t.Fatalf("unexpected verified result: %+v", verified)
	}
}

func TestWebhookAuthorityRequiresConsistentSealedCredentials(t *testing.T) {
	object := func(connectedOnly bool, sealed bool) *mediaauthoritypb.MediaObjectAuthority {
		payload := &mediaauthoritypb.MediaObjectAuthority{
			SchemaVersion: SchemaVersion, ObjectKind: mediaauthoritypb.MediaObjectKind_MEDIA_OBJECT_KIND_LIVE_STREAM,
			TenantId: "tenant-1", InternalName: "internal-1", PlaybackId: "playback-1",
			Lifecycle: mediaauthoritypb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_ACTIVE,
			PlaybackPolicy: &mediaauthoritypb.PlaybackPolicy{
				Kind: mediaauthoritypb.PlaybackPolicyKind_PLAYBACK_POLICY_KIND_WEBHOOK, ConnectedOnly: connectedOnly,
			},
			Object: &mediaauthoritypb.MediaObjectAuthority_LiveStream{LiveStream: &mediaauthoritypb.LiveStreamAuthority{StreamId: "stream-1", IngestMode: "push"}},
		}
		if sealed {
			payload.SealedPlaybackSecrets = []*mediaauthoritypb.SealedCellSecret{{
				AudienceCellId: "cell-a", RecipientKeyId: "seal-1", EphemeralPublicKey: make([]byte, 32), Nonce: make([]byte, 12), Ciphertext: make([]byte, 16),
			}}
		}
		return payload
	}
	newEnvelope := func(payload *mediaauthoritypb.MediaObjectAuthority) error {
		_, err := NewEnvelope(mediaauthoritypb.AuthorityKind_AUTHORITY_KIND_MEDIA_OBJECT, LiveStreamAuthorityID("stream-1"), 1,
			fixtureNow, fixtureNow.Add(time.Minute), fixtureNow.Add(time.Hour), "key", "cell-a", payload,
			[]*mediaauthoritypb.AuthoritySourceRevision{{Service: "commodore", Revision: "1"}})
		return err
	}
	if err := newEnvelope(object(false, true)); err != nil {
		t.Fatalf("autonomous sealed webhook rejected: %v", err)
	}
	if err := newEnvelope(object(false, false)); !errors.Is(err, ErrMalformed) {
		t.Fatalf("autonomous webhook without sealed credential error = %v, want ErrMalformed", err)
	}
	if err := newEnvelope(object(true, true)); !errors.Is(err, ErrMalformed) {
		t.Fatalf("connected-only webhook with sealed credential error = %v, want ErrMalformed", err)
	}
	if err := newEnvelope(object(true, false)); err != nil {
		t.Fatalf("connected-only webhook rejected: %v", err)
	}
}

func TestAuthorityDurabilityBounds(t *testing.T) {
	t.Run("version fits signed database range", func(t *testing.T) {
		_, err := NewEnvelope(
			mediaauthoritypb.AuthorityKind_AUTHORITY_KIND_TENANT,
			"tenant-1",
			math.MaxUint64,
			fixtureNow,
			fixtureNow.Add(time.Minute),
			fixtureNow.Add(time.Hour),
			"key",
			"cell-a",
			fixtureTenant(),
			[]*mediaauthoritypb.AuthoritySourceRevision{{Service: "purser", Revision: "1"}},
		)
		if !errors.Is(err, ErrMalformed) {
			t.Fatalf("error = %v, want ErrMalformed", err)
		}
	})

	t.Run("tenant validity is bounded", func(t *testing.T) {
		_, err := NewEnvelope(
			mediaauthoritypb.AuthorityKind_AUTHORITY_KIND_TENANT,
			"tenant-1",
			1,
			fixtureNow,
			fixtureNow.Add(time.Minute),
			fixtureNow.Add(maxTenantValidity+time.Second),
			"key",
			"cell-a",
			fixtureTenant(),
			[]*mediaauthoritypb.AuthoritySourceRevision{{Service: "purser", Revision: "1"}},
		)
		if !errors.Is(err, ErrMalformed) {
			t.Fatalf("error = %v, want ErrMalformed", err)
		}
	})

	t.Run("payload is bounded before persistence", func(t *testing.T) {
		payload := fixtureTenant()
		payload.DecisionReason = strings.Repeat("x", maxPayloadBytes)
		_, err := NewEnvelope(
			mediaauthoritypb.AuthorityKind_AUTHORITY_KIND_TENANT,
			"tenant-1",
			1,
			fixtureNow,
			fixtureNow.Add(time.Minute),
			fixtureNow.Add(time.Hour),
			"key",
			"cell-a",
			payload,
			[]*mediaauthoritypb.AuthoritySourceRevision{{Service: "purser", Revision: "1"}},
		)
		if !errors.Is(err, ErrMalformed) {
			t.Fatalf("error = %v, want ErrMalformed", err)
		}
	})
}

func TestDeniedTenantCannotCarryPositiveAuthority(t *testing.T) {
	payload := fixtureTenant()
	payload.BillingDecision = mediaauthoritypb.TenantBillingDecision_TENANT_BILLING_DECISION_SUSPENDED
	_, err := NewEnvelope(
		mediaauthoritypb.AuthorityKind_AUTHORITY_KIND_TENANT,
		"tenant-1",
		1,
		fixtureNow,
		fixtureNow.Add(time.Minute),
		fixtureNow.Add(time.Hour),
		"key",
		"cell-a",
		payload,
		[]*mediaauthoritypb.AuthoritySourceRevision{{Service: "purser", Revision: "1"}},
	)
	if !errors.Is(err, ErrMalformed) {
		t.Fatalf("error = %v, want ErrMalformed", err)
	}
}

func TestJWTAuthorityRejectsInvalidES256Key(t *testing.T) {
	payload := &mediaauthoritypb.MediaObjectAuthority{
		SchemaVersion: SchemaVersion,
		ObjectKind:    mediaauthoritypb.MediaObjectKind_MEDIA_OBJECT_KIND_LIVE_STREAM,
		TenantId:      "tenant-1",
		InternalName:  "internal-1",
		PlaybackId:    "playback-1",
		Lifecycle:     mediaauthoritypb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_ACTIVE,
		PlaybackPolicy: &mediaauthoritypb.PlaybackPolicy{
			Kind: mediaauthoritypb.PlaybackPolicyKind_PLAYBACK_POLICY_KIND_JWT,
			Jwt:  &mediaauthoritypb.PlaybackJwtPolicy{ActiveKeys: []*mediaauthoritypb.PlaybackSigningKey{{KeyId: "jwt-key-1", Algorithm: "ES256", PublicKeyPem: "not-a-key"}}},
		},
		Object: &mediaauthoritypb.MediaObjectAuthority_LiveStream{LiveStream: &mediaauthoritypb.LiveStreamAuthority{StreamId: "stream-1", IngestMode: "push"}},
	}
	_, err := NewEnvelope(
		mediaauthoritypb.AuthorityKind_AUTHORITY_KIND_MEDIA_OBJECT,
		LiveStreamAuthorityID("stream-1"),
		1,
		fixtureNow,
		fixtureNow.Add(time.Minute),
		fixtureNow.Add(time.Hour),
		"key",
		"cell-a",
		payload,
		[]*mediaauthoritypb.AuthoritySourceRevision{{Service: "commodore", Revision: "1"}},
	)
	if !errors.Is(err, ErrMalformed) {
		t.Fatalf("error = %v, want ErrMalformed", err)
	}
}

func TestNonCanonicalPayloadsAreRejected(t *testing.T) {
	t.Run("grant order", func(t *testing.T) {
		payload := fixtureTenant()
		payload.EffectiveClusterGrants = append(payload.EffectiveClusterGrants,
			&mediaauthoritypb.TenantClusterGrant{ClusterId: "cell-0", AccessSource: clusterpeerpb.TenantClusterAccessSource_TENANT_CLUSTER_ACCESS_SOURCE_OWNER, SubscriptionStatus: "active", ClusterClass: "platform_official"})
		_, err := NewEnvelope(mediaauthoritypb.AuthorityKind_AUTHORITY_KIND_TENANT, "tenant-1", 1, fixtureNow, fixtureNow.Add(time.Minute), fixtureNow.Add(time.Hour), "key", "cell-a", payload, []*mediaauthoritypb.AuthoritySourceRevision{{Service: "purser", Revision: "1"}})
		if !errors.Is(err, ErrNonCanonical) {
			t.Fatalf("error = %v, want ErrNonCanonical", err)
		}
	})

	t.Run("source revision order", func(t *testing.T) {
		_, err := NewEnvelope(mediaauthoritypb.AuthorityKind_AUTHORITY_KIND_TENANT, "tenant-1", 1, fixtureNow, fixtureNow.Add(time.Minute), fixtureNow.Add(time.Hour), "key", "cell-a", fixtureTenant(), []*mediaauthoritypb.AuthoritySourceRevision{{Service: "quartermaster", Revision: "1"}, {Service: "purser", Revision: "1"}})
		if !errors.Is(err, ErrNonCanonical) {
			t.Fatalf("error = %v, want ErrNonCanonical", err)
		}
	})
}

func TestSignClonesEnvelope(t *testing.T) {
	_, privateKey := fixtureKeys()
	envelope := fixtureEnvelope(t, fixtureTenant())
	signed, err := Sign(envelope, privateKey)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	envelope.AuthorityId = "changed"
	if signed.GetEnvelope().GetAuthorityId() != "tenant-1" {
		t.Fatal("signed envelope aliases caller-owned message")
	}
}

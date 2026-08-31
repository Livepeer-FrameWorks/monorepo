package mediaauthority

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/sha256"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	clusterpeerpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/cluster_peer"
	mediaauthoritypb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_authority"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const (
	SchemaVersion          = 1
	SignatureDomain        = "frameworks-media-authority-v1\x00"
	maxClockSkewFuture     = 30 * time.Second
	maxTenantValidity      = 24 * time.Hour
	maxLiveValidity        = 24 * time.Hour
	maxArtifactValidity    = 7 * 24 * time.Hour
	maxPayloadBytes        = 1 << 20
	maxIdentifierBytes     = 255
	maxSignerKeyIDBytes    = 255
	maxAudienceCellIDBytes = 255
)

func LiveStreamAuthorityID(streamID string) string {
	return "live_stream:" + strings.TrimSpace(streamID)
}

func ArtifactAuthorityID(artifactID string) string {
	return "artifact:" + strings.TrimSpace(artifactID)
}

var (
	ErrMalformed        = errors.New("media authority is malformed")
	ErrUnknownSchema    = errors.New("media authority schema is unsupported")
	ErrWrongAudience    = errors.New("media authority audience does not match this cell")
	ErrUnknownSigner    = errors.New("media authority signer is not trusted")
	ErrInvalidSignature = errors.New("media authority signature is invalid")
	ErrPayloadDigest    = errors.New("media authority payload digest does not match")
	ErrExpired          = errors.New("media authority has hard-expired")
	ErrNotYetValid      = errors.New("media authority was issued in the future")
	ErrNonCanonical     = errors.New("media authority is not canonical")
)

// TrustSet maps an envelope signer_key_id to its Ed25519 public key.
type TrustSet map[string]ed25519.PublicKey

// Verified contains a fully verified envelope and exactly one decoded payload.
type Verified struct {
	Envelope     *mediaauthoritypb.AuthorityEnvelope
	Tenant       *mediaauthoritypb.TenantAuthority
	MediaObject  *mediaauthoritypb.MediaObjectAuthority
	NeedsRefresh bool
}

// NewEnvelope deterministically encodes payload and constructs the exact bytes
// that Sign authenticates. The caller owns authority-version allocation.
func NewEnvelope(kind mediaauthoritypb.AuthorityKind, authorityID string, authorityVersion uint64, issuedAt, refreshAfter, validUntil time.Time, signerKeyID, audienceCellID string, payload proto.Message, revisions []*mediaauthoritypb.AuthoritySourceRevision) (*mediaauthoritypb.AuthorityEnvelope, error) {
	payloadBytes, err := proto.MarshalOptions{Deterministic: true}.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("%w: encode payload: %w", ErrMalformed, err)
	}
	digest := sha256.Sum256(payloadBytes)
	envelope := &mediaauthoritypb.AuthorityEnvelope{
		SchemaVersion:    SchemaVersion,
		Kind:             kind,
		AuthorityId:      strings.TrimSpace(authorityID),
		AuthorityVersion: authorityVersion,
		IssuedAt:         timestamp(issuedAt),
		RefreshAfter:     timestamp(refreshAfter),
		ValidUntil:       timestamp(validUntil),
		SignerKeyId:      strings.TrimSpace(signerKeyID),
		AudienceCellId:   strings.TrimSpace(audienceCellID),
		Payload:          payloadBytes,
		PayloadSha256:    digest[:],
		SourceRevisions:  cloneRevisions(revisions),
	}
	if _, err := validateEnvelope(envelope, audienceCellID, issuedAt, false); err != nil {
		return nil, err
	}
	if _, err := decodePayload(envelope); err != nil {
		return nil, err
	}
	return envelope, nil
}

// Sign authenticates a validated envelope without mutating it.
func Sign(envelope *mediaauthoritypb.AuthorityEnvelope, privateKey ed25519.PrivateKey) (*mediaauthoritypb.SignedAuthorityEnvelope, error) {
	if len(privateKey) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("%w: invalid Ed25519 private key length", ErrMalformed)
	}
	if envelope == nil {
		return nil, fmt.Errorf("%w: envelope required", ErrMalformed)
	}
	if _, err := validateEnvelope(envelope, envelope.GetAudienceCellId(), time.Time{}, false); err != nil {
		return nil, err
	}
	if _, err := decodePayload(envelope); err != nil {
		return nil, err
	}
	encoded, err := signingBytes(envelope)
	if err != nil {
		return nil, err
	}
	return &mediaauthoritypb.SignedAuthorityEnvelope{
		Envelope:  proto.Clone(envelope).(*mediaauthoritypb.AuthorityEnvelope), //nolint:errcheck // concrete input type
		Signature: ed25519.Sign(privateKey, encoded),
	}, nil
}

// Verify authenticates, validates, and decodes one cell-bound authority.
func Verify(signed *mediaauthoritypb.SignedAuthorityEnvelope, trust TrustSet, expectedCellID string, now time.Time) (*Verified, error) {
	if signed == nil || signed.GetEnvelope() == nil || len(signed.GetSignature()) != ed25519.SignatureSize {
		return nil, fmt.Errorf("%w: missing envelope or signature", ErrMalformed)
	}
	if err := rejectUnknownFields(signed.ProtoReflect()); err != nil {
		return nil, err
	}
	needsRefresh, err := validateEnvelope(signed.GetEnvelope(), expectedCellID, now, true)
	if err != nil {
		return nil, err
	}
	publicKey, ok := trust[signed.GetEnvelope().GetSignerKeyId()]
	if !ok || len(publicKey) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("%w: %q", ErrUnknownSigner, signed.GetEnvelope().GetSignerKeyId())
	}
	encoded, err := signingBytes(signed.GetEnvelope())
	if err != nil {
		return nil, err
	}
	if !ed25519.Verify(publicKey, encoded, signed.GetSignature()) {
		return nil, ErrInvalidSignature
	}
	verified, err := decodePayload(signed.GetEnvelope())
	if err != nil {
		return nil, err
	}
	verified.NeedsRefresh = needsRefresh
	verified.Envelope = proto.Clone(signed.GetEnvelope()).(*mediaauthoritypb.AuthorityEnvelope) //nolint:errcheck // concrete input type
	return verified, nil
}

func signingBytes(envelope *mediaauthoritypb.AuthorityEnvelope) ([]byte, error) {
	encoded, err := proto.MarshalOptions{Deterministic: true}.Marshal(envelope)
	if err != nil {
		return nil, fmt.Errorf("%w: encode envelope: %w", ErrMalformed, err)
	}
	return append([]byte(SignatureDomain), encoded...), nil
}

func validateEnvelope(envelope *mediaauthoritypb.AuthorityEnvelope, expectedCellID string, now time.Time, checkNow bool) (bool, error) {
	if envelope == nil {
		return false, fmt.Errorf("%w: envelope required", ErrMalformed)
	}
	if err := rejectUnknownFields(envelope.ProtoReflect()); err != nil {
		return false, err
	}
	if envelope.GetSchemaVersion() != SchemaVersion {
		return false, fmt.Errorf("%w: envelope version %d", ErrUnknownSchema, envelope.GetSchemaVersion())
	}
	if envelope.GetKind() == mediaauthoritypb.AuthorityKind_AUTHORITY_KIND_UNSPECIFIED || strings.TrimSpace(envelope.GetAuthorityId()) == "" || envelope.GetAuthorityVersion() == 0 {
		return false, fmt.Errorf("%w: kind, authority_id, and positive version are required", ErrMalformed)
	}
	if envelope.GetAuthorityVersion() > math.MaxInt64 {
		return false, fmt.Errorf("%w: authority version exceeds durable-store range", ErrMalformed)
	}
	if len(envelope.GetAuthorityId()) > maxIdentifierBytes {
		return false, fmt.Errorf("%w: authority_id exceeds %d bytes", ErrMalformed, maxIdentifierBytes)
	}
	if strings.TrimSpace(envelope.GetSignerKeyId()) == "" || strings.TrimSpace(envelope.GetAudienceCellId()) == "" || len(envelope.GetPayload()) == 0 {
		return false, fmt.Errorf("%w: signer, audience, and payload are required", ErrMalformed)
	}
	if len(envelope.GetSignerKeyId()) > maxSignerKeyIDBytes || len(envelope.GetAudienceCellId()) > maxAudienceCellIDBytes {
		return false, fmt.Errorf("%w: signer or audience exceeds durable-store limit", ErrMalformed)
	}
	if len(envelope.GetPayload()) > maxPayloadBytes {
		return false, fmt.Errorf("%w: payload exceeds %d bytes", ErrMalformed, maxPayloadBytes)
	}
	if strings.TrimSpace(expectedCellID) == "" || envelope.GetAudienceCellId() != strings.TrimSpace(expectedCellID) {
		return false, fmt.Errorf("%w: got %q", ErrWrongAudience, envelope.GetAudienceCellId())
	}
	issuedAt, refreshAfter, validUntil, err := envelopeTimes(envelope)
	if err != nil {
		return false, err
	}
	if refreshAfter.Before(issuedAt) || !validUntil.After(refreshAfter) {
		return false, fmt.Errorf("%w: require issued_at <= refresh_after < valid_until", ErrMalformed)
	}
	if len(envelope.GetPayloadSha256()) != sha256.Size {
		return false, fmt.Errorf("%w: digest length", ErrPayloadDigest)
	}
	digest := sha256.Sum256(envelope.GetPayload())
	if !bytes.Equal(digest[:], envelope.GetPayloadSha256()) {
		return false, ErrPayloadDigest
	}
	if err := validateRevisions(envelope.GetSourceRevisions()); err != nil {
		return false, err
	}
	if !checkNow {
		return false, nil
	}
	now = now.UTC()
	if issuedAt.After(now.Add(maxClockSkewFuture)) {
		return false, ErrNotYetValid
	}
	if !now.Before(validUntil) {
		return false, ErrExpired
	}
	return !now.Before(refreshAfter), nil
}

func envelopeTimes(envelope *mediaauthoritypb.AuthorityEnvelope) (time.Time, time.Time, time.Time, error) {
	for name, value := range map[string]interface {
		AsTime() time.Time
		IsValid() bool
	}{
		"issued_at": envelope.GetIssuedAt(), "refresh_after": envelope.GetRefreshAfter(), "valid_until": envelope.GetValidUntil(),
	} {
		if value == nil || !value.IsValid() {
			return time.Time{}, time.Time{}, time.Time{}, fmt.Errorf("%w: invalid %s", ErrMalformed, name)
		}
	}
	return envelope.GetIssuedAt().AsTime().UTC(), envelope.GetRefreshAfter().AsTime().UTC(), envelope.GetValidUntil().AsTime().UTC(), nil
}

func decodePayload(envelope *mediaauthoritypb.AuthorityEnvelope) (*Verified, error) {
	verified := &Verified{}
	switch envelope.GetKind() {
	case mediaauthoritypb.AuthorityKind_AUTHORITY_KIND_TENANT:
		payload := &mediaauthoritypb.TenantAuthority{}
		if err := (proto.UnmarshalOptions{DiscardUnknown: false}).Unmarshal(envelope.GetPayload(), payload); err != nil {
			return nil, fmt.Errorf("%w: tenant payload: %w", ErrMalformed, err)
		}
		if err := validateTenant(envelope, payload); err != nil {
			return nil, err
		}
		verified.Tenant = payload
	case mediaauthoritypb.AuthorityKind_AUTHORITY_KIND_MEDIA_OBJECT:
		payload := &mediaauthoritypb.MediaObjectAuthority{}
		if err := (proto.UnmarshalOptions{DiscardUnknown: false}).Unmarshal(envelope.GetPayload(), payload); err != nil {
			return nil, fmt.Errorf("%w: media-object payload: %w", ErrMalformed, err)
		}
		if err := validateMediaObject(envelope, payload); err != nil {
			return nil, err
		}
		verified.MediaObject = payload
	default:
		return nil, fmt.Errorf("%w: authority kind %d", ErrUnknownSchema, envelope.GetKind())
	}
	return verified, nil
}

func validateTenant(envelope *mediaauthoritypb.AuthorityEnvelope, payload *mediaauthoritypb.TenantAuthority) error {
	if err := rejectUnknownFields(payload.ProtoReflect()); err != nil {
		return err
	}
	if payload.GetSchemaVersion() != SchemaVersion {
		return fmt.Errorf("%w: tenant payload version %d", ErrUnknownSchema, payload.GetSchemaVersion())
	}
	if payload.GetTenantId() == "" || payload.GetTenantId() != envelope.GetAuthorityId() {
		return fmt.Errorf("%w: tenant payload identity mismatch", ErrMalformed)
	}
	if payload.GetLifecycle() == mediaauthoritypb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_UNSPECIFIED || payload.GetBillingDecision() == mediaauthoritypb.TenantBillingDecision_TENANT_BILLING_DECISION_UNSPECIFIED {
		return fmt.Errorf("%w: tenant lifecycle and billing decision are required", ErrMalformed)
	}
	if payload.GetLifecycle() == mediaauthoritypb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_ACTIVE && payload.GetBillingModel() == mediaauthoritypb.TenantBillingModel_TENANT_BILLING_MODEL_UNSPECIFIED {
		return fmt.Errorf("%w: active tenant billing model is required", ErrMalformed)
	}
	issuedAt, _, validUntil, err := envelopeTimes(envelope)
	if err != nil {
		return err
	}
	if validUntil.Sub(issuedAt) > maxTenantValidity {
		return fmt.Errorf("%w: tenant authority validity exceeds %s", ErrMalformed, maxTenantValidity)
	}
	positiveAuthority := payload.GetBillingDecision() == mediaauthoritypb.TenantBillingDecision_TENANT_BILLING_DECISION_ALLOW
	if positiveAuthority && payload.GetLifecycle() != mediaauthoritypb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_ACTIVE {
		return fmt.Errorf("%w: inactive tenant cannot carry an allow decision", ErrMalformed)
	}
	if !positiveAuthority && (payload.GetAllowPlatformSharedPlayback() || len(payload.GetEffectiveClusterGrants()) != 0) {
		return fmt.Errorf("%w: denied tenant cannot carry positive cluster authority", ErrMalformed)
	}
	if payload.GetLifecycle() != mediaauthoritypb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_ACTIVE && payload.GetBillingDecision() != mediaauthoritypb.TenantBillingDecision_TENANT_BILLING_DECISION_INACTIVE {
		return fmt.Errorf("%w: inactive tenant must carry an inactive billing decision", ErrMalformed)
	}
	if limits := payload.GetResourceLimits(); limits != nil && (limits.GetMaxStreams() < 0 || limits.GetMaxViewers() < 0) {
		return fmt.Errorf("%w: tenant limits cannot be negative", ErrMalformed)
	}
	if payload.GetTierLevel() < 0 {
		return fmt.Errorf("%w: tenant tier level cannot be negative", ErrMalformed)
	}
	if err := validateGrants(envelope, payload.GetTierLevel(), payload.GetEffectiveClusterGrants()); err != nil {
		return err
	}
	lastMeter := ""
	for _, allowance := range payload.GetAllowances() {
		if allowance == nil || strings.TrimSpace(allowance.GetMeter()) == "" || allowance.GetMeter() <= lastMeter {
			return fmt.Errorf("%w: allowances must be non-nil, unique, and sorted by meter", ErrNonCanonical)
		}
		lastMeter = allowance.GetMeter()
		for _, value := range []float64{allowance.GetIncluded(), allowance.GetUsed(), allowance.GetRemaining()} {
			if value < 0 || math.IsNaN(value) || math.IsInf(value, 0) {
				return fmt.Errorf("%w: invalid allowance value", ErrMalformed)
			}
		}
	}
	return nil
}

func validateGrants(envelope *mediaauthoritypb.AuthorityEnvelope, tierLevel int32, grants []*mediaauthoritypb.TenantClusterGrant) error {
	lastClusterID := ""
	for _, grant := range grants {
		if grant == nil || strings.TrimSpace(grant.GetClusterId()) == "" || grant.GetAccessSource() == 0 {
			return fmt.Errorf("%w: incomplete cluster grant", ErrMalformed)
		}
		if grant.GetClusterId() <= lastClusterID {
			return fmt.Errorf("%w: cluster grants must be unique and sorted", ErrNonCanonical)
		}
		lastClusterID = grant.GetClusterId()
		if grant.GetSubscriptionStatus() != "active" {
			return fmt.Errorf("%w: effective cluster grant is not active", ErrMalformed)
		}
		if strings.TrimSpace(grant.GetControlCellId()) == "" || len(grant.GetControlCellId()) > maxAudienceCellIDBytes ||
			len(grant.GetEligibleServingCellIds()) > maxRecipients || !strictlySorted(grant.GetEligibleServingCellIds()) {
			return fmt.Errorf("%w: cluster grant cell scope is incomplete or noncanonical", ErrNonCanonical)
		}
		for _, cellID := range grant.GetEligibleServingCellIds() {
			if strings.TrimSpace(cellID) == "" || len(cellID) > maxAudienceCellIDBytes {
				return fmt.Errorf("%w: cluster grant has an invalid eligible serving cell", ErrMalformed)
			}
		}
		if !grantProvenanceAllowed(tierLevel, grant) {
			return fmt.Errorf("%w: cluster grant class exceeds tenant tier", ErrMalformed)
		}
		if expiry := grant.GetExpiresAt(); expiry != nil {
			if !expiry.IsValid() || envelope.GetValidUntil().AsTime().After(expiry.AsTime()) {
				return fmt.Errorf("%w: authority outlives cluster grant", ErrMalformed)
			}
		}
		if limits := grant.GetResourceLimits(); limits != nil && (limits.GetMaxStreams() < 0 || limits.GetMaxViewers() < 0) {
			return fmt.Errorf("%w: cluster grant limits cannot be negative", ErrMalformed)
		}
	}
	return nil
}

func grantProvenanceAllowed(tierLevel int32, grant *mediaauthoritypb.TenantClusterGrant) bool {
	class := strings.TrimSpace(grant.GetClusterClass())
	switch grant.GetAccessSource() {
	case clusterpeerpb.TenantClusterAccessSource_TENANT_CLUSTER_ACCESS_SOURCE_OWNER,
		clusterpeerpb.TenantClusterAccessSource_TENANT_CLUSTER_ACCESS_SOURCE_OPERATOR_OVERRIDE:
		return true
	case clusterpeerpb.TenantClusterAccessSource_TENANT_CLUSTER_ACCESS_SOURCE_PRIVATE_INVITE:
		return class == "tenant_private"
	case clusterpeerpb.TenantClusterAccessSource_TENANT_CLUSTER_ACCESS_SOURCE_PLATFORM_TIER,
		clusterpeerpb.TenantClusterAccessSource_TENANT_CLUSTER_ACCESS_SOURCE_MARKETPLACE_SUBSCRIPTION:
		return clusterClassAllowed(tierLevel, class)
	default:
		return false
	}
}

func clusterClassAllowed(tierLevel int32, clusterClass string) bool {
	switch strings.TrimSpace(clusterClass) {
	case "platform_official":
		return true
	case "third_party_marketplace":
		return tierLevel >= 2
	case "tenant_private":
		return tierLevel >= 4
	default:
		return false
	}
}

func validateMediaObject(envelope *mediaauthoritypb.AuthorityEnvelope, payload *mediaauthoritypb.MediaObjectAuthority) error {
	if err := rejectUnknownFields(payload.ProtoReflect()); err != nil {
		return err
	}
	if payload.GetSchemaVersion() != SchemaVersion {
		return fmt.Errorf("%w: media-object payload version %d", ErrUnknownSchema, payload.GetSchemaVersion())
	}
	if payload.GetTenantId() == "" || payload.GetInternalName() == "" || payload.GetPlaybackId() == "" || payload.GetLifecycle() == 0 {
		return fmt.Errorf("%w: media-object identity and lifecycle are required", ErrMalformed)
	}
	if err := validateSealedCellSecrets(envelope, payload.GetSealedPlaybackSecrets()); err != nil {
		return err
	}
	if payload.GetLifecycle() != mediaauthoritypb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_ACTIVE {
		if payload.GetPlaybackPolicy().GetKind() != mediaauthoritypb.PlaybackPolicyKind_PLAYBACK_POLICY_KIND_DENY || len(payload.GetSealedPlaybackSecrets()) != 0 {
			return fmt.Errorf("%w: inactive media object must carry only a deny policy", ErrMalformed)
		}
	}
	switch payload.GetObjectKind() {
	case mediaauthoritypb.MediaObjectKind_MEDIA_OBJECT_KIND_LIVE_STREAM:
		if err := validateMaxValidity(envelope, maxLiveValidity, "live stream"); err != nil {
			return err
		}
		stream := payload.GetLiveStream()
		if stream == nil || strings.TrimSpace(stream.GetStreamId()) == "" || LiveStreamAuthorityID(stream.GetStreamId()) != envelope.GetAuthorityId() || strings.TrimSpace(stream.GetIngestMode()) == "" {
			return fmt.Errorf("%w: live stream identity mismatch", ErrMalformed)
		}
		if len(stream.GetPublishingCredentialSha256()) != 0 {
			if len(stream.GetPublishingCredentialSha256()) != sha256.Size || strings.TrimSpace(stream.GetOutageIngestClusterId()) == "" {
				return fmt.Errorf("%w: incomplete publishing authority", ErrMalformed)
			}
			for _, configJSON := range []string{stream.GetProcessesJson(), stream.GetDvrProcessesJson()} {
				if configJSON != "" && !json.Valid([]byte(configJSON)) {
					return fmt.Errorf("%w: invalid process policy JSON", ErrMalformed)
				}
			}
		}
		if payload.GetLifecycle() != mediaauthoritypb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_ACTIVE && len(stream.GetSealedCellSecrets()) != 0 {
			return fmt.Errorf("%w: inactive live stream cannot carry sealed credentials", ErrMalformed)
		}
		if err := validateSealedCellSecrets(envelope, stream.GetSealedCellSecrets()); err != nil {
			return err
		}
	case mediaauthoritypb.MediaObjectKind_MEDIA_OBJECT_KIND_ARTIFACT:
		if err := validateMaxValidity(envelope, maxArtifactValidity, "artifact"); err != nil {
			return err
		}
		artifact := payload.GetArtifact()
		if artifact == nil || strings.TrimSpace(artifact.GetArtifactId()) == "" || ArtifactAuthorityID(artifact.GetArtifactId()) != envelope.GetAuthorityId() || artifact.GetArtifactHash() == "" || artifact.GetArtifactKind() == 0 {
			return fmt.Errorf("%w: artifact identity mismatch", ErrMalformed)
		}
	default:
		return fmt.Errorf("%w: media object kind is required", ErrMalformed)
	}
	return validatePlaybackPolicy(payload.GetPlaybackPolicy(), payload.GetSealedPlaybackSecrets())
}

func validateSealedCellSecrets(envelope *mediaauthoritypb.AuthorityEnvelope, boxes []*mediaauthoritypb.SealedCellSecret) error {
	if len(boxes) > maxRecipients {
		return fmt.Errorf("%w: too many sealed cell secrets", ErrMalformed)
	}
	lastCell := ""
	matchingAudience := 0
	for _, box := range boxes {
		if box == nil || strings.TrimSpace(box.GetAudienceCellId()) == "" || box.GetAudienceCellId() <= lastCell ||
			strings.TrimSpace(box.GetRecipientKeyId()) == "" || len(box.GetEphemeralPublicKey()) != 32 ||
			len(box.GetNonce()) != 12 || len(box.GetCiphertext()) < 16 {
			return fmt.Errorf("%w: sealed cell secrets must be complete, unique, and sorted", ErrNonCanonical)
		}
		lastCell = box.GetAudienceCellId()
		if box.GetAudienceCellId() == envelope.GetAudienceCellId() {
			matchingAudience++
		}
	}
	// Historical delivery cells receive the same signed public payload so a
	// newer version can revoke their old authority, but they are deliberately
	// omitted from newly sealed credentials. An active recipient therefore has
	// one matching box while a revoked recipient has none; duplicates are never
	// valid.
	if matchingAudience > 1 {
		return fmt.Errorf("%w: sealed secret set contains duplicate boxes for the envelope audience", ErrMalformed)
	}
	return nil
}

func validatePlaybackPolicy(policy *mediaauthoritypb.PlaybackPolicy, sealedSecrets []*mediaauthoritypb.SealedCellSecret) error {
	if policy == nil {
		return fmt.Errorf("%w: playback policy required", ErrMalformed)
	}
	switch policy.GetKind() {
	case mediaauthoritypb.PlaybackPolicyKind_PLAYBACK_POLICY_KIND_PUBLIC:
		if policy.GetJwt() != nil || policy.GetConnectedOnly() || len(sealedSecrets) != 0 {
			return fmt.Errorf("%w: public playback policy has incompatible fields", ErrMalformed)
		}
	case mediaauthoritypb.PlaybackPolicyKind_PLAYBACK_POLICY_KIND_JWT:
		jwt := policy.GetJwt()
		if jwt == nil || policy.GetConnectedOnly() || len(jwt.GetActiveKeys()) == 0 || len(sealedSecrets) != 0 {
			return fmt.Errorf("%w: incomplete JWT playback policy", ErrMalformed)
		}
		if !strictlySorted(jwt.GetAllowedKeyIds()) || !strictlySorted(jwt.GetRequiredAudiences()) {
			return fmt.Errorf("%w: JWT constraints must be unique and sorted", ErrNonCanonical)
		}
		active := make(map[string]struct{}, len(jwt.GetActiveKeys()))
		lastKey := ""
		for _, key := range jwt.GetActiveKeys() {
			if key == nil || key.GetKeyId() <= lastKey || key.GetAlgorithm() != "ES256" || strings.TrimSpace(key.GetPublicKeyPem()) == "" {
				return fmt.Errorf("%w: JWT keys must be ES256, unique, complete, and sorted", ErrNonCanonical)
			}
			if err := validateES256PublicKey(key.GetPublicKeyPem()); err != nil {
				return err
			}
			lastKey = key.GetKeyId()
			active[key.GetKeyId()] = struct{}{}
		}
		for _, keyID := range jwt.GetAllowedKeyIds() {
			if _, ok := active[keyID]; !ok {
				return fmt.Errorf("%w: allowed JWT key is absent from active keyset", ErrMalformed)
			}
		}
	case mediaauthoritypb.PlaybackPolicyKind_PLAYBACK_POLICY_KIND_WEBHOOK:
		if policy.GetJwt() != nil || (policy.GetConnectedOnly() && len(sealedSecrets) != 0) || (!policy.GetConnectedOnly() && len(sealedSecrets) == 0) {
			return fmt.Errorf("%w: webhook playback policy and sealed credentials disagree", ErrMalformed)
		}
	case mediaauthoritypb.PlaybackPolicyKind_PLAYBACK_POLICY_KIND_DENY:
		if policy.GetJwt() != nil || policy.GetConnectedOnly() || len(sealedSecrets) != 0 {
			return fmt.Errorf("%w: deny playback policy has incompatible fields", ErrMalformed)
		}
	default:
		return fmt.Errorf("%w: playback policy kind is required", ErrMalformed)
	}
	return nil
}

func validateMaxValidity(envelope *mediaauthoritypb.AuthorityEnvelope, maximum time.Duration, label string) error {
	issuedAt, _, validUntil, err := envelopeTimes(envelope)
	if err != nil {
		return err
	}
	if validUntil.Sub(issuedAt) > maximum {
		return fmt.Errorf("%w: %s authority validity exceeds %s", ErrMalformed, label, maximum)
	}
	return nil
}

func validateES256PublicKey(encoded string) error {
	block, rest := pem.Decode([]byte(encoded))
	if block == nil || len(bytes.TrimSpace(rest)) != 0 || block.Type != "PUBLIC KEY" {
		return fmt.Errorf("%w: JWT ES256 public key must be one PKIX public-key PEM block", ErrMalformed)
	}
	parsed, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return fmt.Errorf("%w: parse JWT ES256 public key: %w", ErrMalformed, err)
	}
	publicKey, ok := parsed.(*ecdsa.PublicKey)
	if !ok || publicKey.Curve != elliptic.P256() {
		return fmt.Errorf("%w: JWT ES256 public key must use P-256", ErrMalformed)
	}
	return nil
}

func validateRevisions(revisions []*mediaauthoritypb.AuthoritySourceRevision) error {
	if len(revisions) == 0 {
		return fmt.Errorf("%w: source revision required", ErrMalformed)
	}
	lastService := ""
	for _, revision := range revisions {
		if revision == nil || strings.TrimSpace(revision.GetService()) == "" || strings.TrimSpace(revision.GetRevision()) == "" {
			return fmt.Errorf("%w: incomplete source revision", ErrMalformed)
		}
		if revision.GetService() <= lastService {
			return fmt.Errorf("%w: source revisions must be unique and sorted", ErrNonCanonical)
		}
		lastService = revision.GetService()
	}
	return nil
}

func rejectUnknownFields(message protoreflect.Message) error {
	if len(message.GetUnknown()) != 0 {
		return fmt.Errorf("%w: unknown protobuf fields", ErrUnknownSchema)
	}
	var nestedErr error
	message.Range(func(field protoreflect.FieldDescriptor, value protoreflect.Value) bool {
		if field.IsMap() {
			if field.MapValue().Message() == nil {
				return true
			}
			value.Map().Range(func(_ protoreflect.MapKey, nested protoreflect.Value) bool {
				if err := rejectUnknownFields(nested.Message()); err != nil {
					nestedErr = err
					return false
				}
				return true
			})
		} else if field.IsList() && field.Message() != nil {
			list := value.List()
			for i := 0; i < list.Len(); i++ {
				if err := rejectUnknownFields(list.Get(i).Message()); err != nil {
					nestedErr = err
					return false
				}
			}
		} else if field.Message() != nil {
			if err := rejectUnknownFields(value.Message()); err != nil {
				nestedErr = err
				return false
			}
		}
		return nestedErr == nil
	})
	return nestedErr
}

func strictlySorted(values []string) bool {
	return sort.StringsAreSorted(values) && !hasAdjacentDuplicate(values)
}

func hasAdjacentDuplicate(values []string) bool {
	for i := 1; i < len(values); i++ {
		if values[i] == values[i-1] {
			return true
		}
	}
	return false
}

func cloneRevisions(revisions []*mediaauthoritypb.AuthoritySourceRevision) []*mediaauthoritypb.AuthoritySourceRevision {
	out := make([]*mediaauthoritypb.AuthoritySourceRevision, 0, len(revisions))
	for _, revision := range revisions {
		if revision == nil {
			out = append(out, nil)
			continue
		}
		out = append(out, proto.Clone(revision).(*mediaauthoritypb.AuthoritySourceRevision)) //nolint:errcheck // concrete input type
	}
	return out
}

func timestamp(value time.Time) *timestamppb.Timestamp {
	return timestamppb.New(value.UTC())
}

package mediaauthority

import (
	"bytes"
	"context"
	"crypto/ecdh"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"frameworks/api_balancing/internal/artifacts"
	"frameworks/api_balancing/internal/database/foghorndb"
	sharedauthority "github.com/Livepeer-FrameWorks/monorepo/pkg/mediaauthority"
	clusterpeerpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/cluster_peer"
	mediaauthoritypb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_authority"
	"github.com/prometheus/client_golang/prometheus"
	"google.golang.org/protobuf/proto"
)

var (
	ErrRollback          = errors.New("media authority version rollback")
	ErrVersionConflict   = errors.New("media authority version digest conflict")
	ErrTombstoneTerminal = errors.New("media object authority tombstone is terminal")
)

const (
	mediaAuthorityLockNamespace int32 = 0x6d617574 // "maut"
	mediaAuthorityLockTimeout         = 5 * time.Second
)

type ApplyStatus string

const (
	ApplyStatusApplied   ApplyStatus = "applied"
	ApplyStatusDuplicate ApplyStatus = "duplicate"
)

type ApplyResult struct {
	Status    ApplyStatus
	Kind      string
	ID        string
	Version   uint64
	Refreshed bool
}

type Store struct {
	db             *sql.DB
	cellID         string
	trust          sharedauthority.TrustSet
	now            func() time.Time
	sealKeyID      string
	sealPrivateKey *ecdh.PrivateKey
	refresh        *refreshCoordinator
	runtimePeers   RuntimePeerResolver
	applyOutcomes  *prometheus.CounterVec
}

// RuntimePeerResolver supplies cell-local liveness and addresses. Those
// values are intentionally absent from signed tenant grants and must never be
// used as authority-comparison input.
type RuntimePeerResolver interface {
	GetPeerAddr(clusterID string) string
	IsPeerConnected(clusterID string) bool
}

func (s *Store) SetRuntimePeerResolver(resolver RuntimePeerResolver) {
	if s != nil {
		s.runtimePeers = resolver
	}
}

// SetApplyOutcomeMetric installs the bounded authority-apply outcome counter.
func (s *Store) SetApplyOutcomeMetric(counter *prometheus.CounterVec) {
	if s != nil {
		s.applyOutcomes = counter
	}
}

type readinessPreservation struct {
	read   bool
	ingest bool
	source bool
}

func NewStore(db *sql.DB, cellID string, trust sharedauthority.TrustSet) (*Store, error) {
	if db == nil || strings.TrimSpace(cellID) == "" || len(trust) == 0 {
		return nil, errors.New("media authority store requires database, control-cell ID, and trust set")
	}
	return &Store{db: db, cellID: strings.TrimSpace(cellID), trust: cloneTrust(trust), now: time.Now, refresh: &refreshCoordinator{}}, nil
}

func (s *Store) SetSealPrivateKey(keyID string, privateKey *ecdh.PrivateKey) error {
	if s == nil || strings.TrimSpace(keyID) == "" || privateKey == nil || privateKey.Curve() != ecdh.X25519() {
		return errors.New("media authority seal key requires a key ID and X25519 private key")
	}
	if expected := sharedauthority.SealRecipientKeyID(s.cellID, privateKey.PublicKey()); strings.TrimSpace(keyID) != expected {
		return errors.New("media authority seal key does not match the configured control cell")
	}
	s.sealKeyID = strings.TrimSpace(keyID)
	s.sealPrivateKey = privateKey
	return nil
}

// Apply verifies before opening the transaction, then serializes by authority
// identity and commits the envelope and decoded projection atomically.
func (s *Store) Apply(ctx context.Context, encoded []byte) (result ApplyResult, applyErr error) {
	signed := &mediaauthoritypb.SignedAuthorityEnvelope{}
	metricOutcome := "persist_error"
	defer func() {
		if s.applyOutcomes == nil {
			return
		}
		kind := result.Kind
		if kind == "" && signed.GetEnvelope() != nil {
			kind = authorityKind(signed.GetEnvelope().GetKind())
		}
		if kind == "" {
			kind = "unknown"
		}
		if applyErr == nil {
			metricOutcome = string(result.Status)
		}
		s.applyOutcomes.WithLabelValues(kind, metricOutcome).Inc()
	}()
	if err := (proto.UnmarshalOptions{DiscardUnknown: false}).Unmarshal(encoded, signed); err != nil {
		metricOutcome = "verification_rejected"
		verifyErr := fmt.Errorf("decode signed media authority: %w", err)
		return ApplyResult{}, errors.Join(verifyErr, s.recordVerificationFailure(ctx, signed, verifyErr))
	}
	verified, verifyErr := sharedauthority.Verify(signed, s.trust, s.cellID, s.now().UTC())
	if verifyErr != nil {
		metricOutcome = "verification_rejected"
		return ApplyResult{}, errors.Join(verifyErr, s.recordVerificationFailure(ctx, signed, verifyErr))
	}

	envelope := verified.Envelope
	kind := authorityKind(envelope.GetKind())
	result = ApplyResult{Kind: kind, ID: envelope.GetAuthorityId(), Version: envelope.GetAuthorityVersion(), Refreshed: verified.NeedsRefresh}

	tx, beginErr := s.db.BeginTx(ctx, nil)
	if beginErr != nil {
		return ApplyResult{}, fmt.Errorf("begin media authority apply: %w", beginErr)
	}
	defer tx.Rollback() //nolint:errcheck // best effort after commit/error
	queries := foghorndb.New(tx)
	if err := queries.SetLocalMediaAuthorityLockTimeout(ctx, mediaAuthorityLockTimeout.String()); err != nil {
		return ApplyResult{}, fmt.Errorf("bound media authority lock wait: %w", err)
	}
	// Artifact authority and the derivative purge/publication saga share one
	// lock domain. Take the asset lock before the authority-identity lock so a
	// concurrent active projection cannot commit in the absence-check window of
	// a stale purge, and establish a single lock order for future callers.
	if object := verified.MediaObject; object != nil && object.GetObjectKind() == mediaauthoritypb.MediaObjectKind_MEDIA_OBJECT_KIND_ARTIFACT {
		assetKey := strings.TrimSpace(object.GetArtifact().GetArtifactHash())
		if assetKey == "" {
			return ApplyResult{}, errors.New("artifact media authority requires artifact hash")
		}
		if err := queries.LockThumbnailAsset(ctx, foghorndb.LockThumbnailAssetParams{
			LockNamespace: artifacts.ThumbnailAssetLockNamespace,
			AssetKey:      assetKey,
		}); err != nil {
			return ApplyResult{}, fmt.Errorf("lock media-object asset: %w", err)
		}
	}
	identity := foghorndb.LockMediaAuthorityParams{
		LockNamespace: mediaAuthorityLockNamespace,
		AuthorityKind: kind,
		AuthorityID:   envelope.GetAuthorityId(),
	}
	if err := queries.LockMediaAuthority(ctx, identity); err != nil {
		return ApplyResult{}, fmt.Errorf("lock media authority: %w", err)
	}

	current, currentErr := queries.GetMediaAuthorityForUpdate(ctx, foghorndb.GetMediaAuthorityForUpdateParams{
		AuthorityKind: kind,
		AuthorityID:   envelope.GetAuthorityId(),
	})
	switch {
	case currentErr == nil && current.AuthorityVersion > int64(envelope.GetAuthorityVersion()):
		metricOutcome = "rollback_rejected"
		if err := insertAudit(ctx, queries, signed, "rollback_rejected", ErrRollback.Error()); err != nil {
			return ApplyResult{}, fmt.Errorf("record media authority rollback: %w", err)
		}
		if err := tx.Commit(); err != nil {
			return ApplyResult{}, fmt.Errorf("commit media authority rollback audit: %w", err)
		}
		return result, ErrRollback
	case currentErr == nil && current.AuthorityVersion == int64(envelope.GetAuthorityVersion()) && !bytes.Equal(current.PayloadSha256, envelope.GetPayloadSha256()):
		metricOutcome = "conflict_rejected"
		if err := insertAudit(ctx, queries, signed, "conflict_rejected", ErrVersionConflict.Error()); err != nil {
			return ApplyResult{}, fmt.Errorf("record media authority conflict: %w", err)
		}
		if err := tx.Commit(); err != nil {
			return ApplyResult{}, fmt.Errorf("commit media authority conflict audit: %w", err)
		}
		return result, ErrVersionConflict
	case currentErr == nil && current.AuthorityVersion == int64(envelope.GetAuthorityVersion()):
		if err := insertAudit(ctx, queries, signed, "duplicate", ""); err != nil {
			return ApplyResult{}, fmt.Errorf("record duplicate media authority: %w", err)
		}
		if err := tx.Commit(); err != nil {
			return ApplyResult{}, fmt.Errorf("commit duplicate media authority audit: %w", err)
		}
		result.Status = ApplyStatusDuplicate
		return result, nil
	case currentErr != nil && !errors.Is(currentErr, sql.ErrNoRows):
		return ApplyResult{}, fmt.Errorf("load current media authority: %w", currentErr)
	}
	if terminal, err := rejectsObjectTombstoneResurrection(current, currentErr == nil, verified); err != nil {
		return ApplyResult{}, err
	} else if terminal {
		metricOutcome = "terminal_lifecycle_rejected"
		if err := insertAudit(ctx, queries, signed, "terminal_lifecycle_rejected", ErrTombstoneTerminal.Error()); err != nil {
			return ApplyResult{}, fmt.Errorf("record media authority terminal lifecycle rejection: %w", err)
		}
		if err := tx.Commit(); err != nil {
			return ApplyResult{}, fmt.Errorf("commit media authority terminal lifecycle audit: %w", err)
		}
		return result, ErrTombstoneTerminal
	}

	sourceRevisions, err := marshalSourceRevisions(envelope.GetSourceRevisions())
	if err != nil {
		return ApplyResult{}, err
	}
	if err := queries.UpsertMediaAuthority(ctx, foghorndb.UpsertMediaAuthorityParams{
		AuthorityKind: kind, AuthorityID: envelope.GetAuthorityId(), AuthorityVersion: int64(envelope.GetAuthorityVersion()),
		SignerKeyID: envelope.GetSignerKeyId(), AudienceCellID: envelope.GetAudienceCellId(),
		IssuedAt: envelope.GetIssuedAt().AsTime(), RefreshAfter: envelope.GetRefreshAfter().AsTime(), ValidUntil: envelope.GetValidUntil().AsTime(),
		PayloadSha256: envelope.GetPayloadSha256(), SignedEnvelope: encoded, Payload: envelope.GetPayload(), SourceRevisions: sourceRevisions,
	}); err != nil {
		return ApplyResult{}, fmt.Errorf("persist verified media authority: %w", err)
	}
	preserve := s.readinessPreservation(current, currentErr, verified)
	if err := applyProjection(ctx, queries, verified, preserve); err != nil {
		return ApplyResult{}, err
	}
	if err := insertAudit(ctx, queries, signed, "applied", ""); err != nil {
		return ApplyResult{}, fmt.Errorf("record applied media authority: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return ApplyResult{}, fmt.Errorf("commit media authority apply: %w", err)
	}
	result.Status = ApplyStatusApplied
	return result, nil
}

func rejectsObjectTombstoneResurrection(current foghorndb.GetMediaAuthorityForUpdateRow, hasCurrent bool, verified *sharedauthority.Verified) (bool, error) {
	if !hasCurrent || verified == nil || verified.MediaObject == nil {
		return false, nil
	}
	previous := &mediaauthoritypb.MediaObjectAuthority{}
	if err := (proto.UnmarshalOptions{DiscardUnknown: false}).Unmarshal(current.Payload, previous); err != nil {
		return false, fmt.Errorf("decode current media object authority lifecycle: %w", err)
	}
	return previous.GetLifecycle() == mediaauthoritypb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_TOMBSTONE &&
		verified.MediaObject.GetLifecycle() != mediaauthoritypb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_TOMBSTONE, nil
}

func applyProjection(ctx context.Context, queries *foghorndb.Queries, verified *sharedauthority.Verified, preserve readinessPreservation) error {
	envelope := verified.Envelope
	if tenant := verified.Tenant; tenant != nil {
		if tenant.GetLifecycle() != mediaauthoritypb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_ACTIVE ||
			tenant.GetBillingDecision() != mediaauthoritypb.TenantBillingDecision_TENANT_BILLING_DECISION_ALLOW {
			preserve = readinessPreservation{read: true, ingest: true, source: true}
		}
		allowances, err := marshalAllowances(tenant)
		if err != nil {
			return err
		}
		limits := tenant.GetResourceLimits()
		params := foghorndb.UpsertTenantAuthorityProjectionParams{
			TenantID: tenant.GetTenantId(), AuthorityVersion: int64(envelope.GetAuthorityVersion()),
			Lifecycle: lifecycle(tenant.GetLifecycle()), BillingDecision: billingDecision(tenant.GetBillingDecision()),
			BillingModel: billingModel(tenant.GetBillingModel()), OfficialClusterID: tenant.GetOfficialClusterId(),
			AllowPlatformSharedPlayback: tenant.GetAllowPlatformSharedPlayback(), Allowances: allowances,
			DecisionReason: tenant.GetDecisionReason(), ValidUntil: envelope.GetValidUntil().AsTime(),
			PreserveLocalReadReady: preserve.read, PreserveLocalIngestReady: preserve.ingest,
			PreserveLocalSourceReady: preserve.source,
		}
		if limits != nil {
			params.MaxStreams = limits.GetMaxStreams()
			params.MaxViewers = limits.GetMaxViewers()
		}
		if err := queries.UpsertTenantAuthorityProjection(ctx, params); err != nil {
			return fmt.Errorf("apply tenant authority projection: %w", err)
		}
		if err := queries.DeleteTenantAuthorityGrants(ctx, tenant.GetTenantId()); err != nil {
			return fmt.Errorf("replace tenant authority grants: %w", err)
		}
		for _, grant := range tenant.GetEffectiveClusterGrants() {
			expiresAt := sql.NullTime{}
			if grant.GetExpiresAt() != nil {
				expiresAt = sql.NullTime{Time: grant.GetExpiresAt().AsTime(), Valid: true}
			}
			if err := queries.InsertTenantAuthorityGrant(ctx, foghorndb.InsertTenantAuthorityGrantParams{
				TenantID: tenant.GetTenantId(), ClusterID: grant.GetClusterId(), AuthorityVersion: int64(envelope.GetAuthorityVersion()),
				AccessSource: accessSource(grant.GetAccessSource()), AccessLevel: grant.GetAccessLevel(),
				SubscriptionStatus: grant.GetSubscriptionStatus(), ClusterClass: grant.GetClusterClass(),
				ClusterType: grant.GetClusterType(), DeploymentModel: grant.GetDeploymentModel(),
				OwnerTenantID: grant.GetOwnerTenantId(), ExpiresAt: expiresAt,
			}); err != nil {
				return fmt.Errorf("apply tenant authority grant %q: %w", grant.GetClusterId(), err)
			}
		}
		return nil
	}

	object := verified.MediaObject
	if object.GetLifecycle() != mediaauthoritypb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_ACTIVE {
		preserve = readinessPreservation{read: true, ingest: true, source: true}
	}
	policyBytes, err := proto.MarshalOptions{Deterministic: true}.Marshal(object.GetPlaybackPolicy())
	if err != nil {
		return fmt.Errorf("encode playback authority projection: %w", err)
	}
	params := foghorndb.UpsertMediaObjectAuthorityProjectionParams{
		AuthorityID: envelope.GetAuthorityId(), AuthorityVersion: int64(envelope.GetAuthorityVersion()),
		TenantID: object.GetTenantId(), UserID: object.GetUserId(), InternalName: object.GetInternalName(),
		PlaybackID: object.GetPlaybackId(), Lifecycle: lifecycle(object.GetLifecycle()), OriginClusterID: object.GetOriginClusterId(),
		PlaybackPolicyKind: playbackPolicyKind(object.GetPlaybackPolicy().GetKind()), PlaybackPolicy: policyBytes,
		ValidUntil: envelope.GetValidUntil().AsTime(), PreserveLocalReadReady: preserve.read,
		PreserveLocalIngestReady: preserve.ingest, PreserveLocalSourceReady: preserve.source,
	}
	switch object.GetObjectKind() {
	case mediaauthoritypb.MediaObjectKind_MEDIA_OBJECT_KIND_LIVE_STREAM:
		params.ObjectKind = "live_stream"
		params.StreamID = object.GetLiveStream().GetStreamId()
		params.IngestMode = object.GetLiveStream().GetIngestMode()
		params.PublishingCredentialSha256 = object.GetLiveStream().GetPublishingCredentialSha256()
	case mediaauthoritypb.MediaObjectKind_MEDIA_OBJECT_KIND_ARTIFACT:
		params.ObjectKind = "artifact"
		params.ArtifactID = object.GetArtifact().GetArtifactId()
		params.ArtifactHash = object.GetArtifact().GetArtifactHash()
		params.ArtifactKind = artifactKind(object.GetArtifact().GetArtifactKind())
	}
	if err := queries.UpsertMediaObjectAuthorityProjection(ctx, params); err != nil {
		return fmt.Errorf("apply media-object authority projection: %w", err)
	}
	if object.GetObjectKind() == mediaauthoritypb.MediaObjectKind_MEDIA_OBJECT_KIND_ARTIFACT &&
		object.GetLifecycle() == mediaauthoritypb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_TOMBSTONE {
		artifact := object.GetArtifact()
		if _, err := queries.TombstoneFederatedArtifact(ctx, foghorndb.TombstoneFederatedArtifactParams{
			ArtifactHash: artifact.GetArtifactHash(), TenantID: object.GetTenantId(),
		}); err != nil {
			return fmt.Errorf("tombstone federated artifact pointer: %w", err)
		}
		if _, err := queries.SettleFederatedArtifactCatalogRevision(ctx, foghorndb.SettleFederatedArtifactCatalogRevisionParams{
			ArtifactHash: artifact.GetArtifactHash(), TenantID: object.GetTenantId(),
		}); err != nil {
			return fmt.Errorf("settle tombstoned federated artifact pointer: %w", err)
		}
	}
	return nil
}

func (s *Store) readinessPreservation(_ foghorndb.GetMediaAuthorityForUpdateRow, currentErr error, _ *sharedauthority.Verified) readinessPreservation {
	if currentErr != nil {
		return readinessPreservation{}
	}
	// Readiness is a one-time compatibility cutover for this verified schema,
	// not approval of one payload version. A successfully verified higher
	// version keeps whichever decision surfaces were already promoted; schema
	// changes are rejected by verification until the consumer is upgraded.
	return readinessPreservation{read: true, ingest: true, source: true}
}

func (s *Store) recordVerificationFailure(ctx context.Context, signed *mediaauthoritypb.SignedAuthorityEnvelope, cause error) error {
	return insertAudit(ctx, foghorndb.New(s.db), signed, "verification_rejected", cause.Error())
}

func insertAudit(ctx context.Context, queries *foghorndb.Queries, signed *mediaauthoritypb.SignedAuthorityEnvelope, outcome, reason string) error {
	params := foghorndb.InsertMediaAuthorityApplyAuditParams{Outcome: outcome, Reason: reason}
	if envelope := signed.GetEnvelope(); envelope != nil {
		params.AuthorityKind = authorityKind(envelope.GetKind())
		params.AuthorityID = envelope.GetAuthorityId()
		params.AuthorityVersion = sql.NullInt64{Int64: int64(envelope.GetAuthorityVersion()), Valid: envelope.GetAuthorityVersion() > 0}
		params.SignerKeyID = envelope.GetSignerKeyId()
		params.PayloadSha256 = envelope.GetPayloadSha256()
	}
	return queries.InsertMediaAuthorityApplyAudit(ctx, params)
}

type sourceRevisionJSON struct {
	Service  string `json:"service"`
	Revision string `json:"revision"`
}

func marshalSourceRevisions(revisions []*mediaauthoritypb.AuthoritySourceRevision) (json.RawMessage, error) {
	out := make([]sourceRevisionJSON, 0, len(revisions))
	for _, revision := range revisions {
		out = append(out, sourceRevisionJSON{Service: revision.GetService(), Revision: revision.GetRevision()})
	}
	encoded, err := json.Marshal(out)
	if err != nil {
		return nil, fmt.Errorf("encode media authority source revisions: %w", err)
	}
	return encoded, nil
}

type allowanceJSON struct {
	Meter      string  `json:"meter"`
	Included   float64 `json:"included"`
	Used       float64 `json:"used"`
	Remaining  float64 `json:"remaining"`
	Exhausted  bool    `json:"exhausted"`
	IsFreeTier bool    `json:"is_free_tier"`
}

func marshalAllowances(tenant *mediaauthoritypb.TenantAuthority) (json.RawMessage, error) {
	out := make([]allowanceJSON, 0, len(tenant.GetAllowances()))
	for _, allowance := range tenant.GetAllowances() {
		out = append(out, allowanceJSON{
			Meter: allowance.GetMeter(), Included: allowance.GetIncluded(), Used: allowance.GetUsed(),
			Remaining: allowance.GetRemaining(), Exhausted: allowance.GetExhausted(), IsFreeTier: allowance.GetIsFreeTier(),
		})
	}
	encoded, err := json.Marshal(out)
	if err != nil {
		return nil, fmt.Errorf("encode tenant authority allowances: %w", err)
	}
	return encoded, nil
}

func authorityKind(value mediaauthoritypb.AuthorityKind) string {
	switch value {
	case mediaauthoritypb.AuthorityKind_AUTHORITY_KIND_TENANT:
		return "tenant"
	case mediaauthoritypb.AuthorityKind_AUTHORITY_KIND_MEDIA_OBJECT:
		return "media_object"
	default:
		return ""
	}
}

func lifecycle(value mediaauthoritypb.AuthorityLifecycle) string {
	switch value {
	case mediaauthoritypb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_ACTIVE:
		return "active"
	case mediaauthoritypb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_INACTIVE:
		return "inactive"
	case mediaauthoritypb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_TOMBSTONE:
		return "tombstone"
	default:
		return ""
	}
}

func billingDecision(value mediaauthoritypb.TenantBillingDecision) string {
	switch value {
	case mediaauthoritypb.TenantBillingDecision_TENANT_BILLING_DECISION_ALLOW:
		return "allow"
	case mediaauthoritypb.TenantBillingDecision_TENANT_BILLING_DECISION_PAYMENT_REQUIRED:
		return "payment_required"
	case mediaauthoritypb.TenantBillingDecision_TENANT_BILLING_DECISION_SUSPENDED:
		return "suspended"
	case mediaauthoritypb.TenantBillingDecision_TENANT_BILLING_DECISION_INACTIVE:
		return "inactive"
	default:
		return ""
	}
}

func billingModel(value mediaauthoritypb.TenantBillingModel) string {
	switch value {
	case mediaauthoritypb.TenantBillingModel_TENANT_BILLING_MODEL_POSTPAID:
		return "postpaid"
	case mediaauthoritypb.TenantBillingModel_TENANT_BILLING_MODEL_PREPAID:
		return "prepaid"
	default:
		return "unspecified"
	}
}

func playbackPolicyKind(value mediaauthoritypb.PlaybackPolicyKind) string {
	switch value {
	case mediaauthoritypb.PlaybackPolicyKind_PLAYBACK_POLICY_KIND_PUBLIC:
		return "public"
	case mediaauthoritypb.PlaybackPolicyKind_PLAYBACK_POLICY_KIND_JWT:
		return "jwt"
	case mediaauthoritypb.PlaybackPolicyKind_PLAYBACK_POLICY_KIND_WEBHOOK:
		return "webhook"
	case mediaauthoritypb.PlaybackPolicyKind_PLAYBACK_POLICY_KIND_DENY:
		return "deny"
	default:
		return ""
	}
}

func artifactKind(value mediaauthoritypb.ArtifactKind) string {
	switch value {
	case mediaauthoritypb.ArtifactKind_ARTIFACT_KIND_VOD:
		return "vod"
	case mediaauthoritypb.ArtifactKind_ARTIFACT_KIND_DVR:
		return "dvr"
	case mediaauthoritypb.ArtifactKind_ARTIFACT_KIND_CLIP:
		return "clip"
	case mediaauthoritypb.ArtifactKind_ARTIFACT_KIND_CHAPTER:
		return "chapter"
	default:
		return ""
	}
}

func accessSource(value clusterpeerpb.TenantClusterAccessSource) string {
	switch value {
	case clusterpeerpb.TenantClusterAccessSource_TENANT_CLUSTER_ACCESS_SOURCE_PLATFORM_TIER:
		return "platform_tier"
	case clusterpeerpb.TenantClusterAccessSource_TENANT_CLUSTER_ACCESS_SOURCE_OWNER:
		return "owner"
	case clusterpeerpb.TenantClusterAccessSource_TENANT_CLUSTER_ACCESS_SOURCE_PRIVATE_INVITE:
		return "private_invite"
	case clusterpeerpb.TenantClusterAccessSource_TENANT_CLUSTER_ACCESS_SOURCE_MARKETPLACE_SUBSCRIPTION:
		return "marketplace_subscription"
	case clusterpeerpb.TenantClusterAccessSource_TENANT_CLUSTER_ACCESS_SOURCE_OPERATOR_OVERRIDE:
		return "operator_override"
	default:
		return ""
	}
}

func cloneTrust(input sharedauthority.TrustSet) sharedauthority.TrustSet {
	out := make(sharedauthority.TrustSet, len(input))
	for keyID, publicKey := range input {
		out[keyID] = bytes.Clone(publicKey)
	}
	return out
}

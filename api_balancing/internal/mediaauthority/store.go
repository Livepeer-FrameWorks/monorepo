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
	"github.com/Livepeer-FrameWorks/monorepo/pkg/database"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
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
	Status       ApplyStatus
	Kind         string
	ID           string
	Version      uint64
	Refreshed    bool
	TenantID     string
	StreamID     string
	InternalName string
	// Confirmed reports that this apply made an authority the restore fence was
	// holding back usable again, even when the version was one already held.
	Confirmed bool
}

type Store struct {
	db             *sql.DB
	cellID         string
	trust          sharedauthority.TrustSet
	now            func() time.Time
	sealKeyID      string
	sealPrivateKey *ecdh.PrivateKey
	refresh        *refreshCoordinator
	fetcher        *fetchCoordinator
	uses           *useRecorder
	fence          *restoreFence
	recovery       recoveryCoordinator
	fetchOutcomes  *prometheus.CounterVec
	runtimePeers   RuntimePeerResolver
	servedCluster  func(clusterID string) bool
	applyOutcomes  *prometheus.CounterVec
	applyObserver  func(context.Context, ApplyResult) error
	logger         logging.Logger
}

// SetLogger installs the logger used for rejected applies. Accepted and
// duplicate applies stay silent; they are the steady-state delivery volume.
func (s *Store) SetLogger(logger logging.Logger) {
	if s != nil {
		s.logger = logger
	}
}

// logRejectedApply records why a signed authority was refused. heldVersion is
// zero when this cell holds no authority for the identity.
func (s *Store) logRejectedApply(result ApplyResult, outcome string, heldVersion int64, cause error) {
	if s == nil || s.logger == nil {
		return
	}
	s.logger.WithError(cause).WithFields(logging.Fields{
		"authority_kind":   result.Kind,
		"authority_id":     result.ID,
		"incoming_version": result.Version,
		"held_version":     heldVersion,
		"outcome":          outcome,
		"cell_id":          s.cellID,
	}).Warn("Rejected signed media authority")
}

// SetApplyObserver installs a post-commit observer. Returning an error makes
// authority delivery retry; duplicate delivery invokes the observer again, so
// a transient reconciliation failure cannot lose a live target mutation.
func (s *Store) SetApplyObserver(observer func(context.Context, ApplyResult) error) {
	if s != nil {
		s.applyObserver = observer
	}
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

// SetServedClusterResolver tells the routing overlay which clusters this cell
// serves itself. A tenant's private virtual cluster is served by the cell's
// own Foghorn and Mist nodes: it is neither the cell's physical cluster id nor
// a federation peer with an address, so without this it would be filtered
// out of routing peers and every viewer on it refused as "not authorized".
func (s *Store) SetServedClusterResolver(served func(clusterID string) bool) {
	if s != nil {
		s.servedCluster = served
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
	return &Store{db: db, cellID: strings.TrimSpace(cellID), trust: cloneTrust(trust), now: time.Now, refresh: &refreshCoordinator{}, fetcher: &fetchCoordinator{}, uses: newUseRecorder(), fence: &restoreFence{}}, nil
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
	result, applyErr = s.apply(ctx, encoded, time.Time{}, 0)
	if result.Kind != "media_object" || (result.Status != ApplyStatusApplied && result.Status != ApplyStatusDuplicate) {
		return result, applyErr
	}
	if applyErr != nil {
		return result, applyErr
	}
	required, err := foghorndb.New(s.db).MediaAuthorityConfirmationRequired(ctx, foghorndb.MediaAuthorityConfirmationRequiredParams{AuthorityID: result.ID, AsOf: s.now().UTC()})
	if err != nil {
		return result, fmt.Errorf("check authority confirmation: %w", err)
	}
	if required {
		s.fence.requireRecovery()
		return result, ErrAuthorityConfirmationRequired
	}
	return result, nil
}

var ErrAuthorityConfirmationRequired = errors.New("persisted media authority awaits current-version confirmation after tenant revival")

func (s *Store) apply(ctx context.Context, encoded []byte, confirmedAt time.Time, fenceGeneration uint64) (result ApplyResult, applyErr error) {
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
		// Post-commit reconciliation may retry while a trust barrier is up;
		// the signed authority itself was persisted successfully in that case.
		if result.Status == ApplyStatusApplied || result.Status == ApplyStatusDuplicate {
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
	if tenant := verified.Tenant; tenant != nil {
		result.TenantID = tenant.GetTenantId()
	}
	if object := verified.MediaObject; object != nil {
		result.TenantID = object.GetTenantId()
		result.InternalName = object.GetInternalName()
		result.StreamID = object.GetLiveStream().GetStreamId()
	}

	// The callback is replayed on retryable transaction errors, so it records
	// its decision in these locals and the observer runs only after commit.
	var (
		stage       = "begin"
		commitLabel string
		rejectErr   error
		duplicate   bool
		revived     bool
		heldVersion int64
	)
	txErr := database.WithRetryablePostgresTxWithHook(ctx, s.db, nil, func(error, int) { stage = "begin" }, func(tx *sql.Tx) error {
		stage = "body"
		commitLabel = ""
		rejectErr = nil
		duplicate = false
		revived = false
		heldVersion = 0
		metricOutcome = "persist_error"
		queries := foghorndb.New(tx)
		if err := queries.SetLocalMediaAuthorityLockTimeout(ctx, mediaAuthorityLockTimeout.String()); err != nil {
			return fmt.Errorf("bound media authority lock wait: %w", err)
		}
		// Artifact authority and the derivative purge/publication saga share one
		// lock domain. Take the asset lock before the authority-identity lock so a
		// concurrent active projection cannot commit in the absence-check window of
		// a stale purge, and establish a single lock order for future callers.
		if object := verified.MediaObject; object != nil && object.GetObjectKind() == mediaauthoritypb.MediaObjectKind_MEDIA_OBJECT_KIND_ARTIFACT {
			assetKey := strings.TrimSpace(object.GetArtifact().GetArtifactHash())
			if assetKey == "" {
				return errors.New("artifact media authority requires artifact hash")
			}
			if err := queries.LockThumbnailAsset(ctx, foghorndb.LockThumbnailAssetParams{
				LockNamespace: artifacts.ThumbnailAssetLockNamespace,
				AssetKey:      assetKey,
			}); err != nil {
				return fmt.Errorf("lock media-object asset: %w", err)
			}
		}
		identity := foghorndb.LockMediaAuthorityParams{
			LockNamespace: mediaAuthorityLockNamespace,
			AuthorityKind: kind,
			AuthorityID:   envelope.GetAuthorityId(),
		}
		if err := queries.LockMediaAuthority(ctx, identity); err != nil {
			return fmt.Errorf("lock media authority: %w", err)
		}

		current, currentErr := queries.GetMediaAuthorityForUpdate(ctx, foghorndb.GetMediaAuthorityForUpdateParams{
			AuthorityKind: kind,
			AuthorityID:   envelope.GetAuthorityId(),
		})
		if currentErr == nil {
			heldVersion = current.AuthorityVersion
		}
		switch {
		case currentErr == nil && current.AuthorityVersion > int64(envelope.GetAuthorityVersion()):
			metricOutcome = "stale_version_rejected"
			if err := insertAudit(ctx, queries, signed, "stale_version_rejected", ErrRollback.Error()); err != nil {
				return fmt.Errorf("record media authority rollback: %w", err)
			}
			commitLabel, rejectErr = "commit media authority rollback audit", ErrRollback
			stage = "commit"
			return nil
		case currentErr == nil && current.AuthorityVersion == int64(envelope.GetAuthorityVersion()) && !bytes.Equal(current.PayloadSha256, envelope.GetPayloadSha256()):
			metricOutcome = "conflict_rejected"
			if err := insertAudit(ctx, queries, signed, "conflict_rejected", ErrVersionConflict.Error()); err != nil {
				return fmt.Errorf("record media authority conflict: %w", err)
			}
			commitLabel, rejectErr = "commit media authority conflict audit", ErrVersionConflict
			stage = "commit"
			return nil
		case currentErr == nil && current.AuthorityVersion == int64(envelope.GetAuthorityVersion()):
			if err := insertAudit(ctx, queries, signed, "duplicate", ""); err != nil {
				return fmt.Errorf("record duplicate media authority: %w", err)
			}
			if err := confirmFetchedAuthority(ctx, queries, result, confirmedAt); err != nil {
				return err
			}
			commitLabel, duplicate = "commit duplicate media authority audit", true
			stage = "commit"
			return nil
		case currentErr != nil && !errors.Is(currentErr, sql.ErrNoRows):
			return fmt.Errorf("load current media authority: %w", currentErr)
		}
		if terminal, err := rejectsObjectTombstoneResurrection(current, currentErr == nil, verified); err != nil {
			return err
		} else if terminal {
			metricOutcome = "terminal_lifecycle_rejected"
			if err := insertAudit(ctx, queries, signed, "terminal_lifecycle_rejected", ErrTombstoneTerminal.Error()); err != nil {
				return fmt.Errorf("record media authority terminal lifecycle rejection: %w", err)
			}
			commitLabel, rejectErr = "commit media authority terminal lifecycle audit", ErrTombstoneTerminal
			stage = "commit"
			return nil
		}
		if currentErr == nil {
			rollback, revisionErr := rejectsPlacementRollback(current.Payload, verified)
			if revisionErr != nil {
				return revisionErr
			}
			if rollback {
				metricOutcome = "rollback_rejected"
				if err := insertAudit(ctx, queries, signed, "rollback_rejected", ErrRollback.Error()); err != nil {
					return fmt.Errorf("record placement rollback: %w", err)
				}
				commitLabel, rejectErr = "commit placement rollback audit", ErrRollback
				stage = "commit"
				return nil
			}
		}

		sourceRevisions, err := marshalSourceRevisions(envelope.GetSourceRevisions())
		if err != nil {
			return err
		}
		if err := queries.UpsertMediaAuthority(ctx, foghorndb.UpsertMediaAuthorityParams{
			AuthorityKind: kind, AuthorityID: envelope.GetAuthorityId(), AuthorityVersion: int64(envelope.GetAuthorityVersion()),
			SignerKeyID: envelope.GetSignerKeyId(), AudienceCellID: envelope.GetAudienceCellId(),
			IssuedAt: envelope.GetIssuedAt().AsTime(), RefreshAfter: envelope.GetRefreshAfter().AsTime(), ValidUntil: envelope.GetValidUntil().AsTime(),
			PayloadSha256: envelope.GetPayloadSha256(), SignedEnvelope: encoded, Payload: envelope.GetPayload(), SourceRevisions: sourceRevisions,
		}); err != nil {
			return fmt.Errorf("persist verified media authority: %w", err)
		}
		preserve := s.readinessPreservation(current, currentErr, verified)
		if err := applyProjection(ctx, queries, verified, preserve); err != nil {
			return err
		}
		// Reviving a tenant must not make stale object copies usable. A current
		// fetch begun after this barrier must confirm each retained object.
		revived = verified.Tenant != nil && currentErr == nil && !current.ValidUntil.After(s.now().UTC())
		if revived {
			if err := queries.WithholdTenantObjectsAppliedBefore(ctx, foghorndb.WithholdTenantObjectsAppliedBeforeParams{
				TenantID: verified.Tenant.GetTenantId(), ConfirmedAt: sql.NullTime{Time: confirmedAt, Valid: !confirmedAt.IsZero()},
			}); err != nil {
				return fmt.Errorf("withhold objects of a revived tenant: %w", err)
			}
		}
		if err := promotePlacementReadiness(ctx, queries, verified); err != nil {
			return fmt.Errorf("promote schema-2 media authority readiness: %w", err)
		}
		if err := insertAudit(ctx, queries, signed, "applied", ""); err != nil {
			return fmt.Errorf("record applied media authority: %w", err)
		}
		if err := confirmFetchedAuthority(ctx, queries, result, confirmedAt); err != nil {
			return err
		}
		commitLabel = "commit media authority apply"
		stage = "commit"
		return nil
	})
	if txErr != nil {
		switch stage {
		case "begin":
			return ApplyResult{}, fmt.Errorf("begin media authority apply: %w", txErr)
		case "commit":
			return ApplyResult{}, fmt.Errorf("%s: %w", commitLabel, txErr)
		default:
			return ApplyResult{}, txErr
		}
	}
	if rejectErr != nil {
		s.logRejectedApply(result, metricOutcome, heldVersion, rejectErr)
		return result, rejectErr
	}
	if duplicate {
		result.Status = ApplyStatusDuplicate
	} else {
		result.Status = ApplyStatusApplied
	}
	if revived {
		s.fence.requireRecovery()
	}
	if !confirmedAt.IsZero() {
		result.Confirmed = s.fence.confirm(result.Kind, result.ID, fenceGeneration)
	}
	if s.applyObserver != nil {
		return result, s.applyObserver(ctx, result)
	}
	return result, nil
}

func confirmFetchedAuthority(ctx context.Context, queries *foghorndb.Queries, result ApplyResult, startedAt time.Time) error {
	if startedAt.IsZero() {
		return nil
	}
	if err := queries.ConfirmMediaAuthority(ctx, foghorndb.ConfirmMediaAuthorityParams{
		AuthorityKind: result.Kind, AuthorityID: result.ID, AuthorityVersion: int64(result.Version), ConfirmedAt: startedAt,
	}); err != nil {
		return fmt.Errorf("confirm fetched media authority: %w", err)
	}
	return nil
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

func (s *Store) readinessPreservation(current foghorndb.GetMediaAuthorityForUpdateRow, currentErr error, verified *sharedauthority.Verified) readinessPreservation {
	if currentErr != nil {
		return readinessPreservation{}
	}
	previousSchema, _, _, err := storedPlacementRevision(current.Payload, verified.Tenant != nil)
	if err != nil || previousSchema != verifiedPlacementSchema(verified) {
		return readinessPreservation{}
	}
	// Readiness is a one-time compatibility cutover for this verified schema,
	// not approval of one payload version. A successfully verified higher
	// version keeps promoted surfaces only within the same schema. A new schema
	// resets every surface and requires its own readiness proof before admission.
	return readinessPreservation{read: true, ingest: true, source: true}
}

func verifiedPlacementSchema(verified *sharedauthority.Verified) uint32 {
	if verified.Tenant != nil {
		return verified.Tenant.GetSchemaVersion()
	}
	return verified.MediaObject.GetSchemaVersion()
}

func storedPlacementRevision(payload []byte, tenant bool) (schema uint32, revision, parent uint64, err error) {
	if tenant {
		value := &mediaauthoritypb.TenantAuthority{}
		if err = proto.Unmarshal(payload, value); err == nil {
			return value.GetSchemaVersion(), value.GetMediaPlacement().GetRevision(), 0, nil
		}
	} else {
		value := &mediaauthoritypb.MediaObjectAuthority{}
		if err = proto.Unmarshal(payload, value); err == nil {
			return value.GetSchemaVersion(), value.GetMediaPlacement().GetRevision(), value.GetPlacementTenantRevision(), nil
		}
	}
	return 0, 0, 0, fmt.Errorf("decode current placement revision: %w", err)
}

// Authority delivery versions and policy revisions are independent fences. A
// restarted or rolled-back compiler cannot erase a policy by allocating a newer
// delivery version around an older policy or a lossy legacy-schema payload.
func rejectsPlacementRollback(payload []byte, verified *sharedauthority.Verified) (bool, error) {
	schema, revision, parent, err := storedPlacementRevision(payload, verified.Tenant != nil)
	if err != nil {
		return false, err
	}
	if schema > verifiedPlacementSchema(verified) {
		return true, nil
	}
	if verified.Tenant != nil {
		return revision > verified.Tenant.GetMediaPlacement().GetRevision(), nil
	}
	return revision > verified.MediaObject.GetMediaPlacement().GetRevision() || parent > verified.MediaObject.GetPlacementTenantRevision(), nil
}

func (s *Store) recordVerificationFailure(ctx context.Context, signed *mediaauthoritypb.SignedAuthorityEnvelope, cause error) error {
	rejected := ApplyResult{}
	if envelope := signed.GetEnvelope(); envelope != nil {
		rejected = ApplyResult{Kind: authorityKind(envelope.GetKind()), ID: envelope.GetAuthorityId(), Version: envelope.GetAuthorityVersion()}
	}
	s.logRejectedApply(rejected, "verification_rejected", 0, cause)
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

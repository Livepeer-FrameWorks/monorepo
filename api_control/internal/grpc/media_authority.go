package grpc

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"frameworks/api_control/internal/database/commodoredb"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/database"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	sharedauthority "github.com/Livepeer-FrameWorks/monorepo/pkg/mediaauthority"
	clusterpeerpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/cluster_peer"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	foghornpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn"
	mediaauthoritypb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_authority"
	meteringpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/metering_contract"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	"golang.org/x/sync/errgroup"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const (
	// Reconciliation is the safety net behind event-driven refresh and scheduled
	// renewal: it recompiles tenants (publishing only on drift), re-arms parked
	// targets, and repairs missing renewal obligations.
	mediaAuthorityReconcileInterval = time.Hour
	mediaAuthorityValidity          = 24 * time.Hour
	mediaAuthorityWorkerInterval    = time.Second
	mediaAuthorityLease             = 2 * time.Minute
	mediaAuthorityRefreshBatch      = 8
	mediaAuthorityDeliveryBatch     = 8
	mediaAuthorityRefreshWorkers    = 8
	mediaAuthorityDeliveryWorkers   = 8
	mediaAuthorityRefreshTimeout    = 90 * time.Second
	mediaAuthorityDeliveryTimeout   = 35 * time.Second
	mediaAuthoritySettleTimeout     = 5 * time.Second
	mediaAuthorityStatsTimeout      = 5 * time.Second
	mediaAuthorityHistoryRetention  = 30 * 24 * time.Hour
	mediaAuthorityInboxRetention    = 7 * 24 * time.Hour
	mediaAuthorityRetentionInterval = time.Hour
	// A sweep deletes in batches until nothing old is left or its time is up. A
	// fixed number of batches an hour is a fixed ceiling on deletion, and history
	// that is written faster than that ceiling never stops growing.
	mediaAuthorityRetentionTimeout = 10 * time.Minute
	mediaAuthorityRetentionBatch   = 1000
	// Raised in the change that makes the compiler produce a different payload
	// for unchanged source state. Reconciliation then re-issues every authority
	// in use once; nothing else would, because an unchanged compile publishes
	// nothing.
	mediaAuthorityCompilerRevision = 1
	// Queue gauges and the sweeps that settle deliveries nothing will ever make.
	// Neither is needed to deliver, so they run off the one-second delivery tick.
	mediaAuthorityReplayClockTolerance = 5 * time.Minute
	mediaAuthorityQueueObserveInterval = 30 * time.Second
	mediaAuthorityQueueSweepBatch      = 500
)

type mediaAuthorityCompileFence struct {
	scopeKey   string
	generation int64
}

type mediaAuthorityCompileFenceContextKey struct{}

type mediaAuthorityTenantSource interface {
	GetTenant(ctx context.Context, tenantID string) (*quartermasterpb.GetTenantResponse, error)
	GetTenantEntitlement(ctx context.Context, tenantID string) (*quartermasterpb.GetTenantEntitlementResponse, error)
	ListActiveTenants(ctx context.Context) ([]string, error)
}

type mediaAuthorityBillingSource interface {
	GetTenantBillingStatus(ctx context.Context, tenantID string) (*purserpb.GetTenantBillingStatusResponse, error)
	GetTenantAdmissionStatus(ctx context.Context, tenantID string) (*purserpb.GetTenantAdmissionStatusResponse, error)
}

// RequestMediaAuthorityRefresh durably accepts an owner-service change. It
// does not compile inline: Purser or Quartermaster recovery must not be coupled
// to Foghorn reachability or to every target cell acknowledging immediately.
func (s *CommodoreServer) RequestMediaAuthorityRefresh(ctx context.Context, req *commodorepb.RequestMediaAuthorityRefreshRequest) (*commodorepb.RequestMediaAuthorityRefreshResponse, error) {
	if ctxkeys.GetAuthType(ctx) != "service" {
		return nil, status.Error(codes.PermissionDenied, "media authority refresh requires service authentication")
	}
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "request is required")
	}
	source := strings.TrimSpace(req.GetSourceService())
	if source != "purser" && source != "quartermaster" && source != "commodore" {
		return nil, status.Error(codes.InvalidArgument, "source_service must be purser, quartermaster, or commodore")
	}
	if strings.TrimSpace(req.GetSourceEventId()) == "" || strings.TrimSpace(req.GetTenantId()) == "" || strings.TrimSpace(req.GetReason()) == "" {
		return nil, status.Error(codes.InvalidArgument, "source_event_id, tenant_id, and reason are required")
	}
	tenantID, reason := strings.TrimSpace(req.GetTenantId()), strings.TrimSpace(req.GetReason())
	// Accepted means the obligation is durable. A redelivered owner event folds
	// into the target's existing obligation and costs at most one compile, which
	// publishes nothing when the content is unchanged.
	queries := commodoredb.New(s.db)
	if err := queries.EnqueueMediaAuthorityEvent(ctx, commodoredb.MediaAuthorityTargetForReason(tenantID, reason),
		tenantID, reason, source, strings.TrimSpace(req.GetSourceEventId())); err != nil {
		return nil, status.Errorf(codes.Internal, "persist media authority refresh: %v", err)
	}
	if purserReasonChangesMediaObjects(source, reason) {
		if err := queries.EnqueueMediaAuthorityEvent(ctx, commodoredb.TenantMediaObjectsAuthorityTarget(tenantID),
			tenantID, "tenant_media_objects:"+reason, source, strings.TrimSpace(req.GetSourceEventId())); err != nil {
			return nil, status.Errorf(codes.Internal, "persist media-object refresh: %v", err)
		}
	}
	return &commodorepb.RequestMediaAuthorityRefreshResponse{Accepted: true}, nil
}

// purserReasonChangesMediaObjects reports a billing change that media objects
// read from Purser directly. A stream's process configuration comes from the
// tenant's tier and subscription, not from the tenant authority, so the tenant
// compile cannot detect it and the objects are refreshed alongside it: a tenant
// moving to another tier, or the tier's own definition changing. Every other
// Purser reason (usage, balance, allowances, entitlements) touches only the
// tenant authority; refreshing every object for those would turn metering into
// O(objects) compiles.
func purserReasonChangesMediaObjects(sourceService, reason string) bool {
	if sourceService != "purser" {
		return false
	}
	switch reason {
	case "subscription_authority_changed", "billing_tier_authority_changed":
		return true
	default:
		return false
	}
}

// RequestMediaAuthorityReplay checks a cell's held summary, then requests
// bounded inventory pages on mismatch. Only a proven loss of acknowledged
// state reopens delivery; ordinary applies ahead of their ACKs do not.
func (s *CommodoreServer) RequestMediaAuthorityReplay(ctx context.Context, req *commodorepb.RequestMediaAuthorityReplayRequest) (*commodorepb.RequestMediaAuthorityReplayResponse, error) {
	if ctxkeys.GetAuthType(ctx) != "service" {
		return nil, status.Error(codes.PermissionDenied, "media authority replay requires service authentication")
	}
	if req == nil || strings.TrimSpace(req.GetControlCellId()) == "" {
		return nil, status.Error(codes.InvalidArgument, "control_cell_id is required")
	}
	queries := commodoredb.New(s.db)
	cellID := strings.TrimSpace(req.GetControlCellId())
	if req.GetAsOf() == nil || !req.GetAsOf().IsValid() {
		return nil, status.Error(codes.InvalidArgument, "a valid authority summary timestamp is required")
	}
	if at := req.GetAsOf().AsTime(); !at.After(time.Now().Add(-mediaAuthorityReplayClockTolerance)) || !at.Before(time.Now().Add(mediaAuthorityReplayClockTolerance)) {
		return nil, status.Error(codes.FailedPrecondition, "authority summary clock differs by more than five minutes; synchronize the cell and control-plane clocks")
	}
	if req.GetInventoryPage() {
		return s.reconcileMediaAuthorityPage(ctx, req)
	}
	observedAt, err := queries.MediaAuthorityRecoveryTime(ctx)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "read recovery clock: %v", err)
	}
	var onRecord sharedauthority.HeldSummary
	page := commodoredb.ListAcknowledgedMediaAuthoritiesForCellParams{
		CellID: cellID, AsOf: req.GetAsOf().AsTime(), PageSize: sharedauthority.RecoveryPageSize,
	}
	for {
		acknowledged, listErr := queries.ListAcknowledgedMediaAuthoritiesForCell(ctx, page)
		if listErr != nil {
			return nil, status.Errorf(codes.Internal, "list acknowledged media authorities: %v", listErr)
		}
		for _, row := range acknowledged {
			onRecord.Add(row.AuthorityKind, row.AuthorityID, row.AuthorityVersion)
			page.AfterKind, page.AfterID = row.AuthorityKind, row.AuthorityID
		}
		if len(acknowledged) < int(page.PageSize) {
			break
		}
	}
	if onRecord.Matches(req.GetHeldCount(), req.GetHeldDigest()) {
		return &commodorepb.RequestMediaAuthorityReplayResponse{RecoveryProtocol: sharedauthority.RecoveryProtocol, SummaryChecked: true, HeldMatches: true, ObservedAt: timestamppb.New(observedAt)}, nil
	}
	// A digest mismatch has no direction: applies ahead of acknowledgement and
	// restored state both differ. Inspect bounded identity ranges before mutation.
	return &commodorepb.RequestMediaAuthorityReplayResponse{RecoveryProtocol: sharedauthority.RecoveryProtocol, InventoryRequired: true, ObservedAt: timestamppb.New(observedAt)}, nil
}

func (s *CommodoreServer) mediaAuthorityEnabled() bool {
	return s.db != nil && s.authorityTenantSource != nil && s.authorityBillingSource != nil &&
		strings.TrimSpace(s.mediaAuthorityKeyID) != "" && len(s.mediaAuthorityPrivateKey) != 0
}

func (s *CommodoreServer) compileTenantAuthority(ctx context.Context, tenantID string) error {
	if !s.mediaAuthorityEnabled() {
		return errors.New("media authority compiler is not configured")
	}
	tenantID = strings.TrimSpace(tenantID)
	if tenantID == "" {
		return errors.New("tenant ID is required")
	}
	ctx, skip, useErr := s.decideTenantInUse(ctx, commodoredb.New(s.db), tenantID)
	if useErr != nil || skip {
		return useErr
	}

	var tenantResp *quartermasterpb.GetTenantResponse
	if err := func() error {
		lookupCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		var err error
		tenantResp, err = s.authorityTenantSource.GetTenant(lookupCtx, tenantID)
		return err
	}(); err != nil {
		return fmt.Errorf("load tenant authority identity: %w", err)
	}
	if tenantAuthorityNotFound(tenantResp) {
		return s.compileDeletedTenantAuthority(ctx, tenantID)
	}
	tenant := tenantResp.GetTenant()
	if tenant == nil || tenant.GetId() != tenantID {
		return errors.New("tenant authority identity was not found")
	}

	var entitlement *quartermasterpb.GetTenantEntitlementResponse
	var billing *purserpb.GetTenantBillingStatusResponse
	var admission *purserpb.GetTenantAdmissionStatusResponse
	if tenant.GetIsActive() {
		group, groupCtx := errgroup.WithContext(ctx)
		group.Go(func() error {
			lookupCtx, cancel := context.WithTimeout(groupCtx, 10*time.Second)
			defer cancel()
			var err error
			entitlement, err = s.authorityTenantSource.GetTenantEntitlement(lookupCtx, tenantID)
			if err != nil {
				return fmt.Errorf("load tenant cluster authority: %w", err)
			}
			return nil
		})
		group.Go(func() error {
			lookupCtx, cancel := context.WithTimeout(groupCtx, 10*time.Second)
			defer cancel()
			var err error
			admission, err = s.authorityBillingSource.GetTenantAdmissionStatus(lookupCtx, tenantID)
			if err != nil {
				return fmt.Errorf("load tenant admission authority: %w", err)
			}
			return nil
		})
		group.Go(func() error {
			lookupCtx, cancel := context.WithTimeout(groupCtx, 10*time.Second)
			defer cancel()
			var err error
			billing, err = s.authorityBillingSource.GetTenantBillingStatus(lookupCtx, tenantID)
			if err != nil {
				return fmt.Errorf("load tenant billing authority: %w", err)
			}
			return nil
		})
		if err := group.Wait(); err != nil {
			return err
		}
		if entitlement == nil || billing == nil || admission == nil {
			return errors.New("tenant authority source returned an empty response")
		}
	}

	issuedAt := time.Now().UTC()
	payload, targets, validUntil, revisions, err := buildTenantAuthority(tenant, entitlement, billing, admission, issuedAt)
	if err != nil {
		return err
	}
	if placementErr := s.compileTenantPlacement(ctx, payload, entitlement, targets); placementErr != nil {
		return placementErr
	}
	revisions, err = tenantPlacementSourceRevisions(payload, revisions)
	if err != nil {
		return err
	}
	return s.persistTenantAuthority(ctx, payload, targets, revisions, issuedAt, validUntil)
}

func tenantAuthorityNotFound(resp *quartermasterpb.GetTenantResponse) bool {
	return resp != nil && resp.GetTenant() == nil && strings.EqualFold(strings.TrimSpace(resp.GetError()), "tenant not found")
}

func (s *CommodoreServer) compileDeletedTenantAuthority(ctx context.Context, tenantID string) error {
	current, err := commodoredb.New(s.db).GetCurrentMediaAuthorityPayload(ctx, commodoredb.GetCurrentMediaAuthorityPayloadParams{
		AuthorityKind: "tenant", AuthorityID: tenantID,
	})
	if errors.Is(err, sql.ErrNoRows) {
		// No media cell can retain authority for a tenant that was never compiled.
		return nil
	}
	if err != nil {
		return fmt.Errorf("load deleted tenant authority history: %w", err)
	}
	previous := &mediaauthoritypb.TenantAuthority{}
	if unmarshalErr := proto.Unmarshal(current.Payload, previous); unmarshalErr != nil {
		return fmt.Errorf("decode deleted tenant authority history: %w", unmarshalErr)
	}
	if previous.GetTenantId() != tenantID {
		return errors.New("deleted tenant authority identity mismatch")
	}
	payload := deletedTenantAuthorityPayload(tenantID)
	if placementErr := s.inheritTenantPlacement(ctx, payload, nil, previous); placementErr != nil {
		return placementErr
	}
	revisions, err := tenantPlacementSourceRevisions(payload, []*mediaauthoritypb.AuthoritySourceRevision{{Service: "quartermaster", Revision: "deleted"}})
	if err != nil {
		return err
	}
	issuedAt := time.Now().UTC()
	return s.persistTenantAuthority(ctx, payload, nil, revisions, issuedAt, issuedAt.Add(mediaAuthorityValidity))
}

func deletedTenantAuthorityPayload(tenantID string) *mediaauthoritypb.TenantAuthority {
	return &mediaauthoritypb.TenantAuthority{
		SchemaVersion:   sharedauthority.SchemaVersion,
		TenantId:        tenantID,
		Lifecycle:       mediaauthoritypb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_TOMBSTONE,
		BillingDecision: mediaauthoritypb.TenantBillingDecision_TENANT_BILLING_DECISION_INACTIVE,
		DecisionReason:  "tenant_deleted",
	}
}

func (s *CommodoreServer) compileLiveStreamAuthority(ctx context.Context, streamID string) (retErr error) {
	streamID = strings.TrimSpace(streamID)
	if streamID == "" {
		return errors.New("stream ID is required")
	}
	authorityID := sharedauthority.LiveStreamAuthorityID(streamID)
	queries := commodoredb.New(s.db)
	source, err := queries.GetLiveStreamMediaAuthoritySource(ctx, streamID)
	if errors.Is(err, sql.ErrNoRows) {
		return s.compileDeletedMediaObjectAuthority(ctx, authorityID, mediaauthoritypb.MediaObjectKind_MEDIA_OBJECT_KIND_LIVE_STREAM)
	}
	if err != nil {
		return fmt.Errorf("load live-stream authority source: %w", err)
	}
	if source.DeletedAt.Valid {
		return s.compileDeletedMediaObjectAuthority(ctx, authorityID, mediaauthoritypb.MediaObjectKind_MEDIA_OBJECT_KIND_LIVE_STREAM)
	}
	var policy *mediaauthoritypb.PlaybackPolicy
	ctx = withPlaybackAccessSource(ctx, source.RequiresAuth, source.PlaybackPolicy, source.PlaybackWebhookSecretEnc)
	defer func() {
		desiredLive := &mediaauthoritypb.LiveStreamAuthority{IngestMode: source.IngestMode, PublishingCredentialSha256: sharedauthority.PublishingCredentialDigest(source.StreamKey)}
		retErr = s.revokeChangedAccessOnCompileFailure(ctx, authorityID, mediaauthoritypb.MediaObjectKind_MEDIA_OBJECT_KIND_LIVE_STREAM, policy, desiredLive, retErr)
	}()

	age := time.Duration(source.AgeSeconds) * time.Second
	ingesting := source.ActiveIngestClusterID != "" && time.Duration(source.IngestLeaseAgeSeconds)*time.Second < activeIngestLease
	tenant, targets, secretTargets, tenantValidUntil, err := s.currentTenantAuthorityContext(ctx, source.TenantID)
	if err != nil {
		correct, waitErr := s.mediaObjectWithoutTenantAuthority(ctx, queries, authorityID, source.TenantID, age, ingesting, source.DeletedAt.Valid, err)
		if !correct {
			return waitErr
		}
		if tenant, targets, secretTargets, err = s.lapsedTenantAuthorityContext(ctx, source.TenantID); err != nil {
			return err
		}
		ctx = withMediaAuthorityLapsedParent(ctx)
	}
	if tenant.GetLifecycle() == mediaauthoritypb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_TOMBSTONE {
		return s.compileDeletedMediaObjectAuthority(ctx, authorityID, mediaauthoritypb.MediaObjectKind_MEDIA_OBJECT_KIND_LIVE_STREAM)
	}
	if !source.DeletedAt.Valid {
		var skip bool
		ctx, skip, err = s.decideMediaObjectInUse(ctx, queries, authorityID, source.TenantID, age, ingesting, targets)
		if err != nil || skip {
			return err
		}
	}
	lifecycle := mediaauthoritypb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_ACTIVE
	if source.DeletedAt.Valid {
		lifecycle = mediaauthoritypb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_TOMBSTONE
	} else if !tenantCanServeMediaObjects(tenant, secretTargets) {
		lifecycle = mediaauthoritypb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_INACTIVE
	}
	policy = denyPlaybackPolicy()
	var sealedPlayback []*mediaauthoritypb.SealedCellSecret
	var commitments [][]byte
	if lifecycle == mediaauthoritypb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_ACTIVE {
		policy, err = s.compilePlaybackPolicy(ctx, source.TenantID, source.RequiresAuth, source.PlaybackPolicy)
		if err != nil {
			return fmt.Errorf("compile live-stream playback policy: %w", err)
		}
		playbackSecret, playbackErr := s.compilePlaybackWebhookSecret(authorityID, source.TenantID, source.PlaybackPolicy, source.PlaybackWebhookSecretEnc)
		if playbackErr != nil {
			return fmt.Errorf("compile live-stream webhook authority: %w", playbackErr)
		}
		if playbackSecret != nil {
			var commitment []byte
			sealedPlayback, commitment, err = s.sealAuthoritySecret(authorityID, secretTargets, playbackSecret)
			if err != nil {
				return err
			}
			if commitment != nil {
				commitments = append(commitments, commitment)
			}
		}
		if policy.GetKind() == mediaauthoritypb.PlaybackPolicyKind_PLAYBACK_POLICY_KIND_WEBHOOK {
			policy.ConnectedOnly = len(sealedPlayback) == 0
		}
	}
	// The origin of a stream that is not ingesting is the tenant's preferred
	// cluster: the same route origin connected validation reports, which local
	// ingest promotion requires the signed origin to equal. A stream with neither
	// has no origin, which is an ordinary state and not a compile failure.
	originClusterID := strings.TrimSpace(source.ActiveIngestClusterID)
	if originClusterID == "" {
		originClusterID = strings.TrimSpace(tenant.GetPreferredClusterId())
	}
	var processesJSON, dvrProcessesJSON string
	if lifecycle == mediaauthoritypb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_ACTIVE {
		processesJSON, err = s.resolveProcessesJSONStrict(ctx, source.TenantID, source.StreamID, originClusterID, "live")
		if err != nil {
			return fmt.Errorf("compile live-stream processes: %w", err)
		}
		dvrProcessesJSON, err = s.resolveProcessesJSONStrict(ctx, source.TenantID, source.StreamID, originClusterID, "dvr")
		if err != nil {
			return fmt.Errorf("compile live-stream DVR processes: %w", err)
		}
	}
	var sealedSecrets []*mediaauthoritypb.SealedCellSecret
	if lifecycle == mediaauthoritypb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_ACTIVE {
		liveSecret, secretErr := s.compileLiveStreamSecret(ctx, authorityID, source.StreamID, source.TenantID, source.IngestMode)
		if secretErr != nil {
			return secretErr
		}
		var commitment []byte
		sealedSecrets, commitment, err = s.sealLiveStreamSecret(authorityID, secretTargets, liveSecret)
		if err != nil {
			return err
		}
		if commitment != nil {
			commitments = append(commitments, commitment)
		}
	}
	// A publishing credential authorizes ingest at OutageIngestClusterId while
	// the control plane is unreachable, so it is signed only with a cluster to
	// bind it to. Without it the cell finds no local credential and validates the
	// stream key against Commodore, exactly as it does for an unknown stream.
	var publishingCredential []byte
	if originClusterID != "" {
		publishingCredential = sharedauthority.PublishingCredentialDigest(source.StreamKey)
	}
	payload := &mediaauthoritypb.MediaObjectAuthority{
		SchemaVersion:         sharedauthority.SchemaVersion,
		ObjectKind:            mediaauthoritypb.MediaObjectKind_MEDIA_OBJECT_KIND_LIVE_STREAM,
		TenantId:              source.TenantID,
		UserId:                source.UserID,
		InternalName:          source.InternalName,
		PlaybackId:            source.PlaybackID,
		Lifecycle:             lifecycle,
		OriginClusterId:       originClusterID,
		PlaybackPolicy:        policy,
		SealedPlaybackSecrets: sealedPlayback,
		Object: &mediaauthoritypb.MediaObjectAuthority_LiveStream{LiveStream: &mediaauthoritypb.LiveStreamAuthority{
			StreamId: source.StreamID, IngestMode: source.IngestMode,
			PublishingCredentialSha256: publishingCredential,
			OutageIngestClusterId:      originClusterID,
			RecordingEnabled:           source.IsRecordingEnabled,
			ProcessesJson:              processesJSON,
			DvrProcessesJson:           dvrProcessesJSON,
			SealedCellSecrets:          sealedSecrets,
		}},
	}
	issuedAt := time.Now().UTC()
	validUntil := issuedAt.Add(mediaAuthorityValidity)
	if placementErr := s.compileObjectPlacement(ctx, tenant, payload); placementErr != nil {
		return fmt.Errorf("compile live-stream placement: %w", placementErr)
	}
	// A lapsed tenant (a correction, see mediaObjectWithoutTenantAuthority)
	// carries no validity and caps nothing.
	if !tenantValidUntil.IsZero() && tenantValidUntil.Before(validUntil) {
		validUntil = tenantValidUntil
	}
	// Ages move with the clock and are not part of what the stream says.
	source.AgeSeconds, source.IngestLeaseAgeSeconds = 0, 0
	revision, err := hashJSON(source)
	if err != nil {
		return err
	}
	ctx = withMediaAuthorityLongValidity(ctx, issuedAt.Add(sharedauthority.MaxMediaObjectValidity))
	ctx = withMediaObjectParent(ctx, tenant)
	return s.publishMediaObjectAuthority(ctx, authorityID, payload, targets,
		[]*mediaauthoritypb.AuthoritySourceRevision{{Service: "commodore", Revision: revision}}, issuedAt, validUntil, commitments)
}

func (s *CommodoreServer) compileLiveStreamSecret(ctx context.Context, authorityID, streamID, tenantID, ingestMode string) (*mediaauthoritypb.LiveStreamSecret, error) {
	if len(s.mediaAuthorityRecipients) == 0 {
		return nil, nil
	}
	secret := &mediaauthoritypb.LiveStreamSecret{AuthorityId: authorityID, TenantId: tenantID}
	queries := commodoredb.New(s.db)
	switch strings.TrimSpace(ingestMode) {
	case "pull":
		if s.pullSourceEncryptor == nil {
			return nil, parkAuthorityCompile("source_decryptor_missing", errors.New("pull-source decryptor is unavailable"))
		}
		row, err := queries.GetPullMediaAuthoritySecret(ctx, streamID)
		if err != nil {
			return nil, fmt.Errorf("load pull source for media authority: %w", err)
		}
		uri, err := s.pullSourceEncryptor.Decrypt(row.SourceUriEnc)
		if err != nil {
			s.observeFieldDecryptFailure("pull_source_uri", row.SourceUriEnc)
			return nil, parkAuthorityCompile("source_decrypt_failed", fmt.Errorf("decrypt pull source for media authority: %w", err))
		}
		secret.SourceUri = uri
		secret.SourceEnabled = row.Enabled
		secret.AllowedClusterIds = sortedUnique(row.AllowedClusterIds)
	case "mist_native":
		row, err := queries.GetNativeMediaAuthoritySecret(ctx, streamID)
		if err != nil {
			return nil, fmt.Errorf("load native source for media authority: %w", err)
		}
		secret.NativeSourceSpec = row.SourceSpec
		secret.NativeSourceKind = row.SourceKind
		secret.NativePlacementCount = row.PlacementCount
		secret.NativeAllowedClusterIds = sortedUnique(row.AllowedClusterIds)
		secret.NativeAlwaysOn = row.AlwaysOn
	}
	rows, err := queries.ListEnabledPushTargets(ctx, commodoredb.ListEnabledPushTargetsParams{StreamID: streamID, TenantID: tenantID})
	if err != nil {
		return nil, fmt.Errorf("load push targets for media authority: %w", err)
	}
	for _, row := range rows {
		if s.fieldEncryptor == nil {
			return nil, parkAuthorityCompile("push_target_decryptor_missing", errors.New("push-target decryptor is unavailable"))
		}
		uri, err := s.fieldEncryptor.Decrypt(row.TargetUri)
		if err != nil {
			s.observeFieldDecryptFailure("push_target_uri", row.TargetUri)
			return nil, parkAuthorityCompile("push_target_decrypt_failed", fmt.Errorf("decrypt push target %q for media authority: %w", row.ID, err))
		}
		secret.PushTargets = append(secret.PushTargets, &mediaauthoritypb.PushTargetSecret{TargetId: row.ID, TargetUri: uri, Name: row.Name, Platform: row.Platform.String})
	}
	sort.Slice(secret.PushTargets, func(i, j int) bool { return secret.PushTargets[i].GetTargetId() < secret.PushTargets[j].GetTargetId() })
	// An authenticated empty secret is meaningful for push ingest: it proves
	// the complete desired target set is empty. Omitting it would make a cell
	// unable to distinguish target removal from unavailable sealing authority.
	return secret, nil
}

func (s *CommodoreServer) sealLiveStreamSecret(authorityID string, targets []string, secret *mediaauthoritypb.LiveStreamSecret) ([]*mediaauthoritypb.SealedCellSecret, []byte, error) {
	if secret == nil {
		return nil, nil, nil
	}
	return s.sealAuthoritySecret(authorityID, targets, secret)
}

// sealAuthoritySecret seals a secret for each target cell and returns the
// commitment that identifies the sealed content. The commitment is nil when
// nothing was sealed.
func (s *CommodoreServer) sealAuthoritySecret(authorityID string, targets []string, secret proto.Message) ([]*mediaauthoritypb.SealedCellSecret, []byte, error) {
	if secret == nil || !secret.ProtoReflect().IsValid() {
		return nil, nil, nil
	}
	plaintext, err := proto.MarshalOptions{Deterministic: true}.Marshal(secret)
	if err != nil {
		return nil, nil, fmt.Errorf("encode media-authority secret: %w", err)
	}
	cells := sortedUnique(targets)
	boxes := make([]*mediaauthoritypb.SealedCellSecret, 0, len(cells))
	recipients := make([]string, 0, len(cells))
	for _, cellID := range cells {
		recipient, ok := s.mediaAuthorityRecipients[cellID]
		if !ok {
			return nil, nil, parkAuthorityCompile("seal_recipient_missing", fmt.Errorf("no media authority seal recipient configured for cell %q", cellID))
		}
		box, sealErr := sharedauthority.SealSecret(cellID, authorityID, recipient, plaintext)
		if sealErr != nil {
			return nil, nil, fmt.Errorf("seal media-authority secret for cell %q: %w", cellID, sealErr)
		}
		boxes = append(boxes, box)
		recipients = append(recipients, cellID+"\x00"+recipient.KeyID)
	}
	if len(boxes) == 0 {
		return nil, nil, nil
	}
	commitment, err := sharedauthority.SecretCommitment(s.mediaAuthorityPrivateKey, authorityID, plaintext, recipients)
	if err != nil {
		return nil, nil, fmt.Errorf("commit media-authority secret: %w", err)
	}
	return boxes, commitment, nil
}

func (s *CommodoreServer) compileArtifactAuthority(ctx context.Context, artifactID string) (retErr error) {
	artifactID = strings.TrimSpace(artifactID)
	if artifactID == "" {
		return errors.New("artifact ID is required")
	}
	authorityID := sharedauthority.ArtifactAuthorityID(artifactID)
	queries := commodoredb.New(s.db)
	source, err := queries.GetArtifactMediaAuthoritySource(ctx, artifactID)
	if errors.Is(err, sql.ErrNoRows) {
		return s.compileDeletedMediaObjectAuthority(ctx, authorityID, mediaauthoritypb.MediaObjectKind_MEDIA_OBJECT_KIND_ARTIFACT)
	}
	if err != nil {
		return fmt.Errorf("load artifact authority source: %w", err)
	}
	age := time.Duration(source.AgeSeconds) * time.Second
	terminal := source.ArtifactKind == "dvr" && !source.ParentStreamExists
	if terminal {
		return s.compileDeletedMediaObjectAuthority(ctx, authorityID, mediaauthoritypb.MediaObjectKind_MEDIA_OBJECT_KIND_ARTIFACT)
	}
	var policy *mediaauthoritypb.PlaybackPolicy
	ctx = withPlaybackAccessSource(ctx, source.RequiresAuth, source.PlaybackPolicy, source.PlaybackWebhookSecretEnc)
	defer func() {
		retErr = s.revokeChangedAccessOnCompileFailure(ctx, authorityID, mediaauthoritypb.MediaObjectKind_MEDIA_OBJECT_KIND_ARTIFACT, policy, nil, retErr)
	}()
	tenant, targets, secretTargets, tenantValidUntil, err := s.currentTenantAuthorityContext(ctx, source.TenantID)
	if err != nil {
		correct, waitErr := s.mediaObjectWithoutTenantAuthority(ctx, queries, authorityID, source.TenantID, age, false, terminal, err)
		if !correct {
			return waitErr
		}
		if tenant, targets, secretTargets, err = s.lapsedTenantAuthorityContext(ctx, source.TenantID); err != nil {
			return err
		}
		ctx = withMediaAuthorityLapsedParent(ctx)
	}
	if tenant.GetLifecycle() == mediaauthoritypb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_TOMBSTONE {
		return s.compileDeletedMediaObjectAuthority(ctx, authorityID, mediaauthoritypb.MediaObjectKind_MEDIA_OBJECT_KIND_ARTIFACT)
	}
	if !terminal {
		var skip bool
		ctx, skip, err = s.decideMediaObjectInUse(ctx, queries, authorityID, source.TenantID, age, false, targets)
		if err != nil || skip {
			return err
		}
	}
	lifecycle := mediaauthoritypb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_ACTIVE
	if source.ArtifactKind == "dvr" && !source.ParentStreamExists {
		lifecycle = mediaauthoritypb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_TOMBSTONE
	} else if !tenantCanServeMediaObjects(tenant, secretTargets) {
		lifecycle = mediaauthoritypb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_INACTIVE
	}
	policy = denyPlaybackPolicy()
	var sealedPlayback []*mediaauthoritypb.SealedCellSecret
	var commitments [][]byte
	if lifecycle == mediaauthoritypb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_ACTIVE {
		policy, err = s.compilePlaybackPolicy(ctx, source.TenantID, source.RequiresAuth, source.PlaybackPolicy)
		if err != nil {
			return fmt.Errorf("compile artifact playback policy: %w", err)
		}
		playbackSecret, playbackErr := s.compilePlaybackWebhookSecret(authorityID, source.TenantID, source.PlaybackPolicy, source.PlaybackWebhookSecretEnc)
		if playbackErr != nil {
			return fmt.Errorf("compile artifact webhook authority: %w", playbackErr)
		}
		if playbackSecret != nil {
			var commitment []byte
			sealedPlayback, commitment, err = s.sealAuthoritySecret(authorityID, secretTargets, playbackSecret)
			if err != nil {
				return err
			}
			if commitment != nil {
				commitments = append(commitments, commitment)
			}
		}
		if policy.GetKind() == mediaauthoritypb.PlaybackPolicyKind_PLAYBACK_POLICY_KIND_WEBHOOK {
			policy.ConnectedOnly = len(sealedPlayback) == 0
		}
	}
	payload, err := buildArtifactAuthorityPayload(source, policy, lifecycle)
	if err != nil {
		return err
	}
	payload.SealedPlaybackSecrets = sealedPlayback
	if placementErr := s.compileObjectPlacement(ctx, tenant, payload); placementErr != nil {
		return fmt.Errorf("compile artifact placement: %w", placementErr)
	}
	issuedAt := time.Now().UTC()
	validUntil := issuedAt.Add(7 * 24 * time.Hour)
	if !tenantValidUntil.IsZero() && tenantValidUntil.Before(validUntil) {
		validUntil = tenantValidUntil
	}
	// Age moves with the clock and is not part of what the artifact says.
	source.AgeSeconds = 0
	revision, err := hashJSON(source)
	if err != nil {
		return err
	}
	ctx = withMediaAuthorityLongValidity(ctx, issuedAt.Add(sharedauthority.MaxMediaObjectValidity))
	ctx = withMediaObjectParent(ctx, tenant)
	return s.publishMediaObjectAuthority(ctx, authorityID, payload, targets,
		[]*mediaauthoritypb.AuthoritySourceRevision{{Service: "commodore", Revision: revision}}, issuedAt, validUntil, commitments)
}

func (s *CommodoreServer) compilePlaybackWebhookSecret(authorityID, tenantID, encodedPolicy, encryptedSecret string) (*mediaauthoritypb.MediaObjectSecret, error) {
	if len(s.mediaAuthorityRecipients) == 0 {
		return nil, nil
	}
	var doc policyDoc
	if strings.TrimSpace(encodedPolicy) == "" {
		return nil, nil
	}
	if err := json.Unmarshal([]byte(encodedPolicy), &doc); err != nil {
		return nil, parkAuthorityCompile("invalid_playback_policy", fmt.Errorf("decode playback policy: %w", err))
	}
	if doc.Type != "webhook" {
		return nil, nil
	}
	if doc.Webhook == nil || strings.TrimSpace(doc.Webhook.URL) == "" || strings.TrimSpace(encryptedSecret) == "" {
		return nil, parkAuthorityCompile("invalid_playback_webhook", errors.New("webhook playback policy has incomplete URL or secret"))
	}
	if s.playbackWebhookEncryptor == nil {
		return nil, parkAuthorityCompile("playback_decryptor_missing", errors.New("playback webhook decryptor is unavailable"))
	}
	secret, err := s.playbackWebhookEncryptor.Decrypt(encryptedSecret)
	if err != nil {
		s.observeFieldDecryptFailure("playback_webhook_secret", encryptedSecret)
		return nil, parkAuthorityCompile("playback_decrypt_failed", fmt.Errorf("decrypt playback webhook secret: %w", err))
	}
	return &mediaauthoritypb.MediaObjectSecret{
		AuthorityId: authorityID, TenantId: tenantID,
		PlaybackWebhook: &mediaauthoritypb.PlaybackWebhookSecret{
			Url: doc.Webhook.URL, TimeoutMs: int32(doc.Webhook.TimeoutMs), Secret: secret, ContextJson: string(doc.Webhook.Context),
		},
	}, nil
}

func buildArtifactAuthorityPayload(source commodoredb.GetArtifactMediaAuthoritySourceRow, policy *mediaauthoritypb.PlaybackPolicy, lifecycle mediaauthoritypb.AuthorityLifecycle) (*mediaauthoritypb.MediaObjectAuthority, error) {
	kind, err := artifactAuthorityKind(source.ArtifactKind)
	if err != nil {
		return nil, err
	}
	// The origin cluster produced the artifact and owns its durable storage; it is never derived from a
	// tenant-level cluster. An active artifact whose origin is still unknown is parked until the origin
	// Foghorn's catalog projection records it (the origin_cluster_id update re-enqueues this compile).
	originClusterID := strings.TrimSpace(source.OriginClusterID)
	if originClusterID == "" && lifecycle == mediaauthoritypb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_ACTIVE {
		return nil, parkAuthorityCompile("origin_cluster_unknown",
			fmt.Errorf("artifact %s (%s) has no recorded origin cluster", source.AuthorityID, source.ArtifactKind))
	}
	return &mediaauthoritypb.MediaObjectAuthority{
		SchemaVersion:   sharedauthority.SchemaVersion,
		ObjectKind:      mediaauthoritypb.MediaObjectKind_MEDIA_OBJECT_KIND_ARTIFACT,
		TenantId:        source.TenantID,
		UserId:          source.UserID,
		InternalName:    source.InternalName,
		PlaybackId:      source.PlaybackID,
		Lifecycle:       lifecycle,
		OriginClusterId: originClusterID,
		PlaybackPolicy:  policy,
		Object: &mediaauthoritypb.MediaObjectAuthority_Artifact{Artifact: &mediaauthoritypb.ArtifactAuthority{
			ArtifactId: source.AuthorityID, ArtifactHash: source.ArtifactHash, ArtifactKind: kind, ParentStreamId: source.StreamID,
			ParentStreamInternalName: source.ParentStreamInternalName,
		}},
	}, nil
}

func (s *CommodoreServer) compileDeletedMediaObjectAuthority(ctx context.Context, authorityID string, expectedKind mediaauthoritypb.MediaObjectKind) error {
	return s.compileDeniedMediaObjectAuthority(ctx, authorityID, expectedKind, mediaauthoritypb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_TOMBSTONE, "deleted")
}

// A deletion or proven access revocation must reach prior holders without
// consulting billing or processing. A compiler failure alone is not revocation.
func (s *CommodoreServer) compileDeniedMediaObjectAuthority(ctx context.Context, authorityID string, expectedKind mediaauthoritypb.MediaObjectKind, lifecycle mediaauthoritypb.AuthorityLifecycle, reason string) error {
	current, err := commodoredb.New(s.db).GetCurrentMediaAuthorityPayload(ctx, commodoredb.GetCurrentMediaAuthorityPayloadParams{
		AuthorityKind: "media_object", AuthorityID: authorityID,
	})
	if errors.Is(err, sql.ErrNoRows) {
		// A create/delete transaction may enqueue before the compiler ever distributed
		// an active version. No media cell can retain authority that never existed.
		return nil
	}
	if err != nil {
		return fmt.Errorf("load deleted media-object authority history: %w", err)
	}
	payload := &mediaauthoritypb.MediaObjectAuthority{}
	if unmarshalErr := proto.Unmarshal(current.Payload, payload); unmarshalErr != nil {
		return fmt.Errorf("decode deleted media-object authority history: %w", unmarshalErr)
	}
	if payload.GetObjectKind() != expectedKind {
		return errors.New("deleted media-object authority kind mismatch")
	}
	if payload.GetLifecycle() == mediaauthoritypb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_TOMBSTONE && lifecycle != mediaauthoritypb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_TOMBSTONE {
		return nil
	}
	payload.Lifecycle = lifecycle
	payload.PlaybackPolicy = denyPlaybackPolicy()
	payload.SealedPlaybackSecrets = nil
	payload.CommercialQuotes = nil
	if live := payload.GetLiveStream(); live != nil {
		live.SealedCellSecrets = nil
	}
	issuedAt := time.Now().UTC()
	return s.persistMediaObjectAuthority(ctx, authorityID, payload, nil,
		[]*mediaauthoritypb.AuthoritySourceRevision{{Service: "commodore", Revision: reason}}, issuedAt, issuedAt.Add(mediaAuthorityValidity))
}

func artifactAuthorityKind(kind string) (mediaauthoritypb.ArtifactKind, error) {
	switch strings.TrimSpace(kind) {
	case "vod":
		return mediaauthoritypb.ArtifactKind_ARTIFACT_KIND_VOD, nil
	case "dvr":
		return mediaauthoritypb.ArtifactKind_ARTIFACT_KIND_DVR, nil
	case "clip":
		return mediaauthoritypb.ArtifactKind_ARTIFACT_KIND_CLIP, nil
	case "chapter":
		return mediaauthoritypb.ArtifactKind_ARTIFACT_KIND_CHAPTER, nil
	default:
		return mediaauthoritypb.ArtifactKind_ARTIFACT_KIND_UNSPECIFIED, fmt.Errorf("unsupported artifact authority kind %q", kind)
	}
}

// errTenantAuthorityLapsed is a tenant whose current version exists but has run
// out. It is also errTenantAuthorityMissing: an object compile that needs a
// usable parent waits for it like for a first one.
var errTenantAuthorityLapsed = fmt.Errorf("current tenant authority is hard-expired: %w", errTenantAuthorityMissing)

func (s *CommodoreServer) currentTenantAuthorityContext(ctx context.Context, tenantID string) (*mediaauthoritypb.TenantAuthority, []string, []string, time.Time, error) {
	return s.tenantAuthorityContext(ctx, tenantID, false)
}

// lapsedTenantAuthorityContext is the current tenant version even when it has
// run out, for correcting an object copy cells still hold. It returns no
// validity: a lapsed tenant does not cap the correction, which is bounded by
// the copies it corrects instead.
func (s *CommodoreServer) lapsedTenantAuthorityContext(ctx context.Context, tenantID string) (*mediaauthoritypb.TenantAuthority, []string, []string, error) {
	tenant, targets, secretTargets, _, err := s.tenantAuthorityContext(ctx, tenantID, true)
	return tenant, targets, secretTargets, err
}

type mediaAuthorityLapsedParentContextKey struct{}

// withMediaAuthorityLapsedParent marks an object compile that corrects cell
// copies on a tenant version that has run out.
func withMediaAuthorityLapsedParent(ctx context.Context) context.Context {
	return context.WithValue(ctx, mediaAuthorityLapsedParentContextKey{}, true)
}

func mediaAuthorityLapsedParent(ctx context.Context) bool {
	lapsed, _ := ctx.Value(mediaAuthorityLapsedParentContextKey{}).(bool) //nolint:errcheck // absent means a usable parent
	return lapsed
}

func (s *CommodoreServer) tenantAuthorityContext(ctx context.Context, tenantID string, allowLapsed bool) (*mediaauthoritypb.TenantAuthority, []string, []string, time.Time, error) {
	queries := commodoredb.New(s.db)
	current, err := queries.GetCurrentMediaAuthorityPayload(ctx, commodoredb.GetCurrentMediaAuthorityPayloadParams{
		AuthorityKind: "tenant", AuthorityID: tenantID,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, nil, time.Time{}, fmt.Errorf("load current tenant authority for media object: %w", errTenantAuthorityMissing)
	}
	if err != nil {
		return nil, nil, nil, time.Time{}, fmt.Errorf("load current tenant authority for media object: %w", err)
	}
	tenant := &mediaauthoritypb.TenantAuthority{}
	if unmarshalErr := proto.Unmarshal(current.Payload, tenant); unmarshalErr != nil {
		return nil, nil, nil, time.Time{}, fmt.Errorf("decode current tenant authority: %w", unmarshalErr)
	}
	if tenant.GetTenantId() != tenantID {
		return nil, nil, nil, time.Time{}, errors.New("current tenant authority identity mismatch")
	}
	// Terminal tenant deletion cannot be renewed; it still authorizes deleting
	// its child projections after the envelope expires.
	if !allowLapsed && !current.ValidUntil.After(time.Now().UTC()) && tenant.GetLifecycle() != mediaauthoritypb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_TOMBSTONE {
		return nil, nil, nil, time.Time{}, errTenantAuthorityLapsed
	}
	targets, err := queries.ListActiveMediaAuthorityCells(ctx, commodoredb.ListActiveMediaAuthorityCellsParams{
		AuthorityKind: "tenant", AuthorityID: tenantID,
	})
	if err != nil {
		return nil, nil, nil, time.Time{}, fmt.Errorf("load current tenant authority cells: %w", err)
	}
	secretTargets := activeTenantAuthorityCells(tenant)
	return tenant, targets, secretTargets, current.ValidUntil.UTC(), nil
}

func (s *CommodoreServer) currentTenantServePolicy(ctx context.Context, tenantID string) (string, bool, []*clusterpeerpb.TenantClusterPeer, bool) {
	if !s.mediaAuthorityEnabled() {
		return "", false, nil, false
	}
	current, err := commodoredb.New(s.db).GetCurrentMediaAuthorityPayload(ctx, commodoredb.GetCurrentMediaAuthorityPayloadParams{
		AuthorityKind: "tenant", AuthorityID: tenantID,
	})
	if err != nil || !current.ValidUntil.After(time.Now().UTC()) {
		return "", false, nil, false
	}
	tenant := &mediaauthoritypb.TenantAuthority{}
	if err := proto.Unmarshal(current.Payload, tenant); err != nil || tenant.GetTenantId() != tenantID {
		return "", false, nil, false
	}
	if tenant.GetLifecycle() != mediaauthoritypb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_ACTIVE ||
		tenant.GetBillingDecision() != mediaauthoritypb.TenantBillingDecision_TENANT_BILLING_DECISION_ALLOW {
		return "", false, nil, true
	}
	peers := make([]*clusterpeerpb.TenantClusterPeer, 0, len(tenant.GetEffectiveClusterGrants()))
	for _, grant := range tenant.GetEffectiveClusterGrants() {
		if grant != nil && strings.TrimSpace(grant.GetClusterId()) != "" {
			peers = append(peers, &clusterpeerpb.TenantClusterPeer{ClusterId: grant.GetClusterId()})
		}
	}
	return tenant.GetOfficialClusterId(), tenant.GetAllowPlatformSharedPlayback(), peers, true
}

func tenantCanServeMediaObjects(tenant *mediaauthoritypb.TenantAuthority, secretTargets []string) bool {
	return tenant != nil &&
		tenant.GetLifecycle() == mediaauthoritypb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_ACTIVE &&
		tenant.GetBillingDecision() == mediaauthoritypb.TenantBillingDecision_TENANT_BILLING_DECISION_ALLOW &&
		len(secretTargets) > 0
}

func activeTenantAuthorityCells(tenant *mediaauthoritypb.TenantAuthority) []string {
	if tenant == nil || tenant.GetLifecycle() != mediaauthoritypb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_ACTIVE ||
		tenant.GetBillingDecision() != mediaauthoritypb.TenantBillingDecision_TENANT_BILLING_DECISION_ALLOW {
		return nil
	}
	cells := make(map[string]struct{})
	for _, grant := range tenant.GetEffectiveClusterGrants() {
		if grant == nil {
			continue
		}
		if cell := strings.TrimSpace(grant.GetControlCellId()); cell != "" {
			cells[cell] = struct{}{}
		}
		for _, cell := range grant.GetEligibleServingCellIds() {
			if cell = strings.TrimSpace(cell); cell != "" {
				cells[cell] = struct{}{}
			}
		}
	}
	return sortedSet(cells)
}

func (s *CommodoreServer) compilePlaybackPolicy(ctx context.Context, tenantID string, requiresAuth bool, encoded string) (*mediaauthoritypb.PlaybackPolicy, error) {
	if !requiresAuth {
		return &mediaauthoritypb.PlaybackPolicy{Kind: mediaauthoritypb.PlaybackPolicyKind_PLAYBACK_POLICY_KIND_PUBLIC}, nil
	}
	var doc policyDoc
	if strings.TrimSpace(encoded) == "" {
		return nil, parkAuthorityCompile("invalid_playback_policy", errors.New("protected media object has no playback policy"))
	}
	if err := json.Unmarshal([]byte(encoded), &doc); err != nil {
		return nil, parkAuthorityCompile("invalid_playback_policy", fmt.Errorf("decode playback policy: %w", err))
	}
	switch doc.Type {
	case "jwt":
		if doc.JWT == nil {
			return nil, parkAuthorityCompile("invalid_playback_policy", errors.New("JWT playback policy has no JWT section"))
		}
		keys, err := s.fetchActiveSigningKeys(ctx, tenantID)
		if err != nil {
			return nil, fmt.Errorf("load active playback signing keys: %w", err)
		}
		allowedKids, usable := usableAllowedKids(doc.JWT.AllowedKids, keys)
		if !usable {
			// No active key can verify a token for this policy (none exist, or
			// every allowed kid was revoked). The object stays protected and
			// publishable: it denies playback until a key is created or the
			// policy is updated, instead of parking the whole authority.
			return denyPlaybackPolicy(), nil
		}
		jwt := &mediaauthoritypb.PlaybackJwtPolicy{
			AllowedKeyIds:      allowedKids,
			RequiredAudiences:  sortedUnique(doc.JWT.RequiredAudience),
			RequiredClaimsJson: cloneStringMap(doc.JWT.RequiredClaimsJSON),
		}
		for _, key := range keys {
			jwt.ActiveKeys = append(jwt.ActiveKeys, &mediaauthoritypb.PlaybackSigningKey{
				KeyId: key.GetKid(), Algorithm: key.GetAlgorithm(), PublicKeyPem: key.GetPublicKeyPem(),
			})
		}
		sort.Slice(jwt.ActiveKeys, func(i, j int) bool { return jwt.ActiveKeys[i].GetKeyId() < jwt.ActiveKeys[j].GetKeyId() })
		return &mediaauthoritypb.PlaybackPolicy{
			Kind: mediaauthoritypb.PlaybackPolicyKind_PLAYBACK_POLICY_KIND_JWT, Jwt: jwt, AllowedOrigins: doc.AllowedOrigins,
		}, nil
	case "webhook":
		return &mediaauthoritypb.PlaybackPolicy{
			Kind: mediaauthoritypb.PlaybackPolicyKind_PLAYBACK_POLICY_KIND_WEBHOOK, ConnectedOnly: true, AllowedOrigins: doc.AllowedOrigins,
		}, nil
	case "public":
		return nil, parkAuthorityCompile("invalid_playback_policy", errors.New("protected media object has a public playback policy"))
	default:
		return nil, parkAuthorityCompile("invalid_playback_policy", fmt.Errorf("unsupported playback policy type %q", doc.Type))
	}
}

// usableAllowedKids restricts a JWT policy's allowed kids to the tenant's
// active keys. An empty allow-list means any active key. A non-empty list whose
// kids have all been revoked is unusable rather than empty: dropping it would
// widen the policy to every other active key.
func usableAllowedKids(allowed []string, active []*commodorepb.PlaybackSigningKey) ([]string, bool) {
	if len(active) == 0 {
		return nil, false
	}
	requested := sortedUnique(allowed)
	if len(requested) == 0 {
		return requested, true
	}
	activeKids := make(map[string]struct{}, len(active))
	for _, key := range active {
		activeKids[key.GetKid()] = struct{}{}
	}
	kept := make([]string, 0, len(requested))
	for _, kid := range requested {
		if _, ok := activeKids[kid]; ok {
			kept = append(kept, kid)
		}
	}
	return kept, len(kept) > 0
}

func denyPlaybackPolicy() *mediaauthoritypb.PlaybackPolicy {
	return &mediaauthoritypb.PlaybackPolicy{Kind: mediaauthoritypb.PlaybackPolicyKind_PLAYBACK_POLICY_KIND_DENY}
}

func buildTenantAuthority(tenant *quartermasterpb.Tenant, entitlement *quartermasterpb.GetTenantEntitlementResponse, billing *purserpb.GetTenantBillingStatusResponse, admission *purserpb.GetTenantAdmissionStatusResponse, issuedAt time.Time) (*mediaauthoritypb.TenantAuthority, []string, time.Time, []*mediaauthoritypb.AuthoritySourceRevision, error) {
	payload := &mediaauthoritypb.TenantAuthority{
		SchemaVersion: sharedauthority.SchemaVersion,
		TenantId:      tenant.GetId(),
		Lifecycle:     mediaauthoritypb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_ACTIVE,
	}
	validUntil := issuedAt.Add(mediaAuthorityValidity)
	if !tenant.GetIsActive() {
		payload.Lifecycle = mediaauthoritypb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_INACTIVE
		payload.BillingDecision = mediaauthoritypb.TenantBillingDecision_TENANT_BILLING_DECISION_INACTIVE
		payload.DecisionReason = "tenant_inactive"
		revision, err := hashProtoMessages(tenant)
		if err != nil {
			return nil, nil, time.Time{}, nil, err
		}
		return payload, nil, validUntil, []*mediaauthoritypb.AuthoritySourceRevision{{Service: "quartermaster", Revision: revision}}, nil
	}

	model, err := tenantBillingModel(billing.GetBillingModel())
	if err != nil {
		return nil, nil, time.Time{}, nil, err
	}
	payload.BillingModel = model
	payload.TierLevel = admission.GetTierLevel()
	payload.BillingDecision = mediaauthoritypb.TenantBillingDecision_TENANT_BILLING_DECISION_ALLOW
	switch {
	case billing.GetIsSuspended():
		payload.BillingDecision = mediaauthoritypb.TenantBillingDecision_TENANT_BILLING_DECISION_SUSPENDED
		payload.DecisionReason = "tenant_suspended"
	// Reservation pressure is a short-lived connected-mode capacity signal. It
	// must not be frozen into a 24-hour signed decision because the reservation
	// window can expire without a database mutation. Offline authority follows
	// the durable prepaid balance and explicit suspension state only.
	case model == mediaauthoritypb.TenantBillingModel_TENANT_BILLING_MODEL_PREPAID && billing.GetBalanceCents() <= 0:
		payload.BillingDecision = mediaauthoritypb.TenantBillingDecision_TENANT_BILLING_DECISION_PAYMENT_REQUIRED
		payload.DecisionReason = "prepaid_balance_unavailable"
	}
	positive := payload.GetBillingDecision() == mediaauthoritypb.TenantBillingDecision_TENANT_BILLING_DECISION_ALLOW
	payload.AllowPlatformSharedPlayback = positive
	if billing.GetTenantResourceLimits() != nil {
		payload.ResourceLimits = proto.CloneOf(billing.GetTenantResourceLimits())
	}
	if billing.GetDvrPolicy() != nil {
		payload.DvrPolicy = proto.CloneOf(billing.GetDvrPolicy())
	}
	payload.Allowances = cloneAndSortAllowances(billing.GetAllowances())

	targetSet := map[string]struct{}{}
	if positive {
		peers := append([]*clusterpeerpb.TenantClusterPeer(nil), entitlement.GetEffectiveAccess()...)
		sort.Slice(peers, func(i, j int) bool { return peers[i].GetClusterId() < peers[j].GetClusterId() })
		for _, peer := range peers {
			if !mediaAuthorityPeerAllowed(admission.GetTierLevel(), peer) {
				continue
			}
			grant, cellTargets, grantUntil, grantErr := tenantGrant(peer, issuedAt)
			if grantErr != nil {
				return nil, nil, time.Time{}, nil, grantErr
			}
			payload.EffectiveClusterGrants = append(payload.EffectiveClusterGrants, grant)
			for _, cell := range cellTargets {
				targetSet[cell] = struct{}{}
			}
			if !grantUntil.IsZero() && grantUntil.Before(validUntil) {
				validUntil = grantUntil
			}
		}
	}
	payload.OfficialClusterId = effectiveOfficialClusterID(tenant.GetOfficialClusterId(), payload.GetEffectiveClusterGrants())
	payload.PreferredClusterId = effectivePreferredClusterID(
		tenant.GetPrimaryClusterId(), payload.GetOfficialClusterId(), payload.GetEffectiveClusterGrants(),
	)
	targets := sortedSet(targetSet)
	qmRevision, err := hashProtoMessages(tenant, entitlement)
	if err != nil {
		return nil, nil, time.Time{}, nil, err
	}
	purserRevision, err := hashProtoMessages(billing, admission)
	if err != nil {
		return nil, nil, time.Time{}, nil, err
	}
	revisions := []*mediaauthoritypb.AuthoritySourceRevision{{Service: "purser", Revision: purserRevision}, {Service: "quartermaster", Revision: qmRevision}}
	return payload, targets, validUntil, revisions, nil
}

func effectiveOfficialClusterID(rawOfficialClusterID string, grants []*mediaauthoritypb.TenantClusterGrant) string {
	officialClusterID := strings.TrimSpace(rawOfficialClusterID)
	if officialClusterID == "" {
		return ""
	}
	for _, grant := range grants {
		if grant.GetClusterId() == officialClusterID {
			return officialClusterID
		}
	}
	return ""
}

// effectivePreferredClusterID derives the signed routing role from the same
// filtered authority set that connected responses expose. A stale or
// tier-ineligible configured primary is not authority. The configured primary
// wins only while it remains granted; otherwise the granted official cluster
// is the same fallback Quartermaster exposes to connected callers.
func effectivePreferredClusterID(primaryClusterID, officialClusterID string, grants []*mediaauthoritypb.TenantClusterGrant) string {
	granted := make(map[string]struct{}, len(grants))
	for _, grant := range grants {
		if id := strings.TrimSpace(grant.GetClusterId()); id != "" {
			granted[id] = struct{}{}
		}
	}
	if primary := strings.TrimSpace(primaryClusterID); primary != "" {
		if _, ok := granted[primary]; ok {
			return primary
		}
	}
	if official := strings.TrimSpace(officialClusterID); official != "" {
		if _, ok := granted[official]; ok {
			return official
		}
	}
	return ""
}

func mediaAuthorityPeerAllowed(tierLevel int32, peer *clusterpeerpb.TenantClusterPeer) bool {
	if peer == nil || strings.ToLower(strings.TrimSpace(peer.GetClusterType())) != "edge" {
		return false
	}
	class := strings.ToLower(strings.TrimSpace(peer.GetClusterClass()))
	switch peer.GetAccessSource() {
	case clusterpeerpb.TenantClusterAccessSource_TENANT_CLUSTER_ACCESS_SOURCE_OWNER,
		clusterpeerpb.TenantClusterAccessSource_TENANT_CLUSTER_ACCESS_SOURCE_OPERATOR_OVERRIDE:
		return true
	case clusterpeerpb.TenantClusterAccessSource_TENANT_CLUSTER_ACCESS_SOURCE_PRIVATE_INVITE:
		return class == "tenant_private"
	case clusterpeerpb.TenantClusterAccessSource_TENANT_CLUSTER_ACCESS_SOURCE_PLATFORM_TIER,
		clusterpeerpb.TenantClusterAccessSource_TENANT_CLUSTER_ACCESS_SOURCE_MARKETPLACE_SUBSCRIPTION:
		return mediaAuthorityClusterClassAllowed(tierLevel, class)
	default:
		return false
	}
}

func mediaAuthorityClusterClassAllowed(tierLevel int32, clusterClass string) bool {
	switch strings.ToLower(strings.TrimSpace(clusterClass)) {
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

func tenantBillingModel(value string) (mediaauthoritypb.TenantBillingModel, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "postpaid":
		return mediaauthoritypb.TenantBillingModel_TENANT_BILLING_MODEL_POSTPAID, nil
	case "prepaid":
		return mediaauthoritypb.TenantBillingModel_TENANT_BILLING_MODEL_PREPAID, nil
	default:
		return mediaauthoritypb.TenantBillingModel_TENANT_BILLING_MODEL_UNSPECIFIED, fmt.Errorf("unsupported tenant billing model %q", value)
	}
}

func tenantGrant(peer *clusterpeerpb.TenantClusterPeer, issuedAt time.Time) (*mediaauthoritypb.TenantClusterGrant, []string, time.Time, error) {
	if peer == nil || strings.TrimSpace(peer.GetClusterId()) == "" || !peer.GetAccessActive() || peer.GetSubscriptionStatus() != "active" || peer.GetAccessSource() == clusterpeerpb.TenantClusterAccessSource_TENANT_CLUSTER_ACCESS_SOURCE_UNSPECIFIED {
		return nil, nil, time.Time{}, errors.New("quartermaster returned an incomplete effective cluster grant")
	}
	controlCell := strings.TrimSpace(peer.GetControlCellId())
	if controlCell == "" {
		return nil, nil, time.Time{}, fmt.Errorf("cluster %q has no control_cell_id", peer.GetClusterId())
	}
	targetSet := map[string]struct{}{controlCell: {}}
	for _, cell := range peer.GetEligibleServingCellIds() {
		if cell = strings.TrimSpace(cell); cell != "" {
			targetSet[cell] = struct{}{}
		}
	}
	grant := &mediaauthoritypb.TenantClusterGrant{
		ClusterId:               strings.TrimSpace(peer.GetClusterId()),
		ClusterType:             strings.ToLower(strings.TrimSpace(peer.GetClusterType())),
		AccessSource:            peer.GetAccessSource(),
		AccessLevel:             strings.TrimSpace(peer.GetAccessLevel()),
		SubscriptionStatus:      "active",
		ClusterClass:            strings.TrimSpace(peer.GetClusterClass()),
		DeploymentModel:         strings.TrimSpace(peer.GetDeploymentModel()),
		OwnerTenantId:           strings.TrimSpace(peer.GetOwnerTenantId()),
		AllowPrivatePullSources: peer.GetAllowPrivatePullSources(),
		ControlCellId:           controlCell,
		EligibleServingCellIds:  sortedUnique(peer.GetEligibleServingCellIds()),
	}
	if peer.GetResourceLimits() != nil {
		grant.ResourceLimits = proto.CloneOf(peer.GetResourceLimits())
	}
	var grantUntil time.Time
	if expiry := peer.GetAccessExpiresAt(); expiry != nil {
		if !expiry.IsValid() || !expiry.AsTime().After(issuedAt) {
			return nil, nil, time.Time{}, fmt.Errorf("cluster %q grant is already expired", peer.GetClusterId())
		}
		grant.ExpiresAt = proto.CloneOf(expiry)
		grantUntil = expiry.AsTime().UTC()
	}
	return grant, sortedSet(targetSet), grantUntil, nil
}

func (s *CommodoreServer) persistTenantAuthority(ctx context.Context, payload *mediaauthoritypb.TenantAuthority, targets []string, revisions []*mediaauthoritypb.AuthoritySourceRevision, issuedAt, validUntil time.Time) error {
	issuedAt, validUntil = mediaAuthorityInstant(issuedAt), mediaAuthorityInstant(validUntil)
	if !validUntil.After(issuedAt) {
		return errors.New("tenant authority requires future validity")
	}
	if sharedauthority.IsPlacementSchema(payload.GetSchemaVersion()) {
		deadlineCtx, cancel := context.WithDeadline(ctx, validUntil)
		defer cancel()
		ctx = deadlineCtx
	}
	contentDigest, digestErr := sharedauthority.TenantContentDigest(payload, s.mediaAuthorityKeyID)
	if digestErr != nil {
		return fmt.Errorf("digest tenant authority: %w", digestErr)
	}
	dependentsDigest, digestErr := sharedauthority.TenantDependentsDigest(payload)
	if digestErr != nil {
		return fmt.Errorf("digest tenant authority dependents: %w", digestErr)
	}
	target := commodoredb.TenantMediaAuthorityTarget(payload.GetTenantId())
	tombstone := payload.GetLifecycle() == mediaauthoritypb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_TOMBSTONE
	// The transaction body may run more than once; only a committed publication
	// is observed.
	publishedCause, publishedEarly := "", false
	txErr := database.WithRetryablePostgresTx(ctx, s.db, nil, func(tx *sql.Tx) error {
		publishedCause, publishedEarly = "", false
		queries := commodoredb.New(tx)
		if err := lockMediaAuthorityCompileFence(ctx, queries, "tenant:"+payload.GetTenantId()); err != nil {
			return err
		}
		membershipChanges, err := retireMediaAuthorityTargets(ctx, queries, "tenant", payload.GetTenantId(), targets)
		if err != nil {
			return err
		}
		horizons, err := lockMediaAuthorityTargetHorizons(ctx, queries, "tenant", payload.GetTenantId())
		if err != nil {
			return err
		}
		priorCells, err := mediaAuthorityCellsToCorrect(ctx, queries, "tenant", payload.GetTenantId(), tombstone)
		if err != nil {
			return fmt.Errorf("load prior tenant authority cells: %w", err)
		}
		// The validity published is decided below, once it is known whether the
		// tenant is in use.
		validUntil := validUntil
		targetSet := make(map[string]struct{}, len(targets)+len(priorCells))
		for _, cell := range append(append([]string(nil), targets...), priorCells...) {
			if cell = strings.TrimSpace(cell); cell != "" {
				targetSet[cell] = struct{}{}
			}
		}
		allTargets := sortedSet(targetSet)
		validUntil = mediaAuthorityCorrectionOnlyValidity(validUntil, targets, horizons)
		refreshAfter := mediaAuthorityRefreshAfter(issuedAt, validUntil)
		now := time.Now().UTC()
		decision, err := decideMediaAuthorityPublication(ctx, queries, "tenant", payload.GetTenantId(), target, contentDigest, allTargets, tombstone, validUntil, now)
		if err != nil {
			return err
		}
		if membershipChanges > 0 && (mediaAuthorityInUse(ctx) || tombstone || !decision.horizon.IsZero()) {
			decision.publish, decision.cause = true, mediaAuthorityCauseTargets
		}
		if !decision.publish {
			// An object may have parked waiting for this tenant after the version
			// it now finds was published, and that publication's wake-up missed
			// it. A tenant compile that finds a valid authority wakes such objects
			// whether or not it publishes.
			if !tombstone && decision.hasCurrent && decision.current.ValidUntil.After(now) {
				if _, rearmErr := queries.RearmMediaAuthorityObligationsAwaitingTenant(ctx, payload.GetTenantId()); rearmErr != nil {
					return fmt.Errorf("re-arm media objects awaiting tenant authority: %w", rearmErr)
				}
			}
			return keepMediaAuthorityRenewalScheduled(ctx, queries, target, payload.GetTenantId(), decision, tombstone, now)
		}
		validUntil, refreshAfter = publishedMediaAuthorityValidity(decision, issuedAt, validUntil, refreshAfter)
		version, err := queries.AllocateMediaAuthorityVersion(ctx, commodoredb.AllocateMediaAuthorityVersionParams{AuthorityKind: "tenant", AuthorityID: payload.GetTenantId()})
		if err != nil {
			return fmt.Errorf("allocate tenant authority version: %w", err)
		}
		if version <= 0 {
			return errors.New("allocated invalid tenant authority version")
		}
		if placementErr := guardTenantPlacementPublication(ctx, queries, payload); placementErr != nil {
			return placementErr
		}
		// Placement schemas are monotonic once published, so a cell that cannot
		// enforce this schema must be refused as a target here rather than rolled
		// back later. Prior cells only receive the revocation they already hold
		// state for.
		if sharedauthority.IsPlacementSchema(payload.GetSchemaVersion()) {
			ready, readyErr := placementCellsReadyFor(ctx, queries, targets, payload.GetSchemaVersion())
			if readyErr != nil {
				return readyErr
			}
			if !ready {
				return errors.New("tenant authority targets a cell without placement enforcement capability")
			}
		}
		var firstEnvelope *mediaauthoritypb.AuthorityEnvelope
		signedByCell := make(map[string][]byte, len(allTargets))
		for _, cell := range allTargets {
			cellUntil := mediaAuthorityRecipientValidity(validUntil, horizons[cell])
			envelope, envelopeErr := sharedauthority.NewEnvelope(mediaauthoritypb.AuthorityKind_AUTHORITY_KIND_TENANT, payload.GetTenantId(), uint64(version), issuedAt, mediaAuthorityRefreshAfter(issuedAt, cellUntil), cellUntil, s.mediaAuthorityKeyID, cell, payload, revisions)
			if envelopeErr != nil {
				return envelopeErr
			}
			if firstEnvelope == nil {
				firstEnvelope = envelope
			}
			signed, signErr := sharedauthority.Sign(envelope, s.mediaAuthorityPrivateKey)
			if signErr != nil {
				return signErr
			}
			encoded, encodeErr := proto.MarshalOptions{Deterministic: true}.Marshal(signed)
			if encodeErr != nil {
				return fmt.Errorf("encode signed tenant authority: %w", encodeErr)
			}
			signedByCell[cell] = encoded
		}
		if firstEnvelope == nil {
			// A tenant without a current or historical serving cell has nobody to
			// notify. Keep versioned compiler history so a later grant can advance
			// from an authoritative state without manufacturing failing work.
			firstEnvelope, err = sharedauthority.NewEnvelope(mediaauthoritypb.AuthorityKind_AUTHORITY_KIND_TENANT, payload.GetTenantId(), uint64(version), issuedAt, refreshAfter, validUntil, s.mediaAuthorityKeyID, "unassigned", payload, revisions)
			if err != nil {
				return err
			}
		}
		revisionsJSON, err := json.Marshal(revisionJSON(revisions))
		if err != nil {
			return fmt.Errorf("encode tenant authority revisions: %w", err)
		}
		if insertErr := queries.InsertMediaAuthorityVersion(ctx, commodoredb.InsertMediaAuthorityVersionParams{
			AuthorityKind: "tenant", AuthorityID: payload.GetTenantId(), AuthorityVersion: version,
			PayloadSchemaVersion: int32(firstEnvelope.GetSchemaVersion()), Payload: firstEnvelope.GetPayload(), PayloadSha256: firstEnvelope.GetPayloadSha256(),
			ContentDigest: contentDigest, DependentsDigest: dependentsDigest, Tombstone: tombstone,
			SourceRevisions: revisionsJSON, IssuedAt: issuedAt, RefreshAfter: refreshAfter, ValidUntil: validUntil,
		}); insertErr != nil {
			return fmt.Errorf("persist tenant authority version: %w", insertErr)
		}
		rows, err := queries.UpsertCurrentMediaAuthority(ctx, commodoredb.UpsertCurrentMediaAuthorityParams{AuthorityKind: "tenant", AuthorityID: payload.GetTenantId(), AuthorityVersion: version})
		if err != nil || rows != 1 {
			return fmt.Errorf("advance current tenant authority: rows=%d: %w", rows, err)
		}
		if _, err := queries.SupersedeOlderMediaAuthorityDeliveries(ctx, commodoredb.SupersedeOlderMediaAuthorityDeliveriesParams{
			AuthorityKind: "tenant", AuthorityID: payload.GetTenantId(), AuthorityVersion: version,
		}); err != nil {
			return fmt.Errorf("supersede older tenant authority deliveries: %w", err)
		}
		for _, cell := range allTargets {
			if err := queries.UpsertMediaAuthorityTarget(ctx, commodoredb.UpsertMediaAuthorityTargetParams{
				AuthorityKind: "tenant", AuthorityID: payload.GetTenantId(), CellID: cell, AuthorityVersion: version,
			}); err != nil {
				return fmt.Errorf("record tenant authority target cell %q: %w", cell, err)
			}
			rows, err := queries.EnqueueMediaAuthorityDelivery(ctx, commodoredb.EnqueueMediaAuthorityDeliveryParams{
				AuthorityKind: "tenant", AuthorityID: payload.GetTenantId(), AuthorityVersion: version, CellID: cell, SignedEnvelope: signedByCell[cell],
				ShortLease: mediaAuthorityShortLease(payload.GetSchemaVersion(), issuedAt, mediaAuthorityRecipientValidity(validUntil, horizons[cell])), CorrectionUntil: horizons[cell],
			})
			if err != nil || rows != 1 {
				return fmt.Errorf("enqueue tenant authority for cell %q: rows=%d: %w", cell, rows, err)
			}
		}
		// A tenant nobody uses is published only to correct the copies cells
		// still hold, and is renewed only until it lasts as long as they do:
		// scheduling past that would revive a renewal that went dormant for
		// exactly that reason. A tombstone is renewed only while it corrects.
		if decision.keepsRenewal(ctx, validUntil, tombstone) {
			if err := queries.ScheduleMediaAuthorityRenewal(ctx, target, payload.GetTenantId(), version,
				mediaAuthorityRenewAt(target, issuedAt, refreshAfter, validUntil)); err != nil {
				return fmt.Errorf("schedule tenant authority renewal: %w", err)
			}
		}
		// An object that was waiting for this tenant's authority can compile now.
		if _, err := queries.RearmMediaAuthorityObligationsAwaitingTenant(ctx, payload.GetTenantId()); err != nil {
			return fmt.Errorf("re-arm media objects awaiting tenant authority: %w", err)
		}
		// Media-object authorities are derived from the tenant fields covered by
		// the dependents digest. They are refreshed when it changes, in this
		// transaction, so a committed tenant change can never lose its fanout. A
		// renewal or an allowance change leaves it untouched and refreshes no
		// object. While some cell still takes only short-lived object authorities,
		// objects are capped by the tenant's validity and follow that down too.
		validityCapsObjects, capErr := mediaAuthorityObjectsCappedByTenant(ctx, queries, allTargets)
		if capErr != nil {
			return capErr
		}
		if !decision.hasCurrent || !bytes.Equal(dependentsDigest, decision.current.DependentsDigest) ||
			(validityCapsObjects && validUntil.Before(decision.current.ValidUntil)) {
			if err := queries.EnqueueMediaAuthorityEvent(ctx, commodoredb.TenantMediaObjectsAuthorityTarget(payload.GetTenantId()), payload.GetTenantId(),
				"tenant_media_objects:tenant_authority_changed", "commodore", "tenant-version:"+strconv.FormatInt(version, 10)); err != nil {
				return fmt.Errorf("enqueue tenant media-object refresh: %w", err)
			}
		}
		publishedCause, publishedEarly = decision.cause, decision.renewedEarly(now)
		return nil
	})
	if txErr == nil && publishedCause != "" {
		s.observeMediaAuthorityPublished(ctx, "tenant", publishedCause, publishedEarly)
	}
	return txErr
}

// persistMediaObjectAuthority publishes a media-object authority whose sealed
// content carries no commitments, which always publishes when the payload
// holds sealed secrets.
func (s *CommodoreServer) persistMediaObjectAuthority(ctx context.Context, authorityID string, payload *mediaauthoritypb.MediaObjectAuthority, targets []string, revisions []*mediaauthoritypb.AuthoritySourceRevision, issuedAt, validUntil time.Time) error {
	return s.publishMediaObjectAuthority(ctx, authorityID, payload, targets, revisions, issuedAt, validUntil, nil)
}

// publishMediaObjectAuthority publishes a new version only when the compiled
// authority differs from the current one or its validity is due for renewal.
// commitments identify the payload's sealed secrets, playback secret first.
func (s *CommodoreServer) publishMediaObjectAuthority(ctx context.Context, authorityID string, payload *mediaauthoritypb.MediaObjectAuthority, targets []string, revisions []*mediaauthoritypb.AuthoritySourceRevision, issuedAt, validUntil time.Time, commitments [][]byte) error {
	var commercial *mediaObjectCommercialSnapshot
	if sharedauthority.IsPlacementSchema(payload.GetSchemaVersion()) && payload.GetLifecycle() == mediaauthoritypb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_ACTIVE {
		var err error
		commercial, err = s.prepareMediaObjectCommercial(ctx, payload, targets)
		if err != nil {
			return err
		}
		payload = commercial.payload
		issuedAt = time.Now().UTC()
		// The zero time is a correction on a lapsed parent without quotes: nothing
		// but the object's own validity bounds it.
		if !commercial.validUntil.IsZero() && commercial.validUntil.Before(validUntil) {
			validUntil = commercial.validUntil
		}
		if commercial.revision != "" {
			revisions = append(append([]*mediaauthoritypb.AuthoritySourceRevision(nil), revisions...), &mediaauthoritypb.AuthoritySourceRevision{Service: "purser", Revision: commercial.revision})
		}
	}
	issuedAt, validUntil = mediaAuthorityInstant(issuedAt), mediaAuthorityInstant(validUntil)
	if !validUntil.After(issuedAt) {
		return errors.New("media-object authority requires future validity")
	}
	if commercial != nil {
		var cancel context.CancelFunc
		ctx, cancel = context.WithDeadline(ctx, validUntil)
		defer cancel()
	}
	contentDigest, _, digestErr := sharedauthority.MediaObjectContentDigest(payload, s.mediaAuthorityKeyID, commitments)
	if digestErr != nil {
		return fmt.Errorf("digest media-object authority: %w", digestErr)
	}
	contentDigest, revisions = bindPlaybackAccessSource(ctx, contentDigest, revisions)
	target := mediaObjectAuthorityTarget(authorityID)
	tombstone := payload.GetLifecycle() == mediaauthoritypb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_TOMBSTONE
	// The transaction body may run more than once; only a committed publication
	// is observed.
	publishedCause, publishedEarly := "", false
	txErr := database.WithRetryablePostgresTx(ctx, s.db, nil, func(tx *sql.Tx) error {
		publishedCause, publishedEarly = "", false
		queries := commodoredb.New(tx)
		if err := lockMediaAuthorityCompileFence(ctx, queries, "media_object:"+authorityID); err != nil {
			return err
		}
		if parent, ok := ctx.Value(mediaObjectParentContextKey{}).(*mediaauthoritypb.TenantAuthority); ok {
			current, err := queries.LockCurrentTenantMediaAuthority(ctx, payload.GetTenantId())
			if err != nil {
				return err
			}
			bound := &mediaauthoritypb.TenantAuthority{}
			if err := proto.Unmarshal(current.Payload, bound); err != nil {
				return err
			}
			// The row lock serializes first/cold publication with tenant changes.
			// Fan-out cannot find an object that has not published yet.
			if !proto.Equal(parent, bound) {
				return fmt.Errorf("tenant changed while compiling object: %w", errMediaAuthorityCompileSuperseded)
			}
		}
		if commercial != nil {
			current, err := queries.LockCurrentTenantMediaAuthority(ctx, payload.GetTenantId())
			if err != nil {
				return err
			}
			bound := &mediaauthoritypb.TenantAuthority{}
			if err := proto.Unmarshal(current.Payload, bound); err != nil {
				return err
			}
			// Every placement object is compiled against the tenant it captured,
			// and must still be. Only a quoted one is also bounded by that tenant's
			// validity: its quotes were priced for it. Every decision a cell makes
			// needs the tenant authority anyway, so an unquoted object outliving
			// its tenant's copy never widens what a cell serves. A correction
			// compiled on a lapsed tenant is exempt from the tenant's validity for
			// the same reason: no cell can decide on that tenant until it is
			// renewed, and the correction has to be there first.
			lapsed := mediaAuthorityLapsedParent(ctx)
			parentExpired := !current.ValidUntil.After(time.Now())
			if !proto.Equal(bound, commercial.tenant) || (parentExpired && !lapsed) ||
				(commercial.quoted && ((!lapsed && validUntil.After(current.ValidUntil)) || !validUntil.After(time.Now()))) {
				return errors.New("commercial parent authority changed or expired before publication")
			}
		}
		membershipChanges, err := retireMediaAuthorityTargets(ctx, queries, "media_object", authorityID, targets)
		if err != nil {
			return err
		}
		horizons, err := lockMediaAuthorityTargetHorizons(ctx, queries, "media_object", authorityID)
		if err != nil {
			return err
		}
		priorCells, err := mediaAuthorityCellsToCorrect(ctx, queries, "media_object", authorityID, tombstone)
		if err != nil {
			return fmt.Errorf("load prior media-object cells: %w", err)
		}
		targetSet := map[string]struct{}{}
		for _, cell := range append(append([]string(nil), targets...), priorCells...) {
			if cell = strings.TrimSpace(cell); cell != "" {
				targetSet[cell] = struct{}{}
			}
		}
		allTargets := sortedSet(targetSet)
		now := time.Now().UTC()
		validUntil := validUntil
		if long := mediaAuthorityLongValidity(ctx); (commercial == nil || !commercial.quoted) && !tombstone && long.After(validUntil) {
			accepted, longErr := mediaAuthorityCellsAcceptLongValidity(ctx, queries, allTargets)
			if longErr != nil {
				return longErr
			}
			if accepted {
				validUntil = mediaAuthorityInstant(long)
			}
		}
		validUntil = mediaAuthorityCorrectionOnlyValidity(validUntil, targets, horizons)
		refreshAfter := mediaAuthorityRefreshAfter(issuedAt, validUntil)
		decision, err := decideMediaAuthorityPublication(ctx, queries, "media_object", authorityID, target, contentDigest, allTargets, tombstone, validUntil, now)
		if err != nil {
			return err
		}
		if membershipChanges > 0 && (mediaAuthorityInUse(ctx) || tombstone || !decision.horizon.IsZero()) {
			decision.publish, decision.cause = true, mediaAuthorityCauseTargets
		}
		if !decision.publish {
			return keepMediaAuthorityRenewalScheduled(ctx, queries, target, payload.GetTenantId(), decision, tombstone, now)
		}
		validUntil, refreshAfter = publishedMediaAuthorityValidity(decision, issuedAt, validUntil, refreshAfter)
		version, err := queries.AllocateMediaAuthorityVersion(ctx, commodoredb.AllocateMediaAuthorityVersionParams{AuthorityKind: "media_object", AuthorityID: authorityID})
		if err != nil {
			return fmt.Errorf("allocate media-object authority version: %w", err)
		}
		var firstEnvelope *mediaauthoritypb.AuthorityEnvelope
		signedByCell := make(map[string][]byte, len(allTargets))
		for _, cell := range allTargets {
			cellUntil := mediaAuthorityRecipientValidity(validUntil, horizons[cell])
			envelope, envelopeErr := sharedauthority.NewEnvelope(mediaauthoritypb.AuthorityKind_AUTHORITY_KIND_MEDIA_OBJECT, authorityID, uint64(version), issuedAt, mediaAuthorityRefreshAfter(issuedAt, cellUntil), cellUntil, s.mediaAuthorityKeyID, cell, payload, revisions)
			if envelopeErr != nil {
				return envelopeErr
			}
			if firstEnvelope == nil {
				firstEnvelope = envelope
			}
			signed, signErr := sharedauthority.Sign(envelope, s.mediaAuthorityPrivateKey)
			if signErr != nil {
				return signErr
			}
			encoded, encodeErr := proto.MarshalOptions{Deterministic: true}.Marshal(signed)
			if encodeErr != nil {
				return fmt.Errorf("encode signed media-object authority: %w", encodeErr)
			}
			signedByCell[cell] = encoded
		}
		if firstEnvelope == nil {
			// A media object may outlive its tenant's last serving grant. Keep
			// compiler history without inventing a delivery target; a later grant
			// will rebuild and seal the object for its real cell.
			firstEnvelope, err = sharedauthority.NewEnvelope(mediaauthoritypb.AuthorityKind_AUTHORITY_KIND_MEDIA_OBJECT, authorityID, uint64(version), issuedAt, refreshAfter, validUntil, s.mediaAuthorityKeyID, "unassigned", payload, revisions)
			if err != nil {
				return err
			}
		}
		revisionsJSON, err := json.Marshal(revisionJSON(revisions))
		if err != nil {
			return fmt.Errorf("encode media-object authority revisions: %w", err)
		}
		if insertErr := queries.InsertMediaAuthorityVersion(ctx, commodoredb.InsertMediaAuthorityVersionParams{
			AuthorityKind: "media_object", AuthorityID: authorityID, AuthorityVersion: version,
			PayloadSchemaVersion: int32(firstEnvelope.GetSchemaVersion()), Payload: firstEnvelope.GetPayload(), PayloadSha256: firstEnvelope.GetPayloadSha256(),
			ContentDigest: contentDigest, Tombstone: tombstone,
			SourceRevisions: revisionsJSON, IssuedAt: issuedAt, RefreshAfter: refreshAfter, ValidUntil: validUntil,
		}); insertErr != nil {
			return fmt.Errorf("persist media-object authority version: %w", insertErr)
		}
		rows, err := queries.UpsertCurrentMediaAuthority(ctx, commodoredb.UpsertCurrentMediaAuthorityParams{AuthorityKind: "media_object", AuthorityID: authorityID, AuthorityVersion: version})
		if err != nil || rows != 1 {
			return fmt.Errorf("advance current media-object authority: rows=%d: %w", rows, err)
		}
		if _, err := queries.SupersedeOlderMediaAuthorityDeliveries(ctx, commodoredb.SupersedeOlderMediaAuthorityDeliveriesParams{
			AuthorityKind: "media_object", AuthorityID: authorityID, AuthorityVersion: version,
		}); err != nil {
			return fmt.Errorf("supersede older media-object authority deliveries: %w", err)
		}
		for _, cell := range allTargets {
			if err := queries.UpsertMediaAuthorityTarget(ctx, commodoredb.UpsertMediaAuthorityTargetParams{
				AuthorityKind: "media_object", AuthorityID: authorityID, CellID: cell, AuthorityVersion: version,
			}); err != nil {
				return fmt.Errorf("record media-object authority target cell %q: %w", cell, err)
			}
			rows, err := queries.EnqueueMediaAuthorityDelivery(ctx, commodoredb.EnqueueMediaAuthorityDeliveryParams{
				AuthorityKind: "media_object", AuthorityID: authorityID, AuthorityVersion: version, CellID: cell, SignedEnvelope: signedByCell[cell],
				ShortLease: mediaAuthorityShortLease(payload.GetSchemaVersion(), issuedAt, mediaAuthorityRecipientValidity(validUntil, horizons[cell])), CorrectionUntil: horizons[cell],
			})
			if err != nil || rows != 1 {
				return fmt.Errorf("enqueue media-object authority for cell %q: rows=%d: %w", cell, rows, err)
			}
		}
		// An object nobody uses is published only to correct the copies cells
		// still hold, and is renewed only until it lasts as long as they do. A
		// tombstone is renewed only while it corrects.
		if decision.keepsRenewal(ctx, validUntil, tombstone) {
			if err := queries.ScheduleMediaAuthorityRenewal(ctx, target, payload.GetTenantId(), version,
				mediaAuthorityRenewAt(target, issuedAt, refreshAfter, validUntil)); err != nil {
				return fmt.Errorf("schedule media-object authority renewal: %w", err)
			}
		} else if !tombstone && isMediaAuthorityDeadlineLane(mediaAuthorityObligationLane(ctx)) {
			markMediaAuthorityDormant(ctx)
		}
		publishedCause, publishedEarly = decision.cause, decision.renewedEarly(now)
		return nil
	})
	if txErr == nil && publishedCause != "" {
		s.observeMediaAuthorityPublished(ctx, "media_object", publishedCause, publishedEarly)
	}
	return txErr
}

// mediaObjectAuthorityTarget maps a media-object authority ID to its refresh
// target. Authority IDs are "live_stream:<id>" or "artifact:<id>".
func mediaObjectAuthorityTarget(authorityID string) commodoredb.MediaAuthorityTarget {
	if streamID, ok := strings.CutPrefix(authorityID, sharedauthority.LiveStreamAuthorityID("")); ok {
		return commodoredb.LiveStreamMediaAuthorityTarget(streamID)
	}
	return commodoredb.ArtifactMediaAuthorityTarget(strings.TrimPrefix(authorityID, sharedauthority.ArtifactAuthorityID("")))
}

func (s *CommodoreServer) runMediaAuthorityWorkers(ctx context.Context) {
	processes := []func(context.Context){
		s.adoptLegacyMediaAuthorityRefreshInbox,
		s.processMediaAuthorityEventObligations,
		s.processMediaAuthorityBulkObligations,
		s.processMediaAuthorityObjectRenewals,
		s.processMediaAuthorityTenantRenewals,
		s.processMediaAuthorityDeliveryBatch,
		s.processMediaAuthorityDeadlineDeliveryBatch,
		s.observeMediaAuthorityQueues,
		s.processPlacementActivationBacklog,
	}
	runMediaAuthorityWorkerGroup(ctx, processes...)
}

func runMediaAuthorityWorkerGroup(ctx context.Context, processes ...func(context.Context)) {
	var workers sync.WaitGroup
	workers.Add(len(processes))
	for _, process := range processes {
		go func() {
			defer workers.Done()
			runMediaAuthorityWorker(ctx, process)
		}()
	}
	workers.Wait()
}

func runMediaAuthorityWorker(ctx context.Context, process func(context.Context)) {
	ticker := time.NewTicker(mediaAuthorityWorkerInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			process(ctx)
		}
	}
}

func (s *CommodoreServer) runMediaAuthorityReconciler(ctx context.Context) {
	if !s.mediaAuthorityEnabled() {
		return
	}
	s.reconcileMediaAuthorities(ctx)
	ticker := time.NewTicker(mediaAuthorityReconcileInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.reconcileMediaAuthorities(ctx)
		}
	}
}

// reconcileMediaAuthorities is the safety net behind event-driven refresh. It
// recompiles the tenants that are in use, which publishes only where content
// drifted, and a tenant that was never compiled at all, which is how a missed
// creation event is recovered. A tenant nobody uses is left alone: it is
// compiled when it is next used. The work goes to the bulk lane, spread over
// the interval, so it never stands in front of a change.
//
// Objects are recompiled only when the compiler itself changed (its signing key
// or the shape of what it signs): that is the one change no source event
// announces, and an unchanged compile would otherwise never re-issue them.
func (s *CommodoreServer) reconcileMediaAuthorities(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	queries := commodoredb.New(s.db)
	// The renewal rows are how the tenants in use are found, so an authority that
	// lost its row is repaired first.
	s.repairMediaAuthorityRenewals(ctx, queries)
	tenants, err := queries.ListWarmTenantIDs(ctx)
	if err != nil {
		s.logger.WithError(err).Warn("Failed to enumerate tenants in use for media authority reconciliation")
		return
	}
	reissue := s.mediaAuthorityCompilerChanged(ctx, queries)
	now := time.Now().UTC()
	for _, tenantID := range sortedUnique(append(tenants, s.neverCompiledTenants(ctx, queries)...)) {
		notBefore := now.Add(mediaAuthoritySpread(tenantID, mediaAuthorityReconcileInterval))
		if enqueueErr := queries.EnqueueMediaAuthorityBulk(ctx, commodoredb.TenantMediaAuthorityTarget(tenantID), tenantID,
			"periodic_reconciliation", "reconcile:"+tenantID, notBefore); enqueueErr != nil {
			s.logger.WithError(enqueueErr).WithField("tenant_id", tenantID).Warn("Failed to enqueue media authority reconciliation")
		}
		if !reissue {
			continue
		}
		if enqueueErr := queries.EnqueueMediaAuthorityBulk(ctx, commodoredb.TenantMediaObjectsAuthorityTarget(tenantID), tenantID,
			"tenant_media_objects:compiler_changed", "reconcile:"+tenantID, notBefore); enqueueErr != nil {
			s.logger.WithError(enqueueErr).WithField("tenant_id", tenantID).Warn("Failed to enqueue media-object re-issue")
			reissue = false
		}
	}
	if reissue {
		if storeErr := queries.SetMediaAuthorityCompilerFingerprint(ctx, s.mediaAuthorityCompilerFingerprint()); storeErr != nil {
			s.logger.WithError(storeErr).Warn("Failed to record the media authority compiler fingerprint")
		}
	}
	if rearmed, rearmErr := queries.RearmParkedMediaAuthorityObligations(ctx); rearmErr != nil {
		s.logger.WithError(rearmErr).Warn("Failed to re-arm parked media authority refresh obligations")
	} else if rearmed > 0 {
		s.logger.WithField("targets", rearmed).Info("Re-armed parked media authority refresh obligations")
	}
}

// neverCompiledTenants returns the active tenants that have no authority at all.
func (s *CommodoreServer) neverCompiledTenants(ctx context.Context, queries *commodoredb.Queries) []string {
	callCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	active, err := s.authorityTenantSource.ListActiveTenants(callCtx)
	cancel()
	if err != nil {
		if ctx.Err() == nil {
			s.logger.WithError(err).Warn("Failed to enumerate tenants for media authority reconciliation")
		}
		return nil
	}
	known, err := queries.ListCurrentTenantAuthorityIDs(ctx)
	if err != nil {
		s.logger.WithError(err).Warn("Failed to enumerate existing tenant authorities for reconciliation")
		return nil
	}
	compiled := make(map[string]struct{}, len(known))
	for _, tenantID := range known {
		compiled[tenantID] = struct{}{}
	}
	var missing []string
	for _, tenantID := range active {
		if tenantID = strings.TrimSpace(tenantID); tenantID != "" {
			if _, ok := compiled[tenantID]; !ok {
				missing = append(missing, tenantID)
			}
		}
	}
	return missing
}

// mediaAuthorityCompilerFingerprint identifies what this compiler signs with and
// how it shapes payloads. mediaAuthorityCompilerRevision is raised by hand in the
// change that alters a payload for unchanged source state.
func (s *CommodoreServer) mediaAuthorityCompilerFingerprint() string {
	return fmt.Sprintf("key=%s;schema=%d;revision=%d", s.mediaAuthorityKeyID, sharedauthority.NodePlacementSchemaVersion, mediaAuthorityCompilerRevision)
}

func (s *CommodoreServer) mediaAuthorityCompilerChanged(ctx context.Context, queries *commodoredb.Queries) bool {
	stored, err := queries.GetMediaAuthorityCompilerFingerprint(ctx)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		s.logger.WithError(err).Warn("Failed to read the media authority compiler fingerprint")
		return false
	}
	return stored != s.mediaAuthorityCompilerFingerprint()
}

// mediaAuthoritySpread places a key at a fixed offset inside a window, so work
// swept from a set is spread over the window the same way every time.
func mediaAuthoritySpread(key string, window time.Duration) time.Duration {
	if window <= 0 {
		return 0
	}
	hash := fnv.New64a()
	_, _ = hash.Write([]byte(key))
	return time.Duration(hash.Sum64() % uint64(window))
}

// repairMediaAuthorityRenewals gives every live current authority without a
// live renewal obligation one. Publication and every no-op compile maintain the
// obligation themselves; this covers an authority nothing has compiled since it
// lost its schedule. A tombstone is terminal and is never scheduled.
//
// The renewal rows are also the tenant's index into its published objects, which
// is what a tenant change fans out over. Repair is bounded by batch count and
// elapsed time so database contention cannot stall periodic reconciliation.
// Historical tombstones are marked terminal and leave subsequent batches.
func (s *CommodoreServer) repairMediaAuthorityRenewals(ctx context.Context, queries *commodoredb.Queries) {
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	for pass := 0; pass < mediaAuthorityRenewalRepairPasses && ctx.Err() == nil; pass++ {
		rows, err := queries.ListCurrentMediaAuthoritiesWithoutRenewal(ctx, mediaAuthorityRenewalRepairBatch)
		if err != nil {
			s.logger.WithError(err).Warn("Failed to list media authorities without a renewal obligation")
			return
		}
		scheduled := 0
		for _, row := range rows {
			target, tenantID, tombstone, decodeErr := mediaAuthorityRenewalIdentity(row.AuthorityKind, row.AuthorityID, row.Payload)
			if decodeErr != nil {
				s.logger.WithError(decodeErr).WithField("authority_id", row.AuthorityID).Warn("Failed to decode a current media authority for renewal repair")
				continue
			}
			if tombstone {
				marked, markErr := queries.MarkMediaAuthorityVersionTombstone(ctx, commodoredb.MarkMediaAuthorityVersionTombstoneParams{
					AuthorityKind: row.AuthorityKind, AuthorityID: row.AuthorityID, AuthorityVersion: row.AuthorityVersion,
				})
				if markErr != nil {
					s.logger.WithError(markErr).WithField("authority_id", row.AuthorityID).Warn("Failed to mark terminal authority metadata")
				} else if marked > 0 {
					scheduled++
				}
				continue
			}
			renewAt := mediaAuthorityRenewAt(target, row.IssuedAt, row.RefreshAfter, row.ValidUntil)
			if scheduleErr := ensureMediaAuthorityRenewal(ctx, queries, target, tenantID, row.AuthorityVersion, renewAt, row.ValidUntil, false); scheduleErr != nil {
				s.logger.WithError(scheduleErr).WithField("target", target.Key).Warn("Failed to schedule a missing media authority renewal")
				continue
			}
			scheduled++
		}
		if len(rows) < mediaAuthorityRenewalRepairBatch || scheduled == 0 {
			return
		}
	}
}

func mediaAuthorityRenewalIdentity(authorityKind, authorityID string, payload []byte) (commodoredb.MediaAuthorityTarget, string, bool, error) {
	if authorityKind == "tenant" {
		tenant := &mediaauthoritypb.TenantAuthority{}
		if err := proto.Unmarshal(payload, tenant); err != nil {
			return commodoredb.MediaAuthorityTarget{}, "", false, err
		}
		return commodoredb.TenantMediaAuthorityTarget(authorityID), authorityID,
			tenant.GetLifecycle() == mediaauthoritypb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_TOMBSTONE, nil
	}
	object := &mediaauthoritypb.MediaObjectAuthority{}
	if err := proto.Unmarshal(payload, object); err != nil {
		return commodoredb.MediaAuthorityTarget{}, "", false, err
	}
	return mediaObjectAuthorityTarget(authorityID), object.GetTenantId(),
		object.GetLifecycle() == mediaauthoritypb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_TOMBSTONE, nil
}

func (s *CommodoreServer) runMediaAuthorityRetention(ctx context.Context) {
	if s.db == nil {
		return
	}
	sweep := func() {
		sweepCtx, cancel := context.WithTimeout(ctx, mediaAuthorityRetentionTimeout)
		defer cancel()
		if err := s.sweepMediaAuthorityRetention(sweepCtx, time.Now().UTC()); err != nil && ctx.Err() == nil {
			s.logger.WithError(err).Warn("Failed to retain media authority history")
		}
	}
	sweep()
	ticker := time.NewTicker(mediaAuthorityRetentionInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			sweep()
		}
	}
}

func (s *CommodoreServer) sweepMediaAuthorityRetention(ctx context.Context, now time.Time) error {
	queries := commodoredb.New(s.db)
	for ctx.Err() == nil {
		targetRows, err := queries.DeleteRetiredMediaAuthorityTargets(ctx, commodoredb.DeleteRetiredMediaAuthorityTargetsParams{
			ExpiredBefore: sql.NullTime{Time: now, Valid: true}, BatchSize: mediaAuthorityRetentionBatch,
		})
		if err != nil {
			return fmt.Errorf("delete retired media authority targets: %w", err)
		}
		inboxRows, err := queries.DeleteCompletedMediaAuthorityRefreshInbox(ctx, commodoredb.DeleteCompletedMediaAuthorityRefreshInboxParams{
			CompletedBefore: now.Add(-mediaAuthorityInboxRetention), BatchSize: mediaAuthorityRetentionBatch,
		})
		if err != nil {
			return fmt.Errorf("delete completed media authority refresh inbox: %w", err)
		}
		deliveryRows, err := queries.DeleteExpiredMediaAuthorityDeliveries(ctx, commodoredb.DeleteExpiredMediaAuthorityDeliveriesParams{
			ExpiredBefore: now.Add(-mediaAuthorityHistoryRetention), BatchSize: mediaAuthorityRetentionBatch,
		})
		if err != nil {
			return fmt.Errorf("delete expired media authority deliveries: %w", err)
		}
		versionRows, err := queries.DeleteOrphanedMediaAuthorityVersions(ctx, commodoredb.DeleteOrphanedMediaAuthorityVersionsParams{
			ExpiredBefore: now.Add(-mediaAuthorityHistoryRetention), BatchSize: mediaAuthorityRetentionBatch,
		})
		if err != nil {
			return fmt.Errorf("delete expired media authority versions: %w", err)
		}
		if targetRows < mediaAuthorityRetentionBatch && inboxRows < mediaAuthorityRetentionBatch && deliveryRows < mediaAuthorityRetentionBatch && versionRows < mediaAuthorityRetentionBatch {
			return nil
		}
	}
	return nil
}

// withMediaAuthorityCompileFence allocates an authority-scoped fencing generation
// without retaining a pooled connection while remote authority sources are
// queried. Persistence locks and verifies this generation in its short local
// transaction, so an older compile of the same authority cannot commit after
// a newer compile of that authority has started.
func (s *CommodoreServer) withMediaAuthorityCompileFence(ctx context.Context, scopeKey string, fn func(context.Context) error) error {
	scopeKey = strings.TrimSpace(scopeKey)
	if scopeKey == "" {
		return errors.New("media-authority compile requires scope key")
	}
	generation, err := commodoredb.New(s.db).BeginMediaAuthorityCompile(ctx, scopeKey)
	if err != nil {
		return fmt.Errorf("begin media-authority compile: %w", err)
	}
	compileCtx := context.WithValue(ctx, mediaAuthorityCompileFenceContextKey{}, mediaAuthorityCompileFence{scopeKey: scopeKey, generation: generation})
	return fn(compileCtx)
}

func lockMediaAuthorityCompileFence(ctx context.Context, queries *commodoredb.Queries, scopeKey string) error {
	fence, ok := ctx.Value(mediaAuthorityCompileFenceContextKey{}).(mediaAuthorityCompileFence)
	if !ok {
		return nil
	}
	if strings.TrimSpace(scopeKey) != fence.scopeKey {
		return errors.New("media-authority compile fence scope mismatch")
	}
	current, err := queries.LockMediaAuthorityCompile(ctx, fence.scopeKey)
	if err != nil {
		return fmt.Errorf("lock media-authority compile fence: %w", err)
	}
	if current != fence.generation {
		return fmt.Errorf("%w: generation %d, now %d", errMediaAuthorityCompileSuperseded, fence.generation, current)
	}
	return nil
}

func (s *CommodoreServer) processMediaAuthorityDeliveryBatch(ctx context.Context) {
	if s.db == nil || s.foghornPool == nil || s.quartermasterClient == nil {
		return
	}
	cells, err := commodoredb.New(s.db).ListMediaAuthorityDeliveryCells(ctx)
	if err != nil {
		if ctx.Err() == nil {
			s.logger.WithError(err).Warn("Failed to list cells with media authority deliveries waiting")
		}
		return
	}
	// Each cell is drained by its own goroutine with its own workers, and the
	// tick does not wait for any of them: a cell that is slow or down keeps only
	// its own workers busy and never delays the next claim for another cell.
	for _, cellID := range cells {
		if _, draining := s.mediaAuthorityDeliveryCells.LoadOrStore(cellID, struct{}{}); draining {
			continue
		}
		go func() {
			defer s.mediaAuthorityDeliveryCells.Delete(cellID)
			s.drainMediaAuthorityDeliveryCell(ctx, cellID)
		}()
	}
}

// drainMediaAuthorityDeliveryCell delivers to one cell until nothing is waiting
// for it. A single claimant feeds the cell's workers and claims again as soon as
// one frees.
func (s *CommodoreServer) drainMediaAuthorityDeliveryCell(ctx context.Context, cellID string) {
	completed := make(chan struct{}, mediaAuthorityDeliveryWorkers)
	running := 0
	wait := func() {
		for running > 0 {
			<-completed
			running--
		}
	}
	for {
		if ctx.Err() != nil {
			wait()
			return
		}
		available := mediaAuthorityDeliveryWorkers - running
		if available == 0 {
			select {
			case <-completed:
				running--
			case <-ctx.Done():
			}
			continue
		}
		rows, err := commodoredb.New(s.db).ClaimMediaAuthorityDeliveries(ctx, commodoredb.ClaimMediaAuthorityDeliveriesParams{
			CellID: cellID, LeaseMs: mediaAuthorityLease.Milliseconds(), BatchSize: int32(available),
		})
		if err != nil {
			if ctx.Err() == nil {
				s.logger.WithError(err).WithField("cell_id", cellID).Warn("Failed to claim media authority deliveries")
			}
			wait()
			return
		}
		if len(rows) == 0 {
			if running == 0 {
				return
			}
			select {
			case <-completed:
				running--
			case <-ctx.Done():
			}
			continue
		}
		for _, row := range rows {
			running++
			go func() {
				defer func() { completed <- struct{}{} }()
				s.processMediaAuthorityDeliveryRow(ctx, row, mediaAuthorityDeliveryTimeout)
			}()
		}
	}
}

// observeMediaAuthorityQueues settles deliveries that can no longer be made and
// refreshes the queue gauges. It runs on its own interval, off the delivery
// tick: none of it is needed to deliver, and at a second's interval it would
// cost more than the deliveries do.
func (s *CommodoreServer) observeMediaAuthorityQueues(ctx context.Context) {
	if s.db == nil {
		return
	}
	now := time.Now()
	if last := s.mediaAuthorityQueuesObservedAt.Load(); now.Sub(time.Unix(0, last)) < mediaAuthorityQueueObserveInterval ||
		!s.mediaAuthorityQueuesObservedAt.CompareAndSwap(last, now.UnixNano()) {
		return
	}
	queries := commodoredb.New(s.db)
	if _, err := queries.SupersedeExpiredObsoleteMediaAuthorityDeliveries(ctx, mediaAuthorityQueueSweepBatch); err != nil {
		s.logger.WithError(err).Warn("Failed to settle obsolete media authority deliveries")
	}
	if _, err := queries.SettleExpiredMediaAuthorityDeliveries(ctx, mediaAuthorityQueueSweepBatch); err != nil {
		s.logger.WithError(err).Warn("Failed to settle expired media authority deliveries")
	}
	statsCtx, statsCancel := context.WithTimeout(ctx, mediaAuthorityStatsTimeout)
	s.observeMediaAuthorityDeliveryStats(statsCtx)
	statsCancel()
	s.observeMediaAuthorityObligationStats(ctx)
}

func (s *CommodoreServer) processMediaAuthorityDeliveryRow(ctx context.Context, row commodoredb.ClaimMediaAuthorityDeliveriesRow, timeout time.Duration) {
	deliveryErr := runMediaAuthorityDelivery(ctx, timeout, func(deliveryCtx context.Context) error {
		return s.deliverMediaAuthority(deliveryCtx, row)
	})
	if deliveryErr == nil {
		ackCtx, ackCancel := context.WithTimeout(context.Background(), mediaAuthoritySettleTimeout)
		deliveryErr = s.acknowledgeMediaAuthorityDelivery(ackCtx, row)
		ackCancel()
	}
	if deliveryErr != nil {
		s.observeMediaAuthorityDeliveryAttempt(row.AuthorityKind, "failed")
		settleCtx, settleCancel := context.WithTimeout(context.Background(), mediaAuthoritySettleTimeout)
		s.failMediaAuthorityDelivery(settleCtx, row, deliveryErr)
		settleCancel()
		return
	}
	s.observeMediaAuthorityDeliveryAttempt(row.AuthorityKind, "acknowledged")
}

func runMediaAuthorityDelivery(ctx context.Context, timeout time.Duration, deliver func(context.Context) error) error {
	deliveryCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return deliver(deliveryCtx)
}

func (s *CommodoreServer) observeMediaAuthorityDeliveryAttempt(authorityKind, result string) {
	if s.metrics != nil && s.metrics.MediaAuthorityDeliveryAttempts != nil {
		s.metrics.MediaAuthorityDeliveryAttempts.WithLabelValues(authorityKind, result).Inc()
	}
}

func (s *CommodoreServer) observeMediaAuthorityDeliveryStats(ctx context.Context) {
	if s.metrics == nil || s.metrics.MediaAuthorityPending == nil || s.metrics.MediaAuthorityMaxVersionLag == nil || s.metrics.MediaAuthorityOldestPendingSeconds == nil {
		return
	}
	rows, err := commodoredb.New(s.db).ListMediaAuthorityDeliveryStats(ctx)
	if err != nil {
		s.logger.WithError(err).Warn("Failed to observe media authority delivery backlog")
		return
	}
	seen := map[string]struct{}{}
	for _, row := range rows {
		seen[row.AuthorityKind] = struct{}{}
		s.metrics.MediaAuthorityPending.WithLabelValues(row.AuthorityKind).Set(float64(row.PendingCount))
		s.metrics.MediaAuthorityMaxVersionLag.WithLabelValues(row.AuthorityKind).Set(float64(row.MaxVersionLag))
		s.metrics.MediaAuthorityOldestPendingSeconds.WithLabelValues(row.AuthorityKind).Set(row.OldestPendingSeconds)
		if s.metrics.MediaAuthorityRejectedDeliveries != nil {
			s.metrics.MediaAuthorityRejectedDeliveries.WithLabelValues(row.AuthorityKind).Set(float64(row.RejectedCount))
		}
	}
	for _, kind := range []string{"tenant", "media_object"} {
		if _, ok := seen[kind]; ok {
			continue
		}
		s.metrics.MediaAuthorityPending.WithLabelValues(kind).Set(0)
		s.metrics.MediaAuthorityMaxVersionLag.WithLabelValues(kind).Set(0)
		s.metrics.MediaAuthorityOldestPendingSeconds.WithLabelValues(kind).Set(0)
		if s.metrics.MediaAuthorityRejectedDeliveries != nil {
			s.metrics.MediaAuthorityRejectedDeliveries.WithLabelValues(kind).Set(0)
		}
	}
}

func (s *CommodoreServer) deliverMediaAuthority(ctx context.Context, row commodoredb.ClaimMediaAuthorityDeliveriesRow) error {
	return runSignedMediaAuthorityDelivery(ctx, row, s.applyMediaAuthorityDelivery)
}

func runSignedMediaAuthorityDelivery(ctx context.Context, row commodoredb.ClaimMediaAuthorityDeliveriesRow, deliver func(context.Context, commodoredb.ClaimMediaAuthorityDeliveriesRow, *mediaauthoritypb.SignedAuthorityEnvelope) error) error {
	signed := &mediaauthoritypb.SignedAuthorityEnvelope{}
	if err := (proto.UnmarshalOptions{DiscardUnknown: false}).Unmarshal(row.SignedEnvelope, signed); err != nil {
		return fmt.Errorf("decode queued media authority: %w", err)
	}
	expectedKind := mediaauthoritypb.AuthorityKind_AUTHORITY_KIND_UNSPECIFIED
	switch row.AuthorityKind {
	case "tenant":
		expectedKind = mediaauthoritypb.AuthorityKind_AUTHORITY_KIND_TENANT
	case "media_object":
		expectedKind = mediaauthoritypb.AuthorityKind_AUTHORITY_KIND_MEDIA_OBJECT
	}
	envelope := signed.GetEnvelope()
	if expectedKind == mediaauthoritypb.AuthorityKind_AUTHORITY_KIND_UNSPECIFIED || envelope == nil || row.AuthorityVersion <= 0 || envelope.GetKind() != expectedKind || envelope.GetAuthorityId() != row.AuthorityID || envelope.GetAuthorityVersion() != uint64(row.AuthorityVersion) || row.CellID == "" || envelope.GetAudienceCellId() != row.CellID {
		return errors.New("queued media authority identity mismatch")
	}
	if envelope.GetValidUntil() == nil || envelope.GetValidUntil().CheckValid() != nil {
		return errors.New("queued media authority has invalid expiry")
	}
	deliveryCtx, cancel := context.WithDeadline(ctx, envelope.GetValidUntil().AsTime())
	defer cancel()
	if err := deliveryCtx.Err(); err != nil {
		return err
	}
	if err := deliver(deliveryCtx, row, signed); err != nil {
		return err
	}
	return deliveryCtx.Err()
}

func (s *CommodoreServer) applyMediaAuthorityDelivery(ctx context.Context, row commodoredb.ClaimMediaAuthorityDeliveriesRow, signed *mediaauthoritypb.SignedAuthorityEnvelope) error {
	client, err := s.resolveFoghornForClusterDirect(ctx, row.CellID)
	if err != nil {
		return fmt.Errorf("resolve Foghorn control cell %q: %w", row.CellID, err)
	}
	resp, err := client.ApplyMediaAuthority(ctx, signed)
	if err != nil {
		// A cell refuses a signed authority only when the envelope was not meant
		// for it (wrong audience, unknown signer, bad signature). The pooled
		// connection is keyed by cell, and a replaced replica can leave that
		// connection attached to an address another cell's Foghorn now owns;
		// drop it so the next attempt discovers and dials the cell afresh.
		if status.Code(err) == codes.PermissionDenied && s.foghornPool != nil {
			s.foghornPool.Remove(foghornPoolKey(row.CellID, ""))
		}
		// Whatever went wrong, the addresses remembered for this cell are not
		// trusted for the retry.
		s.forgetFoghornDiscovery(row.CellID)
		return fmt.Errorf("apply media authority at cell %q: %w", row.CellID, err)
	}
	expectedKind := mediaauthoritypb.AuthorityKind_AUTHORITY_KIND_TENANT
	if row.AuthorityKind == "media_object" {
		expectedKind = mediaauthoritypb.AuthorityKind_AUTHORITY_KIND_MEDIA_OBJECT
	}
	validOutcome := resp.GetOutcome() == foghornpb.MediaAuthorityApplyOutcome_MEDIA_AUTHORITY_APPLY_OUTCOME_APPLIED || resp.GetOutcome() == foghornpb.MediaAuthorityApplyOutcome_MEDIA_AUTHORITY_APPLY_OUTCOME_DUPLICATE
	if resp.GetAuthorityKind() != expectedKind || resp.GetAuthorityId() != row.AuthorityID || resp.GetAuthorityVersion() != uint64(row.AuthorityVersion) || !validOutcome {
		return fmt.Errorf("cell %q acknowledged mismatched media authority", row.CellID)
	}
	// The attestation is recorded separately from the delivery acknowledgement:
	// a lost attestation only delays activation until the next acknowledgement,
	// while a lost acknowledgement would redeliver an already applied authority.
	if recordErr := s.recordCellPlacementCapability(ctx, row.CellID, resp.GetPlacementCapability()); recordErr != nil && s.logger != nil {
		s.logger.WithError(recordErr).WithField("cell_id", row.CellID).Warn("Failed to record cell placement capability attestation")
	}
	return nil
}

func (s *CommodoreServer) acknowledgeMediaAuthorityDelivery(ctx context.Context, row commodoredb.ClaimMediaAuthorityDeliveriesRow) error {
	// nil options means READ COMMITTED, and reconcilePlacementActivation below
	// depends on it: it serializes on an advisory lock and then re-reads the
	// delivery rows, which only observes the previous holder's committed
	// acknowledgement because a new snapshot is taken per statement. Raising the
	// isolation here would restore the lost-update bug the lock exists to close.
	return database.WithRetryablePostgresTx(ctx, s.db, nil, func(tx *sql.Tx) error {
		queries := commodoredb.New(tx)
		affected, err := queries.MarkMediaAuthorityDeliveryAcknowledged(ctx, commodoredb.MarkMediaAuthorityDeliveryAcknowledgedParams{
			AuthorityKind: row.AuthorityKind, AuthorityID: row.AuthorityID, AuthorityVersion: row.AuthorityVersion, CellID: row.CellID,
		})
		if err != nil || affected != 1 {
			return fmt.Errorf("acknowledge media authority delivery: rows=%d: %w", affected, err)
		}
		if err := queries.UpsertMediaAuthorityDistribution(ctx, commodoredb.UpsertMediaAuthorityDistributionParams{
			AuthorityKind: row.AuthorityKind, AuthorityID: row.AuthorityID, CellID: row.CellID, AuthorityVersion: row.AuthorityVersion,
		}); err != nil {
			return err
		}
		// Activation evidence is derived from acknowledgements, in the same
		// transaction, so an effective revision can never outrun its deliveries.
		if err := s.reconcilePlacementActivation(ctx, queries, row.AuthorityKind, row.AuthorityID); err != nil {
			return fmt.Errorf("reconcile placement activation: %w", err)
		}
		return nil
	})
}

func (s *CommodoreServer) failMediaAuthorityDelivery(ctx context.Context, row commodoredb.ClaimMediaAuthorityDeliveriesRow, cause error) {
	rejected := mediaAuthorityDeliveryRejected(cause)
	_, err := commodoredb.New(s.db).RecordMediaAuthorityDeliveryFailure(ctx, commodoredb.RecordMediaAuthorityDeliveryFailureParams{
		NextAttemptAt: time.Now().Add(authorityBackoff(row.Attempts, row.AuthorityKind, row.AuthorityID, row.CellID)), LastError: sql.NullString{String: cause.Error(), Valid: true},
		AuthorityKind: row.AuthorityKind, AuthorityID: row.AuthorityID, AuthorityVersion: row.AuthorityVersion, CellID: row.CellID,
		Rejected: rejected,
	})
	if err != nil {
		s.logger.WithError(err).WithField("delivery_error", cause.Error()).Error("Failed to reschedule media authority delivery")
		return
	}
	if rejected {
		s.logger.WithError(cause).WithFields(logging.Fields{
			"authority_kind": row.AuthorityKind, "authority_id": row.AuthorityID,
			"authority_version": row.AuthorityVersion, "cell_id": row.CellID,
		}).Warn("Cell rejected signed media authority; delivery will not be retried until the cell requests replay")
	}
}

// mediaAuthorityDeliveryRejected reports a refusal the cell repeats for every
// retry of the same envelope: Foghorn maps stale versions, digest conflicts,
// and terminal tombstones to FailedPrecondition.
// mediaAuthorityDeliveryRejected reports a refusal that retrying the same
// envelope cannot change. FailedPrecondition is the cell holding something that
// outranks it (a newer version, a conflicting digest, a terminal tombstone).
// InvalidArgument is the envelope itself: malformed for that cell's release, or
// past its validity by the time it arrived. Either way the delivery settles as
// rejected; retrying it would refuse again, forever, and write the cell an audit
// row each time.
func mediaAuthorityDeliveryRejected(cause error) bool {
	code := status.Code(cause)
	return code == codes.FailedPrecondition || code == codes.InvalidArgument
}

func authorityBackoff(attempt int32, identity ...string) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	if attempt > 8 {
		attempt = 8
	}
	base := time.Duration(1<<uint(attempt-1)) * time.Second
	hash := fnv.New64a()
	for _, part := range identity {
		_, _ = hash.Write([]byte(part))
		_, _ = hash.Write([]byte{0})
	}
	window := uint64(base / 2)
	if window == 0 {
		return base
	}
	return base + time.Duration(hash.Sum64()%window)
}

func hashProtoMessages(messages ...proto.Message) (string, error) {
	hash := sha256.New()
	for _, message := range messages {
		if message == nil {
			continue
		}
		encoded, err := proto.MarshalOptions{Deterministic: true}.Marshal(message)
		if err != nil {
			return "", fmt.Errorf("encode media authority source revision: %w", err)
		}
		var length [8]byte
		for i := range length {
			length[7-i] = byte(uint64(len(encoded)) >> (8 * i))
		}
		_, _ = hash.Write(length[:])
		_, _ = hash.Write(encoded)
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil)), nil
}

func hashJSON(value any) (string, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("encode media authority source revision: %w", err)
	}
	sum := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func sortedUnique(values []string) []string {
	set := make(map[string]struct{}, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			set[value] = struct{}{}
		}
	}
	return sortedSet(set)
}

func cloneStringMap(values map[string]string) map[string]string {
	if len(values) == 0 {
		return nil
	}
	out := make(map[string]string, len(values))
	for key, value := range values {
		out[key] = value
	}
	return out
}

func cloneAndSortAllowances(values []*meteringpb.MeterAllowance) []*meteringpb.MeterAllowance {
	out := make([]*meteringpb.MeterAllowance, 0, len(values))
	for _, value := range values {
		if value != nil {
			out = append(out, proto.CloneOf(value))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].GetMeter() < out[j].GetMeter() })
	return out
}

func sortedSet(values map[string]struct{}) []string {
	out := make([]string, 0, len(values))
	for value := range values {
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

type revisionRecord struct {
	Service  string `json:"service"`
	Revision string `json:"revision"`
}

func revisionJSON(revisions []*mediaauthoritypb.AuthoritySourceRevision) []revisionRecord {
	out := make([]revisionRecord, 0, len(revisions))
	for _, revision := range revisions {
		out = append(out, revisionRecord{Service: revision.GetService(), Revision: revision.GetRevision()})
	}
	return out
}

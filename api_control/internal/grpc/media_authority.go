package grpc

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"sort"
	"strings"
	"sync"
	"time"

	"frameworks/api_control/internal/database/commodoredb"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/database"
	sharedauthority "github.com/Livepeer-FrameWorks/monorepo/pkg/mediaauthority"
	clusterpeerpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/cluster_peer"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	mediaauthoritypb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_authority"
	meteringpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/metering_contract"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	"golang.org/x/sync/errgroup"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

const (
	mediaAuthorityRefreshInterval = 10 * time.Minute
	mediaAuthorityValidity        = 24 * time.Hour
	mediaAuthorityWorkerInterval  = time.Second
	mediaAuthorityLease           = 2 * time.Minute
	mediaAuthorityRefreshBatch    = 8
	mediaAuthorityDeliveryBatch   = 8
	mediaAuthorityRefreshWorkers  = 8
	mediaAuthorityDeliveryWorkers = 8
	mediaAuthorityRefreshTimeout  = 90 * time.Second
	mediaAuthorityDeliveryTimeout = 35 * time.Second
	mediaAuthoritySettleTimeout   = 5 * time.Second
	mediaAuthorityStatsTimeout    = 5 * time.Second
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
	rows, err := commodoredb.New(s.db).InsertMediaAuthorityRefreshInbox(ctx, commodoredb.InsertMediaAuthorityRefreshInboxParams{
		SourceService: source,
		SourceEventID: strings.TrimSpace(req.GetSourceEventId()),
		TenantID:      strings.TrimSpace(req.GetTenantId()),
		Reason:        strings.TrimSpace(req.GetReason()),
	})
	if err != nil {
		return nil, status.Errorf(codes.Internal, "persist media authority refresh: %v", err)
	}
	return &commodorepb.RequestMediaAuthorityRefreshResponse{Accepted: rows == 1}, nil
}

// RequestMediaAuthorityReplay reopens acknowledged deliveries for the current
// authority versions assigned to one control cell. Reapplying an already
// present version is an idempotent no-op at Foghorn; a restored or rebuilt
// cell database receives the complete current set without waiting for a new
// owner mutation.
func (s *CommodoreServer) RequestMediaAuthorityReplay(ctx context.Context, req *commodorepb.RequestMediaAuthorityReplayRequest) (*commodorepb.RequestMediaAuthorityReplayResponse, error) {
	if ctxkeys.GetAuthType(ctx) != "service" {
		return nil, status.Error(codes.PermissionDenied, "media authority replay requires service authentication")
	}
	if req == nil || strings.TrimSpace(req.GetControlCellId()) == "" {
		return nil, status.Error(codes.InvalidArgument, "control_cell_id is required")
	}
	count, err := commodoredb.New(s.db).RequeueCurrentMediaAuthoritiesForCell(ctx, strings.TrimSpace(req.GetControlCellId()))
	if err != nil {
		return nil, status.Errorf(codes.Internal, "requeue media authority deliveries: %v", err)
	}
	return &commodorepb.RequestMediaAuthorityReplayResponse{RequeuedCount: count}, nil
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
	issuedAt := time.Now().UTC()
	return s.persistTenantAuthority(ctx, payload, nil,
		[]*mediaauthoritypb.AuthoritySourceRevision{{Service: "quartermaster", Revision: "deleted"}},
		issuedAt, issuedAt.Add(mediaAuthorityValidity))
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

func (s *CommodoreServer) compileLiveStreamAuthority(ctx context.Context, streamID string) error {
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

	tenant, targets, secretTargets, tenantValidUntil, err := s.currentTenantAuthorityContext(ctx, source.TenantID)
	if err != nil {
		return err
	}
	lifecycle := mediaauthoritypb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_ACTIVE
	if source.DeletedAt.Valid {
		lifecycle = mediaauthoritypb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_TOMBSTONE
	} else if !tenantCanServeMediaObjects(tenant, secretTargets) {
		lifecycle = mediaauthoritypb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_INACTIVE
	}
	policy := denyPlaybackPolicy()
	var sealedPlayback []*mediaauthoritypb.SealedCellSecret
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
			sealedPlayback, err = s.sealAuthoritySecret(authorityID, secretTargets, playbackSecret)
			if err != nil {
				return err
			}
		}
		if policy.GetKind() == mediaauthoritypb.PlaybackPolicyKind_PLAYBACK_POLICY_KIND_WEBHOOK {
			policy.ConnectedOnly = len(sealedPlayback) == 0
		}
	}
	originClusterID := strings.TrimSpace(source.ActiveIngestClusterID)
	if originClusterID == "" {
		originClusterID = strings.TrimSpace(tenant.GetOfficialClusterId())
	}
	if lifecycle == mediaauthoritypb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_ACTIVE && originClusterID == "" {
		return errors.New("compile live-stream authority: active stream has no ingest owner cluster")
	}
	processesJSON := s.resolveProcessesJSON(ctx, source.TenantID, source.StreamID, originClusterID, "live")
	dvrProcessesJSON := s.resolveProcessesJSON(ctx, source.TenantID, source.StreamID, originClusterID, "dvr")
	var sealedSecrets []*mediaauthoritypb.SealedCellSecret
	if lifecycle == mediaauthoritypb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_ACTIVE {
		liveSecret, secretErr := s.compileLiveStreamSecret(ctx, authorityID, source.StreamID, source.TenantID, source.IngestMode)
		if secretErr != nil {
			return secretErr
		}
		sealedSecrets, err = s.sealLiveStreamSecret(authorityID, secretTargets, liveSecret)
		if err != nil {
			return err
		}
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
			PublishingCredentialSha256: sharedauthority.PublishingCredentialDigest(source.StreamKey),
			OutageIngestClusterId:      originClusterID,
			RecordingEnabled:           source.IsRecordingEnabled,
			ProcessesJson:              processesJSON,
			DvrProcessesJson:           dvrProcessesJSON,
			SealedCellSecrets:          sealedSecrets,
		}},
	}
	issuedAt := time.Now().UTC()
	validUntil := issuedAt.Add(mediaAuthorityValidity)
	if tenantValidUntil.Before(validUntil) {
		validUntil = tenantValidUntil
	}
	revision, err := hashJSON(source)
	if err != nil {
		return err
	}
	return s.persistMediaObjectAuthority(ctx, authorityID, payload, targets,
		[]*mediaauthoritypb.AuthoritySourceRevision{{Service: "commodore", Revision: revision}}, issuedAt, validUntil)
}

func (s *CommodoreServer) compileLiveStreamSecret(ctx context.Context, authorityID, streamID, tenantID, ingestMode string) (*mediaauthoritypb.LiveStreamSecret, error) {
	if len(s.mediaAuthorityRecipients) == 0 {
		return nil, nil
	}
	secret := &mediaauthoritypb.LiveStreamSecret{AuthorityId: authorityID, TenantId: tenantID}
	queries := commodoredb.New(s.db)
	switch strings.TrimSpace(ingestMode) {
	case "pull":
		row, err := queries.GetPullMediaAuthoritySecret(ctx, streamID)
		if err != nil {
			return nil, fmt.Errorf("load pull source for media authority: %w", err)
		}
		uri, err := s.pullSourceEncryptor.Decrypt(row.SourceUriEnc)
		if err != nil {
			return nil, fmt.Errorf("decrypt pull source for media authority: %w", err)
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
		uri, err := s.fieldEncryptor.Decrypt(row.TargetUri)
		if err != nil {
			return nil, fmt.Errorf("decrypt push target %q for media authority: %w", row.ID, err)
		}
		secret.PushTargets = append(secret.PushTargets, &mediaauthoritypb.PushTargetSecret{TargetId: row.ID, TargetUri: uri, Name: row.Name, Platform: row.Platform.String})
	}
	sort.Slice(secret.PushTargets, func(i, j int) bool { return secret.PushTargets[i].GetTargetId() < secret.PushTargets[j].GetTargetId() })
	if secret.GetSourceUri() == "" && secret.GetNativeSourceSpec() == "" && len(secret.GetPushTargets()) == 0 {
		return nil, nil
	}
	return secret, nil
}

func (s *CommodoreServer) sealLiveStreamSecret(authorityID string, targets []string, secret *mediaauthoritypb.LiveStreamSecret) ([]*mediaauthoritypb.SealedCellSecret, error) {
	if secret == nil {
		return nil, nil
	}
	return s.sealAuthoritySecret(authorityID, targets, secret)
}

func (s *CommodoreServer) sealAuthoritySecret(authorityID string, targets []string, secret proto.Message) ([]*mediaauthoritypb.SealedCellSecret, error) {
	if secret == nil || !secret.ProtoReflect().IsValid() {
		return nil, nil
	}
	plaintext, err := proto.MarshalOptions{Deterministic: true}.Marshal(secret)
	if err != nil {
		return nil, fmt.Errorf("encode media-authority secret: %w", err)
	}
	boxes := make([]*mediaauthoritypb.SealedCellSecret, 0, len(targets))
	for _, cellID := range sortedUnique(targets) {
		recipient, ok := s.mediaAuthorityRecipients[cellID]
		if !ok {
			return nil, fmt.Errorf("no media authority seal recipient configured for cell %q", cellID)
		}
		box, err := sharedauthority.SealSecret(cellID, authorityID, recipient, plaintext)
		if err != nil {
			return nil, fmt.Errorf("seal media-authority secret for cell %q: %w", cellID, err)
		}
		boxes = append(boxes, box)
	}
	return boxes, nil
}

func (s *CommodoreServer) compileArtifactAuthority(ctx context.Context, artifactID string) error {
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
	tenant, targets, secretTargets, tenantValidUntil, err := s.currentTenantAuthorityContext(ctx, source.TenantID)
	if err != nil {
		return err
	}
	lifecycle := mediaauthoritypb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_ACTIVE
	if source.ArtifactKind == "dvr" && !source.ParentStreamExists {
		lifecycle = mediaauthoritypb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_TOMBSTONE
	} else if !tenantCanServeMediaObjects(tenant, secretTargets) {
		lifecycle = mediaauthoritypb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_INACTIVE
	}
	policy := denyPlaybackPolicy()
	var sealedPlayback []*mediaauthoritypb.SealedCellSecret
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
			sealedPlayback, err = s.sealAuthoritySecret(authorityID, secretTargets, playbackSecret)
			if err != nil {
				return err
			}
		}
		if policy.GetKind() == mediaauthoritypb.PlaybackPolicyKind_PLAYBACK_POLICY_KIND_WEBHOOK {
			policy.ConnectedOnly = len(sealedPlayback) == 0
		}
	}
	payload, err := buildArtifactAuthorityPayload(source, tenant, policy, lifecycle)
	if err != nil {
		return err
	}
	payload.SealedPlaybackSecrets = sealedPlayback
	issuedAt := time.Now().UTC()
	validUntil := issuedAt.Add(7 * 24 * time.Hour)
	if tenantValidUntil.Before(validUntil) {
		validUntil = tenantValidUntil
	}
	revision, err := hashJSON(source)
	if err != nil {
		return err
	}
	return s.persistMediaObjectAuthority(ctx, authorityID, payload, targets,
		[]*mediaauthoritypb.AuthoritySourceRevision{{Service: "commodore", Revision: revision}}, issuedAt, validUntil)
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
		return nil, fmt.Errorf("decode playback policy: %w", err)
	}
	if doc.Type != "webhook" {
		return nil, nil
	}
	if doc.Webhook == nil || strings.TrimSpace(doc.Webhook.URL) == "" || strings.TrimSpace(encryptedSecret) == "" {
		return nil, errors.New("webhook playback policy has incomplete URL or secret")
	}
	if s.playbackWebhookEncryptor == nil {
		return nil, errors.New("playback webhook decryptor is unavailable")
	}
	secret, err := s.playbackWebhookEncryptor.Decrypt(encryptedSecret)
	if err != nil {
		return nil, fmt.Errorf("decrypt playback webhook secret: %w", err)
	}
	return &mediaauthoritypb.MediaObjectSecret{
		AuthorityId: authorityID, TenantId: tenantID,
		PlaybackWebhook: &mediaauthoritypb.PlaybackWebhookSecret{Url: doc.Webhook.URL, TimeoutMs: int32(doc.Webhook.TimeoutMs), Secret: secret},
	}, nil
}

func buildArtifactAuthorityPayload(source commodoredb.GetArtifactMediaAuthoritySourceRow, tenant *mediaauthoritypb.TenantAuthority, policy *mediaauthoritypb.PlaybackPolicy, lifecycle mediaauthoritypb.AuthorityLifecycle) (*mediaauthoritypb.MediaObjectAuthority, error) {
	kind, err := artifactAuthorityKind(source.ArtifactKind)
	if err != nil {
		return nil, err
	}
	originClusterID := strings.TrimSpace(source.OriginClusterID)
	if originClusterID == "" && tenant != nil {
		originClusterID = strings.TrimSpace(tenant.GetOfficialClusterId())
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
	payload.Lifecycle = mediaauthoritypb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_TOMBSTONE
	payload.PlaybackPolicy = denyPlaybackPolicy()
	payload.SealedPlaybackSecrets = nil
	if live := payload.GetLiveStream(); live != nil {
		live.SealedCellSecrets = nil
	}
	queries := commodoredb.New(s.db)
	currentTargets, err := queries.ListCurrentMediaAuthorityDeliveryCells(ctx, commodoredb.ListCurrentMediaAuthorityDeliveryCellsParams{
		AuthorityKind: "media_object", AuthorityID: authorityID,
	})
	if err != nil {
		return fmt.Errorf("load deleted media-object current delivery cells: %w", err)
	}
	priorTargets, err := queries.ListMediaAuthorityPriorCells(ctx, commodoredb.ListMediaAuthorityPriorCellsParams{
		AuthorityKind: "media_object", AuthorityID: authorityID,
	})
	if err != nil {
		return fmt.Errorf("load deleted media-object prior cells: %w", err)
	}
	targets := sortedUnique(append(currentTargets, priorTargets...))
	issuedAt := time.Now().UTC()
	return s.persistMediaObjectAuthority(ctx, authorityID, payload, targets,
		[]*mediaauthoritypb.AuthoritySourceRevision{{Service: "commodore", Revision: "deleted"}}, issuedAt, issuedAt.Add(mediaAuthorityValidity))
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

func (s *CommodoreServer) currentTenantAuthorityContext(ctx context.Context, tenantID string) (*mediaauthoritypb.TenantAuthority, []string, []string, time.Time, error) {
	queries := commodoredb.New(s.db)
	current, err := queries.GetCurrentMediaAuthorityPayload(ctx, commodoredb.GetCurrentMediaAuthorityPayloadParams{
		AuthorityKind: "tenant", AuthorityID: tenantID,
	})
	if err != nil {
		return nil, nil, nil, time.Time{}, fmt.Errorf("load current tenant authority for media object: %w", err)
	}
	if !current.ValidUntil.After(time.Now().UTC()) {
		return nil, nil, nil, time.Time{}, errors.New("current tenant authority is hard-expired")
	}
	tenant := &mediaauthoritypb.TenantAuthority{}
	if unmarshalErr := proto.Unmarshal(current.Payload, tenant); unmarshalErr != nil {
		return nil, nil, nil, time.Time{}, fmt.Errorf("decode current tenant authority: %w", unmarshalErr)
	}
	if tenant.GetTenantId() != tenantID {
		return nil, nil, nil, time.Time{}, errors.New("current tenant authority identity mismatch")
	}
	targets, err := queries.ListCurrentMediaAuthorityDeliveryCells(ctx, commodoredb.ListCurrentMediaAuthorityDeliveryCellsParams{
		AuthorityKind: "tenant", AuthorityID: tenantID,
	})
	if err != nil {
		return nil, nil, nil, time.Time{}, fmt.Errorf("load current tenant authority cells: %w", err)
	}
	secretTargets := activeTenantAuthorityCells(tenant)
	return tenant, targets, secretTargets, current.ValidUntil.UTC(), nil
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
		return nil, errors.New("protected media object has no playback policy")
	}
	if err := json.Unmarshal([]byte(encoded), &doc); err != nil {
		return nil, fmt.Errorf("decode playback policy: %w", err)
	}
	switch doc.Type {
	case "jwt":
		if doc.JWT == nil {
			return nil, errors.New("JWT playback policy has no JWT section")
		}
		keys, err := s.fetchActiveSigningKeys(ctx, tenantID)
		if err != nil {
			return nil, fmt.Errorf("load active playback signing keys: %w", err)
		}
		jwt := &mediaauthoritypb.PlaybackJwtPolicy{
			AllowedKeyIds:      sortedUnique(doc.JWT.AllowedKids),
			RequiredAudiences:  sortedUnique(doc.JWT.RequiredAudience),
			RequiredClaimsJson: cloneStringMap(doc.JWT.RequiredClaimsJSON),
		}
		for _, key := range keys {
			jwt.ActiveKeys = append(jwt.ActiveKeys, &mediaauthoritypb.PlaybackSigningKey{
				KeyId: key.GetKid(), Algorithm: key.GetAlgorithm(), PublicKeyPem: key.GetPublicKeyPem(),
			})
		}
		sort.Slice(jwt.ActiveKeys, func(i, j int) bool { return jwt.ActiveKeys[i].GetKeyId() < jwt.ActiveKeys[j].GetKeyId() })
		return &mediaauthoritypb.PlaybackPolicy{Kind: mediaauthoritypb.PlaybackPolicyKind_PLAYBACK_POLICY_KIND_JWT, Jwt: jwt}, nil
	case "webhook":
		return &mediaauthoritypb.PlaybackPolicy{Kind: mediaauthoritypb.PlaybackPolicyKind_PLAYBACK_POLICY_KIND_WEBHOOK, ConnectedOnly: true}, nil
	case "public":
		return nil, errors.New("protected media object has a public playback policy")
	default:
		return nil, fmt.Errorf("unsupported playback policy type %q", doc.Type)
	}
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
	if peer == nil {
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
	refreshAfter := issuedAt.Add(mediaAuthorityRefreshInterval)
	if !refreshAfter.Before(validUntil) {
		refreshAfter = issuedAt
	}
	return database.WithRetryablePostgresTx(ctx, s.db, nil, func(tx *sql.Tx) error {
		queries := commodoredb.New(tx)
		if err := lockMediaAuthorityCompileFence(ctx, queries, "tenant:"+payload.GetTenantId()); err != nil {
			return err
		}
		priorCells, err := queries.ListMediaAuthorityPriorCells(ctx, commodoredb.ListMediaAuthorityPriorCellsParams{AuthorityKind: "tenant", AuthorityID: payload.GetTenantId()})
		if err != nil {
			return fmt.Errorf("load prior tenant authority cells: %w", err)
		}
		targetSet := make(map[string]struct{}, len(targets)+len(priorCells))
		for _, cell := range append(append([]string(nil), targets...), priorCells...) {
			if cell = strings.TrimSpace(cell); cell != "" {
				targetSet[cell] = struct{}{}
			}
		}
		allTargets := sortedSet(targetSet)
		version, err := queries.AllocateMediaAuthorityVersion(ctx, commodoredb.AllocateMediaAuthorityVersionParams{AuthorityKind: "tenant", AuthorityID: payload.GetTenantId()})
		if err != nil {
			return fmt.Errorf("allocate tenant authority version: %w", err)
		}
		if version <= 0 {
			return errors.New("allocated invalid tenant authority version")
		}
		var firstEnvelope *mediaauthoritypb.AuthorityEnvelope
		signedByCell := make(map[string][]byte, len(allTargets))
		for _, cell := range allTargets {
			envelope, envelopeErr := sharedauthority.NewEnvelope(mediaauthoritypb.AuthorityKind_AUTHORITY_KIND_TENANT, payload.GetTenantId(), uint64(version), issuedAt, refreshAfter, validUntil, s.mediaAuthorityKeyID, cell, payload, revisions)
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
			PayloadSchemaVersion: sharedauthority.SchemaVersion, Payload: firstEnvelope.GetPayload(), PayloadSha256: firstEnvelope.GetPayloadSha256(),
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
			})
			if err != nil || rows != 1 {
				return fmt.Errorf("enqueue tenant authority for cell %q: rows=%d: %w", cell, rows, err)
			}
		}
		return nil
	})
}

func (s *CommodoreServer) persistMediaObjectAuthority(ctx context.Context, authorityID string, payload *mediaauthoritypb.MediaObjectAuthority, targets []string, revisions []*mediaauthoritypb.AuthoritySourceRevision, issuedAt, validUntil time.Time) error {
	if !validUntil.After(issuedAt) {
		return errors.New("media-object authority requires future validity")
	}
	refreshAfter := issuedAt.Add(mediaAuthorityRefreshInterval)
	if !refreshAfter.Before(validUntil) {
		refreshAfter = issuedAt
	}
	return database.WithRetryablePostgresTx(ctx, s.db, nil, func(tx *sql.Tx) error {
		queries := commodoredb.New(tx)
		if err := lockMediaAuthorityCompileFence(ctx, queries, "media_object:"+authorityID); err != nil {
			return err
		}
		priorCells, err := queries.ListMediaAuthorityPriorCells(ctx, commodoredb.ListMediaAuthorityPriorCellsParams{AuthorityKind: "media_object", AuthorityID: authorityID})
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
		version, err := queries.AllocateMediaAuthorityVersion(ctx, commodoredb.AllocateMediaAuthorityVersionParams{AuthorityKind: "media_object", AuthorityID: authorityID})
		if err != nil {
			return fmt.Errorf("allocate media-object authority version: %w", err)
		}
		var firstEnvelope *mediaauthoritypb.AuthorityEnvelope
		signedByCell := make(map[string][]byte, len(allTargets))
		for _, cell := range allTargets {
			envelope, envelopeErr := sharedauthority.NewEnvelope(mediaauthoritypb.AuthorityKind_AUTHORITY_KIND_MEDIA_OBJECT, authorityID, uint64(version), issuedAt, refreshAfter, validUntil, s.mediaAuthorityKeyID, cell, payload, revisions)
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
			PayloadSchemaVersion: sharedauthority.SchemaVersion, Payload: firstEnvelope.GetPayload(), PayloadSha256: firstEnvelope.GetPayloadSha256(),
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
			})
			if err != nil || rows != 1 {
				return fmt.Errorf("enqueue media-object authority for cell %q: rows=%d: %w", cell, rows, err)
			}
		}
		return nil
	})
}

func (s *CommodoreServer) runMediaAuthorityWorkers(ctx context.Context) {
	runMediaAuthorityWorkerPair(ctx, s.processMediaAuthorityRefreshBatch, s.processMediaAuthorityDeliveryBatch)
}

func runMediaAuthorityWorkerPair(ctx context.Context, refresh, deliver func(context.Context)) {
	var workers sync.WaitGroup
	workers.Add(2)
	go func() {
		defer workers.Done()
		runMediaAuthorityWorker(ctx, refresh)
	}()
	go func() {
		defer workers.Done()
		runMediaAuthorityWorker(ctx, deliver)
	}()
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
	reconcile := func() {
		callCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		tenants, err := s.authorityTenantSource.ListActiveTenants(callCtx)
		cancel()
		if err != nil {
			if ctx.Err() == nil {
				s.logger.WithError(err).Warn("Failed to enumerate tenants for media authority reconciliation")
			}
			tenants = nil
		}
		queries := commodoredb.New(s.db)
		knownTenants, knownErr := queries.ListCurrentTenantAuthorityIDs(ctx)
		if knownErr != nil {
			s.logger.WithError(knownErr).Warn("Failed to enumerate existing tenant authorities for reconciliation")
		} else {
			tenants = sortedUnique(append(tenants, knownTenants...))
		}
		bucket := time.Now().UTC().Unix() / int64(mediaAuthorityRefreshInterval/time.Second)
		for _, tenantID := range tenants {
			tenantID = strings.TrimSpace(tenantID)
			if tenantID == "" {
				continue
			}
			_, enqueueErr := queries.InsertMediaAuthorityRefreshInbox(ctx, commodoredb.InsertMediaAuthorityRefreshInboxParams{
				SourceService: "commodore",
				SourceEventID: fmt.Sprintf("reconcile:%d:%s", bucket, tenantID),
				TenantID:      tenantID,
				Reason:        "periodic_reconciliation",
			})
			if enqueueErr != nil {
				s.logger.WithError(enqueueErr).WithField("tenant_id", tenantID).Warn("Failed to enqueue media authority reconciliation")
			}
		}
		streams, err := queries.ListLiveStreamMediaAuthoritySources(ctx)
		if err != nil {
			s.logger.WithError(err).Warn("Failed to enumerate live streams for media authority reconciliation")
		} else {
			for _, stream := range streams {
				_, enqueueErr := queries.InsertMediaAuthorityRefreshInbox(ctx, commodoredb.InsertMediaAuthorityRefreshInboxParams{
					SourceService: "commodore",
					SourceEventID: fmt.Sprintf("reconcile:%d:live_stream:%s", bucket, stream.StreamID),
					TenantID:      stream.TenantID,
					Reason:        "media_object:live_stream:" + stream.StreamID + ":periodic_reconciliation",
				})
				if enqueueErr != nil {
					s.logger.WithError(enqueueErr).WithField("stream_id", stream.StreamID).Warn("Failed to enqueue live-stream authority reconciliation")
				}
			}
		}
		artifacts, err := queries.ListArtifactMediaAuthoritySources(ctx)
		if err != nil {
			s.logger.WithError(err).Warn("Failed to enumerate artifacts for media authority reconciliation")
		} else {
			for _, artifact := range artifacts {
				_, enqueueErr := queries.InsertMediaAuthorityRefreshInbox(ctx, commodoredb.InsertMediaAuthorityRefreshInboxParams{
					SourceService: "commodore",
					SourceEventID: fmt.Sprintf("reconcile:%d:%s:%s", bucket, artifact.ArtifactKind, artifact.AuthorityID),
					TenantID:      artifact.TenantID,
					Reason:        "media_object:" + artifact.ArtifactKind + ":" + artifact.AuthorityID + ":periodic_reconciliation",
				})
				if enqueueErr != nil {
					s.logger.WithError(enqueueErr).WithField("artifact_id", artifact.AuthorityID).Warn("Failed to enqueue artifact authority reconciliation")
				}
			}
		}
	}

	reconcile()
	ticker := time.NewTicker(mediaAuthorityRefreshInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			reconcile()
		}
	}
}

func (s *CommodoreServer) processMediaAuthorityRefreshBatch(ctx context.Context) {
	if !s.mediaAuthorityEnabled() {
		return
	}
	rows, err := commodoredb.New(s.db).ClaimMediaAuthorityRefreshInbox(ctx, commodoredb.ClaimMediaAuthorityRefreshInboxParams{LeaseMs: mediaAuthorityLease.Milliseconds(), BatchSize: mediaAuthorityRefreshBatch})
	if err != nil {
		s.logger.WithError(err).Warn("Failed to claim media authority refresh inbox")
		return
	}
	group, groupCtx := errgroup.WithContext(ctx)
	group.SetLimit(mediaAuthorityRefreshWorkers)
	for _, row := range rows {
		row := row
		group.Go(func() error {
			rowCtx, cancel := context.WithTimeout(groupCtx, mediaAuthorityRefreshTimeout)
			defer cancel()
			s.processMediaAuthorityRefreshRow(rowCtx, row)
			return nil
		})
	}
	if waitErr := group.Wait(); waitErr != nil && ctx.Err() == nil {
		s.logger.WithError(waitErr).Warn("Media authority refresh batch ended early")
	}
}

func (s *CommodoreServer) processMediaAuthorityRefreshRow(ctx context.Context, row commodoredb.ClaimMediaAuthorityRefreshInboxRow) {
	tenantRefresh := false
	objectKind, objectID, objectRefresh := parseMediaObjectRefreshReason(row.Reason)
	scopeKey := mediaAuthorityCompileScope(row.TenantID, objectKind, objectID, objectRefresh)
	compileErr := s.withMediaAuthorityCompileFence(ctx, scopeKey, func(compileCtx context.Context) error {
		if objectRefresh {
			switch objectKind {
			case "live_stream":
				return s.compileLiveStreamAuthority(compileCtx, objectID)
			case "clip", "dvr", "vod", "chapter":
				return s.compileArtifactAuthority(compileCtx, objectID)
			default:
				return fmt.Errorf("unsupported media-object refresh kind %q", objectKind)
			}
		}
		if strings.HasPrefix(strings.TrimSpace(row.Reason), "tenant_media_objects:") {
			tenantRefresh = true
			return s.completeTenantAuthorityRefresh(compileCtx, row)
		}
		tenantRefresh = true
		if err := s.compileTenantAuthority(compileCtx, row.TenantID); err != nil {
			return err
		}
		return s.completeTenantAuthorityRefresh(compileCtx, row)
	})
	if compileErr != nil {
		settleCtx, settleCancel := context.WithTimeout(context.Background(), mediaAuthoritySettleTimeout)
		s.failMediaAuthorityRefresh(settleCtx, row, compileErr)
		settleCancel()
		return
	}
	if tenantRefresh {
		return
	}
	if affected, err := commodoredb.New(s.db).CompleteMediaAuthorityRefreshInbox(ctx, commodoredb.CompleteMediaAuthorityRefreshInboxParams{SourceService: row.SourceService, SourceEventID: row.SourceEventID}); err != nil || affected != 1 {
		completionErr := err
		if completionErr == nil {
			completionErr = fmt.Errorf("complete media authority refresh: rows=%d, want 1", affected)
		} else {
			completionErr = fmt.Errorf("complete media authority refresh: %w", completionErr)
		}
		settleCtx, settleCancel := context.WithTimeout(context.Background(), mediaAuthoritySettleTimeout)
		s.failMediaAuthorityRefresh(settleCtx, row, completionErr)
		settleCancel()
	}
}

func mediaAuthorityCompileScope(tenantID, objectKind, objectID string, objectRefresh bool) string {
	if objectRefresh {
		switch objectKind {
		case "live_stream":
			return "media_object:" + sharedauthority.LiveStreamAuthorityID(objectID)
		case "clip", "dvr", "vod", "chapter":
			return "media_object:" + sharedauthority.ArtifactAuthorityID(objectID)
		}
	}
	return "tenant:" + strings.TrimSpace(tenantID)
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
		return fmt.Errorf("media-authority compile generation %d was superseded by %d", fence.generation, current)
	}
	return nil
}

// completeTenantAuthorityRefresh transactionally creates the dependent object
// refreshes before acknowledging a source event that can change object policy,
// secret recipients, or validity. Balance/allowance-only Purser events skip
// that fanout because those decisions live exclusively in tenant authority.
func (s *CommodoreServer) completeTenantAuthorityRefresh(ctx context.Context, row commodoredb.ClaimMediaAuthorityRefreshInboxRow) error {
	seed := sha256.Sum256([]byte(row.SourceService + "\x00" + row.SourceEventID))
	prefix := "tenant-fanout:" + hex.EncodeToString(seed[:12])
	return database.WithRetryablePostgresTx(ctx, s.db, nil, func(tx *sql.Tx) error {
		queries := commodoredb.New(tx)
		if !tenantRefreshRequiresObjectFanout(row.SourceService, row.Reason) {
			affected, err := queries.CompleteMediaAuthorityRefreshInbox(ctx, commodoredb.CompleteMediaAuthorityRefreshInboxParams{SourceService: row.SourceService, SourceEventID: row.SourceEventID})
			if err != nil || affected != 1 {
				return fmt.Errorf("complete tenant-only authority refresh: rows=%d: %w", affected, err)
			}
			return nil
		}
		streams, err := queries.ListTenantLiveStreamMediaAuthoritySources(ctx, row.TenantID)
		if err != nil {
			return fmt.Errorf("list tenant live-stream authority sources: %w", err)
		}
		for _, stream := range streams {
			if _, enqueueErr := queries.InsertMediaAuthorityRefreshInbox(ctx, commodoredb.InsertMediaAuthorityRefreshInboxParams{
				SourceService: "commodore", SourceEventID: prefix + ":live:" + stream.StreamID,
				TenantID: row.TenantID, Reason: "media_object:live_stream:" + stream.StreamID + ":tenant_authority_changed",
			}); enqueueErr != nil {
				return fmt.Errorf("enqueue tenant live-stream authority refresh: %w", enqueueErr)
			}
		}
		artifacts, err := queries.ListTenantArtifactMediaAuthoritySources(ctx, row.TenantID)
		if err != nil {
			return fmt.Errorf("list tenant artifact authority sources: %w", err)
		}
		for _, artifact := range artifacts {
			if _, enqueueErr := queries.InsertMediaAuthorityRefreshInbox(ctx, commodoredb.InsertMediaAuthorityRefreshInboxParams{
				SourceService: "commodore", SourceEventID: prefix + ":" + artifact.ArtifactKind + ":" + artifact.AuthorityID,
				TenantID: row.TenantID, Reason: "media_object:" + artifact.ArtifactKind + ":" + artifact.AuthorityID + ":tenant_authority_changed",
			}); enqueueErr != nil {
				return fmt.Errorf("enqueue tenant artifact authority refresh: %w", enqueueErr)
			}
		}
		affected, err := queries.CompleteMediaAuthorityRefreshInbox(ctx, commodoredb.CompleteMediaAuthorityRefreshInboxParams{SourceService: row.SourceService, SourceEventID: row.SourceEventID})
		if err != nil || affected != 1 {
			return fmt.Errorf("complete tenant authority refresh: rows=%d: %w", affected, err)
		}
		return nil
	})
}

func tenantRefreshRequiresObjectFanout(sourceService, reason string) bool {
	if strings.TrimSpace(sourceService) != "purser" {
		return true
	}
	switch strings.TrimSpace(reason) {
	case "allowance_usage_changed", "prepaid_admission_gate_changed":
		// These mutate only tenant admission. Media objects contain no balance
		// or allowance state, so re-signing every object would add no authority
		// and turns metering into O(objects × cells) work.
		return false
	default:
		return true
	}
}

func (s *CommodoreServer) failMediaAuthorityRefresh(ctx context.Context, row commodoredb.ClaimMediaAuthorityRefreshInboxRow, cause error) {
	next := time.Now().Add(authorityBackoff(row.Attempts, row.SourceService, row.SourceEventID))
	_, err := commodoredb.New(s.db).FailMediaAuthorityRefreshInbox(ctx, commodoredb.FailMediaAuthorityRefreshInboxParams{
		NextAttemptAt: next, LastError: sql.NullString{String: cause.Error(), Valid: true}, SourceService: row.SourceService, SourceEventID: row.SourceEventID,
	})
	if err != nil {
		s.logger.WithError(err).WithError(cause).Error("Failed to reschedule media authority refresh")
	}
}

func (s *CommodoreServer) processMediaAuthorityDeliveryBatch(ctx context.Context) {
	if s.db == nil || s.foghornPool == nil || s.quartermasterClient == nil {
		return
	}
	statsCtx, statsCancel := context.WithTimeout(ctx, mediaAuthorityStatsTimeout)
	s.observeMediaAuthorityDeliveryStats(statsCtx)
	statsCancel()
	rows, err := commodoredb.New(s.db).ClaimMediaAuthorityDeliveries(ctx, commodoredb.ClaimMediaAuthorityDeliveriesParams{LeaseMs: mediaAuthorityLease.Milliseconds(), BatchSize: mediaAuthorityDeliveryBatch})
	if err != nil {
		s.logger.WithError(err).Warn("Failed to claim media authority deliveries")
		return
	}
	group, groupCtx := errgroup.WithContext(ctx)
	group.SetLimit(mediaAuthorityDeliveryWorkers)
	for _, row := range rows {
		row := row
		group.Go(func() error {
			deliveryErr := runMediaAuthorityDelivery(groupCtx, mediaAuthorityDeliveryTimeout, func(deliveryCtx context.Context) error {
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
				return nil
			}
			s.observeMediaAuthorityDeliveryAttempt(row.AuthorityKind, "acknowledged")
			return nil
		})
	}
	if waitErr := group.Wait(); waitErr != nil && ctx.Err() == nil {
		s.logger.WithError(waitErr).Warn("Media authority delivery batch ended early")
	}
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
	}
	for _, kind := range []string{"tenant", "media_object"} {
		if _, ok := seen[kind]; ok {
			continue
		}
		s.metrics.MediaAuthorityPending.WithLabelValues(kind).Set(0)
		s.metrics.MediaAuthorityMaxVersionLag.WithLabelValues(kind).Set(0)
		s.metrics.MediaAuthorityOldestPendingSeconds.WithLabelValues(kind).Set(0)
	}
}

func (s *CommodoreServer) deliverMediaAuthority(ctx context.Context, row commodoredb.ClaimMediaAuthorityDeliveriesRow) error {
	signed := &mediaauthoritypb.SignedAuthorityEnvelope{}
	if err := (proto.UnmarshalOptions{DiscardUnknown: false}).Unmarshal(row.SignedEnvelope, signed); err != nil {
		return fmt.Errorf("decode queued media authority: %w", err)
	}
	client, err := s.resolveFoghornForClusterDirect(ctx, row.CellID)
	if err != nil {
		return fmt.Errorf("resolve Foghorn control cell %q: %w", row.CellID, err)
	}
	resp, err := client.ApplyMediaAuthority(ctx, signed)
	if err != nil {
		return fmt.Errorf("apply media authority at cell %q: %w", row.CellID, err)
	}
	expectedKind := mediaauthoritypb.AuthorityKind_AUTHORITY_KIND_TENANT
	if row.AuthorityKind == "media_object" {
		expectedKind = mediaauthoritypb.AuthorityKind_AUTHORITY_KIND_MEDIA_OBJECT
	}
	if resp.GetAuthorityKind() != expectedKind || resp.GetAuthorityId() != row.AuthorityID || resp.GetAuthorityVersion() != uint64(row.AuthorityVersion) || resp.GetOutcome() == 0 {
		return fmt.Errorf("cell %q acknowledged mismatched media authority", row.CellID)
	}
	return nil
}

func (s *CommodoreServer) acknowledgeMediaAuthorityDelivery(ctx context.Context, row commodoredb.ClaimMediaAuthorityDeliveriesRow) error {
	return database.WithRetryablePostgresTx(ctx, s.db, nil, func(tx *sql.Tx) error {
		queries := commodoredb.New(tx)
		affected, err := queries.MarkMediaAuthorityDeliveryAcknowledged(ctx, commodoredb.MarkMediaAuthorityDeliveryAcknowledgedParams{
			AuthorityKind: row.AuthorityKind, AuthorityID: row.AuthorityID, AuthorityVersion: row.AuthorityVersion, CellID: row.CellID,
		})
		if err != nil || affected != 1 {
			return fmt.Errorf("acknowledge media authority delivery: rows=%d: %w", affected, err)
		}
		return queries.UpsertMediaAuthorityDistribution(ctx, commodoredb.UpsertMediaAuthorityDistributionParams{
			AuthorityKind: row.AuthorityKind, AuthorityID: row.AuthorityID, CellID: row.CellID, AuthorityVersion: row.AuthorityVersion,
		})
	})
}

func (s *CommodoreServer) failMediaAuthorityDelivery(ctx context.Context, row commodoredb.ClaimMediaAuthorityDeliveriesRow, cause error) {
	_, err := commodoredb.New(s.db).RecordMediaAuthorityDeliveryFailure(ctx, commodoredb.RecordMediaAuthorityDeliveryFailureParams{
		NextAttemptAt: time.Now().Add(authorityBackoff(row.Attempts, row.AuthorityKind, row.AuthorityID, row.CellID)), LastError: sql.NullString{String: cause.Error(), Valid: true},
		AuthorityKind: row.AuthorityKind, AuthorityID: row.AuthorityID, AuthorityVersion: row.AuthorityVersion, CellID: row.CellID,
	})
	if err != nil {
		s.logger.WithError(err).WithError(cause).Error("Failed to reschedule media authority delivery")
	}
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

func parseMediaObjectRefreshReason(reason string) (kind, id string, ok bool) {
	parts := strings.SplitN(reason, ":", 4)
	if len(parts) != 4 || parts[0] != "media_object" || strings.TrimSpace(parts[1]) == "" || strings.TrimSpace(parts[2]) == "" {
		return "", "", false
	}
	return parts[1], parts[2], true
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

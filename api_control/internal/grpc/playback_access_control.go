package grpc

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"strings"
	"time"

	"frameworks/api_control/internal/database/commodoredb"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/auth"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
)

// activeSigningKeyCap matches Studio's per-project limit of 10 active keys.
const activeSigningKeyCap = 10

// CreateSigningKey generates a new ES256 keypair, stores the public key, and
// returns the private PEM exactly once. The private key is never persisted.
func (s *CommodoreServer) CreateSigningKey(ctx context.Context, req *commodorepb.CreateSigningKeyRequest) (*commodorepb.CreateSigningKeyResponse, error) {
	userID, tenantID, err := extractUserContext(ctx)
	if err != nil {
		return nil, err
	}
	name := strings.TrimSpace(req.GetName())
	if name == "" {
		return nil, status.Error(codes.InvalidArgument, "name is required")
	}

	privatePEM, publicPEM, kid, err := auth.GenerateES256Keypair()
	if err != nil {
		s.logger.WithError(err).Error("ES256 keypair generation failed")
		return nil, status.Errorf(codes.Internal, "key generation failed")
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		s.logger.WithError(err).Error("begin signing-key tx failed")
		return nil, status.Errorf(codes.Internal, "database error")
	}
	defer tx.Rollback() //nolint:errcheck // rollback is best-effort

	// Serialize concurrent CreateSigningKey for this tenant so the cap check
	// and INSERT are atomic. Released on commit/rollback.
	queries := commodoredb.New(tx)
	if lockErr := queries.LockSigningKeyTenant(ctx, tenantID); lockErr != nil {
		s.logger.WithError(lockErr).Error("advisory lock for signing-key create failed")
		return nil, status.Errorf(codes.Internal, "database error")
	}

	activeCount, cntErr := queries.CountActiveSigningKeys(ctx, tenantID)
	if cntErr != nil {
		s.logger.WithError(cntErr).Error("count active signing keys failed")
		return nil, status.Errorf(codes.Internal, "database error")
	}
	if activeCount >= activeSigningKeyCap {
		return nil, status.Errorf(codes.ResourceExhausted, "tenant has reached the active signing-key cap (%d); revoke an existing key first", activeSigningKeyCap)
	}

	created, insErr := queries.CreateSigningKey(ctx, commodoredb.CreateSigningKeyParams{
		TenantID:     tenantID,
		Kid:          kid,
		Name:         name,
		PublicKeyPem: publicPEM,
	})
	if insErr != nil {
		s.logger.WithError(insErr).Error("insert signing key failed")
		return nil, status.Errorf(codes.Internal, "database error")
	}
	if !created.CreatedAt.Valid {
		s.logger.Error("insert signing key returned NULL created_at")
		return nil, status.Errorf(codes.Internal, "database error")
	}

	if auditErr := s.writeSigningKeyAudit(ctx, tx, tenantID, kid, "create", userID, name); auditErr != nil {
		return nil, status.Errorf(codes.Internal, "database error")
	}

	if commitErr := tx.Commit(); commitErr != nil {
		s.logger.WithError(commitErr).Error("commit signing-key tx failed")
		return nil, status.Errorf(codes.Internal, "database error")
	}

	return &commodorepb.CreateSigningKeyResponse{
		SigningKey: &commodorepb.SigningKey{
			Id:           created.ID,
			Kid:          kid,
			Name:         name,
			Algorithm:    "ES256",
			PublicKeyPem: publicPEM,
			Status:       "active",
			CreatedAt:    created.CreatedAt.Time.UTC().Format(time.RFC3339Nano),
		},
		PrivateKeyPem: privatePEM,
	}, nil
}

// GetSigningKey fetches a single signing key, tenant-scoped.
func (s *CommodoreServer) GetSigningKey(ctx context.Context, req *commodorepb.GetSigningKeyRequest) (*commodorepb.SigningKey, error) {
	_, tenantID, err := extractUserContext(ctx)
	if err != nil {
		return nil, err
	}
	id := strings.TrimSpace(req.GetId())
	if id == "" {
		return nil, status.Error(codes.InvalidArgument, "id is required")
	}
	row, err := commodoredb.New(s.db).GetSigningKey(ctx, commodoredb.GetSigningKeyParams{ID: id, TenantID: tenantID})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, status.Error(codes.NotFound, "signing key not found")
	}
	if err != nil {
		s.logger.WithError(err).Error("get signing key failed")
		return nil, status.Errorf(codes.Internal, "database error")
	}
	return signingKeyProto(row.ID, row.Kid, row.Name, row.Algorithm, row.PublicKeyPem, row.Status, row.CreatedAt, row.LastUsedAt, row.RevokedAt)
}

// ListSigningKeys returns the tenant's signing keys with optional status filter.
func (s *CommodoreServer) ListSigningKeys(ctx context.Context, req *commodorepb.ListSigningKeysRequest) (*commodorepb.ListSigningKeysResponse, error) {
	_, tenantID, err := extractUserContext(ctx)
	if err != nil {
		return nil, err
	}
	limit := int(req.GetLimit())
	if limit <= 0 || limit > 100 {
		limit = 50
	}

	queries := commodoredb.New(s.db)
	statusFilter := strings.ToLower(strings.TrimSpace(req.GetStatusFilter()))
	if statusFilter != "active" && statusFilter != "revoked" {
		statusFilter = ""
	}
	var afterCreatedAt time.Time
	if afterID := strings.TrimSpace(req.GetAfterId()); afterID != "" {
		cursor, cursorErr := queries.GetSigningKeyCursor(ctx, commodoredb.GetSigningKeyCursorParams{ID: afterID, TenantID: tenantID})
		if cursorErr != nil {
			if errors.Is(cursorErr, sql.ErrNoRows) {
				return nil, status.Error(codes.InvalidArgument, "after cursor not found")
			}
			s.logger.WithError(cursorErr).Error("lookup signing key cursor failed")
			return nil, status.Errorf(codes.Internal, "database error")
		}
		if !cursor.Valid {
			s.logger.Error("signing key cursor returned NULL created_at")
			return nil, status.Errorf(codes.Internal, "database error")
		}
		afterCreatedAt = cursor.Time
	}

	out, err := listSigningKeysFromCatalog(ctx, queries, tenantID, statusFilter, strings.TrimSpace(req.GetAfterId()), afterCreatedAt, int32(limit+1))
	if err != nil {
		s.logger.WithError(err).Error("list signing keys failed")
		return nil, status.Errorf(codes.Internal, "database error")
	}

	resp := &commodorepb.ListSigningKeysResponse{}
	if len(out) > limit {
		resp.NextAfterId = out[limit-1].Id
		out = out[:limit]
	}
	resp.SigningKeys = out
	return resp, nil
}

// RevokeSigningKey marks the key revoked and persists a durable invalidation
// outbox row in the same transaction so the mutation cannot succeed without a
// retry record. After commit, attempts a synchronous fanout; partial failure
// leaves the row pending for the worker to replay.
func (s *CommodoreServer) RevokeSigningKey(ctx context.Context, req *commodorepb.RevokeSigningKeyRequest) (*commodorepb.SigningKey, error) {
	userID, tenantID, err := extractUserContext(ctx)
	if err != nil {
		return nil, err
	}
	id := strings.TrimSpace(req.GetId())
	if id == "" {
		return nil, status.Error(codes.InvalidArgument, "id is required")
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		s.logger.WithError(err).Error("begin revoke signing-key tx failed")
		return nil, status.Errorf(codes.Internal, "database error")
	}
	defer tx.Rollback() //nolint:errcheck // rollback is best-effort after Commit

	revoked, err := commodoredb.New(tx).RevokeSigningKey(ctx, commodoredb.RevokeSigningKeyParams{ID: id, TenantID: tenantID})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, status.Error(codes.NotFound, "signing key not found or already revoked")
	}
	if err != nil {
		s.logger.WithError(err).Error("revoke signing key failed")
		return nil, status.Errorf(codes.Internal, "database error")
	}
	sk, err := signingKeyProto(revoked.ID, revoked.Kid, revoked.Name, revoked.Algorithm, revoked.PublicKeyPem, revoked.Status, revoked.CreatedAt, revoked.LastUsedAt, revoked.RevokedAt)
	if err != nil {
		s.logger.WithError(err).Error("decode revoked signing key failed")
		return nil, status.Errorf(codes.Internal, "database error")
	}

	// Empty internal_names = scope-all; Foghorn fans out across every protected
	// stream the tenant currently owns. Snapshotting the list here would miss
	// streams added between revoke and worker run, so we let Foghorn re-resolve.
	outboxID, enqueueErr := s.enqueueInvalidationOutbox(ctx, tx, tenantID, "key_revoked", nil)
	if enqueueErr != nil {
		s.logger.WithError(enqueueErr).Error("enqueue invalidation outbox failed; aborting revoke")
		return nil, status.Errorf(codes.Internal, "database error")
	}

	if auditErr := s.writeSigningKeyAudit(ctx, tx, tenantID, sk.GetKid(), "revoke", userID, sk.GetName()); auditErr != nil {
		return nil, status.Errorf(codes.Internal, "database error")
	}

	if commitErr := tx.Commit(); commitErr != nil {
		s.logger.WithError(commitErr).Error("commit revoke signing-key tx failed")
		return nil, status.Errorf(codes.Internal, "database error")
	}

	s.tryDispatchInvalidationOutbox(ctx, outboxID, tenantID, "key_revoked", nil)

	return sk, nil
}

func (s *CommodoreServer) RecordSigningKeyUse(ctx context.Context, req *commodorepb.RecordSigningKeyUseRequest) (*emptypb.Empty, error) {
	tenantID := strings.TrimSpace(req.GetTenantId())
	kid := strings.TrimSpace(req.GetKid())
	if tenantID == "" || kid == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id and kid are required")
	}
	if err := commodoredb.New(s.db).RecordSigningKeyUse(ctx, commodoredb.RecordSigningKeyUseParams{TenantID: tenantID, Kid: kid}); err != nil {
		s.logger.WithError(err).WithFields(logging.Fields{
			"tenant_id": tenantID,
			"kid":       kid,
		}).Warn("record signing key use failed")
		return nil, status.Errorf(codes.Internal, "database error")
	}
	return &emptypb.Empty{}, nil
}

// SetPlaybackPolicy persists a per-object playback policy and triggers the
// cache-invalidate + invalidate_sessions fanout. Validates exactly one of
// stream_id / vod_asset_id / clip_id.
func (s *CommodoreServer) SetPlaybackPolicy(ctx context.Context, req *commodorepb.SetPlaybackPolicyRequest) (*commodorepb.SetPlaybackPolicyResponse, error) {
	userID, tenantID, err := extractUserContext(ctx)
	if err != nil {
		return nil, err
	}
	target, err := pickPolicyTarget(req)
	if err != nil {
		return nil, err
	}
	policyType := strings.ToLower(strings.TrimSpace(req.GetType()))
	switch policyType {
	case "public", "jwt", "webhook":
	default:
		return nil, status.Error(codes.InvalidArgument, `type must be "public", "jwt", or "webhook"`)
	}

	// Build the JSONB blob (no plaintext secret in the JSON — it goes in the
	// separate fieldcrypt-encrypted column).
	policyJSON, err := buildPolicyJSON(policyType, req)
	if err != nil {
		return nil, err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		s.logger.WithError(err).Error("begin set-policy tx failed")
		return nil, status.Errorf(codes.Internal, "database error")
	}
	defer tx.Rollback() //nolint:errcheck // rollback is best-effort after Commit

	// Encrypt webhook secret at validation time (and SSRF-validate the URL).
	var webhookSecretEnc sql.NullString
	if policyType == "webhook" {
		wh := req.GetWebhook()
		if wh == nil {
			return nil, status.Error(codes.InvalidArgument, "webhook policy requires webhook block")
		}
		if vErr := validateWebhookURL(ctx, wh.GetUrl()); vErr != nil {
			return nil, status.Errorf(codes.InvalidArgument, "invalid webhook url: %v", vErr)
		}
		secret := strings.TrimSpace(wh.GetSecretPt())
		if secret == "" {
			existing, lookupErr := lookupExistingWebhookSecret(ctx, tx, target, tenantID)
			if lookupErr != nil {
				return nil, lookupErr
			}
			webhookSecretEnc = existing
		} else {
			enc, encErr := s.playbackWebhookEncryptor.Encrypt(secret)
			if encErr != nil {
				s.logger.WithError(encErr).Error("encrypt webhook secret failed")
				return nil, status.Errorf(codes.Internal, "secret encryption failed")
			}
			webhookSecretEnc = sql.NullString{String: enc, Valid: true}
		}
	}

	requiresAuth := policyType != "public"

	queries := commodoredb.New(tx)
	var responseID string
	switch target.kind {
	case "stream":
		responseID, err = queries.SetStreamPlaybackPolicy(ctx, commodoredb.SetStreamPlaybackPolicyParams{
			RequiresAuth: requiresAuth, PlaybackPolicy: string(policyJSON), WebhookSecret: webhookSecretEnc,
			TargetID: target.id, TenantID: tenantID,
		})
	case "vod_asset":
		responseID, err = queries.SetVODPlaybackPolicy(ctx, commodoredb.SetVODPlaybackPolicyParams{
			RequiresAuth: requiresAuth, PlaybackPolicy: string(policyJSON), WebhookSecret: webhookSecretEnc,
			TargetID: target.id, TenantID: tenantID,
		})
	case "clip":
		responseID, err = queries.SetClipPlaybackPolicy(ctx, commodoredb.SetClipPlaybackPolicyParams{
			RequiresAuth: requiresAuth, PlaybackPolicy: string(policyJSON), WebhookSecret: webhookSecretEnc,
			TargetID: target.id, TenantID: tenantID,
		})
	}
	if errors.Is(err, sql.ErrNoRows) {
		if target.kind == "vod_asset" {
			isChapter, chapterErr := queries.IsDVRChapterPlaybackTarget(ctx, commodoredb.IsDVRChapterPlaybackTargetParams{
				TargetID: target.id, TenantID: tenantID,
			})
			if chapterErr != nil {
				s.logger.WithError(chapterErr).Error("classify DVR chapter playback target failed")
				return nil, status.Error(codes.Internal, "database error")
			}
			if isChapter {
				return nil, status.Error(codes.FailedPrecondition, "artifact is a DVR chapter; update playback policy on the recording")
			}
		}
		return nil, status.Errorf(codes.NotFound, "%s not found", target.kind)
	}
	if err != nil {
		s.logger.WithFields(logging.Fields{
			"target": target.kind,
			"error":  err,
		}).Error("update playback policy failed")
		return nil, status.Errorf(codes.Internal, "database error")
	}

	// Snapshot the changed object's internal_name so the worker can replay an
	// invalidation for exactly the affected stream/asset/clip.
	scopedNames := s.scopeInternalNames(ctx, tenantID, protectedScopeForTarget(target))
	outboxID, enqueueErr := s.enqueueInvalidationOutbox(ctx, tx, tenantID, "policy_change", scopedNames)
	if enqueueErr != nil {
		s.logger.WithError(enqueueErr).Error("enqueue invalidation outbox failed; aborting policy change")
		return nil, status.Errorf(codes.Internal, "database error")
	}
	// Playback policy is snapshotted into every signed media-object authority.
	// Queue a tenant fanout in the same transaction as the mutation so local
	// playback cannot retain an older stream/DVR/chapter decision after commit.
	if _, refreshErr := queries.InsertMediaAuthorityRefreshInbox(ctx, commodoredb.InsertMediaAuthorityRefreshInboxParams{
		SourceService: "commodore",
		SourceEventID: "playback-policy:" + outboxID,
		TenantID:      tenantID,
		Reason:        "playback_policy_changed",
	}); refreshErr != nil {
		s.logger.WithError(refreshErr).Error("enqueue media authority refresh failed; aborting policy change")
		return nil, status.Errorf(codes.Internal, "database error")
	}

	if commitErr := tx.Commit(); commitErr != nil {
		s.logger.WithError(commitErr).Error("commit set-policy tx failed")
		return nil, status.Errorf(codes.Internal, "database error")
	}

	s.tryDispatchInvalidationOutbox(ctx, outboxID, tenantID, "policy_change", scopedNames)

	resp := &commodorepb.SetPlaybackPolicyResponse{RequiresAuth: requiresAuth}
	switch target.kind {
	case "stream":
		resp.StreamId = responseID
		s.emitStreamChangeEvent(ctx, eventStreamUpdated, tenantID, userID, responseID, []string{"playback_policy"})
	case "vod_asset":
		resp.VodAssetId = responseID
		s.emitArtifactEvent(ctx, eventPlaybackPolicyChanged, tenantID, userID, ipcpb.ArtifactEvent_ARTIFACT_TYPE_VOD, responseID, "", policyType, nil)
	case "clip":
		resp.ClipId = responseID
		s.emitArtifactEvent(ctx, eventPlaybackPolicyChanged, tenantID, userID, ipcpb.ArtifactEvent_ARTIFACT_TYPE_CLIP, responseID, "", policyType, nil)
	}
	return resp, nil
}

// ResolvePlaybackPolicy returns policy data for public reads and enforcement.
// Webhook secrets are decrypted only when include_webhook_secret is set.
//
// Caller provides exactly one of playback_id or internal_name:
//   - GraphQL field resolvers have playback_id (public identifier).
//   - Foghorn USER_NEW handler has the MistServer internal_name only.
func (s *CommodoreServer) ResolvePlaybackPolicy(ctx context.Context, req *commodorepb.ResolvePlaybackPolicyRequest) (*commodorepb.ResolvePlaybackPolicyResponse, error) {
	playbackID := strings.TrimSpace(req.GetPlaybackId())
	internalName := strings.TrimSpace(req.GetInternalName())
	if (playbackID == "") == (internalName == "") {
		return nil, status.Error(codes.InvalidArgument, "exactly one of playback_id or internal_name is required")
	}

	var (
		policyJSON []byte
		secretEnc  sql.NullString
		tenantID   string
		err        error
	)
	if playbackID != "" {
		policyJSON, secretEnc, tenantID, err = s.lookupPolicyByPlaybackID(ctx, playbackID)
	} else {
		policyJSON, secretEnc, tenantID, err = s.lookupPolicyByInternalName(ctx, internalName)
	}
	if err != nil {
		return nil, err
	}

	resp := &commodorepb.ResolvePlaybackPolicyResponse{TenantId: tenantID}
	if len(policyJSON) == 0 {
		resp.Type = "public"
		return resp, nil
	}

	var parsed policyDoc
	if err := json.Unmarshal(policyJSON, &parsed); err != nil {
		s.logger.WithError(err).WithField("playback_id", playbackID).Error("decode playback policy failed")
		return nil, status.Errorf(codes.Internal, "policy decode error")
	}
	resp.Type = parsed.Type

	switch parsed.Type {
	case "jwt":
		jwtPolicy := &commodorepb.PlaybackJwtPolicy{}
		if parsed.JWT != nil {
			jwtPolicy.AllowedKids = parsed.JWT.AllowedKids
			jwtPolicy.RequiredAudience = parsed.JWT.RequiredAudience
			jwtPolicy.RequiredClaimsJson = parsed.JWT.RequiredClaimsJSON
		}
		keys, err := s.fetchActiveSigningKeys(ctx, tenantID)
		if err != nil {
			s.logger.WithError(err).Error("fetch active signing keys failed")
			return nil, status.Errorf(codes.Internal, "keyset fetch error")
		}
		jwtPolicy.ActiveKeys = keys
		resp.JwtPolicy = jwtPolicy
	case "webhook":
		if parsed.Webhook == nil {
			s.logger.WithField("playback_id", playbackID).Error("webhook policy missing webhook block")
			return nil, status.Errorf(codes.Internal, "policy state inconsistent")
		}
		secret := ""
		if req.GetIncludeWebhookSecret() {
			if !secretEnc.Valid {
				s.logger.WithField("playback_id", playbackID).Error("webhook policy missing encrypted secret")
				return nil, status.Errorf(codes.Internal, "policy state inconsistent")
			}
			decrypted, err := s.playbackWebhookEncryptor.Decrypt(secretEnc.String)
			if err != nil {
				s.logger.WithError(err).Error("decrypt webhook secret failed")
				return nil, status.Errorf(codes.Internal, "secret decrypt error")
			}
			secret = decrypted
		}
		resp.WebhookPolicy = &commodorepb.PlaybackWebhookPolicy{
			Url:       parsed.Webhook.URL,
			TimeoutMs: int32(parsed.Webhook.TimeoutMs),
			SecretPt:  secret,
		}
	case "public":
		// fall through; nothing else to populate
	default:
		s.logger.WithField("type", parsed.Type).WithField("playback_id", playbackID).Error("unknown playback policy type")
		return nil, status.Errorf(codes.Internal, "unknown policy type")
	}

	return resp, nil
}

// ----------------------------------------------------------------------------
// helpers
// ----------------------------------------------------------------------------

type policyTarget struct {
	kind string // "stream" | "vod_asset" | "clip"
	id   string
}

func lookupExistingWebhookSecret(ctx context.Context, tx *sql.Tx, target policyTarget, tenantID string) (sql.NullString, error) {
	queries := commodoredb.New(tx)
	var existing sql.NullString
	var err error
	switch target.kind {
	case "stream":
		existing, err = queries.GetStreamWebhookSecret(ctx, commodoredb.GetStreamWebhookSecretParams{TargetID: target.id, TenantID: tenantID})
	case "vod_asset":
		existing, err = queries.GetVODWebhookSecret(ctx, commodoredb.GetVODWebhookSecretParams{TargetID: target.id, TenantID: tenantID})
	case "clip":
		existing, err = queries.GetClipWebhookSecret(ctx, commodoredb.GetClipWebhookSecretParams{TargetID: target.id, TenantID: tenantID})
	default:
		err = sql.ErrNoRows
	}
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return existing, status.Errorf(codes.NotFound, "%s not found", target.kind)
		}
		return existing, status.Error(codes.Internal, "database error")
	}
	if !existing.Valid || strings.TrimSpace(existing.String) == "" {
		return existing, status.Error(codes.InvalidArgument, "webhook policy requires a non-empty secret")
	}
	return existing, nil
}

func pickPolicyTarget(req *commodorepb.SetPlaybackPolicyRequest) (policyTarget, error) {
	count := 0
	var t policyTarget
	if id := strings.TrimSpace(req.GetStreamId()); id != "" {
		t = policyTarget{kind: "stream", id: id}
		count++
	}
	if id := strings.TrimSpace(req.GetVodAssetId()); id != "" {
		t = policyTarget{kind: "vod_asset", id: id}
		count++
	}
	if id := strings.TrimSpace(req.GetClipId()); id != "" {
		t = policyTarget{kind: "clip", id: id}
		count++
	}
	if count == 0 {
		return t, status.Error(codes.InvalidArgument, "exactly one of stream_id, vod_asset_id, clip_id is required")
	}
	if count > 1 {
		return t, status.Error(codes.InvalidArgument, "only one of stream_id, vod_asset_id, clip_id may be set")
	}
	return t, nil
}

// policyDoc is the on-disk shape of playback_policy JSONB.
//
// Pointer types on JWT/Webhook so encoding/json's `omitempty` actually
// omits them — struct-value omitempty in Go only triggers on the zero
// value of the type, which for nested structs almost never matches.
type policyDoc struct {
	Type    string              `json:"type"`
	JWT     *policyJWTSection   `json:"jwt,omitempty"`
	Webhook *policyWebhookField `json:"webhook,omitempty"`
}

type policyJWTSection struct {
	AllowedKids        []string          `json:"allowed_kids,omitempty"`
	RequiredAudience   []string          `json:"required_audience,omitempty"`
	RequiredClaimsJSON map[string]string `json:"required_claims_json,omitempty"`
}

type policyWebhookField struct {
	URL       string `json:"url"`
	TimeoutMs int    `json:"timeout_ms,omitempty"`
}

func buildPolicyJSON(policyType string, req *commodorepb.SetPlaybackPolicyRequest) ([]byte, error) {
	doc := policyDoc{Type: policyType}
	switch policyType {
	case "public":
		// nothing else
	case "jwt":
		j := req.GetJwt()
		// Always emit a jwt block for jwt policies (even if empty) so
		// downstream readers can rely on its presence as a type marker.
		section := &policyJWTSection{}
		if j != nil {
			section.AllowedKids = j.GetAllowedKids()
			section.RequiredAudience = j.GetRequiredAudience()
			section.RequiredClaimsJSON = j.GetRequiredClaimsJson()
		}
		doc.JWT = section
	case "webhook":
		w := req.GetWebhook()
		if w == nil {
			return nil, status.Error(codes.InvalidArgument, "webhook policy requires webhook block")
		}
		timeout := int(w.GetTimeoutMs())
		if timeout <= 0 {
			timeout = 5000
		}
		if timeout > 10000 {
			timeout = 10000
		}
		doc.Webhook = &policyWebhookField{
			URL:       w.GetUrl(),
			TimeoutMs: timeout,
		}
	}
	return json.Marshal(doc)
}

// validateWebhookURL is the create-time SSRF guard. The dial-time guard
// re-resolves at the actual fetch in Foghorn's webhook client.
func validateWebhookURL(ctx context.Context, raw string) error {
	if raw == "" {
		return errors.New("url required")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return err
	}
	if u.Scheme != "https" {
		return errors.New("scheme must be https")
	}
	if u.User != nil {
		return errors.New("userinfo not allowed; auth via HMAC signature")
	}
	host := u.Hostname()
	if host == "" {
		return errors.New("host required")
	}
	hostLower := strings.ToLower(host)
	if strings.HasSuffix(hostLower, "frameworks.network") || strings.HasSuffix(hostLower, ".internal") {
		return errors.New("host is operator-internal")
	}

	resolver := net.DefaultResolver
	addrs, err := resolver.LookupHost(ctx, host)
	if err != nil {
		return fmt.Errorf("dns lookup failed: %w", err)
	}
	for _, a := range addrs {
		ip, parseErr := netip.ParseAddr(a)
		if parseErr != nil {
			continue
		}
		if isBlockedIP(ip) {
			return fmt.Errorf("host resolves to blocked address %s", ip.String())
		}
	}
	return nil
}

// isBlockedIP rejects loopback, link-local, RFC1918, CGNAT, IANA-reserved,
// and IPv6 ULA / link-local ranges. Used both at create-time validation and
// dial-time re-resolution (DNS rebinding defense).
func isBlockedIP(ip netip.Addr) bool {
	if !ip.IsValid() {
		return true
	}
	if ip.IsLoopback() || ip.IsUnspecified() || ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() || ip.IsMulticast() || ip.IsPrivate() ||
		ip.IsInterfaceLocalMulticast() {
		return true
	}
	// 100.64.0.0/10 (CGNAT)
	if ip.Is4() {
		v4 := ip.As4()
		if v4[0] == 100 && v4[1] >= 64 && v4[1] <= 127 {
			return true
		}
		// 0.0.0.0/8 (already covered by IsUnspecified for /32 only)
		if v4[0] == 0 {
			return true
		}
	}
	// IPv4-mapped IPv6: re-check the underlying v4
	if ip.Is4In6() {
		return isBlockedIP(ip.Unmap())
	}
	return false
}

func signingKeyProto(id, kid, name, alg, pubPEM, st string, createdAt, lastUsedAt, revokedAt sql.NullTime) (*commodorepb.SigningKey, error) {
	if !createdAt.Valid {
		return nil, errors.New("signing key has NULL created_at")
	}
	sk := &commodorepb.SigningKey{
		Id:           id,
		Kid:          kid,
		Name:         name,
		Algorithm:    alg,
		PublicKeyPem: pubPEM,
		Status:       st,
		CreatedAt:    createdAt.Time.UTC().Format(time.RFC3339Nano),
	}
	if lastUsedAt.Valid {
		sk.LastUsedAt = lastUsedAt.Time.UTC().Format(time.RFC3339Nano)
	}
	if revokedAt.Valid {
		sk.RevokedAt = revokedAt.Time.UTC().Format(time.RFC3339Nano)
	}
	return sk, nil
}

func listSigningKeysFromCatalog(
	ctx context.Context,
	queries *commodoredb.Queries,
	tenantID, statusFilter, afterID string,
	afterCreatedAt time.Time,
	rowLimit int32,
) ([]*commodorepb.SigningKey, error) {
	out := make([]*commodorepb.SigningKey, 0, rowLimit)
	appendKey := func(id, kid, name, algorithm, publicKeyPEM, keyStatus string, createdAt, lastUsedAt, revokedAt sql.NullTime) error {
		key, err := signingKeyProto(id, kid, name, algorithm, publicKeyPEM, keyStatus, createdAt, lastUsedAt, revokedAt)
		if err == nil {
			out = append(out, key)
		}
		return err
	}

	switch {
	case statusFilter != "" && afterID != "":
		rows, err := queries.ListSigningKeysByStatusAfter(ctx, commodoredb.ListSigningKeysByStatusAfterParams{
			TenantID: tenantID, Status: statusFilter, AfterCreatedAt: afterCreatedAt, AfterID: afterID, RowLimit: rowLimit,
		})
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			if err := appendKey(row.ID, row.Kid, row.Name, row.Algorithm, row.PublicKeyPem, row.Status, row.CreatedAt, row.LastUsedAt, row.RevokedAt); err != nil {
				return nil, err
			}
		}
	case statusFilter != "":
		rows, err := queries.ListSigningKeysByStatus(ctx, commodoredb.ListSigningKeysByStatusParams{
			TenantID: tenantID, Status: statusFilter, RowLimit: rowLimit,
		})
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			if err := appendKey(row.ID, row.Kid, row.Name, row.Algorithm, row.PublicKeyPem, row.Status, row.CreatedAt, row.LastUsedAt, row.RevokedAt); err != nil {
				return nil, err
			}
		}
	case afterID != "":
		rows, err := queries.ListSigningKeysAfter(ctx, commodoredb.ListSigningKeysAfterParams{
			TenantID: tenantID, AfterCreatedAt: afterCreatedAt, AfterID: afterID, RowLimit: rowLimit,
		})
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			if err := appendKey(row.ID, row.Kid, row.Name, row.Algorithm, row.PublicKeyPem, row.Status, row.CreatedAt, row.LastUsedAt, row.RevokedAt); err != nil {
				return nil, err
			}
		}
	default:
		rows, err := queries.ListSigningKeys(ctx, commodoredb.ListSigningKeysParams{TenantID: tenantID, RowLimit: rowLimit})
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			if err := appendKey(row.ID, row.Kid, row.Name, row.Algorithm, row.PublicKeyPem, row.Status, row.CreatedAt, row.LastUsedAt, row.RevokedAt); err != nil {
				return nil, err
			}
		}
	}
	return out, nil
}

func (s *CommodoreServer) lookupPolicyByPlaybackID(ctx context.Context, playbackID string) ([]byte, sql.NullString, string, error) {
	queries := commodoredb.New(s.db)
	type lookup func() (string, sql.NullString, string, error)
	candidates := []struct {
		label string
		query lookup
	}{
		{"streams", func() (string, sql.NullString, string, error) {
			row, err := queries.LookupStreamPolicyByPlaybackID(ctx, playbackID)
			return row.PlaybackPolicy, row.PlaybackWebhookSecretEnc, row.TenantID, err
		}},
		{"vod_assets", func() (string, sql.NullString, string, error) {
			row, err := queries.LookupVODPolicyByPlaybackID(ctx, playbackID)
			return row.PlaybackPolicy, row.PlaybackWebhookSecretEnc, row.TenantID, err
		}},
		{"clips", func() (string, sql.NullString, string, error) {
			row, err := queries.LookupClipPolicyByPlaybackID(ctx, playbackID)
			return row.PlaybackPolicy, row.PlaybackWebhookSecretEnc, row.TenantID, err
		}},
		{"dvr", func() (string, sql.NullString, string, error) {
			row, err := queries.LookupDVRPolicyByPlaybackID(ctx, playbackID)
			return row.PlaybackPolicy, sql.NullString{String: row.PlaybackWebhookSecretEnc, Valid: row.PlaybackWebhookSecretEnc != ""}, row.TenantID, err
		}},
	}
	for _, candidate := range candidates {
		policy, secret, tenantID, err := candidate.query()
		if err == nil {
			return []byte(policy), secret, tenantID, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			s.logger.WithError(err).WithField("playback_id", playbackID).Error("policy lookup (" + candidate.label + ") failed")
			return nil, sql.NullString{}, "", status.Errorf(codes.Internal, "database error")
		}
	}
	return nil, sql.NullString{}, "", status.Errorf(codes.NotFound, "playback id not found")
}

// lookupPolicyByInternalName mirrors lookupPolicyByPlaybackID for the
// Foghorn USER_NEW path, which has the MistServer internal stream name
// instead of the public playback_id. Searches streams, vod_assets, clips,
// dvr_recordings (using the recording's immutable policy snapshot).
func (s *CommodoreServer) lookupPolicyByInternalName(ctx context.Context, internalName string) ([]byte, sql.NullString, string, error) {
	queries := commodoredb.New(s.db)
	type lookup func() (string, sql.NullString, string, error)
	candidates := []struct {
		label string
		query lookup
	}{
		{"streams", func() (string, sql.NullString, string, error) {
			row, err := queries.LookupStreamPolicyByInternalName(ctx, internalName)
			return row.PlaybackPolicy, row.PlaybackWebhookSecretEnc, row.TenantID, err
		}},
		{"vod_assets", func() (string, sql.NullString, string, error) {
			row, err := queries.LookupVODPolicyByInternalName(ctx, internalName)
			return row.PlaybackPolicy, row.PlaybackWebhookSecretEnc, row.TenantID, err
		}},
		{"clips", func() (string, sql.NullString, string, error) {
			row, err := queries.LookupClipPolicyByInternalName(ctx, internalName)
			return row.PlaybackPolicy, row.PlaybackWebhookSecretEnc, row.TenantID, err
		}},
		{"dvr", func() (string, sql.NullString, string, error) {
			row, err := queries.LookupDVRPolicyByInternalName(ctx, internalName)
			return row.PlaybackPolicy, sql.NullString{String: row.PlaybackWebhookSecretEnc, Valid: row.PlaybackWebhookSecretEnc != ""}, row.TenantID, err
		}},
	}
	for _, candidate := range candidates {
		policy, secret, tenantID, err := candidate.query()
		if err == nil {
			return []byte(policy), secret, tenantID, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			s.logger.WithError(err).WithField("internal_name", internalName).Error("policy lookup by internal_name (" + candidate.label + ") failed")
			return nil, sql.NullString{}, "", status.Errorf(codes.Internal, "database error")
		}
	}
	return nil, sql.NullString{}, "", status.Errorf(codes.NotFound, "internal name not found")
}

func (s *CommodoreServer) fetchActiveSigningKeys(ctx context.Context, tenantID string) ([]*commodorepb.PlaybackSigningKey, error) {
	rows, err := commodoredb.New(s.db).ListActivePlaybackSigningKeys(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	out := make([]*commodorepb.PlaybackSigningKey, 0, len(rows))
	for _, row := range rows {
		out = append(out, &commodorepb.PlaybackSigningKey{
			Kid:          row.Kid,
			Algorithm:    row.Algorithm,
			PublicKeyPem: row.PublicKeyPem,
		})
	}
	return out, nil
}

// protectedScope is the input shape scopeInternalNames understands. The
// outbox stores the resolved name list directly, so this exists only to give
// scopeInternalNames a typed boundary for the two interesting cases:
//   - scope.all (key revoke) → empty list, Foghorn fans out across the
//     tenant's currently-protected streams.
//   - scope.target (single object whose policy changed) → that object's
//     internal_name.
type protectedScope struct {
	all    bool
	target *policyTarget
}

func protectedScopeForTarget(t policyTarget) protectedScope { return protectedScope{target: &t} }

// scopeInternalNames returns MistServer session names to invalidate. Empty
// result with scope.all=true lets Foghorn fan out across tenant live streams
// and artifact sessions from its local registries.
func (s *CommodoreServer) scopeInternalNames(ctx context.Context, tenantID string, scope protectedScope) []string {
	if scope.all || scope.target == nil {
		return nil
	}
	t := scope.target
	queries := commodoredb.New(s.db)
	var name string
	var err error
	prefix := ""
	switch t.kind {
	case "stream":
		name, err = queries.GetStreamPolicyScopeName(ctx, commodoredb.GetStreamPolicyScopeNameParams{TargetID: t.id, TenantID: tenantID})
	case "vod_asset":
		name, err = queries.GetVODPolicyScopeName(ctx, commodoredb.GetVODPolicyScopeNameParams{TargetID: t.id, TenantID: tenantID})
		prefix = "vod+"
	case "clip":
		name, err = queries.GetClipPolicyScopeName(ctx, commodoredb.GetClipPolicyScopeNameParams{TargetID: t.id, TenantID: tenantID})
		prefix = "vod+"
	default:
		return nil
	}
	if err != nil {
		s.logger.WithError(err).WithFields(logging.Fields{
			"tenant_id":   tenantID,
			"target_kind": t.kind,
			"target_id":   t.id,
		}).Warn("scopeInternalNames: lookup failed; falling back to all")
		return nil
	}
	if name == "" {
		return nil
	}
	if prefix != "" && !strings.HasPrefix(name, prefix) {
		name = prefix + name
	}
	return []string{name}
}

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

	"github.com/Livepeer-FrameWorks/monorepo/pkg/auth"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/database"
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
	if _, lockErr := tx.ExecContext(ctx, `
		SELECT pg_advisory_xact_lock(hashtext('commodore_signing_keys'), hashtext($1::text))
	`, tenantID); lockErr != nil {
		s.logger.WithError(lockErr).Error("advisory lock for signing-key create failed")
		return nil, status.Errorf(codes.Internal, "database error")
	}

	var activeCount int
	if cntErr := tx.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM commodore.signing_keys
		WHERE tenant_id = $1 AND status = 'active'
	`, tenantID).Scan(&activeCount); cntErr != nil {
		s.logger.WithError(cntErr).Error("count active signing keys failed")
		return nil, status.Errorf(codes.Internal, "database error")
	}
	if activeCount >= activeSigningKeyCap {
		return nil, status.Errorf(codes.ResourceExhausted, "tenant has reached the active signing-key cap (%d); revoke an existing key first", activeSigningKeyCap)
	}

	var (
		id        string
		createdAt time.Time
	)
	if insErr := tx.QueryRowContext(ctx, `
		INSERT INTO commodore.signing_keys (tenant_id, kid, name, public_key_pem, algorithm, status)
		VALUES ($1, $2, $3, $4, 'ES256', 'active')
		RETURNING id, created_at
	`, tenantID, kid, name, publicPEM).Scan(&id, &createdAt); insErr != nil {
		s.logger.WithError(insErr).Error("insert signing key failed")
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
			Id:           id,
			Kid:          kid,
			Name:         name,
			Algorithm:    "ES256",
			PublicKeyPem: publicPEM,
			Status:       "active",
			CreatedAt:    createdAt.UTC().Format(time.RFC3339Nano),
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
	row := s.db.QueryRowContext(ctx, `
		SELECT id, kid, name, algorithm, public_key_pem, status,
		       created_at, last_used_at, revoked_at
		FROM commodore.signing_keys
		WHERE id = $1 AND tenant_id = $2
	`, id, tenantID)
	sk, err := scanSigningKey(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, status.Error(codes.NotFound, "signing key not found")
	}
	if err != nil {
		s.logger.WithError(err).Error("get signing key failed")
		return nil, status.Errorf(codes.Internal, "database error")
	}
	return sk, nil
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

	args := []any{tenantID}
	where := []string{"tenant_id = $1"}
	q := `
		SELECT id, kid, name, algorithm, public_key_pem, status,
		       created_at, last_used_at, revoked_at
		FROM commodore.signing_keys
	`
	if sf := strings.ToLower(strings.TrimSpace(req.GetStatusFilter())); sf == "active" || sf == "revoked" {
		where = append(where, fmt.Sprintf("status = $%d", len(args)+1))
		args = append(args, sf)
	}
	if afterID := strings.TrimSpace(req.GetAfterId()); afterID != "" {
		var afterCreatedAt time.Time
		if cursorErr := s.db.QueryRowContext(ctx, `
				SELECT created_at FROM commodore.signing_keys
				WHERE id::text = $1 AND tenant_id = $2
			`, afterID, tenantID).Scan(&afterCreatedAt); cursorErr != nil {
			if errors.Is(cursorErr, sql.ErrNoRows) {
				return nil, status.Error(codes.InvalidArgument, "after cursor not found")
			}
			s.logger.WithError(cursorErr).Error("lookup signing key cursor failed")
			return nil, status.Errorf(codes.Internal, "database error")
		}
		where = append(where, fmt.Sprintf("(created_at, id) < ($%d, $%d::uuid)", len(args)+1, len(args)+2))
		args = append(args, afterCreatedAt, afterID)
	}
	q += " WHERE " + strings.Join(where, " AND ")
	q += " ORDER BY created_at DESC, id DESC LIMIT $" + fmt.Sprintf("%d", len(args)+1)
	args = append(args, limit+1)

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		s.logger.WithError(err).Error("list signing keys failed")
		return nil, status.Errorf(codes.Internal, "database error")
	}
	defer rows.Close()

	var out []*commodorepb.SigningKey
	for rows.Next() {
		sk, err := scanSigningKey(rows)
		if err != nil {
			s.logger.WithError(err).Error("scan signing key failed")
			return nil, status.Errorf(codes.Internal, "database error")
		}
		out = append(out, sk)
	}
	if err := rows.Err(); err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
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

	row := tx.QueryRowContext(ctx, `
		UPDATE commodore.signing_keys
		SET status = 'revoked', revoked_at = NOW()
		WHERE id = $1 AND tenant_id = $2 AND status = 'active'
		RETURNING id, kid, name, algorithm, public_key_pem, status,
		          created_at, last_used_at, revoked_at
	`, id, tenantID)
	sk, err := scanSigningKey(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, status.Error(codes.NotFound, "signing key not found or already revoked")
	}
	if err != nil {
		s.logger.WithError(err).Error("revoke signing key failed")
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
	if _, err := s.db.ExecContext(ctx, `
		UPDATE commodore.signing_keys
		SET last_used_at = NOW()
		WHERE tenant_id = $1 AND kid = $2 AND status = 'active'
	`, tenantID, kid); err != nil {
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

	tableCol := target.tableColumn()
	whereCol := targetPolicyUpdateWhere(target)

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
			existing, lookupErr := lookupExistingWebhookSecret(ctx, tx, tableCol, target, tenantID)
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

	q := fmt.Sprintf(`
			UPDATE commodore.%s
			SET requires_auth = $1,
			    playback_policy = $2,
		    playback_webhook_secret_enc = $3,
		    updated_at = NOW()
			WHERE %s AND tenant_id = $5
		`, tableCol, whereCol)

	res, err := tx.ExecContext(ctx, q, requiresAuth, database.JSONText(policyJSON), webhookSecretEnc, target.id, tenantID)
	if err != nil {
		s.logger.WithFields(logging.Fields{
			"target": target.kind,
			"error":  err,
		}).Error("update playback policy failed")
		return nil, status.Errorf(codes.Internal, "database error")
	}
	rowsAffected, raErr := res.RowsAffected()
	if raErr != nil {
		s.logger.WithError(raErr).Error("RowsAffected after policy update failed")
		return nil, status.Errorf(codes.Internal, "database error")
	}
	if rowsAffected == 0 {
		return nil, status.Errorf(codes.NotFound, "%s not found", target.kind)
	}

	// Snapshot the changed object's internal_name so the worker can replay an
	// invalidation for exactly the affected stream/asset/clip.
	scopedNames := s.scopeInternalNames(ctx, tenantID, protectedScopeForTarget(target))
	outboxID, enqueueErr := s.enqueueInvalidationOutbox(ctx, tx, tenantID, "policy_change", scopedNames)
	if enqueueErr != nil {
		s.logger.WithError(enqueueErr).Error("enqueue invalidation outbox failed; aborting policy change")
		return nil, status.Errorf(codes.Internal, "database error")
	}

	if commitErr := tx.Commit(); commitErr != nil {
		s.logger.WithError(commitErr).Error("commit set-policy tx failed")
		return nil, status.Errorf(codes.Internal, "database error")
	}

	s.tryDispatchInvalidationOutbox(ctx, outboxID, tenantID, "policy_change", scopedNames)

	responseID, err := s.canonicalPlaybackPolicyTargetID(ctx, tenantID, target)
	if err != nil {
		return nil, err
	}

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

func (t policyTarget) tableColumn() string {
	switch t.kind {
	case "stream":
		return "streams"
	case "vod_asset":
		return "vod_assets"
	case "clip":
		return "clips"
	}
	return ""
}

func targetPolicyUpdateWhere(t policyTarget) string {
	switch t.kind {
	case "vod_asset":
		return "(id::text = $4 OR vod_hash = $4)"
	case "clip":
		return "(id::text = $4 OR clip_hash = $4)"
	default:
		return "id::text = $4"
	}
}

func targetPolicyLookupWhere(t policyTarget) string {
	switch t.kind {
	case "vod_asset":
		return "(id::text = $1 OR vod_hash = $1)"
	case "clip":
		return "(id::text = $1 OR clip_hash = $1)"
	default:
		return "id::text = $1"
	}
}

func lookupExistingWebhookSecret(ctx context.Context, tx *sql.Tx, tableName string, target policyTarget, tenantID string) (sql.NullString, error) {
	var existing sql.NullString
	q := fmt.Sprintf(`
		SELECT playback_webhook_secret_enc
		FROM commodore.%s
		WHERE %s AND tenant_id = $2
	`, tableName, targetPolicyLookupWhere(target))
	if err := tx.QueryRowContext(ctx, q, target.id, tenantID).Scan(&existing); err != nil {
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

func (s *CommodoreServer) canonicalPlaybackPolicyTargetID(ctx context.Context, tenantID string, target policyTarget) (string, error) {
	switch target.kind {
	case "stream":
		return target.id, nil
	case "vod_asset":
		var vodHash string
		if err := s.db.QueryRowContext(ctx, `
			SELECT vod_hash
			FROM commodore.vod_assets
			WHERE tenant_id = $1 AND (id::text = $2 OR vod_hash = $2)
		`, tenantID, target.id).Scan(&vodHash); err != nil {
			s.logger.WithError(err).WithField("target", target.id).Error("resolve canonical VOD policy target failed")
			if errors.Is(err, sql.ErrNoRows) {
				return "", status.Error(codes.NotFound, "vod_asset not found")
			}
			return "", status.Error(codes.Internal, "database error")
		}
		return vodHash, nil
	case "clip":
		var clipHash string
		if err := s.db.QueryRowContext(ctx, `
			SELECT clip_hash
			FROM commodore.clips
			WHERE tenant_id = $1 AND (id::text = $2 OR clip_hash = $2)
		`, tenantID, target.id).Scan(&clipHash); err != nil {
			s.logger.WithError(err).WithField("target", target.id).Error("resolve canonical clip policy target failed")
			if errors.Is(err, sql.ErrNoRows) {
				return "", status.Error(codes.NotFound, "clip not found")
			}
			return "", status.Error(codes.Internal, "database error")
		}
		return clipHash, nil
	}
	return "", status.Error(codes.InvalidArgument, "unknown playback policy target")
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

// scanSigningKey adapts a row-or-rows to a SigningKey proto.
type rowScanner interface {
	Scan(dest ...any) error
}

func scanSigningKey(r rowScanner) (*commodorepb.SigningKey, error) {
	var (
		id, kid, name, alg, pubPEM, st string
		createdAt                      time.Time
		lastUsedAt, revokedAt          sql.NullTime
	)
	if err := r.Scan(&id, &kid, &name, &alg, &pubPEM, &st, &createdAt, &lastUsedAt, &revokedAt); err != nil {
		return nil, err
	}
	sk := &commodorepb.SigningKey{
		Id:           id,
		Kid:          kid,
		Name:         name,
		Algorithm:    alg,
		PublicKeyPem: pubPEM,
		Status:       st,
		CreatedAt:    createdAt.UTC().Format(time.RFC3339Nano),
	}
	if lastUsedAt.Valid {
		sk.LastUsedAt = lastUsedAt.Time.UTC().Format(time.RFC3339Nano)
	}
	if revokedAt.Valid {
		sk.RevokedAt = revokedAt.Time.UTC().Format(time.RFC3339Nano)
	}
	return sk, nil
}

func (s *CommodoreServer) lookupPolicyByPlaybackID(ctx context.Context, playbackID string) ([]byte, sql.NullString, string, error) {
	// Try streams first.
	var (
		policy   []byte
		secret   sql.NullString
		tenantID string
		fetchErr error
	)
	fetchErr = s.db.QueryRowContext(ctx, `
		SELECT playback_policy, playback_webhook_secret_enc, tenant_id
		FROM commodore.streams WHERE playback_id = $1
	`, playbackID).Scan(&policy, &secret, &tenantID)
	if fetchErr == nil {
		out := append([]byte(nil), policy...)
		return out, secret, tenantID, nil
	}
	if !errors.Is(fetchErr, sql.ErrNoRows) {
		s.logger.WithError(fetchErr).WithField("playback_id", playbackID).Error("policy lookup (streams) failed")
		return nil, secret, "", status.Errorf(codes.Internal, "database error")
	}

	// VOD assets.
	fetchErr = s.db.QueryRowContext(ctx, `
		SELECT playback_policy, playback_webhook_secret_enc, tenant_id
		FROM commodore.vod_assets WHERE playback_id = $1
	`, playbackID).Scan(&policy, &secret, &tenantID)
	if fetchErr == nil {
		out := append([]byte(nil), policy...)
		return out, secret, tenantID, nil
	}
	if !errors.Is(fetchErr, sql.ErrNoRows) {
		s.logger.WithError(fetchErr).WithField("playback_id", playbackID).Error("policy lookup (vod_assets) failed")
		return nil, secret, "", status.Errorf(codes.Internal, "database error")
	}

	// Clips.
	fetchErr = s.db.QueryRowContext(ctx, `
		SELECT playback_policy, playback_webhook_secret_enc, tenant_id
		FROM commodore.clips WHERE playback_id = $1
	`, playbackID).Scan(&policy, &secret, &tenantID)
	if fetchErr == nil {
		out := append([]byte(nil), policy...)
		return out, secret, tenantID, nil
	}
	if !errors.Is(fetchErr, sql.ErrNoRows) {
		s.logger.WithError(fetchErr).WithField("playback_id", playbackID).Error("policy lookup (clips) failed")
		return nil, secret, "", status.Errorf(codes.Internal, "database error")
	}

	// DVR inherits source-stream policy at lookup time.
	fetchErr = s.db.QueryRowContext(ctx, `
		SELECT s.playback_policy, s.playback_webhook_secret_enc, s.tenant_id
		FROM commodore.dvr_recordings d
		JOIN commodore.streams s ON s.id = d.stream_id
		WHERE d.playback_id = $1
	`, playbackID).Scan(&policy, &secret, &tenantID)
	if fetchErr == nil {
		out := append([]byte(nil), policy...)
		return out, secret, tenantID, nil
	}
	if !errors.Is(fetchErr, sql.ErrNoRows) {
		s.logger.WithError(fetchErr).WithField("playback_id", playbackID).Error("policy lookup (dvr) failed")
		return nil, secret, "", status.Errorf(codes.Internal, "database error")
	}

	return nil, secret, "", status.Errorf(codes.NotFound, "playback id not found")
}

// lookupPolicyByInternalName mirrors lookupPolicyByPlaybackID for the
// Foghorn USER_NEW path, which has the MistServer internal stream name
// instead of the public playback_id. Searches streams, vod_assets, clips,
// dvr_recordings (the latter inheriting the source stream's policy).
func (s *CommodoreServer) lookupPolicyByInternalName(ctx context.Context, internalName string) ([]byte, sql.NullString, string, error) {
	var (
		policy   []byte
		secret   sql.NullString
		tenantID string
		fetchErr error
	)
	fetchErr = s.db.QueryRowContext(ctx, `
		SELECT playback_policy, playback_webhook_secret_enc, tenant_id
		FROM commodore.streams WHERE internal_name = $1
	`, internalName).Scan(&policy, &secret, &tenantID)
	if fetchErr == nil {
		out := append([]byte(nil), policy...)
		return out, secret, tenantID, nil
	}
	if !errors.Is(fetchErr, sql.ErrNoRows) {
		s.logger.WithError(fetchErr).WithField("internal_name", internalName).Error("policy lookup by internal_name (streams) failed")
		return nil, secret, "", status.Errorf(codes.Internal, "database error")
	}

	fetchErr = s.db.QueryRowContext(ctx, `
		SELECT playback_policy, playback_webhook_secret_enc, tenant_id
		FROM commodore.vod_assets WHERE internal_name = $1
	`, internalName).Scan(&policy, &secret, &tenantID)
	if fetchErr == nil {
		out := append([]byte(nil), policy...)
		return out, secret, tenantID, nil
	}
	if !errors.Is(fetchErr, sql.ErrNoRows) {
		s.logger.WithError(fetchErr).WithField("internal_name", internalName).Error("policy lookup by internal_name (vod_assets) failed")
		return nil, secret, "", status.Errorf(codes.Internal, "database error")
	}

	fetchErr = s.db.QueryRowContext(ctx, `
		SELECT playback_policy, playback_webhook_secret_enc, tenant_id
		FROM commodore.clips WHERE internal_name = $1
	`, internalName).Scan(&policy, &secret, &tenantID)
	if fetchErr == nil {
		out := append([]byte(nil), policy...)
		return out, secret, tenantID, nil
	}
	if !errors.Is(fetchErr, sql.ErrNoRows) {
		s.logger.WithError(fetchErr).WithField("internal_name", internalName).Error("policy lookup by internal_name (clips) failed")
		return nil, secret, "", status.Errorf(codes.Internal, "database error")
	}

	fetchErr = s.db.QueryRowContext(ctx, `
		SELECT s.playback_policy, s.playback_webhook_secret_enc, s.tenant_id
		FROM commodore.dvr_recordings d
		JOIN commodore.streams s ON s.id = d.stream_id
		WHERE d.internal_name = $1
	`, internalName).Scan(&policy, &secret, &tenantID)
	if fetchErr == nil {
		out := append([]byte(nil), policy...)
		return out, secret, tenantID, nil
	}
	if !errors.Is(fetchErr, sql.ErrNoRows) {
		s.logger.WithError(fetchErr).WithField("internal_name", internalName).Error("policy lookup by internal_name (dvr) failed")
		return nil, secret, "", status.Errorf(codes.Internal, "database error")
	}

	return nil, secret, "", status.Errorf(codes.NotFound, "internal name not found")
}

func (s *CommodoreServer) fetchActiveSigningKeys(ctx context.Context, tenantID string) ([]*commodorepb.PlaybackSigningKey, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT kid, algorithm, public_key_pem
		FROM commodore.signing_keys
		WHERE tenant_id = $1 AND status = 'active'
	`, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*commodorepb.PlaybackSigningKey
	for rows.Next() {
		var kid, alg, pem string
		if err := rows.Scan(&kid, &alg, &pem); err != nil {
			return nil, err
		}
		out = append(out, &commodorepb.PlaybackSigningKey{
			Kid:          kid,
			Algorithm:    alg,
			PublicKeyPem: pem,
		})
	}
	return out, rows.Err()
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
	var query string
	prefix := ""
	switch t.kind {
	case "stream":
		query = `SELECT internal_name FROM commodore.streams WHERE id::text = $1 AND tenant_id = $2`
	case "vod_asset":
		query = `SELECT internal_name FROM commodore.vod_assets WHERE (id::text = $1 OR vod_hash = $1) AND tenant_id = $2`
		prefix = "vod+"
	case "clip":
		query = `SELECT internal_name FROM commodore.clips WHERE (id::text = $1 OR clip_hash = $1) AND tenant_id = $2`
		prefix = "vod+"
	default:
		return nil
	}
	var name string
	if err := s.db.QueryRowContext(ctx, query, t.id, tenantID).Scan(&name); err != nil {
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

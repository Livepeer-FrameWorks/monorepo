package bootstrap

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/lib/pq"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/pullsource"
)

// SourceURICipher encrypts a plaintext upstream pull URI for storage in
// commodore.stream_pull_sources.source_uri_enc. The cobra dispatcher wires this
// to the same FieldEncryptor runtime CRUD uses; tests can inject identity.
type SourceURICipher interface {
	Encrypt(plaintext string) (string, error)
	Decrypt(stored string) (string, error)
}

// ClusterCapabilityResolver is the narrow Quartermaster surface ReconcilePullStreams
// needs to enforce cluster eligibility. The cobra dispatcher wires this to a
// real Quartermaster gRPC client; tests inject a static set.
type ClusterCapabilityResolver interface {
	// MediaClusterCapabilities returns the cluster_id + allow_private_pull_sources
	// for every media-capable cluster the platform currently has registered.
	// Pull streams declared through bootstrap are operator-owned, so every
	// registered media cluster is a candidate for eligibility filtering.
	MediaClusterCapabilities(ctx context.Context) ([]pullsource.ClusterCapability, error)
}

// ReconcilePullStreams provisions operator-owned pull-input streams declared in
// the rendered bootstrap file into commodore.streams + commodore.stream_pull_sources.
// Stable key: (tenant_id, playback_id). Idempotent semantics:
//
//   - Stream absent ⇒ create both rows (commodore.streams + stream_pull_sources).
//   - Stream present, all fields match ⇒ noop.
//   - Stream present, mutable fields differ ⇒ update.
//
// SourceURICipher decrypts existing values for idempotent comparison and
// encrypts plaintext SourceURI before INSERT/UPDATE.
//
// ClusterCapabilityResolver is the eligibility gate: private/multicast sources
// require explicit allowed_cluster_ids, and each listed cluster must be an edge
// cluster with allow_private_pull_sources=true. Defense-in-depth — the CLI
// render path enforces the same rule earlier, but stale rendered files /
// out-of-band callers must still fail closed here.
func ReconcilePullStreams(
	ctx context.Context,
	exec DBTX,
	streams []PullStream,
	resolver TenantResolver,
	clusters ClusterCapabilityResolver,
	cipher SourceURICipher,
) (Result, error) {
	if exec == nil {
		return Result{}, errors.New("ReconcilePullStreams: nil executor")
	}
	if resolver == nil {
		return Result{}, errors.New("ReconcilePullStreams: nil tenant resolver")
	}
	if clusters == nil {
		return Result{}, errors.New("ReconcilePullStreams: nil cluster resolver")
	}
	if cipher == nil {
		return Result{}, errors.New("ReconcilePullStreams: nil cipher")
	}

	candidates, err := clusters.MediaClusterCapabilities(ctx)
	if err != nil {
		return Result{}, fmt.Errorf("list media clusters: %w", err)
	}

	res := Result{}
	for _, ps := range streams {
		class, shapeErr := validatePullStreamShape(ps)
		if shapeErr != nil {
			return Result{}, shapeErr
		}
		// Normalise in place so downstream INSERT/UPDATE and idempotent
		// compare use the same canonical form (sorted, deduped).
		ps.AllowedClusterIDs = normalizeAllowedClusterIDs(ps.AllowedClusterIDs)
		if placementErr := validatePullStreamPlacement(ps, class, candidates); placementErr != nil {
			return Result{}, placementErr
		}
		alias, err := AliasFromRef(ps.OwnerTenant.Ref)
		if err != nil {
			return Result{}, fmt.Errorf("pull_stream %q: %w", ps.PlaybackID, err)
		}
		tenantID, err := resolver.Resolve(ctx, alias)
		if err != nil {
			return Result{}, fmt.Errorf("pull_stream %q: %w", ps.PlaybackID, err)
		}

		action, err := reconcilePullStream(ctx, exec, tenantID, alias, ps, cipher)
		if err != nil {
			return Result{}, fmt.Errorf("pull_stream %q: %w", ps.PlaybackID, err)
		}
		switch action {
		case "created":
			res.Created = append(res.Created, ps.PlaybackID)
		case "updated":
			res.Updated = append(res.Updated, ps.PlaybackID)
		case "noop":
			res.Noop = append(res.Noop, ps.PlaybackID)
		}
	}
	return res, nil
}

// validatePullStreamShape exercises the offline checks: required fields,
// URI parseability, scheme + always-blocked host set. Returns the URI class
// so the apply path can layer cluster eligibility on top.
func validatePullStreamShape(p PullStream) (pullsource.Class, error) {
	if p.PlaybackID == "" {
		return pullsource.ClassBlocked, errors.New("playback_id required")
	}
	if p.OwnerTenant.Ref == "" {
		return pullsource.ClassBlocked, fmt.Errorf("pull_stream %q: owner_tenant.ref required", p.PlaybackID)
	}
	if p.Title == "" {
		return pullsource.ClassBlocked, fmt.Errorf("pull_stream %q: title required", p.PlaybackID)
	}
	if p.SourceURI == "" {
		return pullsource.ClassBlocked, fmt.Errorf("pull_stream %q: source_uri required (resolver should have resolved any source_uri_ref)", p.PlaybackID)
	}
	class, classErr := pullsource.Classify(p.SourceURI)
	if class == pullsource.ClassBlocked {
		return pullsource.ClassBlocked, fmt.Errorf("pull_stream %q: source_uri: %w", p.PlaybackID, classErr)
	}
	return class, nil
}

// validatePullStreamPlacement layers placement validation on top of shape
// validation. Combines the per-source allowed_cluster_ids list with the
// capability gate via the shared pullsource.FilterPlacementClusters helper.
// Apply path only — `--check` skips this since it's offline and has no
// Quartermaster access.
func validatePullStreamPlacement(p PullStream, class pullsource.Class, candidates []pullsource.ClusterCapability) error {
	if len(candidates) == 0 {
		return fmt.Errorf("pull_stream %q: no media (edge) cluster is registered", p.PlaybackID)
	}
	eligible, rejects := pullsource.FilterPlacementClusters(class, p.AllowedClusterIDs, candidates)
	if err := formatPlacementRejects(p.PlaybackID, pullsource.Redact(p.SourceURI), rejects); err != nil {
		return err
	}
	if len(eligible) == 0 {
		return fmt.Errorf("pull_stream %q: no eligible media cluster", p.PlaybackID)
	}
	return nil
}

// formatPlacementRejects turns pullsource.PlacementReject entries into a
// single apply-time error so operators see every offending ID + reason in
// one CLI pass.
func formatPlacementRejects(playbackID, redactedURI string, rejects []pullsource.PlacementReject) error {
	if len(rejects) == 0 {
		return nil
	}
	parts := make([]string, 0, len(rejects))
	for _, r := range rejects {
		switch r.Reason {
		case pullsource.PlacementRejectEmptyForPrivate:
			parts = append(parts, fmt.Sprintf(
				"source_uri %s is private/multicast and requires explicit allowed_cluster_ids", redactedURI))
		case pullsource.PlacementRejectUnknownCluster:
			parts = append(parts, fmt.Sprintf(
				"allowed_cluster_ids entry %q is not a registered media (edge) cluster", r.ClusterID))
		case pullsource.PlacementRejectMissingPrivateCapability:
			parts = append(parts, fmt.Sprintf(
				"allowed_cluster_ids entry %q does not have allow_private_pull_sources=true", r.ClusterID))
		default:
			parts = append(parts, fmt.Sprintf("allowed_cluster_ids entry %q rejected: %s", r.ClusterID, r.Reason))
		}
	}
	return fmt.Errorf("pull_stream %q: %s", playbackID, strings.Join(parts, "; "))
}

// normalizeAllowedClusterIDs dedups, trims, and sorts so the persisted
// TEXT[] column and idempotent compare use the same canonical form.
func normalizeAllowedClusterIDs(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, id := range in {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	slices.Sort(out)
	return out
}

func reconcilePullStream(ctx context.Context, exec DBTX, tenantID, alias string, p PullStream, cipher SourceURICipher) (string, error) {
	const probeSQL = `
			SELECT s.id::text, s.title, COALESCE(s.description, ''), s.ingest_mode,
			       p.source_uri_enc, p.enabled, COALESCE(p.allowed_cluster_ids, '{}')
			FROM commodore.streams s
			LEFT JOIN commodore.stream_pull_sources p ON p.stream_id = s.id
			WHERE s.tenant_id = $1::uuid AND lower(s.playback_id::text) = lower($2)`

	var (
		streamID                   string
		curTitle, curDesc, curMode string
		curURIEnc                  sql.NullString
		curEnabled                 sql.NullBool
		curAllowedClusters         pq.StringArray
	)
	err := exec.QueryRowContext(ctx, probeSQL, tenantID, p.PlaybackID).Scan(
		&streamID, &curTitle, &curDesc, &curMode, &curURIEnc, &curEnabled, &curAllowedClusters,
	)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return createPullStream(ctx, exec, tenantID, alias, p, cipher)
	case err != nil:
		return "", fmt.Errorf("probe stream: %w", err)
	}

	if curMode != "pull" {
		return "", fmt.Errorf("stream %q already exists with ingest_mode=%q; refusing to convert", p.PlaybackID, curMode)
	}

	curURI := ""
	if curURIEnc.Valid {
		var err error
		curURI, err = cipher.Decrypt(curURIEnc.String)
		if err != nil {
			return "", fmt.Errorf("decrypt current source_uri: %w", err)
		}
	}

	streamFieldsEq := curTitle == p.Title && curDesc == p.Description
	curAllowed := []string(curAllowedClusters)
	pullFieldsEq := curURIEnc.Valid && curURI == p.SourceURI &&
		curEnabled.Valid && curEnabled.Bool == p.Enabled &&
		slices.Equal(curAllowed, p.AllowedClusterIDs)

	if streamFieldsEq && pullFieldsEq {
		return "noop", nil
	}

	if !streamFieldsEq {
		const updateStreamSQL = `
			UPDATE commodore.streams
			SET title = $2, description = $3, updated_at = NOW()
			WHERE id = $1::uuid`
		if _, err := exec.ExecContext(ctx, updateStreamSQL, streamID, p.Title, p.Description); err != nil {
			return "", fmt.Errorf("update stream: %w", err)
		}
	}
	if !pullFieldsEq {
		encURI, err := cipher.Encrypt(p.SourceURI)
		if err != nil {
			return "", fmt.Errorf("encrypt source_uri: %w", err)
		}
		const upsertPullSQL = `
				INSERT INTO commodore.stream_pull_sources
					(stream_id, source_uri_enc, enabled, allowed_cluster_ids, created_at, updated_at)
				VALUES ($1::uuid, $2, $3, $4, NOW(), NOW())
				ON CONFLICT (stream_id) DO UPDATE
					SET source_uri_enc      = EXCLUDED.source_uri_enc,
					    enabled             = EXCLUDED.enabled,
					    allowed_cluster_ids = EXCLUDED.allowed_cluster_ids,
					    updated_at          = NOW()`
		if _, err := exec.ExecContext(ctx, upsertPullSQL, streamID, encURI, p.Enabled, pq.Array(p.AllowedClusterIDs)); err != nil {
			return "", fmt.Errorf("upsert stream_pull_sources: %w", err)
		}
	}
	return "updated", nil
}

func createPullStream(ctx context.Context, exec DBTX, tenantID, alias string, p PullStream, cipher SourceURICipher) (string, error) {
	// streams.user_id is NOT NULL with no FK to users. A pull stream needs an
	// owner user in its tenant; resolve it before the INSERT so the missing-
	// owner case fails with a tenant-named precondition error.
	const ownerSQL = `
		SELECT id::text FROM commodore.users
		WHERE tenant_id = $1::uuid AND role = 'owner'
		ORDER BY created_at LIMIT 1`
	var ownerID string
	switch err := exec.QueryRowContext(ctx, ownerSQL, tenantID).Scan(&ownerID); {
	case errors.Is(err, sql.ErrNoRows):
		return "", fmt.Errorf("tenant %s has no owner user — provision owners before pull streams", alias)
	case err != nil:
		return "", fmt.Errorf("lookup owner user: %w", err)
	}

	encURI, err := cipher.Encrypt(p.SourceURI)
	if err != nil {
		return "", fmt.Errorf("encrypt source_uri: %w", err)
	}

	// commodore.streams: stream_key + internal_name are auto-generated by the
	// SQL function for normal create flows; bootstrap inserts them inline. The
	// stream_key is unused for pull streams (no encoder pushes), but the column
	// is NOT NULL so we generate a stable placeholder derived from the playback_id.
	const insertStreamSQL = `
		INSERT INTO commodore.streams
			(id, tenant_id, user_id, stream_key, playback_id, internal_name,
			 title, description, ingest_mode, created_at, updated_at)
		VALUES (gen_random_uuid(), $1::uuid, $5::uuid,
		        'pull-' || $2, $2,
		        replace(gen_random_uuid()::text, '-', ''),
		        $3, $4, 'pull', NOW(), NOW())
		RETURNING id::text`
	var streamID string
	if err := exec.QueryRowContext(ctx, insertStreamSQL,
		tenantID, p.PlaybackID, p.Title, p.Description, ownerID,
	).Scan(&streamID); err != nil {
		return "", fmt.Errorf("insert stream: %w", err)
	}

	const insertPullSQL = `
		INSERT INTO commodore.stream_pull_sources
			(stream_id, source_uri_enc, enabled, allowed_cluster_ids, created_at, updated_at)
		VALUES ($1::uuid, $2, $3, $4, NOW(), NOW())`
	if _, err := exec.ExecContext(ctx, insertPullSQL, streamID, encURI, p.Enabled, pq.Array(p.AllowedClusterIDs)); err != nil {
		return "", fmt.Errorf("insert stream_pull_sources: %w", err)
	}
	return "created", nil
}

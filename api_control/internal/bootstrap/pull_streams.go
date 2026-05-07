package bootstrap

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

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
	// Pull streams in bootstrap are operator-owned (system_tenant), so all
	// media clusters are candidates; tenant-scoped access lands when tenant
	// pull streams ship via the API path.
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
// ClusterCapabilityResolver is the eligibility gate: a private URI is rejected
// at apply time when no media cluster has allow_private_pull_sources=true.
// Defense-in-depth — the CLI render path enforces the same rule earlier, but
// stale rendered files / out-of-band callers must still fail closed here.
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
		if eligibilityErr := validatePullStreamEligibility(ps, class, candidates); eligibilityErr != nil {
			return Result{}, eligibilityErr
		}
		alias, err := AliasFromRef(ps.OwnerTenant.Ref)
		if err != nil {
			return Result{}, fmt.Errorf("pull_stream %q: %w", ps.PlaybackID, err)
		}
		tenantID, err := resolver.Resolve(ctx, alias)
		if err != nil {
			return Result{}, fmt.Errorf("pull_stream %q: %w", ps.PlaybackID, err)
		}

		action, err := reconcilePullStream(ctx, exec, tenantID, ps, cipher)
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

// validatePullStreamEligibility layers cluster eligibility on top of shape
// validation. Apply path only — `--check` skips this since it's offline and
// has no Quartermaster access.
func validatePullStreamEligibility(p PullStream, class pullsource.Class, candidates []pullsource.ClusterCapability) error {
	eligible := pullsource.EligiblePullClusters(class, candidates)
	if len(eligible) == 0 {
		switch class {
		case pullsource.ClassPrivate:
			return fmt.Errorf("pull_stream %q: source_uri is private but no registered cluster has allow_private_pull_sources=true", p.PlaybackID)
		default:
			return fmt.Errorf("pull_stream %q: source_uri %s but no media cluster is registered", p.PlaybackID, class)
		}
	}
	return nil
}

func reconcilePullStream(ctx context.Context, exec DBTX, tenantID string, p PullStream, cipher SourceURICipher) (string, error) {
	const probeSQL = `
			SELECT s.id::text, s.title, COALESCE(s.description, ''), s.ingest_mode,
			       p.source_uri_enc, p.enabled
			FROM commodore.streams s
			LEFT JOIN commodore.stream_pull_sources p ON p.stream_id = s.id
			WHERE s.tenant_id = $1::uuid AND lower(s.playback_id::text) = lower($2)`

	var (
		streamID                   string
		curTitle, curDesc, curMode string
		curURIEnc                  sql.NullString
		curEnabled                 sql.NullBool
	)
	err := exec.QueryRowContext(ctx, probeSQL, tenantID, p.PlaybackID).Scan(
		&streamID, &curTitle, &curDesc, &curMode, &curURIEnc, &curEnabled,
	)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return createPullStream(ctx, exec, tenantID, p, cipher)
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
	pullFieldsEq := curURIEnc.Valid && curURI == p.SourceURI &&
		curEnabled.Valid && curEnabled.Bool == p.Enabled

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
					(stream_id, source_uri_enc, enabled, created_at, updated_at)
				VALUES ($1::uuid, $2, $3, NOW(), NOW())
				ON CONFLICT (stream_id) DO UPDATE
					SET source_uri_enc = EXCLUDED.source_uri_enc,
					    enabled        = EXCLUDED.enabled,
					    updated_at     = NOW()`
		if _, err := exec.ExecContext(ctx, upsertPullSQL, streamID, encURI, p.Enabled); err != nil {
			return "", fmt.Errorf("upsert stream_pull_sources: %w", err)
		}
	}
	return "updated", nil
}

func createPullStream(ctx context.Context, exec DBTX, tenantID string, p PullStream, cipher SourceURICipher) (string, error) {
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
		VALUES (gen_random_uuid(), $1::uuid,
		        (SELECT id FROM commodore.users
		         WHERE tenant_id = $1::uuid AND role = 'owner' ORDER BY created_at LIMIT 1),
		        'pull-' || $2, $2,
		        replace(gen_random_uuid()::text, '-', ''),
		        $3, $4, 'pull', NOW(), NOW())
		RETURNING id::text`
	var streamID string
	if err := exec.QueryRowContext(ctx, insertStreamSQL,
		tenantID, p.PlaybackID, p.Title, p.Description,
	).Scan(&streamID); err != nil {
		return "", fmt.Errorf("insert stream: %w", err)
	}

	const insertPullSQL = `
		INSERT INTO commodore.stream_pull_sources
			(stream_id, source_uri_enc, enabled, created_at, updated_at)
		VALUES ($1::uuid, $2, $3, NOW(), NOW())`
	if _, err := exec.ExecContext(ctx, insertPullSQL, streamID, encURI, p.Enabled); err != nil {
		return "", fmt.Errorf("insert stream_pull_sources: %w", err)
	}
	return "created", nil
}

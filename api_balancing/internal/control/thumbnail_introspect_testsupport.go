//go:build schema_verify

package control

import (
	"context"
	"database/sql"
	"errors"
	"strings"
)

// Pointer-state INTROSPECTION for the thumbnail publication real-Postgres tests. Chandler serves a DETERMINISTIC key
// and never resolves an active version, so nothing in production calls this — it exists ONLY to let the schema_verify
// suite assert the active-pointer / tombstone state the publish CAS maintains. Guarded by the `schema_verify` build tag
// so it is compiled for those tests (in this package AND the jobs package, which builds control with the same tag) but
// is NEVER part of a production binary.

// ThumbnailResolveState is the pointer-state classification for an asset_key: exactly one of these.
type ThumbnailResolveState int

const (
	// ThumbnailLegacyAllowed: no version published and the parent is live/never-versioned (no active pointer, no
	// tombstone) — the un-versioned baseline state.
	ThumbnailLegacyAllowed ThumbnailResolveState = iota
	// ThumbnailActive: a version is published and the parent is not terminal.
	ThumbnailActive
	// ThumbnailGone: the parent artifact is TERMINAL (deleted/failed/expired/aborted) or a live-stream cleanup
	// tombstone exists — the asset is gone. Kept distinct from LEGACY so a deleted asset is never reported as merely
	// un-versioned.
	ThumbnailGone
)

// IntrospectThumbnailPointerState classifies an asset's active-pointer + tombstone DB state (globally-unique stream_id
// UUID / opaque clip-dvr-vod hash) as ACTIVE (a version pointer, non-terminal parent), LEGACY (no version yet), or GONE
// (terminal parent / cleanup tombstone). It is NOT a serving path — Chandler serves the deterministic key with no such
// lookup; this exists only so the schema_verify tests can assert the pointer/tombstone state the publish CAS maintains.
// A nil DB / empty asset_key errors (absence of authority, not proof of absence).
func IntrospectThumbnailPointerState(ctx context.Context, dbh *sql.DB, assetKey string) (version string, state ThumbnailResolveState, err error) {
	if dbh == nil {
		return "", ThumbnailLegacyAllowed, errors.New("thumbnail resolve: no database authority")
	}
	if strings.TrimSpace(assetKey) == "" {
		return "", ThumbnailLegacyAllowed, errors.New("thumbnail resolve: empty asset_key")
	}
	var active sql.NullString
	var gone bool
	// Single read keyed by asset_key: the active token (if any) + whether the asset is GONE — either the parent artifact
	// is terminal, OR (for a live stream with no artifact row) a durable cleanup tombstone exists. The tombstone LEFT
	// JOIN is what makes a deleted live stream report GONE instead of its stale pointer. active_token is the per-token
	// candidate segment (`v/{token}/…`), ALWAYS set on a published pointer; active_version is the attempt-id anchor only.
	qErr := dbh.QueryRowContext(ctx, `
		SELECT p.active_token,
		       COALESCE(a.status IN `+artifactTerminalStatusSQL+`, false) OR t.asset_key IS NOT NULL AS gone
		  FROM (SELECT $1::text AS k) k
		  LEFT JOIN foghorn.thumbnail_active_pointer p ON p.asset_key = k.k
		  LEFT JOIN foghorn.artifacts a ON a.artifact_hash = k.k
		  LEFT JOIN foghorn.stream_cleanup_obligation t ON t.asset_key = k.k
	`, assetKey).Scan(&active, &gone)
	if qErr != nil {
		return "", ThumbnailLegacyAllowed, qErr
	}
	if gone {
		return "", ThumbnailGone, nil
	}
	if active.Valid && active.String != "" {
		return active.String, ThumbnailActive, nil
	}
	return "", ThumbnailLegacyAllowed, nil
}

// ResolveActiveThumbnailVersion is the simple form where only the active version matters: ok=true with a version for an
// ACTIVE asset; ok=false for both LEGACY and GONE.
func ResolveActiveThumbnailVersion(ctx context.Context, dbh *sql.DB, assetKey string) (version string, ok bool, err error) {
	v, state, e := IntrospectThumbnailPointerState(ctx, dbh, assetKey)
	if e != nil {
		return "", false, e
	}
	return v, state == ThumbnailActive, nil
}

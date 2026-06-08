package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/mist"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/streamident"
	"github.com/google/uuid"
)

// 5-minute canonical ledger rebuilders. Each rebuilder reads its source
// table with argMax-dedup, buckets into 5-minute windows by source-time
// overlap, and appends one projection row per (natural-key, window).
// Re-running a window produces an identical row (deterministic from source
// data); the projection table's argMax-on-read view collapses re-emissions.
// See docs/architecture/meter-contracts.md for the projection model.

const (
	// LedgerRebuildInterval is how often each ledger worker wakes up to
	// recompute the trailing window. Five minutes matches the ledger
	// granularity so steady-state work is bounded.
	LedgerRebuildInterval = 5 * time.Minute

	// LedgerSettlementLag is the trailing extra window the rebuilder
	// replays each pass to absorb late-arriving facts (raw_mist_triggers
	// produced minutes after the wall-clock time the event represents,
	// storage_snapshots delayed by collector batching, etc.). Two
	// minutes mirrors the billing-cursor settlement lag for symmetry.
	LedgerSettlementLag = 2 * time.Minute

	// LedgerInitialLookback is the bounded bootstrap range used when a
	// cursor row does not exist yet. Rebuilders are forward-only;
	// explicit historical backfills use admin tooling rather than
	// first-boot scans.
	LedgerInitialLookback = 2 * LedgerRebuildInterval
)

// LedgerScheduler runs the five ledger rebuilders on independent goroutines.
// Each runs at LedgerRebuildInterval with its own ticker.
type LedgerScheduler struct {
	h      *AnalyticsHandler
	logger logging.Logger
}

func NewLedgerScheduler(h *AnalyticsHandler) *LedgerScheduler {
	return &LedgerScheduler{h: h, logger: h.logger}
}

// Start launches the five rebuild goroutines. They run until ctx is
// cancelled. Errors per pass are logged but do not terminate the
// goroutine; the next tick retries.
func (s *LedgerScheduler) Start(ctx context.Context) {
	rebuilders := []struct {
		name string
		run  func(context.Context, time.Time, time.Time) error
	}{
		{"viewer_usage_5m", s.h.rebuildViewerUsage5m},
		{"stream_runtime_5m", s.h.rebuildStreamRuntime5m},
		{"storage_gb_seconds_5m", s.h.rebuildStorageGBSeconds5m},
		{"processing_5m", s.h.rebuildProcessing5m},
		{"api_usage_5m", s.h.rebuildApiUsage5m},
	}

	for _, r := range rebuilders {
		go s.runLoop(ctx, r.name, r.run)
	}

	// Stale-close workers run alongside the rebuilders. See stale_close.go.
	s.startStaleCloseLoops(ctx)
}

func (s *LedgerScheduler) runLoop(ctx context.Context, name string, run func(context.Context, time.Time, time.Time) error) {
	ticker := time.NewTicker(LedgerRebuildInterval)
	defer ticker.Stop()

	doPass := func() {
		now := time.Now().UTC()
		windowEnd := now.Add(-LedgerSettlementLag).Truncate(5 * time.Minute)
		windowStart, err := s.h.getLedgerRebuildCursor(ctx, name, windowEnd.Add(-LedgerInitialLookback))
		if err != nil {
			s.logger.WithError(err).WithField("ledger", name).Warn("Ledger cursor read failed")
			return
		}
		if !windowStart.Before(windowEnd) {
			return
		}
		if err := run(ctx, windowStart, windowEnd); err != nil {
			s.logger.WithError(err).WithFields(logging.Fields{
				"ledger":       name,
				"window_start": windowStart,
				"window_end":   windowEnd,
			}).Warn("Ledger rebuild pass failed")
			return
		}
		if err := s.h.recordLedgerRebuildCursor(ctx, name, windowEnd); err != nil {
			s.logger.WithError(err).WithFields(logging.Fields{
				"ledger": name,
				"cursor": windowEnd,
			}).Warn("Ledger cursor write failed after successful rebuild")
		}
	}

	doPass() // initial pass on boot
	for {
		select {
		case <-ctx.Done():
			s.logger.WithField("ledger", name).Info("Ledger rebuilder stopping")
			return
		case <-ticker.C:
			doPass()
		}
	}
}

func (h *AnalyticsHandler) getLedgerRebuildCursor(ctx context.Context, ledgerName string, defaultStart time.Time) (time.Time, error) {
	rows, err := h.clickhouse.Query(ctx, `
		SELECT argMax(last_processed_projection_ms, updated_at_ms)
		FROM periscope.ledger_rebuild_cursors
		WHERE ledger_name = ?
		GROUP BY ledger_name`,
		ledgerName)
	if err != nil {
		return time.Time{}, err
	}
	defer func() { _ = rows.Close() }()
	if !rows.Next() {
		return defaultStart.UTC(), nil
	}
	var lastProcessedMS int64
	if err := rows.Scan(&lastProcessedMS); err != nil {
		return time.Time{}, err
	}
	if lastProcessedMS <= 0 {
		return defaultStart.UTC(), nil
	}
	return time.UnixMilli(lastProcessedMS).UTC(), nil
}

func (h *AnalyticsHandler) recordLedgerRebuildCursor(ctx context.Context, ledgerName string, processedThrough time.Time) error {
	batch, err := h.clickhouse.PrepareBatch(ctx, `
		INSERT INTO periscope.ledger_rebuild_cursors (
			ledger_name, last_processed_projection_ms, updated_at_ms
		)`)
	if err != nil {
		return fmt.Errorf("ledger_rebuild_cursors prepare: %w", err)
	}
	defer closeClickHouseBatch(batch)
	if err := batch.Append(ledgerName, processedThrough.UnixMilli(), time.Now().UnixMilli()); err != nil {
		return fmt.Errorf("ledger_rebuild_cursors append: %w", err)
	}
	if err := batch.Send(); err != nil {
		return fmt.Errorf("ledger_rebuild_cursors send: %w", err)
	}
	return nil
}

// --- viewer_usage_5m ---

// rebuildViewerUsage5m projects finalized viewer sessions across the
// 5-minute windows they overlap. Reads sessions whose projection_version_ms
// landed in the replay window — that bounds the rebuilder's work to
// "sessions we just learned about." A session finalized 5 minutes ago
// that spanned 2 hours produces 24 ledger rows in a single pass.
func (h *AnalyticsHandler) rebuildViewerUsage5m(ctx context.Context, windowStart, windowEnd time.Time) error {
	rows, err := h.clickhouse.Query(ctx, `
		WITH sessions AS (
			SELECT
				tenant_id, node_id, session_id,
				argMax(source_event_id,      projection_version_ms) AS source_event_id,
				argMax(cluster_id,           projection_version_ms) AS cluster_id,
				argMax(stream_id,            projection_version_ms) AS stream_id,
				argMax(source_started_at_ms, projection_version_ms) AS source_started_at_ms,
				argMax(source_ended_at_ms,   projection_version_ms) AS source_ended_at_ms,
				argMax(duration_seconds,    projection_version_ms) AS duration_seconds,
				argMax(uploaded_bytes,      projection_version_ms) AS uploaded_bytes,
				argMax(downloaded_bytes,    projection_version_ms) AS downloaded_bytes,
				argMax(closed_reason,       projection_version_ms) AS closed_reason
			FROM periscope.viewer_sessions_final
			WHERE projection_version_ms >= ? AND projection_version_ms < ?
			GROUP BY tenant_id, node_id, session_id
		)
		SELECT
			tenant_id, node_id, session_id,
			source_event_id, cluster_id, stream_id,
			source_started_at_ms, source_ended_at_ms,
			duration_seconds, uploaded_bytes, downloaded_bytes
		FROM sessions
		WHERE closed_reason = 'final'
		  AND source_started_at_ms > 0
		  AND source_ended_at_ms > source_started_at_ms`,
		windowStart.UnixMilli(), windowEnd.UnixMilli())
	if err != nil {
		return fmt.Errorf("viewer_usage_5m source query: %w", err)
	}
	defer func() { _ = rows.Close() }()

	type viewerSessionProjection struct {
		tenantID, nodeID, sessionID        string
		sourceEventID, clusterID, streamID string
		startMS, endMS                     int64
		durationSeconds                    uint32
		upBytes, downBytes                 uint64
	}
	var sessions []viewerSessionProjection
	for rows.Next() {
		var s viewerSessionProjection
		if scanErr := rows.Scan(&s.tenantID, &s.nodeID, &s.sessionID, &s.sourceEventID, &s.clusterID, &s.streamID, &s.startMS, &s.endMS, &s.durationSeconds, &s.upBytes, &s.downBytes); scanErr != nil {
			return fmt.Errorf("viewer_usage_5m scan: %w", scanErr)
		}
		sessions = append(sessions, s)
	}
	if iterErr := rows.Err(); iterErr != nil {
		return fmt.Errorf("viewer_usage_5m iterate: %w", iterErr)
	}
	if len(sessions) == 0 {
		return nil
	}

	projectionVersionMS := time.Now().UnixMilli()
	type viewerUsageEmission struct {
		windowStartMS                    int64
		tenantID, clusterID, streamID    string
		nodeID, sessionID, sourceEventID string
		secondsObserved                  uint32
		upObserved, downObserved         uint64
	}
	var emissions []viewerUsageEmission
	for _, s := range sessions {
		totalSpanMS := s.endMS - s.startMS
		if totalSpanMS <= 0 {
			continue
		}
		desiredKeys := map[viewerUsageWindowKey]struct{}{}
		for windowMS, overlapMS := range windowsForSpan(s.startMS, s.endMS) {
			if overlapMS <= 0 {
				continue
			}
			desiredKeys[viewerUsageWindowKey{
				windowStartMS: windowMS,
				clusterID:     s.clusterID,
				streamID:      s.streamID,
			}] = struct{}{}
			fraction := float64(overlapMS) / float64(totalSpanMS)
			upObserved := uint64(float64(s.upBytes) * fraction)
			downObserved := uint64(float64(s.downBytes) * fraction)
			emissions = append(emissions, viewerUsageEmission{
				windowStartMS:   windowMS,
				tenantID:        s.tenantID,
				clusterID:       s.clusterID,
				streamID:        s.streamID,
				nodeID:          s.nodeID,
				sessionID:       s.sessionID,
				sourceEventID:   s.sourceEventID,
				secondsObserved: uint32(overlapMS / 1000),
				upObserved:      upObserved,
				downObserved:    downObserved,
			})
		}
		tombstones, tombstoneErr := h.viewerUsageTombstones(ctx, s.tenantID, s.nodeID, s.sessionID, desiredKeys)
		if tombstoneErr != nil {
			return tombstoneErr
		}
		for _, t := range tombstones {
			emissions = append(emissions, viewerUsageEmission{
				windowStartMS: t.windowStartMS,
				tenantID:      s.tenantID,
				clusterID:     t.clusterID,
				streamID:      t.streamID,
				nodeID:        s.nodeID,
				sessionID:     s.sessionID,
				sourceEventID: t.sourceEventID,
			})
		}
	}
	if len(emissions) == 0 {
		return nil
	}

	batch, err := h.clickhouse.PrepareBatch(ctx, `
		INSERT INTO periscope.viewer_usage_5m (
			window_start, tenant_id, cluster_id, stream_id, node_id, session_id,
			seconds_observed, up_bytes_observed, down_bytes_observed,
			source_event_id, projection_version_ms
		)`)
	if err != nil {
		return fmt.Errorf("viewer_usage_5m prepare: %w", err)
	}
	defer closeClickHouseBatch(batch)

	for _, e := range emissions {
		if err := batch.Append(
			time.UnixMilli(e.windowStartMS).UTC(),
			e.tenantID, e.clusterID, e.streamID, e.nodeID, e.sessionID,
			e.secondsObserved, e.upObserved, e.downObserved,
			e.sourceEventID, projectionVersionMS,
		); err != nil {
			return fmt.Errorf("viewer_usage_5m append: %w", err)
		}
	}
	if err := batch.Send(); err != nil {
		return fmt.Errorf("viewer_usage_5m send: %w", err)
	}
	if h.metrics != nil && h.metrics.ClickHouseInserts != nil {
		h.metrics.ClickHouseInserts.WithLabelValues("viewer_usage_5m", "inserted").Add(float64(len(emissions)))
	}
	return nil
}

type viewerUsageWindowKey struct {
	windowStartMS int64
	clusterID     string
	streamID      string
}

type viewerUsageTombstone struct {
	viewerUsageWindowKey
	sourceEventID string
}

func (h *AnalyticsHandler) viewerUsageTombstones(ctx context.Context, tenantID, nodeID, sessionID string, desired map[viewerUsageWindowKey]struct{}) ([]viewerUsageTombstone, error) {
	rows, err := h.clickhouse.Query(ctx, `
		SELECT
			toInt64(toUnixTimestamp(window_start)) * 1000 AS window_start_ms,
			cluster_id,
			toString(stream_id) AS stream_id,
			argMax(source_event_id, projection_version_ms) AS source_event_id,
			argMax(seconds_observed, projection_version_ms) AS seconds_observed,
			argMax(up_bytes_observed, projection_version_ms) AS up_bytes_observed,
			argMax(down_bytes_observed, projection_version_ms) AS down_bytes_observed
		FROM periscope.viewer_usage_5m
		WHERE tenant_id = ?
		  AND node_id = ?
		  AND session_id = ?
		GROUP BY window_start, cluster_id, stream_id`,
		tenantID, nodeID, sessionID)
	if err != nil {
		return nil, fmt.Errorf("viewer_usage_5m tombstone lookup: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []viewerUsageTombstone
	for rows.Next() {
		var (
			key                      viewerUsageWindowKey
			sourceEventID            string
			secondsObserved          uint32
			upObserved, downObserved uint64
		)
		if err := rows.Scan(&key.windowStartMS, &key.clusterID, &key.streamID, &sourceEventID, &secondsObserved, &upObserved, &downObserved); err != nil {
			return nil, fmt.Errorf("viewer_usage_5m tombstone scan: %w", err)
		}
		if secondsObserved == 0 && upObserved == 0 && downObserved == 0 {
			continue
		}
		if _, ok := desired[key]; ok {
			continue
		}
		out = append(out, viewerUsageTombstone{
			viewerUsageWindowKey: key,
			sourceEventID:        sourceEventID,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("viewer_usage_5m tombstone iterate: %w", err)
	}
	return out, nil
}

// --- stream_runtime_5m ---

func (h *AnalyticsHandler) rebuildStreamRuntime5m(ctx context.Context, windowStart, windowEnd time.Time) error {
	projectionVersionMS := time.Now().UnixMilli()
	batch, err := h.clickhouse.PrepareBatch(ctx, `
		INSERT INTO periscope.stream_runtime_5m (
			window_start, tenant_id, cluster_id, stream_id,
			active_seconds, peak_viewers,
			source_event_id, projection_version_ms
		)`)
	if err != nil {
		return fmt.Errorf("stream_runtime_5m prepare: %w", err)
	}
	defer closeClickHouseBatch(batch)

	rowsEmitted := 0
	rows, err := h.clickhouse.Query(ctx, `
		WITH s AS (
			SELECT
				tenant_id, node_id, stream_id, source_event_id,
				argMax(cluster_id,           projection_version_ms) AS cluster_id,
				argMax(stream_name,          projection_version_ms) AS stream_name,
				argMax(source_started_at_ms, projection_version_ms) AS source_started_at_ms,
				argMax(source_ended_at_ms,   projection_version_ms) AS source_ended_at_ms,
				argMax(total_viewers,        projection_version_ms) AS peak_viewers,
				argMax(closed_reason,        projection_version_ms) AS closed_reason
			FROM periscope.stream_sessions_final
			WHERE projection_version_ms >= ? AND projection_version_ms < ?
			GROUP BY tenant_id, node_id, stream_id, source_event_id
		)
		SELECT
			tenant_id, node_id, cluster_id, stream_id, stream_name, source_event_id,
			source_started_at_ms, source_ended_at_ms, peak_viewers
		FROM s
		WHERE closed_reason = 'final'
		  AND source_ended_at_ms > 0`,
		windowStart.UnixMilli(), windowEnd.UnixMilli())
	if err != nil {
		return fmt.Errorf("stream_runtime_5m source query: %w", err)
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var (
			tenantID, nodeID, clusterID, streamID, streamName, sourceEventID string
			startMS, endMS                                                   int64
			peakViewers                                                      int64
		)
		if scanErr := rows.Scan(&tenantID, &nodeID, &clusterID, &streamID, &streamName, &sourceEventID, &startMS, &endMS, &peakViewers); scanErr != nil {
			return fmt.Errorf("stream_runtime_5m scan: %w", scanErr)
		}
		if startMS <= 0 || endMS <= startMS {
			resolvedStartMS, lookupErr := h.lookupStreamRuntimeStartMS(ctx, tenantID, streamID, nodeID, clusterID, streamName, endMS)
			if lookupErr != nil {
				return lookupErr
			}
			if resolvedStartMS > 0 && resolvedStartMS < endMS {
				startMS = resolvedStartMS
			}
		}
		if startMS <= 0 || endMS <= startMS {
			if !isArtifactRuntimeStream(streamName) {
				h.logger.WithFields(logging.Fields{
					"tenant_id":       tenantID,
					"stream_id":       streamID,
					"node_id":         nodeID,
					"cluster_id":      clusterID,
					"stream_name":     streamName,
					"source_event_id": sourceEventID,
					"ended_at_ms":     endMS,
				}).Warn("Skipping stream runtime projection: missing resolved stream start")
			}
			continue
		}
		sourceEventID = streamRuntimeSessionKey(tenantID, nodeID, streamID, startMS)
		emitted, appendErr := appendStreamRuntimeSpan(batch, tenantID, clusterID, streamID, sourceEventID, startMS, endMS, peakViewers, projectionVersionMS)
		if appendErr != nil {
			return appendErr
		}
		rowsEmitted += emitted
	}
	if rowsErr := rows.Err(); rowsErr != nil {
		return fmt.Errorf("stream_runtime_5m iterate: %w", rowsErr)
	}

	liveRows, err := h.clickhouse.Query(ctx, `
		SELECT
			toString(s.tenant_id) AS tenant_id,
			s.node_id AS node_id,
			ifNull(nullIf(e.cluster_id, ''), '') AS cluster_id,
			toString(s.stream_id) AS stream_id,
			s.internal_name AS stream_name,
			ifNull(s.started_at, s.updated_at) AS started_at,
			toInt64(s.current_viewers) AS peak_viewers
		FROM periscope.stream_state_current AS s FINAL
		LEFT JOIN (
			SELECT
				tenant_id,
				stream_id,
				node_id,
				internal_name,
				argMaxIf(cluster_id, timestamp, cluster_id != '') AS cluster_id
			FROM periscope.stream_event_log
			WHERE status = 'live'
			  AND event_type IN ('stream_start', 'stream_lifecycle', 'stream_buffer', 'track_list_update')
			GROUP BY tenant_id, stream_id, node_id, internal_name
		) AS e
			ON e.tenant_id = s.tenant_id
		   AND e.stream_id = s.stream_id
		   AND e.node_id = s.node_id
		   AND e.internal_name = s.internal_name
		WHERE s.status = 'live'
		  AND ifNull(s.started_at, s.updated_at) < ?
		  AND now() >= ?`,
		windowEnd, windowStart)
	if err != nil {
		return fmt.Errorf("stream_runtime_5m live source query: %w", err)
	}
	defer func() { _ = liveRows.Close() }()

	liveEndMS := time.Now().UTC().UnixMilli()
	if windowEndMS := windowEnd.UnixMilli(); liveEndMS > windowEndMS {
		liveEndMS = windowEndMS
	}
	for liveRows.Next() {
		var (
			tenantID, nodeID, clusterID, streamID, streamName string
			startedAt                                         time.Time
			peakViewers                                       int64
		)
		if err := liveRows.Scan(&tenantID, &nodeID, &clusterID, &streamID, &streamName, &startedAt, &peakViewers); err != nil {
			return fmt.Errorf("stream_runtime_5m live scan: %w", err)
		}
		if clusterID == "" || isArtifactRuntimeStream(streamName) {
			continue
		}
		sessionStartMS := startedAt.UTC().UnixMilli()
		startMS := sessionStartMS
		if windowStartMS := windowStart.UnixMilli(); startMS < windowStartMS {
			startMS = windowStartMS
		}
		if startMS <= 0 || liveEndMS <= startMS {
			continue
		}
		sourceEventID := streamRuntimeSessionKey(tenantID, nodeID, streamID, sessionStartMS)
		emitted, err := appendStreamRuntimeSpan(batch, tenantID, clusterID, streamID, sourceEventID, startMS, liveEndMS, peakViewers, projectionVersionMS)
		if err != nil {
			return err
		}
		rowsEmitted += emitted
	}
	if err := liveRows.Err(); err != nil {
		return fmt.Errorf("stream_runtime_5m live iterate: %w", err)
	}

	if rowsEmitted == 0 {
		return nil
	}
	if err := batch.Send(); err != nil {
		return fmt.Errorf("stream_runtime_5m send: %w", err)
	}
	if h.metrics != nil && h.metrics.ClickHouseInserts != nil {
		h.metrics.ClickHouseInserts.WithLabelValues("stream_runtime_5m", "inserted").Add(float64(rowsEmitted))
	}
	return nil
}

func streamRuntimeSessionKey(tenantID, nodeID, streamID string, sourceStartedAtMS int64) string {
	return fmt.Sprintf("stream-runtime:%s:%s:%s:%d", tenantID, nodeID, streamID, sourceStartedAtMS)
}

func appendStreamRuntimeSpan(batch clickhouseBatch, tenantID, clusterID, streamID, sourceEventID string, startMS, endMS, peakViewers, projectionVersionMS int64) (int, error) {
	if startMS <= 0 || endMS <= startMS || clusterID == "" || streamID == "" {
		return 0, nil
	}
	pv := uint32(peakViewers)
	if peakViewers < 0 {
		pv = 0
	}
	rowsEmitted := 0
	for windowMS, overlapMS := range windowsForSpan(startMS, endMS) {
		if overlapMS <= 0 {
			continue
		}
		activeSeconds := uint32(overlapMS / 1000)
		if err := batch.Append(
			time.UnixMilli(windowMS).UTC(),
			tenantID, clusterID, streamID,
			activeSeconds, pv,
			sourceEventID, projectionVersionMS,
		); err != nil {
			return rowsEmitted, fmt.Errorf("stream_runtime_5m append: %w", err)
		}
		rowsEmitted++
	}
	return rowsEmitted, nil
}

func isArtifactRuntimeStream(streamName string) bool {
	for _, candidate := range []string{strings.TrimSpace(streamName), strings.TrimSpace(mist.ExtractInternalName(streamName))} {
		if candidate == "" {
			continue
		}
		if streamident.Parse(candidate).IsArtifact() {
			return true
		}
	}
	return false
}

func (h *AnalyticsHandler) lookupStreamRuntimeStartMS(ctx context.Context, tenantID, streamID, nodeID, clusterID, streamName string, fallbackEndedAtMS int64) (int64, error) {
	parsedTenantID, err := uuid.Parse(tenantID)
	if err != nil {
		return 0, fmt.Errorf("parse stream runtime tenant_id %q: %w", tenantID, err)
	}
	parsedStreamID, err := uuid.Parse(streamID)
	if err != nil {
		return 0, fmt.Errorf("parse stream runtime stream_id %q: %w", streamID, err)
	}
	return h.lookupStreamStartedAtMS(ctx, streamStartLookup{
		tenantID:     parsedTenantID,
		streamID:     parsedStreamID,
		nodeID:       nodeID,
		clusterID:    clusterID,
		internalName: mist.ExtractInternalName(streamName),
	}, fallbackEndedAtMS), nil
}

// --- storage_gb_seconds_5m ---

// rebuildStorageGBSeconds5m integrates total_bytes over time per
// (tenant, cluster, scope, provider attribution), bucketing GB-seconds
// into 5-minute windows under a hold-constant-between-snapshots assumption.
//
// Cursor is on `ingested_at_ms` (NOT the source `timestamp`): a snapshot
// recorded at 10:00 but ingested at 10:15 lands in the cursor pass
// covering ingest time ~10:15, not the cursor pass that walked
// timestamp 10:00. Without this, a delayed snapshot would be silently
// skipped after the cursor advanced past its source window.
//
// Affected-bucket recompute: for each snapshot ingested in this cursor
// window, we recompute both the source bucket and the immediately
// preceding bucket. The preceding bucket can only be fully closed once a
// later snapshot arrives. Each affected bucket gets its own seed
// snapshot at-or-before bucket_start so the leading edge integrates
// cleanly. Billing walks this ledger by first projection time, so a
// bucket's first projection is normal usage; only subsequent projection
// changes produce correction rows.
func (h *AnalyticsHandler) rebuildStorageGBSeconds5m(ctx context.Context, windowStart, windowEnd time.Time) error {
	cursorStartMS := windowStart.UnixMilli()
	cursorEndMS := windowEnd.UnixMilli()
	rows, err := h.clickhouse.Query(ctx, `
		WITH newly_ingested AS (
			SELECT
				tenant_id, cluster_id, storage_scope,
				storage_provider_tenant_id, storage_provider_cluster_id, storage_backend,
				toStartOfFiveMinute(timestamp) AS source_bucket
			FROM periscope.storage_snapshots
			WHERE ingested_at_ms >= ? AND ingested_at_ms < ?
		),
		affected_buckets AS (
			SELECT DISTINCT
				tenant_id, cluster_id, storage_scope,
				storage_provider_tenant_id, storage_provider_cluster_id, storage_backend,
				bucket_start
			FROM (
				SELECT
					tenant_id, cluster_id, storage_scope,
					storage_provider_tenant_id, storage_provider_cluster_id, storage_backend,
					source_bucket AS bucket_start
				FROM newly_ingested

				UNION ALL

				SELECT
					tenant_id, cluster_id, storage_scope,
					storage_provider_tenant_id, storage_provider_cluster_id, storage_backend,
					source_bucket - INTERVAL 5 MINUTE AS bucket_start
				FROM newly_ingested
			)
		)
		SELECT tenant_id, cluster_id, storage_scope,
		       storage_provider_tenant_id, storage_provider_cluster_id, storage_backend,
		       ts_ms, ingested_at_ms, total_bytes, file_count
		FROM (
			-- All snapshots whose timestamp falls in (or just after) any
			-- bucket touched by recently-ingested rows. The +5 minute
			-- extension covers the closing edge of each bucket.
			SELECT
				s.tenant_id,
				s.cluster_id,
				s.storage_scope,
				s.storage_provider_tenant_id,
				s.storage_provider_cluster_id,
				s.storage_backend,
				toInt64(toUnixTimestamp(s.timestamp)) * 1000 AS ts_ms,
				s.ingested_at_ms,
				s.total_bytes,
				s.file_count
			FROM periscope.storage_snapshots s
			WHERE (s.tenant_id, s.cluster_id, s.storage_scope,
			       s.storage_provider_tenant_id, s.storage_provider_cluster_id, s.storage_backend,
			       toStartOfFiveMinute(s.timestamp)) IN (SELECT * FROM affected_buckets)
			   OR (s.tenant_id, s.cluster_id, s.storage_scope,
			       s.storage_provider_tenant_id, s.storage_provider_cluster_id, s.storage_backend,
			       toStartOfFiveMinute(s.timestamp - INTERVAL 5 MINUTE)) IN (SELECT * FROM affected_buckets)
			UNION ALL
			-- Seed: latest snapshot at-or-before each affected bucket's
			-- start, per (key, bucket), so every affected bucket's leading
			-- edge has its own anchor for hold-constant integration. A
			-- key-only LIMIT 1 BY would pick only the seed nearest the
			-- latest affected bucket and starve earlier buckets.
			SELECT tenant_id, cluster_id, storage_scope,
			       storage_provider_tenant_id, storage_provider_cluster_id, storage_backend,
			       ts_ms, ingested_at_ms, total_bytes, file_count
			FROM (
				SELECT
					s.tenant_id,
					s.cluster_id,
					s.storage_scope,
					s.storage_provider_tenant_id,
					s.storage_provider_cluster_id,
					s.storage_backend,
					a.bucket_start                         AS bucket_start,
					toInt64(toUnixTimestamp(s.timestamp)) * 1000 AS ts_ms,
					s.ingested_at_ms,
					s.total_bytes,
					s.file_count
				FROM periscope.storage_snapshots s
				INNER JOIN affected_buckets a USING (
					tenant_id, cluster_id, storage_scope,
					storage_provider_tenant_id, storage_provider_cluster_id, storage_backend
				)
				WHERE s.timestamp < a.bucket_start
				ORDER BY tenant_id, cluster_id, storage_scope, storage_provider_tenant_id, storage_provider_cluster_id, storage_backend, bucket_start, timestamp DESC
				LIMIT 1 BY tenant_id, cluster_id, storage_scope, storage_provider_tenant_id, storage_provider_cluster_id, storage_backend, bucket_start
			)
		)
		ORDER BY tenant_id, cluster_id, storage_scope, storage_provider_tenant_id, storage_provider_cluster_id, storage_backend, ts_ms, ingested_at_ms`,
		cursorStartMS, cursorEndMS)
	if err != nil {
		return fmt.Errorf("storage_snapshots query: %w", err)
	}
	defer func() { _ = rows.Close() }()

	type key struct {
		tenant, cluster, scope                   string
		providerTenant, providerCluster, backend string
	}
	type accum struct {
		// per-window accumulated GB-seconds
		gbSecondsByWindow map[int64]float64
		fileCountByWindow map[int64]uint64
		closedByWindow    map[int64]bool
		prevTsMS          int64
		prevBytes         uint64
		prevFileCount     uint64
	}
	state := map[key]*accum{}
	const fiveMinuteMS = int64(5 * 60 * 1000)

	for rows.Next() {
		var (
			tenantID, clusterID, scope                          string
			providerTenantID, providerClusterID, storageBackend string
			tsMS                                                int64
			ingestedAtMS                                        int64
			totalBytes                                          uint64
			fileCount                                           uint32
		)
		scanErr := rows.Scan(&tenantID, &clusterID, &scope, &providerTenantID, &providerClusterID, &storageBackend, &tsMS, &ingestedAtMS, &totalBytes, &fileCount)
		if scanErr != nil {
			return fmt.Errorf("storage_gb_seconds_5m scan: %w", scanErr)
		}
		k := key{tenantID, clusterID, scope, providerTenantID, providerClusterID, storageBackend}
		a, ok := state[k]
		if !ok {
			a = &accum{gbSecondsByWindow: map[int64]float64{}, fileCountByWindow: map[int64]uint64{}, closedByWindow: map[int64]bool{}}
			state[k] = a
		}
		if a.prevTsMS > 0 && tsMS > a.prevTsMS {
			// Integrate prevBytes over [prevTsMS, tsMS) into the 5-min
			// windows that interval spans. No clipping to the cursor
			// window: the affected-bucket query above already bounds the
			// snapshot set to buckets we want recomputed, and clipping
			// to the cursor's INGEST window would chop integration at
			// the wrong edges (a late snapshot recorded an hour ago is
			// integrating over source-time minutes, not ingest-time
			// minutes).
			gibibytes := float64(a.prevBytes) / (1024 * 1024 * 1024)
			for w, ms := range windowsForSpan(a.prevTsMS, tsMS) {
				if ms <= 0 {
					continue
				}
				a.gbSecondsByWindow[w] += gibibytes * float64(ms) / 1000.0
				if cur := a.fileCountByWindow[w]; cur < a.prevFileCount {
					a.fileCountByWindow[w] = a.prevFileCount
				}
				if tsMS >= w+fiveMinuteMS {
					a.closedByWindow[w] = true
				}
			}
		}
		a.prevTsMS = tsMS
		a.prevBytes = totalBytes
		a.prevFileCount = uint64(fileCount)
	}
	if iterErr := rows.Err(); iterErr != nil {
		return fmt.Errorf("storage_gb_seconds_5m iterate: %w", iterErr)
	}

	// No trailing integration: without a source-time snapshot at-or-after
	// the bucket end, we cannot prove the last value held for the rest of
	// that 5-minute bucket. Such buckets are withheld below and emitted by
	// the later pass that observes the closing anchor, so Purser never
	// bills a partial storage bucket that a later projection has to fix.

	if len(state) == 0 {
		return nil
	}

	projectionVersionMS := time.Now().UnixMilli()
	batch, err := h.clickhouse.PrepareBatch(ctx, `
		INSERT INTO periscope.storage_gb_seconds_5m (
			window_start, tenant_id, cluster_id, storage_scope,
			storage_provider_tenant_id, storage_provider_cluster_id, storage_backend,
			gb_seconds, file_count, projection_version_ms
		)`)
	if err != nil {
		return fmt.Errorf("storage_gb_seconds_5m prepare: %w", err)
	}
	defer closeClickHouseBatch(batch)

	rowsEmitted := 0
	for k, a := range state {
		for w, gbs := range a.gbSecondsByWindow {
			if !a.closedByWindow[w] {
				continue
			}
			if err := h.storageProjectionDiverged(ctx, projectionVersionMS, time.UnixMilli(w).UTC(),
				k.tenant, k.cluster, k.scope, k.providerTenant, k.providerCluster, k.backend,
				gbs, a.fileCountByWindow[w]); err != nil {
				return err
			}
			if err := batch.Append(
				time.UnixMilli(w).UTC(),
				k.tenant, k.cluster, k.scope,
				k.providerTenant, k.providerCluster, k.backend,
				gbs, a.fileCountByWindow[w], projectionVersionMS,
			); err != nil {
				return fmt.Errorf("storage_gb_seconds_5m append: %w", err)
			}
			rowsEmitted++
		}
	}
	if rowsEmitted == 0 {
		return nil
	}
	if err := batch.Send(); err != nil {
		return fmt.Errorf("storage_gb_seconds_5m send: %w", err)
	}
	if h.metrics != nil && h.metrics.ClickHouseInserts != nil {
		h.metrics.ClickHouseInserts.WithLabelValues("storage_gb_seconds_5m", "inserted").Add(float64(rowsEmitted))
	}
	return nil
}

func (h *AnalyticsHandler) storageProjectionDiverged(
	ctx context.Context,
	observedAtMS int64,
	windowStart time.Time,
	tenantID, clusterID, scope, providerTenantID, providerClusterID, backend string,
	gbSeconds float64,
	fileCount uint64,
) error {
	naturalKey, err := json.Marshal(map[string]any{
		"tenant_id":                   tenantID,
		"cluster_id":                  clusterID,
		"storage_scope":               scope,
		"storage_provider_tenant_id":  providerTenantID,
		"storage_provider_cluster_id": providerClusterID,
		"storage_backend":             backend,
		"window_start":                windowStart.Format(time.RFC3339),
	})
	if err != nil {
		return fmt.Errorf("storage_gb_seconds_5m divergence key: %w", err)
	}

	record := func(priorGBSeconds float64, priorFileCount uint64) error {
		priorValue, marshalErr := json.Marshal(map[string]any{"gb_seconds": priorGBSeconds, "file_count": priorFileCount})
		if marshalErr != nil {
			return fmt.Errorf("storage_gb_seconds_5m divergence prior: %w", marshalErr)
		}
		newValue, marshalErr := json.Marshal(map[string]any{"gb_seconds": gbSeconds, "file_count": fileCount})
		if marshalErr != nil {
			return fmt.Errorf("storage_gb_seconds_5m divergence new: %w", marshalErr)
		}
		sourceEventID := fmt.Sprintf("storage_gb_seconds_5m:%s", string(naturalKey))
		if recordErr := h.recordProjectionDivergence(ctx, observedAtMS, "storage_gb_seconds_5m", "storage_gb_seconds_"+scope, "projection", string(naturalKey), string(priorValue), string(newValue), sourceEventID); recordErr != nil {
			return fmt.Errorf("record storage_gb_seconds_5m divergence: %w", recordErr)
		}
		return nil
	}

	rows, err := h.clickhouse.Query(ctx, `
		SELECT
			argMax(gb_seconds, projection_version_ms) AS gb_seconds,
			argMax(file_count, projection_version_ms) AS file_count
		FROM periscope.storage_gb_seconds_5m
		WHERE tenant_id = ?
		  AND cluster_id = ?
		  AND storage_scope = ?
		  AND storage_provider_tenant_id = ?
		  AND storage_provider_cluster_id = ?
		  AND storage_backend = ?
		  AND window_start = ?
		GROUP BY tenant_id, cluster_id, storage_scope,
		         storage_provider_tenant_id, storage_provider_cluster_id, storage_backend,
		         window_start`,
		tenantID, clusterID, scope, providerTenantID, providerClusterID, backend, windowStart)
	if err != nil {
		return fmt.Errorf("storage_gb_seconds_5m divergence lookup: %w", err)
	}
	defer func() { _ = rows.Close() }()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return fmt.Errorf("storage_gb_seconds_5m divergence lookup iterate: %w", err)
		}
		return nil
	}

	var priorGBSeconds float64
	var priorFileCount uint64
	if err := rows.Scan(&priorGBSeconds, &priorFileCount); err != nil {
		return fmt.Errorf("storage_gb_seconds_5m divergence scan: %w", err)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("storage_gb_seconds_5m divergence iterate: %w", err)
	}
	if math.Abs(priorGBSeconds-gbSeconds) <= 1e-9 && priorFileCount == fileCount {
		return nil
	}
	return record(priorGBSeconds, priorFileCount)
}

// --- processing_5m ---

func (h *AnalyticsHandler) rebuildProcessing5m(ctx context.Context, windowStart, windowEnd time.Time) error {
	// Aggregate processing_segments_final by source_event_id for segments
	// whose projection_version_ms landed in the replay window. Process,
	// codec, track, cluster, and media_seconds are materialized with
	// argMax so a replay that fixes attribution replaces the prior shape
	// instead of adding a second logical segment.
	rows, err := h.clickhouse.Query(ctx, `
		WITH seg AS (
			SELECT
				tenant_id, node_id, stream_id, source_event_id,
				argMax(process_type,        projection_version_ms) AS process_type,
				argMax(output_codec,        projection_version_ms) AS output_codec,
				argMax(track_type,          projection_version_ms) AS track_type,
				argMax(cluster_id,           projection_version_ms) AS cluster_id,
				argMax(media_seconds,        projection_version_ms) AS media_seconds,
				argMax(source_started_at_ms, projection_version_ms) AS source_started_at_ms
			FROM periscope.processing_segments_final
			WHERE projection_version_ms >= ? AND projection_version_ms < ?
			GROUP BY tenant_id, node_id, stream_id, source_event_id
		)
		SELECT
			toStartOfFiveMinute(toDateTime(intDiv(source_started_at_ms, 1000))) AS window_start,
			tenant_id, cluster_id, stream_id, process_type, output_codec, track_type, source_event_id,
			sum(media_seconds) AS media_seconds
		FROM seg
		WHERE source_started_at_ms > 0
		GROUP BY window_start, tenant_id, cluster_id, stream_id, process_type, output_codec, track_type, source_event_id`,
		windowStart.UnixMilli(), windowEnd.UnixMilli())
	if err != nil {
		return fmt.Errorf("processing_5m source query: %w", err)
	}
	defer func() { _ = rows.Close() }()

	projectionVersionMS := time.Now().UnixMilli()
	batch, err := h.clickhouse.PrepareBatch(ctx, `
		INSERT INTO periscope.processing_5m (
			window_start, tenant_id, cluster_id, stream_id, process_type, output_codec, track_type, source_event_id,
			media_seconds, projection_version_ms
		)`)
	if err != nil {
		return fmt.Errorf("processing_5m prepare: %w", err)
	}
	defer closeClickHouseBatch(batch)

	rowsEmitted := 0
	for rows.Next() {
		var (
			windowStartT                                                       time.Time
			tenantID, clusterID, streamID, processType, outputCodec, trackType string
			sourceEventID                                                      string
			rawDurationSeconds                                                 float64
		)
		if err := rows.Scan(&windowStartT, &tenantID, &clusterID, &streamID, &processType, &outputCodec, &trackType, &sourceEventID, &rawDurationSeconds); err != nil {
			return fmt.Errorf("processing_5m scan: %w", err)
		}
		if err := batch.Append(
			windowStartT,
			tenantID, clusterID, streamID, processType, outputCodec, trackType, sourceEventID,
			rawDurationSeconds, projectionVersionMS,
		); err != nil {
			return fmt.Errorf("processing_5m append: %w", err)
		}
		rowsEmitted++
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("processing_5m iterate: %w", err)
	}
	if rowsEmitted == 0 {
		return nil
	}
	if err := batch.Send(); err != nil {
		return fmt.Errorf("processing_5m send: %w", err)
	}
	if h.metrics != nil && h.metrics.ClickHouseInserts != nil {
		h.metrics.ClickHouseInserts.WithLabelValues("processing_5m", "inserted").Add(float64(rowsEmitted))
	}
	return nil
}

// --- api_usage_5m ---

func (h *AnalyticsHandler) rebuildApiUsage5m(ctx context.Context, windowStart, windowEnd time.Time) error {
	// AggregateFunction states must be written by ClickHouse itself —
	// passing them through Go would require carrying opaque bytes. Use
	// INSERT...SELECT so uniqCombinedState() lands directly in the
	// destination column. The downstream hourly/daily MVs merge the
	// states via uniqCombinedMergeState; query-time finalization uses
	// uniqCombinedMerge.
	projectionVersionMS := time.Now().UnixMilli()
	if err := h.clickhouse.Exec(ctx, `
		INSERT INTO periscope.api_usage_5m
		SELECT
			toStartOfFiveMinute(timestamp)              AS window_start,
			tenant_id,
			auth_type,
			operation_type,
			ifNull(operation_name, '')                  AS operation_name,
			sum(request_count)                           AS requests,
			sum(error_count)                             AS errors,
			sum(total_duration_ms)                       AS duration_ms,
			sum(total_complexity)                        AS complexity,
			uniqCombinedArrayState(user_hashes)          AS unique_users_state,
			uniqCombinedArrayState(token_hashes)         AS unique_tokens_state,
			?                                            AS projection_version_ms
		FROM periscope.api_requests
		WHERE timestamp >= ? AND timestamp < ?
		GROUP BY window_start, tenant_id, auth_type, operation_type, operation_name`,
		projectionVersionMS, windowStart, windowEnd); err != nil {
		return fmt.Errorf("api_usage_5m insert: %w", err)
	}
	if h.metrics != nil && h.metrics.ClickHouseInserts != nil {
		h.metrics.ClickHouseInserts.WithLabelValues("api_usage_5m", "inserted").Inc()
	}
	return nil
}

// --- shared helpers ---

// windowsForSpan walks the 5-minute windows the interval [startMS, endMS)
// overlaps and returns a map from window-start (Unix ms) to overlap (ms)
// within that window. Half-open: a span ending exactly at a 5-min
// boundary contributes nothing to that boundary's window.
func windowsForSpan(startMS, endMS int64) map[int64]int64 {
	out := map[int64]int64{}
	if endMS <= startMS {
		return out
	}
	const fiveMinMS = int64(5 * 60 * 1000)
	cur := (startMS / fiveMinMS) * fiveMinMS
	for cur < endMS {
		next := cur + fiveMinMS
		overlapStart := maxTwoInt64(cur, startMS)
		overlapEnd := minTwoInt64(next, endMS)
		overlap := overlapEnd - overlapStart
		if overlap > 0 {
			out[cur] = overlap
		}
		cur = next
	}
	return out
}

func maxTwoInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

func minTwoInt64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}

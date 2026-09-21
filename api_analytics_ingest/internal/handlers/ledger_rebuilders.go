package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"math/bits"
	"sort"
	"strings"
	"time"

	"frameworks/api_analytics_ingest/internal/database/periscopeingestdb"

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
	// consume the next projection/ingestion window. Five minutes matches the ledger
	// granularity so steady-state work is bounded.
	LedgerRebuildInterval = 5 * time.Minute

	// LedgerSettlementLag keeps the newest projection/ingestion interval open
	// while writers settle. Delayed facts cursor on projection/ingestion time,
	// not their older source timestamp, so they are still discovered. Two
	// minutes mirrors the billing-cursor settlement lag for symmetry.
	LedgerSettlementLag = 2 * time.Minute

	// LedgerInitialLookback is the bounded bootstrap range used when a
	// cursor row does not exist yet. Rebuilders are forward-only;
	// explicit historical backfills use admin tooling rather than
	// first-boot scans.
	LedgerInitialLookback = 2 * LedgerRebuildInterval

	// DeliveryLedgerInitialLookback populates the newly introduced shared
	// playback/restream delivery ledger. Established ledgers resume from the
	// v1-to-v2 cursor seed and never replay their retained history on each
	// replica merely because the cursor engine changed.
	DeliveryLedgerInitialLookback = 90 * 24 * time.Hour

	// LedgerRebuildChunkSpan caps each source scan. Emission batches separately
	// cap client memory and ClickHouse insert volume. Catch-up checkpoints every
	// successful chunk independently.
	LedgerRebuildChunkSpan     = time.Hour
	DeliveryLedgerChunkSpan    = time.Hour
	LedgerCatchupChunksPerPass = 24
	LedgerCatchupPassesOnBoot  = 96
	LedgerEmissionBatchSize    = 1000
	LedgerChunkTimeout         = 10 * time.Minute

	// LedgerMaxSpan bounds rows emitted by one corrupted or malicious source
	// fact. Longer sessions must be split upstream or quarantined.
	LedgerMaxSpan = 90 * 24 * time.Hour
)

// LedgerScheduler runs the six ledger rebuilders on independent goroutines.
// Each runs at LedgerRebuildInterval with its own ticker.
type LedgerScheduler struct {
	h            *AnalyticsHandler
	logger       logging.Logger
	lease        LedgerLease
	chunkTimeout time.Duration
}

func NewLedgerScheduler(h *AnalyticsHandler, optionalLease ...LedgerLease) *LedgerScheduler {
	lease := LedgerLease(localLedgerLease{})
	if len(optionalLease) > 0 && optionalLease[0] != nil {
		lease = optionalLease[0]
	}
	return &LedgerScheduler{h: h, logger: h.logger, lease: lease, chunkTimeout: LedgerChunkTimeout}
}

// Start launches the six rebuild goroutines. They run until ctx is
// cancelled. Errors per pass are logged but do not terminate the
// goroutine; the next tick retries.
func (s *LedgerScheduler) Start(ctx context.Context) {
	rebuilders := []struct {
		name string
		run  func(context.Context, time.Time, time.Time) error
	}{
		{"viewer_usage_5m", s.h.rebuildViewerUsage5m},
		{"delivery_usage_5m", s.h.rebuildDeliveryUsage5m},
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

// rebuildDeliveryUsage5m is the sole writer for the shared playback/restream
// delivery ledger. Its cursor discovers both source projections so playback
// cannot be emitted independently by the viewer worker.
func (h *AnalyticsHandler) rebuildDeliveryUsage5m(ctx context.Context, windowStart, windowEnd time.Time) error {
	emissions := make([]deliveryUsageEmission, 0, LedgerEmissionBatchSize)
	flush := func() error {
		if len(emissions) == 0 {
			return nil
		}
		if err := h.writeDeliveryUsage5m(ctx, emissions, time.Now().UnixMilli()); err != nil {
			return err
		}
		emissions = emissions[:0]
		return nil
	}
	emit := func(emission deliveryUsageEmission) error {
		emissions = append(emissions, emission)
		if len(emissions) >= LedgerEmissionBatchSize {
			return flush()
		}
		return nil
	}
	desiredByIdentity := make(map[deliveryUsageIdentity]map[deliveryUsageWindowKey]struct{})
	if err := h.reconcilePlaybackDeliveryUsage5m(ctx, windowStart, windowEnd, desiredByIdentity, emit); err != nil {
		return err
	}
	rows, err := h.clickhouse.Query(ctx, `
		WITH delivery_sessions AS (
			SELECT tenant_id, node_id, source_event_id AS delivery_id, 'restream' AS delivery_kind,
				source_event_id, argMax(cluster_id, projection_version_ms) AS cluster_id,
				argMax(stream_id, projection_version_ms) AS stream_id,
				argMax(platform, projection_version_ms) AS platform,
				argMax(state, projection_version_ms) AS state,
				argMax(source_started_at_ms, projection_version_ms) AS started_at_ms,
				argMax(source_ended_at_ms, projection_version_ms) AS ended_at_ms,
				toUInt64(0) AS up_bytes,
				argMax(bytes_sent, projection_version_ms) AS down_bytes
			FROM periscope.restream_sessions_final
			WHERE projection_version_ms >= ? AND projection_version_ms < ?
			GROUP BY tenant_id, node_id, source_event_id
		)
		SELECT toString(tenant_id), node_id, delivery_id, delivery_kind, source_event_id,
		       cluster_id, toString(stream_id), platform, state, started_at_ms, ended_at_ms, up_bytes, down_bytes
		FROM delivery_sessions`,
		windowStart.UnixMilli(), windowEnd.UnixMilli())
	if err != nil {
		return fmt.Errorf("delivery_usage_5m source query: %w", err)
	}
	defer func() { _ = rows.Close() }()

	type source struct {
		tenantID, nodeID, deliveryID, kind, sourceEventID string
		clusterID, streamID, platform                     string
		state                                             string
		startMS, endMS                                    int64
		upBytes, downBytes                                uint64
	}
	for rows.Next() {
		var s source
		if scanErr := rows.Scan(&s.tenantID, &s.nodeID, &s.deliveryID, &s.kind, &s.sourceEventID, &s.clusterID, &s.streamID, &s.platform, &s.state, &s.startMS, &s.endMS, &s.upBytes, &s.downBytes); scanErr != nil {
			return fmt.Errorf("delivery_usage_5m scan: %w", scanErr)
		}
		identity := deliveryUsageIdentity{tenantID: s.tenantID, nodeID: s.nodeID, kind: s.kind, deliveryID: s.deliveryID}
		if (s.state != "idle" && s.state != "failed") || s.startMS <= 0 || s.endMS <= s.startMS {
			desiredByIdentity[identity] = map[deliveryUsageWindowKey]struct{}{}
			continue
		}
		totalMS := s.endMS - s.startMS
		if totalMS > LedgerMaxSpan.Milliseconds() {
			h.logger.WithFields(logging.Fields{
				"ledger": "delivery_usage_5m", "delivery_kind": s.kind,
			}).Warn("Skipping over-span delivery fact without retracting prior ledger rows")
			if h.metrics != nil && h.metrics.ClickHouseInserts != nil {
				h.metrics.ClickHouseInserts.WithLabelValues("delivery_usage_5m", "quarantined_span").Inc()
			}
			continue
		}
		desiredKeys := map[deliveryUsageWindowKey]struct{}{}
		desiredByIdentity[identity] = desiredKeys
		var cumulativeOverlapMS int64
		for _, window := range orderedWindowsForSpan(s.startMS, s.endMS) {
			windowMS, overlapMS := window.windowStartMS, window.overlapMS
			if overlapMS <= 0 {
				continue
			}
			desiredKeys[deliveryUsageWindowKey{
				windowStartMS: windowMS, clusterID: s.clusterID, streamID: s.streamID, platform: s.platform,
			}] = struct{}{}
			previousOverlapMS := cumulativeOverlapMS
			cumulativeOverlapMS += overlapMS
			upObserved := mulDivUint64(s.upBytes, uint64(cumulativeOverlapMS), uint64(totalMS)) -
				mulDivUint64(s.upBytes, uint64(previousOverlapMS), uint64(totalMS))
			downObserved := mulDivUint64(s.downBytes, uint64(cumulativeOverlapMS), uint64(totalMS)) -
				mulDivUint64(s.downBytes, uint64(previousOverlapMS), uint64(totalMS))
			if emitErr := emit(deliveryUsageEmission{
				windowStartMS: windowMS, tenantID: s.tenantID, clusterID: s.clusterID, streamID: s.streamID,
				nodeID: s.nodeID, kind: s.kind, deliveryID: s.deliveryID, platform: s.platform,
				secondsObserved: uint32(overlapMS / 1000), upObserved: upObserved,
				downObserved: downObserved, sourceEventID: s.sourceEventID,
			}); emitErr != nil {
				return emitErr
			}
		}
	}
	if rowsErr := rows.Err(); rowsErr != nil {
		return fmt.Errorf("delivery_usage_5m iterate: %w", rowsErr)
	}
	tombstones, err := h.deliveryUsageTombstonesForProjectionWindow(ctx, windowStart, windowEnd, desiredByIdentity)
	if err != nil {
		return err
	}
	for identity, identityTombstones := range tombstones {
		for _, tombstone := range identityTombstones {
			if err := emit(deliveryUsageEmission{
				windowStartMS: tombstone.windowStartMS, tenantID: identity.tenantID, clusterID: tombstone.clusterID,
				streamID: tombstone.streamID, nodeID: identity.nodeID, kind: identity.kind, deliveryID: identity.deliveryID,
				platform: tombstone.platform, sourceEventID: tombstone.sourceEventID,
			}); err != nil {
				return err
			}
		}
	}
	return flush()
}

func (h *AnalyticsHandler) reconcilePlaybackDeliveryUsage5m(ctx context.Context, windowStart, windowEnd time.Time, desiredByIdentity map[deliveryUsageIdentity]map[deliveryUsageWindowKey]struct{}, emit func(deliveryUsageEmission) error) error {
	rows, err := h.clickhouse.Query(ctx, `
		WITH sessions AS (
			SELECT tenant_id, node_id, session_id,
				argMax(source_event_id, projection_version_ms) AS source_event_id,
				argMax(cluster_id, projection_version_ms) AS cluster_id,
				argMax(stream_id, projection_version_ms) AS stream_id,
				argMax(source_started_at_ms, projection_version_ms) AS source_started_at_ms,
				argMax(source_ended_at_ms, projection_version_ms) AS source_ended_at_ms,
				argMax(uploaded_bytes, projection_version_ms) AS uploaded_bytes,
				argMax(downloaded_bytes, projection_version_ms) AS downloaded_bytes,
				argMax(closed_reason, projection_version_ms) AS closed_reason
			FROM periscope.viewer_sessions_final
			WHERE projection_version_ms >= ? AND projection_version_ms < ?
			GROUP BY tenant_id, node_id, session_id
		)
		SELECT
			toString(tenant_id), node_id, session_id, source_event_id,
			cluster_id, toString(stream_id), source_started_at_ms,
			source_ended_at_ms, uploaded_bytes, downloaded_bytes, closed_reason
		FROM sessions`,
		windowStart.UnixMilli(), windowEnd.UnixMilli())
	if err != nil {
		return fmt.Errorf("delivery_usage_5m playback reconciliation query: %w", err)
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var tenantID, nodeID, sessionID, sourceEventID, clusterID, streamID string
		var closedReason string
		var startMS, endMS int64
		var upBytes, downBytes uint64
		if scanErr := rows.Scan(&tenantID, &nodeID, &sessionID, &sourceEventID, &clusterID, &streamID, &startMS, &endMS, &upBytes, &downBytes, &closedReason); scanErr != nil {
			return fmt.Errorf("delivery_usage_5m playback reconciliation scan: %w", scanErr)
		}
		identity := deliveryUsageIdentity{tenantID: tenantID, nodeID: nodeID, kind: "playback", deliveryID: sessionID}
		if closedReason != "final" || startMS <= 0 || endMS <= startMS {
			desiredByIdentity[identity] = map[deliveryUsageWindowKey]struct{}{}
			continue
		}
		totalMS := endMS - startMS
		if totalMS > LedgerMaxSpan.Milliseconds() {
			h.logger.WithFields(logging.Fields{"ledger": "delivery_usage_5m", "delivery_kind": "playback"}).Warn("Skipping over-span delivery fact without retracting prior ledger rows")
			if h.metrics != nil && h.metrics.ClickHouseInserts != nil {
				h.metrics.ClickHouseInserts.WithLabelValues("delivery_usage_5m", "quarantined_span").Inc()
			}
			continue
		}
		desiredKeys := make(map[deliveryUsageWindowKey]struct{})
		desiredByIdentity[identity] = desiredKeys
		var cumulativeOverlapMS int64
		for _, window := range orderedWindowsForSpan(startMS, endMS) {
			if window.overlapMS <= 0 {
				continue
			}
			desiredKeys[deliveryUsageWindowKey{windowStartMS: window.windowStartMS, clusterID: clusterID, streamID: streamID}] = struct{}{}
			previousOverlapMS := cumulativeOverlapMS
			cumulativeOverlapMS += window.overlapMS
			if emitErr := emit(deliveryUsageEmission{
				windowStartMS: window.windowStartMS, tenantID: tenantID, clusterID: clusterID, streamID: streamID,
				nodeID: nodeID, kind: "playback", deliveryID: sessionID, sourceEventID: sourceEventID,
				secondsObserved: uint32(window.overlapMS / 1000),
				upObserved: mulDivUint64(upBytes, uint64(cumulativeOverlapMS), uint64(totalMS)) -
					mulDivUint64(upBytes, uint64(previousOverlapMS), uint64(totalMS)),
				downObserved: mulDivUint64(downBytes, uint64(cumulativeOverlapMS), uint64(totalMS)) -
					mulDivUint64(downBytes, uint64(previousOverlapMS), uint64(totalMS)),
			}); emitErr != nil {
				return emitErr
			}
		}
	}
	if rowsErr := rows.Err(); rowsErr != nil {
		return fmt.Errorf("delivery_usage_5m playback reconciliation iterate: %w", rowsErr)
	}
	return nil
}

type deliveryUsageEmission struct {
	windowStartMS                                                     int64
	tenantID, clusterID, streamID, nodeID, kind, deliveryID, platform string
	secondsObserved                                                   uint32
	upObserved, downObserved                                          uint64
	sourceEventID                                                     string
}

func (h *AnalyticsHandler) writeDeliveryUsage5m(ctx context.Context, emissions []deliveryUsageEmission, projectionVersionMS int64) error {
	if len(emissions) == 0 {
		return nil
	}
	batch, err := periscopeingestdb.PrepareDeliveryUsage5m(ctx, h.clickhouse)
	if err != nil {
		return fmt.Errorf("delivery_usage_5m prepare: %w", err)
	}
	defer func() { _ = batch.Close() }()
	for _, emission := range emissions {
		tenantID, parseErr := uuid.Parse(emission.tenantID)
		if parseErr != nil {
			return fmt.Errorf("delivery_usage_5m tenant_id %q: %w", emission.tenantID, parseErr)
		}
		streamID, parseErr := uuid.Parse(emission.streamID)
		if parseErr != nil {
			return fmt.Errorf("delivery_usage_5m stream_id %q: %w", emission.streamID, parseErr)
		}
		if appendErr := batch.Append(periscopeingestdb.DeliveryUsage5mRow{
			WindowStart: time.UnixMilli(emission.windowStartMS).UTC(), TenantID: tenantID, ClusterID: emission.clusterID,
			StreamID: streamID, NodeID: emission.nodeID, DeliveryKind: emission.kind, DeliveryID: emission.deliveryID,
			Platform: emission.platform, SecondsObserved: emission.secondsObserved,
			UpBytesObserved: emission.upObserved, DownBytesObserved: emission.downObserved,
			SourceEventID: emission.sourceEventID, ProjectionVersionMS: projectionVersionMS,
		}); appendErr != nil {
			return fmt.Errorf("delivery_usage_5m append: %w", appendErr)
		}
	}
	if err := batch.Send(); err != nil {
		return fmt.Errorf("delivery_usage_5m send: %w", err)
	}
	if h.metrics != nil && h.metrics.ClickHouseInserts != nil {
		h.metrics.ClickHouseInserts.WithLabelValues("delivery_usage_5m", "inserted").Add(float64(len(emissions)))
	}
	return nil
}

type deliveryUsageWindowKey struct {
	windowStartMS int64
	clusterID     string
	streamID      string
	platform      string
}

type deliveryUsageIdentity struct {
	tenantID, nodeID, kind, deliveryID string
}

type deliveryUsageTombstone struct {
	deliveryUsageWindowKey
	sourceEventID string
}

func (h *AnalyticsHandler) deliveryUsageTombstonesForProjectionWindow(ctx context.Context, windowStart, windowEnd time.Time, desiredByIdentity map[deliveryUsageIdentity]map[deliveryUsageWindowKey]struct{}) (map[deliveryUsageIdentity][]deliveryUsageTombstone, error) {
	out := make(map[deliveryUsageIdentity][]deliveryUsageTombstone)
	if len(desiredByIdentity) == 0 {
		return out, nil
	}
	rows, err := h.clickhouse.Query(ctx, `
		WITH changed_bounds AS (
			SELECT tenant_id, node_id, delivery_kind, delivery_id,
				greatest(minIf(source_started_at_ms, source_started_at_ms > 0 AND source_ended_at_ms > source_started_at_ms),
					toUnixTimestamp64Milli(now64(3) - INTERVAL 90 DAY)) AS min_started_at_ms,
				if(maxIf(source_ended_at_ms, source_started_at_ms > 0 AND source_ended_at_ms > source_started_at_ms) > 0,
					maxIf(source_ended_at_ms, source_started_at_ms > 0 AND source_ended_at_ms > source_started_at_ms),
					toUnixTimestamp64Milli(now64(3))) AS max_ended_at_ms,
				countIf(source_started_at_ms <= 0 OR source_ended_at_ms <= source_started_at_ms) > 0 AS has_unbounded_retraction
			FROM (
				SELECT tenant_id, node_id, 'restream' AS delivery_kind, source_event_id AS delivery_id,
					source_started_at_ms, source_ended_at_ms
				FROM periscope.restream_sessions_final
				WHERE projection_version_ms >= ? AND projection_version_ms < ?
				UNION ALL
				SELECT tenant_id, node_id, 'playback' AS delivery_kind, session_id AS delivery_id,
					source_started_at_ms, source_ended_at_ms
				FROM periscope.viewer_sessions_final
				WHERE projection_version_ms >= ? AND projection_version_ms < ?
			)
			GROUP BY tenant_id, node_id, delivery_kind, delivery_id
		), scan_bounds AS (
			SELECT
				if(countIf(has_unbounded_retraction) > 0,
					toStartOfFiveMinute(now() - INTERVAL 90 DAY),
					toDateTime(intDiv(min(min_started_at_ms), 1000)) - INTERVAL 5 MINUTE) AS min_window_start,
				if(countIf(has_unbounded_retraction) > 0,
					now(),
					toDateTime(intDiv(max(max_ended_at_ms), 1000)) + INTERVAL 5 MINUTE) AS max_window_start
			FROM changed_bounds
		)
		SELECT toString(ledger.tenant_id), ledger.node_id, ledger.delivery_kind, ledger.delivery_id,
			toInt64(toUnixTimestamp(ledger.window_start)) * 1000 AS window_start_ms,
			ledger.cluster_id, toString(ledger.stream_id), ledger.platform,
			argMax(ledger.source_event_id, ledger.projection_version_ms) AS source_event_id,
			argMax(ledger.seconds_observed, ledger.projection_version_ms) AS seconds_observed,
			argMax(ledger.up_bytes_observed, ledger.projection_version_ms) AS up_bytes_observed,
			argMax(ledger.down_bytes_observed, ledger.projection_version_ms) AS down_bytes_observed
		FROM periscope.delivery_usage_5m AS ledger
		INNER JOIN changed_bounds AS changed
		  ON ledger.tenant_id = changed.tenant_id
		 AND ledger.node_id = changed.node_id
		 AND ledger.delivery_kind = changed.delivery_kind
		 AND ledger.delivery_id = changed.delivery_id
		WHERE ledger.projection_version_ms >= toUnixTimestamp64Milli(now64(3) - INTERVAL 90 DAY)
		  AND ledger.window_start >= (SELECT min_window_start FROM scan_bounds)
		  AND ledger.window_start < (SELECT max_window_start FROM scan_bounds)
		  AND (changed.has_unbounded_retraction
		    OR (ledger.window_start >= toDateTime(intDiv(changed.min_started_at_ms, 1000)) - INTERVAL 5 MINUTE
		    AND ledger.window_start < toDateTime(intDiv(changed.max_ended_at_ms, 1000)) + INTERVAL 5 MINUTE))
		GROUP BY ledger.tenant_id, ledger.node_id, ledger.delivery_kind, ledger.delivery_id,
			ledger.window_start, ledger.cluster_id, ledger.stream_id, ledger.platform`,
		windowStart.UnixMilli(), windowEnd.UnixMilli(), windowStart.UnixMilli(), windowEnd.UnixMilli())
	if err != nil {
		return nil, fmt.Errorf("delivery_usage_5m batched tombstone lookup: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var (
			identity                 deliveryUsageIdentity
			key                      deliveryUsageWindowKey
			sourceEventID            string
			secondsObserved          uint32
			upObserved, downObserved uint64
		)
		if err := rows.Scan(&identity.tenantID, &identity.nodeID, &identity.kind, &identity.deliveryID, &key.windowStartMS, &key.clusterID, &key.streamID, &key.platform, &sourceEventID, &secondsObserved, &upObserved, &downObserved); err != nil {
			return nil, fmt.Errorf("delivery_usage_5m batched tombstone scan: %w", err)
		}
		desired, eligible := desiredByIdentity[identity]
		if !eligible || (secondsObserved == 0 && upObserved == 0 && downObserved == 0) {
			continue
		}
		if _, current := desired[key]; current {
			continue
		}
		out[identity] = append(out[identity], deliveryUsageTombstone{deliveryUsageWindowKey: key, sourceEventID: sourceEventID})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("delivery_usage_5m batched tombstone iterate: %w", err)
	}
	return out, nil
}

func (s *LedgerScheduler) runLoop(ctx context.Context, name string, run func(context.Context, time.Time, time.Time) error) {
	ticker := time.NewTicker(LedgerRebuildInterval)
	defer ticker.Stop()

	doPass := func(maxPasses int) {
		now := time.Now().UTC()
		windowEnd := now.Add(-LedgerSettlementLag).Truncate(5 * time.Minute)
		maxChunks := maxPasses * LedgerCatchupChunksPerPass
		for chunk := 0; chunk < maxChunks; chunk++ {
			release, leader, leaderErr := s.lease.TryAcquire(ctx, "ledger:"+name)
			if leaderErr != nil {
				s.setLedgerLeader(name, false)
				s.logger.WithError(leaderErr).WithField("ledger", name).Warn("Ledger rebuild lease acquisition failed")
				return
			}
			if !leader {
				s.setLedgerLeader(name, false)
				// Keep every replica's cursor-lag series current even when another
				// process owns the writer lease.
				if _, err := s.h.getLedgerRebuildCursor(ctx, name, windowEnd.Add(-initialLedgerLookback(name))); err != nil {
					s.logger.WithError(err).WithField("ledger", name).Warn("Ledger cursor observation failed on non-leader")
				}
				s.logger.WithField("ledger", name).Debug("Ledger rebuild chunk assigned to another Periscope replica")
				return
			}
			s.setLedgerLeader(name, true)
			chunkCtx, chunkCancel := context.WithTimeout(ctx, s.chunkTimeout)
			caughtUp, passErr := s.runLedgerChunk(chunkCtx, name, windowEnd, run)
			chunkCancel()
			releaseCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			releaseErr := release(releaseCtx)
			cancel()
			if releaseErr != nil {
				s.logger.WithError(releaseErr).WithField("ledger", name).Warn("Ledger rebuild lease release failed")
				return
			}
			s.setLedgerLeader(name, false)
			if passErr != nil {
				s.logger.WithError(passErr).WithFields(logging.Fields{
					"ledger":     name,
					"window_end": windowEnd,
				}).Warn("Ledger rebuild chunk failed")
				return
			}
			if caughtUp {
				return
			}
			select {
			case <-ctx.Done():
				return
			default:
			}
		}
	}

	doPass(LedgerCatchupPassesOnBoot)
	for {
		select {
		case <-ctx.Done():
			s.logger.WithField("ledger", name).Info("Ledger rebuilder stopping")
			return
		case <-ticker.C:
			doPass(1)
		}
	}
}

func (s *LedgerScheduler) setLedgerLeader(name string, leader bool) {
	if s.h.metrics == nil || s.h.metrics.LedgerLeader == nil {
		return
	}
	value := 0.0
	if leader {
		value = 1
	}
	s.h.metrics.LedgerLeader.WithLabelValues(name).Set(value)
}

func (s *LedgerScheduler) runLedgerPass(ctx context.Context, name string, windowEnd time.Time, run func(context.Context, time.Time, time.Time) error) error {
	windowStart, err := s.h.getLedgerRebuildCursor(ctx, name, windowEnd.Add(-initialLedgerLookback(name)))
	if err != nil {
		return fmt.Errorf("read cursor: %w", err)
	}
	for chunks := 0; chunks < LedgerCatchupChunksPerPass && windowStart.Before(windowEnd); chunks++ {
		chunkEnd := windowStart.Add(ledgerRebuildChunkSpan(name))
		if chunkEnd.After(windowEnd) {
			chunkEnd = windowEnd
		}
		if err := run(ctx, windowStart, chunkEnd); err != nil {
			return fmt.Errorf("rebuild [%s, %s): %w", windowStart, chunkEnd, err)
		}
		if err := s.h.recordLedgerRebuildCursor(ctx, name, chunkEnd); err != nil {
			return fmt.Errorf("checkpoint %s: %w", chunkEnd, err)
		}
		windowStart = chunkEnd
	}
	return nil
}

func (s *LedgerScheduler) runLedgerChunk(ctx context.Context, name string, windowEnd time.Time, run func(context.Context, time.Time, time.Time) error) (bool, error) {
	windowStart, err := s.h.getLedgerRebuildCursor(ctx, name, windowEnd.Add(-initialLedgerLookback(name)))
	if err != nil {
		return false, fmt.Errorf("read cursor: %w", err)
	}
	if !windowStart.Before(windowEnd) {
		return true, nil
	}
	chunkEnd := windowStart.Add(ledgerRebuildChunkSpan(name))
	if chunkEnd.After(windowEnd) {
		chunkEnd = windowEnd
	}
	if err := run(ctx, windowStart, chunkEnd); err != nil {
		return false, fmt.Errorf("rebuild [%s, %s): %w", windowStart, chunkEnd, err)
	}
	if err := s.h.recordLedgerRebuildCursor(ctx, name, chunkEnd); err != nil {
		return false, fmt.Errorf("checkpoint %s: %w", chunkEnd, err)
	}
	return !chunkEnd.Before(windowEnd), nil
}

func initialLedgerLookback(name string) time.Duration {
	if name == "delivery_usage_5m" {
		return DeliveryLedgerInitialLookback
	}
	return LedgerInitialLookback
}

func ledgerRebuildChunkSpan(name string) time.Duration {
	if name == "delivery_usage_5m" {
		return DeliveryLedgerChunkSpan
	}
	return LedgerRebuildChunkSpan
}

func (h *AnalyticsHandler) getLedgerRebuildCursor(ctx context.Context, ledgerName string, defaultStart time.Time) (time.Time, error) {
	rows, err := h.clickhouse.Query(ctx, `
		SELECT max(last_processed_projection_ms)
		FROM periscope.ledger_rebuild_cursors_v2
		WHERE ledger_name = ?
		GROUP BY ledger_name`,
		ledgerName)
	if err != nil {
		return time.Time{}, err
	}
	defer func() { _ = rows.Close() }()
	if !rows.Next() {
		cursor := defaultStart.UTC()
		h.observeLedgerCursorLag(ledgerName, cursor)
		return cursor, nil
	}
	var lastProcessedMS int64
	if err := rows.Scan(&lastProcessedMS); err != nil {
		return time.Time{}, err
	}
	if lastProcessedMS <= 0 {
		cursor := defaultStart.UTC()
		h.observeLedgerCursorLag(ledgerName, cursor)
		return cursor, nil
	}
	cursor := time.UnixMilli(lastProcessedMS).UTC()
	h.observeLedgerCursorLag(ledgerName, cursor)
	return cursor, nil
}

func (h *AnalyticsHandler) observeLedgerCursorLag(ledgerName string, cursor time.Time) {
	if h.metrics == nil || h.metrics.LedgerCursorLag == nil {
		return
	}
	lag := time.Since(cursor).Seconds()
	if lag < 0 {
		lag = 0
	}
	h.metrics.LedgerCursorLag.WithLabelValues(ledgerName).Set(lag)
}

func (h *AnalyticsHandler) recordLedgerRebuildCursor(ctx context.Context, ledgerName string, processedThrough time.Time) error {
	batch, err := periscopeingestdb.PrepareLedgerRebuildCursor(ctx, h.clickhouse)
	if err != nil {
		return fmt.Errorf("ledger_rebuild_cursors prepare: %w", err)
	}
	defer func() { _ = batch.Close() }()
	if err := batch.Append(periscopeingestdb.LedgerRebuildCursorRow{
		LedgerName: ledgerName, LastProcessedProjectionMS: processedThrough.UnixMilli(), UpdatedAtMS: time.Now().UnixMilli(),
	}); err != nil {
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

	emissions := make([]viewerUsageEmission, 0, LedgerEmissionBatchSize)
	flush := func() error {
		if len(emissions) == 0 {
			return nil
		}
		if writeErr := h.writeViewerUsage5m(ctx, emissions, time.Now().UnixMilli()); writeErr != nil {
			return writeErr
		}
		emissions = emissions[:0]
		return nil
	}
	emit := func(emission viewerUsageEmission) error {
		emissions = append(emissions, emission)
		if len(emissions) >= LedgerEmissionBatchSize {
			return flush()
		}
		return nil
	}
	desiredByIdentity := make(map[viewerUsageIdentity]map[viewerUsageWindowKey]struct{})
	hadSessions := false
	for rows.Next() {
		var s viewerSessionProjection
		if scanErr := rows.Scan(&s.tenantID, &s.nodeID, &s.sessionID, &s.sourceEventID, &s.clusterID, &s.streamID, &s.startMS, &s.endMS, &s.durationSeconds, &s.upBytes, &s.downBytes); scanErr != nil {
			return fmt.Errorf("viewer_usage_5m scan: %w", scanErr)
		}
		hadSessions = true
		totalSpanMS := s.endMS - s.startMS
		if totalSpanMS <= 0 {
			continue
		}
		if totalSpanMS > LedgerMaxSpan.Milliseconds() {
			h.logger.WithField("ledger", "viewer_usage_5m").Warn("Skipping over-span viewer fact without retracting prior ledger rows")
			if h.metrics != nil && h.metrics.ClickHouseInserts != nil {
				h.metrics.ClickHouseInserts.WithLabelValues("viewer_usage_5m", "quarantined_span").Inc()
			}
			continue
		}
		desiredKeys := map[viewerUsageWindowKey]struct{}{}
		identity := viewerUsageIdentity{tenantID: s.tenantID, nodeID: s.nodeID, sessionID: s.sessionID}
		desiredByIdentity[identity] = desiredKeys
		var cumulativeOverlapMS int64
		for _, window := range orderedWindowsForSpan(s.startMS, s.endMS) {
			windowMS, overlapMS := window.windowStartMS, window.overlapMS
			if overlapMS <= 0 {
				continue
			}
			desiredKeys[viewerUsageWindowKey{
				windowStartMS: windowMS,
				clusterID:     s.clusterID,
				streamID:      s.streamID,
			}] = struct{}{}
			previousOverlapMS := cumulativeOverlapMS
			cumulativeOverlapMS += overlapMS
			upObserved := mulDivUint64(s.upBytes, uint64(cumulativeOverlapMS), uint64(totalSpanMS)) -
				mulDivUint64(s.upBytes, uint64(previousOverlapMS), uint64(totalSpanMS))
			downObserved := mulDivUint64(s.downBytes, uint64(cumulativeOverlapMS), uint64(totalSpanMS)) -
				mulDivUint64(s.downBytes, uint64(previousOverlapMS), uint64(totalSpanMS))
			if emitErr := emit(viewerUsageEmission{
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
			}); emitErr != nil {
				return emitErr
			}
		}
	}
	if iterErr := rows.Err(); iterErr != nil {
		return fmt.Errorf("viewer_usage_5m iterate: %w", iterErr)
	}
	if !hadSessions {
		return nil
	}
	tombstones, err := h.viewerUsageTombstonesForProjectionWindow(ctx, windowStart, windowEnd, desiredByIdentity)
	if err != nil {
		return err
	}
	for identity, identityTombstones := range tombstones {
		for _, t := range identityTombstones {
			if err := emit(viewerUsageEmission{
				windowStartMS: t.windowStartMS,
				tenantID:      identity.tenantID,
				clusterID:     t.clusterID,
				streamID:      t.streamID,
				nodeID:        identity.nodeID,
				sessionID:     identity.sessionID,
				sourceEventID: t.sourceEventID,
			}); err != nil {
				return err
			}
		}
	}
	return flush()
}

type viewerUsageEmission struct {
	windowStartMS                    int64
	tenantID, clusterID, streamID    string
	nodeID, sessionID, sourceEventID string
	secondsObserved                  uint32
	upObserved, downObserved         uint64
}

func (h *AnalyticsHandler) writeViewerUsage5m(ctx context.Context, emissions []viewerUsageEmission, projectionVersionMS int64) error {
	batch, err := periscopeingestdb.PrepareViewerUsage5m(ctx, h.clickhouse)
	if err != nil {
		return fmt.Errorf("viewer_usage_5m prepare: %w", err)
	}
	defer func() { _ = batch.Close() }()

	for _, e := range emissions {
		tenantUUID, tenantErr := uuid.Parse(e.tenantID)
		if tenantErr != nil {
			return fmt.Errorf("viewer_usage_5m tenant_id %q: %w", e.tenantID, tenantErr)
		}
		streamUUID, streamErr := uuid.Parse(e.streamID)
		if streamErr != nil {
			return fmt.Errorf("viewer_usage_5m stream_id %q: %w", e.streamID, streamErr)
		}
		if err := batch.Append(periscopeingestdb.ViewerUsage5mRow{
			WindowStart: time.UnixMilli(e.windowStartMS).UTC(),
			TenantID:    tenantUUID, ClusterID: e.clusterID, StreamID: streamUUID, NodeID: e.nodeID, SessionID: e.sessionID,
			SecondsObserved: e.secondsObserved, UpBytesObserved: e.upObserved, DownBytesObserved: e.downObserved,
			SourceEventID: e.sourceEventID, ProjectionVersionMS: projectionVersionMS,
		}); err != nil {
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

type viewerUsageIdentity struct {
	tenantID, nodeID, sessionID string
}

type viewerUsageTombstone struct {
	viewerUsageWindowKey
	sourceEventID string
}

func (h *AnalyticsHandler) viewerUsageTombstonesForProjectionWindow(ctx context.Context, windowStart, windowEnd time.Time, desiredByIdentity map[viewerUsageIdentity]map[viewerUsageWindowKey]struct{}) (map[viewerUsageIdentity][]viewerUsageTombstone, error) {
	out := make(map[viewerUsageIdentity][]viewerUsageTombstone)
	if len(desiredByIdentity) == 0 {
		return out, nil
	}
	rows, err := h.clickhouse.Query(ctx, `
		WITH changed_bounds AS (
			SELECT tenant_id, node_id, session_id,
				greatest(minIf(source_started_at_ms, source_started_at_ms > 0 AND source_ended_at_ms > source_started_at_ms),
					toUnixTimestamp64Milli(now64(3) - INTERVAL 90 DAY)) AS min_started_at_ms,
				if(maxIf(source_ended_at_ms, source_started_at_ms > 0 AND source_ended_at_ms > source_started_at_ms) > 0,
					maxIf(source_ended_at_ms, source_started_at_ms > 0 AND source_ended_at_ms > source_started_at_ms),
					toUnixTimestamp64Milli(now64(3))) AS max_ended_at_ms,
				countIf(source_started_at_ms <= 0 OR source_ended_at_ms <= source_started_at_ms) > 0 AS has_unbounded_retraction
			FROM periscope.viewer_sessions_final
			WHERE projection_version_ms >= ? AND projection_version_ms < ?
			GROUP BY tenant_id, node_id, session_id
		)
		SELECT toString(ledger.tenant_id), ledger.node_id, ledger.session_id,
			toInt64(toUnixTimestamp(ledger.window_start)) * 1000 AS window_start_ms,
			ledger.cluster_id, toString(ledger.stream_id),
			argMax(ledger.source_event_id, ledger.projection_version_ms) AS source_event_id,
			argMax(ledger.seconds_observed, ledger.projection_version_ms) AS seconds_observed,
			argMax(ledger.up_bytes_observed, ledger.projection_version_ms) AS up_bytes_observed,
			argMax(ledger.down_bytes_observed, ledger.projection_version_ms) AS down_bytes_observed
		FROM periscope.viewer_usage_5m AS ledger
		INNER JOIN changed_bounds AS changed
		  ON ledger.tenant_id = changed.tenant_id
		 AND ledger.node_id = changed.node_id
		 AND ledger.session_id = changed.session_id
		WHERE ledger.projection_version_ms >= toUnixTimestamp64Milli(now64(3) - INTERVAL 90 DAY)
		  AND (changed.has_unbounded_retraction
		    OR (ledger.window_start >= toDateTime(intDiv(changed.min_started_at_ms, 1000)) - INTERVAL 5 MINUTE
		    AND ledger.window_start < toDateTime(intDiv(changed.max_ended_at_ms, 1000)) + INTERVAL 5 MINUTE))
		GROUP BY ledger.tenant_id, ledger.node_id, ledger.session_id, ledger.window_start, ledger.cluster_id, ledger.stream_id`,
		windowStart.UnixMilli(), windowEnd.UnixMilli())
	if err != nil {
		return nil, fmt.Errorf("viewer_usage_5m batched tombstone lookup: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var (
			identity                 viewerUsageIdentity
			key                      viewerUsageWindowKey
			sourceEventID            string
			secondsObserved          uint32
			upObserved, downObserved uint64
		)
		if err := rows.Scan(&identity.tenantID, &identity.nodeID, &identity.sessionID, &key.windowStartMS, &key.clusterID, &key.streamID, &sourceEventID, &secondsObserved, &upObserved, &downObserved); err != nil {
			return nil, fmt.Errorf("viewer_usage_5m batched tombstone scan: %w", err)
		}
		desired, eligible := desiredByIdentity[identity]
		if !eligible || (secondsObserved == 0 && upObserved == 0 && downObserved == 0) {
			continue
		}
		if _, current := desired[key]; current {
			continue
		}
		out[identity] = append(out[identity], viewerUsageTombstone{viewerUsageWindowKey: key, sourceEventID: sourceEventID})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("viewer_usage_5m batched tombstone iterate: %w", err)
	}
	return out, nil
}

// --- stream_runtime_5m ---

func (h *AnalyticsHandler) rebuildStreamRuntime5m(ctx context.Context, windowStart, windowEnd time.Time) error {
	projectionVersionMS := time.Now().UnixMilli()
	batch, err := periscopeingestdb.PrepareStreamRuntime5m(ctx, h.clickhouse)
	if err != nil {
		return fmt.Errorf("stream_runtime_5m prepare: %w", err)
	}
	defer func() { _ = batch.Close() }()

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

func appendStreamRuntimeSpan(batch *periscopeingestdb.Writer[periscopeingestdb.StreamRuntime5mRow], tenantID, clusterID, streamID, sourceEventID string, startMS, endMS, peakViewers, projectionVersionMS int64) (int, error) {
	if startMS <= 0 || endMS <= startMS || clusterID == "" || streamID == "" {
		return 0, nil
	}
	tenantUUID, err := uuid.Parse(tenantID)
	if err != nil {
		return 0, fmt.Errorf("stream_runtime_5m tenant_id %q: %w", tenantID, err)
	}
	streamUUID, err := uuid.Parse(streamID)
	if err != nil {
		return 0, fmt.Errorf("stream_runtime_5m stream_id %q: %w", streamID, err)
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
		if err := batch.Append(periscopeingestdb.StreamRuntime5mRow{
			WindowStart: time.UnixMilli(windowMS).UTC(),
			TenantID:    tenantUUID, ClusterID: clusterID, StreamID: streamUUID,
			ActiveSeconds: activeSeconds, PeakViewers: pv,
			SourceEventID: sourceEventID, ProjectionVersionMS: projectionVersionMS,
		}); err != nil {
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
	batch, err := periscopeingestdb.PrepareStorageGBSeconds5m(ctx, h.clickhouse)
	if err != nil {
		return fmt.Errorf("storage_gb_seconds_5m prepare: %w", err)
	}
	defer func() { _ = batch.Close() }()

	rowsEmitted := 0
	for k, a := range state {
		tenantUUID, tenantErr := uuid.Parse(k.tenant)
		if tenantErr != nil {
			return fmt.Errorf("storage_gb_seconds_5m tenant_id %q: %w", k.tenant, tenantErr)
		}
		for w, gbs := range a.gbSecondsByWindow {
			if !a.closedByWindow[w] {
				continue
			}
			if err := h.storageProjectionDiverged(ctx, projectionVersionMS, time.UnixMilli(w).UTC(),
				k.tenant, k.cluster, k.scope, k.providerTenant, k.providerCluster, k.backend,
				gbs, a.fileCountByWindow[w]); err != nil {
				return err
			}
			if err := batch.Append(periscopeingestdb.StorageGBSeconds5mRow{
				WindowStart: time.UnixMilli(w).UTC(),
				TenantID:    tenantUUID, ClusterID: k.cluster, StorageScope: k.scope,
				StorageProviderTenantID: k.providerTenant, StorageProviderClusterID: k.providerCluster, StorageBackend: k.backend,
				GBSeconds: gbs, FileCount: a.fileCountByWindow[w], ProjectionVersionMS: projectionVersionMS,
			}); err != nil {
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

	record := func(priorGBSeconds float64, priorFileCount uint64, priorProjectionVersionMS int64) error {
		priorValue, marshalErr := json.Marshal(map[string]any{"gb_seconds": priorGBSeconds, "file_count": priorFileCount})
		if marshalErr != nil {
			return fmt.Errorf("storage_gb_seconds_5m divergence prior: %w", marshalErr)
		}
		newValue, marshalErr := json.Marshal(map[string]any{"gb_seconds": gbSeconds, "file_count": fileCount})
		if marshalErr != nil {
			return fmt.Errorf("storage_gb_seconds_5m divergence new: %w", marshalErr)
		}
		sourceEventID := fmt.Sprintf("storage_gb_seconds_5m:%s", string(naturalKey))
		if recordErr := h.recordProjectionDivergence(ctx, observedAtMS, priorProjectionVersionMS, "storage_gb_seconds_5m", "storage_gb_seconds_"+scope, "projection", string(naturalKey), string(priorValue), string(newValue), sourceEventID); recordErr != nil {
			return fmt.Errorf("record storage_gb_seconds_5m divergence: %w", recordErr)
		}
		return nil
	}

	rows, err := h.clickhouse.Query(ctx, `
		SELECT
			argMax(gb_seconds, projection_version_ms) AS gb_seconds,
			argMax(file_count, projection_version_ms) AS file_count,
			max(projection_version_ms) AS latest_projection_version_ms
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
	var priorProjectionVersionMS int64
	if err := rows.Scan(&priorGBSeconds, &priorFileCount, &priorProjectionVersionMS); err != nil {
		return fmt.Errorf("storage_gb_seconds_5m divergence scan: %w", err)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("storage_gb_seconds_5m divergence iterate: %w", err)
	}
	if math.Abs(priorGBSeconds-gbSeconds) <= 1e-9 && priorFileCount == fileCount {
		return nil
	}
	return record(priorGBSeconds, priorFileCount, priorProjectionVersionMS)
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
	batch, err := periscopeingestdb.PrepareProcessing5m(ctx, h.clickhouse)
	if err != nil {
		return fmt.Errorf("processing_5m prepare: %w", err)
	}
	defer func() { _ = batch.Close() }()

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
		tenantUUID, tenantErr := uuid.Parse(tenantID)
		if tenantErr != nil {
			return fmt.Errorf("processing_5m tenant_id %q: %w", tenantID, tenantErr)
		}
		streamUUID, streamErr := uuid.Parse(streamID)
		if streamErr != nil {
			return fmt.Errorf("processing_5m stream_id %q: %w", streamID, streamErr)
		}
		if err := batch.Append(periscopeingestdb.Processing5mRow{
			WindowStart: windowStartT,
			TenantID:    tenantUUID, ClusterID: clusterID, StreamID: streamUUID, ProcessType: processType,
			OutputCodec: outputCodec, TrackType: trackType, SourceEventID: sourceEventID,
			MediaSeconds: rawDurationSeconds, ProjectionVersionMS: projectionVersionMS,
		}); err != nil {
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
			(window_start, tenant_id, auth_type, operation_type, operation_name, service,
			 llm_model, llm_provider, requests, errors, duration_ms, complexity,
			 llm_input_tokens, llm_output_tokens, unique_users_state, unique_tokens_state,
			 projection_version_ms, root_fields)
		WITH affected_windows AS (
			SELECT DISTINCT tenant_id, toStartOfFiveMinute(timestamp) AS window_start
			FROM periscope.api_requests
			WHERE ingested_at_ms >= ? AND ingested_at_ms < ?
		), dedup AS (
			SELECT tenant_id, source_event_id,
				argMax(timestamp, ingested_at_ms) AS source_timestamp,
				argMax(auth_type, ingested_at_ms) AS auth_type,
				argMax(operation_type, ingested_at_ms) AS operation_type,
				argMax(operation_name, ingested_at_ms) AS operation_name,
				argMax(source_node, ingested_at_ms) AS source_node,
				argMax(llm_model, ingested_at_ms) AS llm_model,
				argMax(llm_provider, ingested_at_ms) AS llm_provider,
				argMax(request_count, ingested_at_ms) AS request_count,
				argMax(error_count, ingested_at_ms) AS error_count,
				argMax(total_duration_ms, ingested_at_ms) AS total_duration_ms,
				argMax(total_complexity, ingested_at_ms) AS total_complexity,
				argMax(llm_input_tokens, ingested_at_ms) AS llm_input_tokens,
				argMax(llm_output_tokens, ingested_at_ms) AS llm_output_tokens,
				argMax(user_hashes, ingested_at_ms) AS user_hashes,
				argMax(token_hashes, ingested_at_ms) AS token_hashes,
				-- The first stored copy fixes a source event's signature, so a later copy
				-- carrying root fields (a replay across the upgrade) cannot move counts out
				-- of a signature an earlier projection holds; api_usage_5m_v keys on it.
				argMin(root_fields, ingested_at_ms) AS root_fields
			FROM periscope.api_requests
			WHERE (tenant_id, toStartOfFiveMinute(timestamp)) IN affected_windows
			GROUP BY tenant_id, source_event_id
		)
		SELECT
			toStartOfFiveMinute(source_timestamp)       AS window_start,
			tenant_id,
			auth_type,
			operation_type,
			ifNull(operation_name, '')                  AS operation_name,
			ifNull(nullIf(source_node, ''), 'bridge')     AS service,
			llm_model,
			llm_provider,
			sum(request_count)                           AS requests,
			sum(error_count)                             AS errors,
			sum(total_duration_ms)                       AS duration_ms,
			sum(total_complexity)                        AS complexity,
			sum(llm_input_tokens)                        AS llm_input_tokens,
			sum(llm_output_tokens)                       AS llm_output_tokens,
			uniqCombinedArrayState(user_hashes)          AS unique_users_state,
			uniqCombinedArrayState(token_hashes)         AS unique_tokens_state,
			?                                            AS projection_version_ms,
			root_fields
		FROM dedup
		GROUP BY window_start, tenant_id, auth_type, operation_type, operation_name,
		         service, llm_model, llm_provider, root_fields`,
		windowStart.UnixMilli(), windowEnd.UnixMilli(), projectionVersionMS); err != nil {
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
	if endMS <= startMS || endMS-startMS > LedgerMaxSpan.Milliseconds() {
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

type windowOverlap struct {
	windowStartMS int64
	overlapMS     int64
}

func orderedWindowsForSpan(startMS, endMS int64) []windowOverlap {
	windows := windowsForSpan(startMS, endMS)
	starts := make([]int64, 0, len(windows))
	for windowStartMS := range windows {
		starts = append(starts, windowStartMS)
	}
	sort.Slice(starts, func(i, j int) bool { return starts[i] < starts[j] })
	out := make([]windowOverlap, 0, len(starts))
	for _, windowStartMS := range starts {
		out = append(out, windowOverlap{windowStartMS: windowStartMS, overlapMS: windows[windowStartMS]})
	}
	return out
}

func mulDivUint64(value, numerator, denominator uint64) uint64 {
	if value == 0 || numerator == 0 || denominator == 0 {
		return 0
	}
	hi, lo := bits.Mul64(value, numerator)
	quotient, _ := bits.Div64(hi, lo, denominator)
	return quotient
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

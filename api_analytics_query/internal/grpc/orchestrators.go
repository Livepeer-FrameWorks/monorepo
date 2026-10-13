package grpc

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"frameworks/api_analytics_query/internal/database/periscopequerydb"
	periscopepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/periscope"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// ListOrchestrators returns identity-level rows ((tenant, orch_addr,
// last_seen)). Per-instance config (price/capabilities/hardware) lives on
// orchestrator_instance_state_current and is fetched by ListOrchestratorInstances
// or GetOrchestrator. Splitting these reflects the underlying reality that
// one orch eth address can front N independently-configured instances behind
// a load-balanced DNS hostname.
func (s *PeriscopeServer) ListOrchestrators(ctx context.Context, req *periscopepb.ListOrchestratorsRequest) (*periscopepb.ListOrchestratorsResponse, error) {
	tenantID, err := requireTenantID(ctx, req.GetTenantId())
	if err != nil {
		return nil, err
	}

	params, err := getCursorPagination(req.GetPagination())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid pagination: %v", err)
	}

	query := `
		SELECT tenant_id, orch_addr, last_seen, updated_at
		FROM periscope.orchestrator_state_current FINAL
		WHERE tenant_id = ?
	`
	args := []any{tenantID}

	if orchAddr := strings.TrimSpace(req.GetOrchAddr()); orchAddr != "" {
		query += " AND orch_addr = ?"
		args = append(args, orchAddr)
	}

	query += " ORDER BY orch_addr ASC"
	limit := params.Limit
	if limit <= 0 {
		limit = 200
	}
	query += fmt.Sprintf(" LIMIT %d", limit)

	rows, err := periscopequerydb.Query(ctx, s.clickhouse, query, args...)
	if err != nil {
		return nil, wrapClickhouseError(err, "database error")
	}
	defer func() { _ = rows.Close() }()

	var orchestrators []*periscopepb.Orchestrator
	for rows.Next() {
		var o periscopepb.Orchestrator
		var lastSeen, updatedAt time.Time
		if scanErr := rows.Scan(&o.TenantId, &o.OrchAddr, &lastSeen, &updatedAt); scanErr != nil {
			s.logger.WithError(scanErr).Warn("Failed to scan orchestrator_state_current row")
			continue
		}
		o.LastSeen = timestamppb.New(lastSeen)
		o.UpdatedAt = timestamppb.New(updatedAt)
		orchestrators = append(orchestrators, &o)
	}

	return &periscopepb.ListOrchestratorsResponse{
		Orchestrators: orchestrators,
	}, nil
}

// GetOrchestrator returns one orchestrator's identity row plus every known
// instance and vantage for it. Side-panel data source.
func (s *PeriscopeServer) GetOrchestrator(ctx context.Context, req *periscopepb.GetOrchestratorRequest) (*periscopepb.GetOrchestratorResponse, error) {
	tenantID, err := requireTenantID(ctx, req.GetTenantId())
	if err != nil {
		return nil, err
	}
	orchAddr := strings.TrimSpace(req.GetOrchAddr())
	if orchAddr == "" {
		return nil, status.Error(codes.InvalidArgument, "orch_addr required")
	}

	stateRows, err := periscopequerydb.Query(ctx, s.clickhouse, `
		SELECT tenant_id, orch_addr, last_seen, updated_at
		FROM periscope.orchestrator_state_current FINAL
		WHERE tenant_id = ? AND orch_addr = ?
		LIMIT 1
	`, tenantID, orchAddr)
	if err != nil {
		return nil, wrapClickhouseError(err, "database error")
	}
	defer func() { _ = stateRows.Close() }()

	var orch *periscopepb.Orchestrator
	if stateRows.Next() {
		var o periscopepb.Orchestrator
		var lastSeen, updatedAt time.Time
		if scanErr := stateRows.Scan(&o.TenantId, &o.OrchAddr, &lastSeen, &updatedAt); scanErr != nil {
			return nil, wrapClickhouseError(scanErr, "scan orchestrator state")
		}
		o.LastSeen = timestamppb.New(lastSeen)
		o.UpdatedAt = timestamppb.New(updatedAt)
		orch = &o
	}
	if orch == nil {
		return nil, status.Errorf(codes.NotFound, "orchestrator %q not found for tenant", orchAddr)
	}

	instances, err := s.queryOrchestratorInstances(ctx, tenantID, orchAddr)
	if err != nil {
		return nil, err
	}
	vantages, err := s.queryOrchestratorVantages(ctx, tenantID, orchAddr)
	if err != nil {
		return nil, err
	}

	return &periscopepb.GetOrchestratorResponse{
		Orchestrator: orch,
		Instances:    instances,
		Vantages:     vantages,
	}, nil
}

// ListOrchestratorInstances returns per-instance rows for the tenant
// (optionally filtered to one orch). Each row carries that instance's own
// price/capabilities/hardware. These are usually consistent within an orch's
// pool but not guaranteed.
func (s *PeriscopeServer) ListOrchestratorInstances(ctx context.Context, req *periscopepb.ListOrchestratorInstancesRequest) (*periscopepb.ListOrchestratorInstancesResponse, error) {
	tenantID, err := requireTenantID(ctx, req.GetTenantId())
	if err != nil {
		return nil, err
	}
	instances, err := s.queryOrchestratorInstances(ctx, tenantID, strings.TrimSpace(req.GetOrchAddr()))
	if err != nil {
		return nil, err
	}
	return &periscopepb.ListOrchestratorInstancesResponse{Instances: instances}, nil
}

// ListOrchestratorVantages returns every per-(gateway, instance) row for
// the tenant. Federation map calls this without a filter to render every
// observation; side panel calls with a filter for the per-region table.
func (s *PeriscopeServer) ListOrchestratorVantages(ctx context.Context, req *periscopepb.ListOrchestratorVantagesRequest) (*periscopepb.ListOrchestratorVantagesResponse, error) {
	tenantID, err := requireTenantID(ctx, req.GetTenantId())
	if err != nil {
		return nil, err
	}

	vantages, err := s.queryOrchestratorVantages(ctx, tenantID, strings.TrimSpace(req.GetOrchAddr()))
	if err != nil {
		return nil, err
	}
	return &periscopepb.ListOrchestratorVantagesResponse{Vantages: vantages}, nil
}

type orchestratorPerformanceKey struct {
	ts            time.Time
	gatewayID     string
	gatewayRegion string
	resolvedIP    string
}

// performanceTotals carries additive sums so a bucket that both the legacy
// tables and network_observations contribute to keeps an exact weighted mean.
type performanceTotals struct {
	attempts, successes, failures uint64
	latencySum, latencyCount      uint64
	maxLatency                    uint32
	pixels                        uint64
}

func (t *performanceTotals) add(o performanceTotals) {
	t.attempts += o.attempts
	t.successes += o.successes
	t.failures += o.failures
	t.latencySum += o.latencySum
	t.latencyCount += o.latencyCount
	if o.maxLatency > t.maxLatency {
		t.maxLatency = o.maxLatency
	}
	t.pixels += o.pixels
}

func (t performanceTotals) mean() float32 {
	if t.latencyCount == 0 {
		return 0
	}
	return float32(t.latencySum) / float32(t.latencyCount)
}

type performanceBucket struct {
	discovery, transcode, ai performanceTotals
}

type performanceSeries map[orchestratorPerformanceKey]*performanceBucket

func (p performanceSeries) bucket(key orchestratorPerformanceKey) *performanceBucket {
	if b := p[key]; b != nil {
		return b
	}
	b := &performanceBucket{}
	p[key] = b
	return b
}

func (p performanceSeries) points() []*periscopepb.OrchestratorPerformancePoint {
	keys := make([]orchestratorPerformanceKey, 0, len(p))
	for key := range p {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		if !keys[i].ts.Equal(keys[j].ts) {
			return keys[i].ts.Before(keys[j].ts)
		}
		if keys[i].gatewayID != keys[j].gatewayID {
			return keys[i].gatewayID < keys[j].gatewayID
		}
		if keys[i].gatewayRegion != keys[j].gatewayRegion {
			return keys[i].gatewayRegion < keys[j].gatewayRegion
		}
		return keys[i].resolvedIP < keys[j].resolvedIP
	})
	points := make([]*periscopepb.OrchestratorPerformancePoint, 0, len(keys))
	for _, key := range keys {
		b := p[key]
		points = append(points, &periscopepb.OrchestratorPerformancePoint{
			Timestamp:              timestamppb.New(key.ts),
			GatewayId:              key.gatewayID,
			GatewayRegion:          key.gatewayRegion,
			ResolvedIp:             key.resolvedIP,
			Attempts:               b.discovery.attempts,
			Successes:              b.discovery.successes,
			Failures:               b.discovery.failures,
			MeanLatencyMs:          b.discovery.mean(),
			MaxLatencyMs:           b.discovery.maxLatency,
			TranscodeAttempts:      b.transcode.attempts,
			TranscodeSuccesses:     b.transcode.successes,
			TranscodeFailures:      b.transcode.failures,
			TranscodeMeanOverallMs: b.transcode.mean(),
			TranscodeMaxOverallMs:  b.transcode.maxLatency,
			TranscodePixels:        b.transcode.pixels,
			AiAttempts:             b.ai.attempts,
			AiSuccesses:            b.ai.successes,
			AiFailures:             b.ai.failures,
			AiMeanLatencyMs:        b.ai.mean(),
			AiMaxLatencyMs:         b.ai.maxLatency,
		})
	}
	return points
}

type performanceFilter struct {
	tenantID, orchAddr    string
	start, end            time.Time
	interval              string
	gatewayID, resolvedIP string
}

// GetOrchestratorPerformanceSeries returns per-(bucket, gateway, instance IP)
// discovery, transcode and AI performance for one orchestrator. Gateways that
// send schema-3 telemetry land only in periscope.network_observations, and
// earlier gateways only in the legacy orchestrator_* tables, so the series sums
// both sources without counting an event twice.
func (s *PeriscopeServer) GetOrchestratorPerformanceSeries(ctx context.Context, req *periscopepb.GetOrchestratorPerformanceSeriesRequest) (*periscopepb.GetOrchestratorPerformanceSeriesResponse, error) {
	tenantID, err := requireTenantID(ctx, req.GetTenantId())
	if err != nil {
		return nil, err
	}
	orchAddr := strings.TrimSpace(req.GetOrchAddr())
	if orchAddr == "" {
		return nil, status.Error(codes.InvalidArgument, "orch_addr required")
	}
	startTime, endTime, err := validateTimeRangeProto(req.GetTimeRange())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid time range: %v", err)
	}
	interval := strings.TrimSpace(req.GetInterval())
	switch interval {
	case "":
		interval = "5m"
	case "5m", "1h":
	default:
		return nil, status.Errorf(codes.InvalidArgument, "interval must be '5m' or '1h', got %q", interval)
	}
	filter := performanceFilter{
		tenantID: tenantID, orchAddr: orchAddr, start: startTime, end: endTime, interval: interval,
		gatewayID: strings.TrimSpace(req.GetGatewayId()), resolvedIP: strings.TrimSpace(req.GetResolvedIp()),
	}

	series := performanceSeries{}
	if err := s.mergeLegacyDiscovery(ctx, series, filter); err != nil {
		return nil, err
	}
	if err := s.mergeLegacyTranscodeOutcomes(ctx, series, filter); err != nil {
		return nil, err
	}
	if err := s.mergeLegacyAIOutcomes(ctx, series, filter); err != nil {
		return nil, err
	}
	if err := s.mergeNetworkObservations(ctx, series, filter); err != nil {
		return nil, err
	}
	return &periscopepb.GetOrchestratorPerformanceSeriesResponse{Points: series.points()}, nil
}

func (f performanceFilter) drillDown(query string, args []any) (string, []any) {
	if f.gatewayID != "" {
		query += " AND gateway_id = ?"
		args = append(args, f.gatewayID)
	}
	if f.resolvedIP != "" {
		query += " AND resolved_ip = ?"
		args = append(args, f.resolvedIP)
	}
	return query, args
}

const performanceGroupBy = " GROUP BY ts, gateway_id, gateway_region, resolved_ip"

// scanPerformance reads rows shaped (ts, gateway_id, gateway_region,
// resolved_ip, attempts, successes, failures, latency_sum, latency_count,
// max_latency[, pixels]) and adds each into the totals target selects.
func (s *PeriscopeServer) scanPerformance(ctx context.Context, series performanceSeries, label string, withPixels bool, target func(*performanceBucket) *performanceTotals, query string, args ...any) error {
	rows, err := periscopequerydb.Query(ctx, s.clickhouse, query+performanceGroupBy, args...)
	if err != nil {
		return wrapClickhouseError(err, "database error")
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var (
			key    orchestratorPerformanceKey
			totals performanceTotals
		)
		dest := []any{&key.ts, &key.gatewayID, &key.gatewayRegion, &key.resolvedIP, &totals.attempts, &totals.successes, &totals.failures, &totals.latencySum, &totals.latencyCount, &totals.maxLatency}
		if withPixels {
			dest = append(dest, &totals.pixels)
		}
		if scanErr := rows.Scan(dest...); scanErr != nil {
			s.logger.WithError(scanErr).Warnf("Failed to scan orchestrator %s performance row", label)
			continue
		}
		target(series.bucket(key)).add(totals)
	}
	if err := rows.Err(); err != nil {
		return wrapClickhouseError(err, "database error")
	}
	return nil
}

func (s *PeriscopeServer) mergeLegacyDiscovery(ctx context.Context, series performanceSeries, f performanceFilter) error {
	table, tsCol := "periscope.orchestrator_discovery_5m", "timestamp_5m"
	if f.interval == "1h" {
		table, tsCol = "periscope.orchestrator_discovery_1h", "timestamp_1h"
	}
	query, args := f.drillDown(fmt.Sprintf(`
		SELECT %s AS ts, gateway_id, gateway_region, resolved_ip,
		       sum(attempts), sum(successes), sum(failures),
		       sum(latency_sum), sum(latency_count), max(max_latency)
		FROM %s
		WHERE tenant_id = ? AND orch_addr = ?
		  AND %s >= ? AND %s <= ?`, tsCol, table, tsCol, tsCol),
		[]any{f.tenantID, f.orchAddr, f.start, f.end})
	return s.scanPerformance(ctx, series, "discovery", false, func(b *performanceBucket) *performanceTotals { return &b.discovery }, query, args...)
}

// Legacy outcome rows carry the stream tenant in tenant_id; the series is
// scoped to the gateway cluster's owner, which they carry separately.
func (s *PeriscopeServer) mergeLegacyTranscodeOutcomes(ctx context.Context, series performanceSeries, f performanceFilter) error {
	query := `
		SELECT toStartOfInterval(timestamp, INTERVAL 5 MINUTE) AS ts, gateway_id, gateway_region, resolved_ip,
		       count(), sumIf(1, success = 1), sumIf(1, success = 0),
		       sumIf(toUInt64(overall_ms), success = 1), sumIf(1, success = 1),
		       maxIf(overall_ms, success = 1), sumIf(pixels, success = 1)
		FROM periscope.orchestrator_transcode_outcomes
		WHERE cluster_owner_tenant_id = ? AND orch_addr = ?
		  AND timestamp >= ? AND timestamp <= ?`
	if f.interval == "1h" {
		query = `
		SELECT timestamp_1h AS ts, gateway_id, gateway_region, resolved_ip,
		       sum(attempts), sum(successes), sum(failures),
		       sum(overall_ms_sum), sum(overall_ms_count),
		       max(max_overall_ms), sum(pixels_sum)
		FROM periscope.orchestrator_transcode_hourly
		WHERE cluster_owner_tenant_id = ? AND orch_addr = ?
		  AND timestamp_1h >= ? AND timestamp_1h <= ?`
	}
	query, args := f.drillDown(query, []any{f.tenantID, f.orchAddr, f.start, f.end})
	return s.scanPerformance(ctx, series, "transcode", true, func(b *performanceBucket) *performanceTotals { return &b.transcode }, query, args...)
}

func (s *PeriscopeServer) mergeLegacyAIOutcomes(ctx context.Context, series performanceSeries, f performanceFilter) error {
	query := `
		SELECT toStartOfInterval(timestamp, INTERVAL 5 MINUTE) AS ts, gateway_id, gateway_region, resolved_ip,
		       count(), sumIf(1, success = 1), sumIf(1, success = 0),
		       sumIf(toUInt64(latency_ms), success = 1), sumIf(1, success = 1),
		       maxIf(latency_ms, success = 1)
		FROM periscope.orchestrator_ai_outcomes
		WHERE cluster_owner_tenant_id = ? AND orch_addr = ?
		  AND timestamp >= ? AND timestamp <= ?`
	if f.interval == "1h" {
		query = `
		SELECT timestamp_1h AS ts, gateway_id, gateway_region, resolved_ip,
		       sum(attempts), sum(successes), sum(failures),
		       sum(latency_ms_sum), sum(latency_ms_count), max(max_latency_ms)
		FROM periscope.orchestrator_ai_hourly
		WHERE cluster_owner_tenant_id = ? AND orch_addr = ?
		  AND timestamp_1h >= ? AND timestamp_1h <= ?`
	}
	query, args := f.drillDown(query, []any{f.tenantID, f.orchAddr, f.start, f.end})
	return s.scanPerformance(ctx, series, "AI", false, func(b *performanceBucket) *performanceTotals { return &b.ai }, query, args...)
}

// mergeNetworkObservations adds the gateway's schema-3 discovery and dispatched
// transcode attempts with the public projection's counting: every
// deduplicated row is a sample, and successes and failures count qualifying
// rows only. Prober rows are excluded; their observer identities are private
// and the series is keyed by gateway. Raw observations are retained 30 days.
func (s *PeriscopeServer) mergeNetworkObservations(ctx context.Context, series performanceSeries, f performanceFilter) error {
	seconds := 300
	if f.interval == "1h" {
		seconds = 3600
	}
	query, args := f.drillDown(fmt.Sprintf(`
		SELECT toStartOfInterval(timestamp, INTERVAL %d SECOND) AS ts, gateway_id, gateway_region, resolved_ip, kind,
		       count(), countIf(success = 1 AND qualifying = 1), countIf(success = 0 AND qualifying = 1),
		       sumIf(toUInt64(latency_ms), success = 1 AND qualifying = 1), countIf(success = 1 AND qualifying = 1),
		       maxIf(latency_ms, success = 1 AND qualifying = 1), sumIf(returned_pixels, success = 1 AND returned_pixels_known = 1)
		FROM periscope.network_observations FINAL
		WHERE tenant_id = ? AND provider = 'livepeer' AND producer_kind = 'gateway'
		  AND kind IN ('discovery', 'attempt') AND resource_id = lower(?)
		  AND timestamp >= ? AND timestamp <= ?`, seconds),
		[]any{f.tenantID, f.orchAddr, f.start, f.end})
	rows, err := periscopequerydb.Query(ctx, s.clickhouse, query+performanceGroupBy+", kind", args...)
	if err != nil {
		return wrapClickhouseError(err, "database error")
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var (
			key    orchestratorPerformanceKey
			kind   string
			totals performanceTotals
		)
		if scanErr := rows.Scan(&key.ts, &key.gatewayID, &key.gatewayRegion, &key.resolvedIP, &kind, &totals.attempts, &totals.successes, &totals.failures, &totals.latencySum, &totals.latencyCount, &totals.maxLatency, &totals.pixels); scanErr != nil {
			s.logger.WithError(scanErr).Warn("Failed to scan network observation performance row")
			continue
		}
		if b := series.bucket(key); kind == "discovery" {
			b.discovery.add(totals)
		} else {
			b.transcode.add(totals)
		}
	}
	if err := rows.Err(); err != nil {
		return wrapClickhouseError(err, "database error")
	}
	return nil
}

// queryOrchestratorInstances reads orchestrator_instance_state_current rows
// for a tenant, optionally filtered to one orch. Used by both
// GetOrchestrator and ListOrchestratorInstances so per-instance reads have
// one query path.
func (s *PeriscopeServer) queryOrchestratorInstances(ctx context.Context, tenantID, orchAddr string) ([]*periscopepb.OrchestratorInstance, error) {
	query := `
		SELECT tenant_id, orch_addr, resolved_ip,
		       canonical_url, advertised_node_urls, capabilities,
		       price_per_unit, pixels_per_unit,
		       capability_price_capabilities, capability_price_positions,
		       capability_price_price_per_units, capability_price_pixels_per_units,
		       hardware, source,
		       last_seen, updated_at
		FROM periscope.orchestrator_instance_state_current FINAL
		WHERE tenant_id = ?
	`
	args := []any{tenantID}
	if orchAddr != "" {
		query += " AND orch_addr = ?"
		args = append(args, orchAddr)
	}
	query += " ORDER BY orch_addr ASC, resolved_ip ASC"

	rows, err := periscopequerydb.Query(ctx, s.clickhouse, query, args...)
	if err != nil {
		return nil, wrapClickhouseError(err, "database error")
	}
	defer func() { _ = rows.Close() }()

	var out []*periscopepb.OrchestratorInstance
	for rows.Next() {
		var inst periscopepb.OrchestratorInstance
		var lastSeen, updatedAt time.Time
		var (
			priceCapabilities []string
			pricePositions    []uint32
			pricePerUnits     []int64
			pixelsPerUnits    []int64
		)
		if scanErr := rows.Scan(
			&inst.TenantId, &inst.OrchAddr, &inst.ResolvedIp,
			&inst.CanonicalUrl, &inst.AdvertisedNodeUrls, &inst.Capabilities,
			&inst.PricePerUnit, &inst.PixelsPerUnit,
			&priceCapabilities, &pricePositions, &pricePerUnits, &pixelsPerUnits,
			&inst.Hardware, &inst.Source,
			&lastSeen, &updatedAt,
		); scanErr != nil {
			s.logger.WithError(scanErr).Warn("Failed to scan orchestrator_instance_state_current row")
			continue
		}
		inst.CapabilityPrices = capabilityPricesFromArrays(priceCapabilities, pricePositions, pricePerUnits, pixelsPerUnits)
		inst.LastSeen = timestamppb.New(lastSeen)
		inst.UpdatedAt = timestamppb.New(updatedAt)
		out = append(out, &inst)
	}
	return out, nil
}

func capabilityPricesFromArrays(capabilities []string, positions []uint32, pricePerUnits []int64, pixelsPerUnits []int64) []*periscopepb.OrchestratorCapabilityPrice {
	count := len(capabilities)
	if len(positions) < count {
		count = len(positions)
	}
	if len(pricePerUnits) < count {
		count = len(pricePerUnits)
	}
	if len(pixelsPerUnits) < count {
		count = len(pixelsPerUnits)
	}
	out := make([]*periscopepb.OrchestratorCapabilityPrice, 0, count)
	for i := 0; i < count; i++ {
		out = append(out, &periscopepb.OrchestratorCapabilityPrice{
			Capability:    capabilities[i],
			Position:      positions[i],
			PricePerUnit:  pricePerUnits[i],
			PixelsPerUnit: pixelsPerUnits[i],
		})
	}
	return out
}

// queryOrchestratorVantages reads orchestrator_vantage_current rows for a
// tenant, optionally filtered to one orch.
func (s *PeriscopeServer) queryOrchestratorVantages(ctx context.Context, tenantID, orchAddr string) ([]*periscopepb.OrchestratorVantage, error) {
	query, args := buildOrchestratorVantagesQuery(tenantID, orchAddr)

	rows, err := periscopequerydb.Query(ctx, s.clickhouse, query, args...)
	if err != nil {
		return nil, wrapClickhouseError(err, "database error")
	}
	defer func() { _ = rows.Close() }()

	var out []*periscopepb.OrchestratorVantage
	for rows.Next() {
		var v periscopepb.OrchestratorVantage
		var geoResolvedAt, lastSeen time.Time
		var dialedRecently uint8
		if scanErr := rows.Scan(
			&v.TenantId, &v.GatewayId, &v.GatewayRegion, &v.OrchAddr, &v.ResolvedIp,
			&v.Latitude, &v.Longitude, &v.City, &v.CountryCode, &v.GeoSource, &geoResolvedAt,
			&v.LatestLatencyMs, &v.Score, &dialedRecently, &lastSeen,
		); scanErr != nil {
			s.logger.WithError(scanErr).Warn("Failed to scan orchestrator_vantage_current row")
			continue
		}
		v.DialedRecently = dialedRecently == 1
		// An unresolved vantage reads back ClickHouse's zero DateTime, the Unix epoch.
		if geoResolvedAt.Unix() > 0 {
			v.GeoResolvedAt = timestamppb.New(geoResolvedAt)
		}
		v.LastSeen = timestamppb.New(lastSeen)
		out = append(out, &v)
	}
	return out, nil
}

func buildOrchestratorVantagesQuery(tenantID, orchAddr string) (string, []any) {
	latestWhere := "tenant_id = ?"
	geoWhere := "tenant_id = ? AND geo_source != 'unknown' AND (latitude != 0 OR longitude != 0)"
	args := []any{tenantID, tenantID}
	if orchAddr != "" {
		latestWhere += " AND orch_addr = ?"
		geoWhere += " AND orch_addr = ?"
		args = []any{tenantID, orchAddr, tenantID, orchAddr}
	}

	query := fmt.Sprintf(`
		WITH latest AS (
			SELECT tenant_id, gateway_id, gateway_region, orch_addr, resolved_ip,
			       latitude, longitude, city, country_code, geo_source, geo_resolved_at,
			       latest_latency_ms, score, dialed_recently, last_seen
		FROM periscope.orchestrator_vantage_current FINAL
		WHERE %s
	),
	geo AS (
		SELECT tenant_id, gateway_id, orch_addr, resolved_ip,
		       argMax(latitude, timestamp) AS geo_latitude,
		       argMax(longitude, timestamp) AS geo_longitude,
		       argMax(country_code, timestamp) AS geo_country_code,
		       argMax(geo_source, timestamp) AS latest_geo_source,
		       max(timestamp) AS latest_geo_resolved_at
		FROM periscope.orchestrator_discovery_samples
		WHERE %s
		GROUP BY tenant_id, gateway_id, orch_addr, resolved_ip
	)
	SELECT latest.tenant_id, latest.gateway_id, latest.gateway_region, latest.orch_addr, latest.resolved_ip,
	       coalesce(geo.geo_latitude, latest.latitude) AS latitude,
	       coalesce(geo.geo_longitude, latest.longitude) AS longitude,
	       latest.city,
	       coalesce(geo.geo_country_code, latest.country_code) AS country_code,
	       coalesce(geo.latest_geo_source, latest.geo_source) AS geo_source,
	       coalesce(geo.latest_geo_resolved_at, latest.geo_resolved_at) AS geo_resolved_at,
	       latest.latest_latency_ms, latest.score, latest.dialed_recently, latest.last_seen
		FROM latest
		LEFT JOIN geo
		  ON geo.tenant_id = latest.tenant_id
		 AND geo.gateway_id = latest.gateway_id
		 AND geo.orch_addr = latest.orch_addr
		 AND geo.resolved_ip = latest.resolved_ip
		ORDER BY latest.orch_addr ASC, latest.gateway_id ASC, latest.resolved_ip ASC`,
		latestWhere,
		geoWhere,
	)
	return query, args
}

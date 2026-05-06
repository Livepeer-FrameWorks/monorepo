package grpc

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type orchestratorPerformanceKey struct {
	ts            time.Time
	gatewayID     string
	gatewayRegion string
	resolvedIP    string
}

// ListOrchestrators returns identity-level rows ((tenant, orch_addr,
// last_seen)). Per-instance config (price/capabilities/hardware) lives on
// orchestrator_instance_state_current and is fetched by ListOrchestratorInstances
// or GetOrchestrator. Splitting these reflects the underlying reality that
// one orch eth address can front N independently-configured instances behind
// a load-balanced DNS hostname.
func (s *PeriscopeServer) ListOrchestrators(ctx context.Context, req *pb.ListOrchestratorsRequest) (*pb.ListOrchestratorsResponse, error) {
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

	rows, err := s.clickhouse.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, wrapClickhouseError(err, "database error")
	}
	defer func() { _ = rows.Close() }()

	var orchestrators []*pb.Orchestrator
	for rows.Next() {
		var o pb.Orchestrator
		var lastSeen, updatedAt time.Time
		if scanErr := rows.Scan(&o.TenantId, &o.OrchAddr, &lastSeen, &updatedAt); scanErr != nil {
			s.logger.WithError(scanErr).Warn("Failed to scan orchestrator_state_current row")
			continue
		}
		o.LastSeen = timestamppb.New(lastSeen)
		o.UpdatedAt = timestamppb.New(updatedAt)
		orchestrators = append(orchestrators, &o)
	}

	return &pb.ListOrchestratorsResponse{
		Orchestrators: orchestrators,
	}, nil
}

// GetOrchestrator returns one orchestrator's identity row plus every known
// instance and vantage for it. Side-panel data source.
func (s *PeriscopeServer) GetOrchestrator(ctx context.Context, req *pb.GetOrchestratorRequest) (*pb.GetOrchestratorResponse, error) {
	tenantID, err := requireTenantID(ctx, req.GetTenantId())
	if err != nil {
		return nil, err
	}
	orchAddr := strings.TrimSpace(req.GetOrchAddr())
	if orchAddr == "" {
		return nil, status.Error(codes.InvalidArgument, "orch_addr required")
	}

	stateRows, err := s.clickhouse.QueryContext(ctx, `
		SELECT tenant_id, orch_addr, last_seen, updated_at
		FROM periscope.orchestrator_state_current FINAL
		WHERE tenant_id = ? AND orch_addr = ?
		LIMIT 1
	`, tenantID, orchAddr)
	if err != nil {
		return nil, wrapClickhouseError(err, "database error")
	}

	var orch *pb.Orchestrator
	if stateRows.Next() {
		var o pb.Orchestrator
		var lastSeen, updatedAt time.Time
		if scanErr := stateRows.Scan(&o.TenantId, &o.OrchAddr, &lastSeen, &updatedAt); scanErr != nil {
			_ = stateRows.Close()
			return nil, wrapClickhouseError(scanErr, "scan orchestrator state")
		}
		o.LastSeen = timestamppb.New(lastSeen)
		o.UpdatedAt = timestamppb.New(updatedAt)
		orch = &o
	}
	_ = stateRows.Close()
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

	return &pb.GetOrchestratorResponse{
		Orchestrator: orch,
		Instances:    instances,
		Vantages:     vantages,
	}, nil
}

// ListOrchestratorInstances returns per-instance rows for the tenant
// (optionally filtered to one orch). Each row carries that instance's own
// price/capabilities/hardware. These are usually consistent within an orch's
// pool but not guaranteed.
func (s *PeriscopeServer) ListOrchestratorInstances(ctx context.Context, req *pb.ListOrchestratorInstancesRequest) (*pb.ListOrchestratorInstancesResponse, error) {
	tenantID, err := requireTenantID(ctx, req.GetTenantId())
	if err != nil {
		return nil, err
	}
	instances, err := s.queryOrchestratorInstances(ctx, tenantID, strings.TrimSpace(req.GetOrchAddr()))
	if err != nil {
		return nil, err
	}
	return &pb.ListOrchestratorInstancesResponse{Instances: instances}, nil
}

// ListOrchestratorVantages returns every per-(gateway, instance) row for
// the tenant. Federation map calls this without a filter to render every
// observation; side panel calls with a filter for the per-region table.
func (s *PeriscopeServer) ListOrchestratorVantages(ctx context.Context, req *pb.ListOrchestratorVantagesRequest) (*pb.ListOrchestratorVantagesResponse, error) {
	tenantID, err := requireTenantID(ctx, req.GetTenantId())
	if err != nil {
		return nil, err
	}

	vantages, err := s.queryOrchestratorVantages(ctx, tenantID, strings.TrimSpace(req.GetOrchAddr()))
	if err != nil {
		return nil, err
	}
	return &pb.ListOrchestratorVantagesResponse{Vantages: vantages}, nil
}

// GetOrchestratorPerformanceSeries combines discovery reachability with
// transcode/AI outcome rollups by gateway and resolved orchestrator instance IP.
func (s *PeriscopeServer) GetOrchestratorPerformanceSeries(ctx context.Context, req *pb.GetOrchestratorPerformanceSeriesRequest) (*pb.GetOrchestratorPerformanceSeriesResponse, error) {
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
	var (
		table string
		tsCol string
	)
	switch interval {
	case "1h":
		table, tsCol = "periscope.orchestrator_discovery_1h", "timestamp_1h"
	case "", "5m":
		table, tsCol = "periscope.orchestrator_discovery_5m", "timestamp_5m"
	default:
		return nil, status.Errorf(codes.InvalidArgument, "interval must be '5m' or '1h', got %q", interval)
	}

	query := fmt.Sprintf(`
		SELECT %s AS ts, gateway_id, gateway_region, resolved_ip,
		       sum(attempts) AS attempts,
		       sum(successes) AS successes,
		       sum(failures) AS failures,
		       sum(latency_sum) AS latency_sum,
		       sum(latency_count) AS latency_count,
		       max(max_latency) AS max_latency
		FROM %s
		WHERE tenant_id = ? AND orch_addr = ?
		  AND %s >= ? AND %s <= ?
	`, tsCol, table, tsCol, tsCol)
	args := []any{tenantID, orchAddr, startTime, endTime}

	if gw := strings.TrimSpace(req.GetGatewayId()); gw != "" {
		query += " AND gateway_id = ?"
		args = append(args, gw)
	}
	if ip := strings.TrimSpace(req.GetResolvedIp()); ip != "" {
		query += " AND resolved_ip = ?"
		args = append(args, ip)
	}

	query += " GROUP BY ts, gateway_id, gateway_region, resolved_ip ORDER BY ts ASC, gateway_id ASC, resolved_ip ASC"

	rows, err := s.clickhouse.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, wrapClickhouseError(err, "database error")
	}
	defer func() { _ = rows.Close() }()

	pointsByKey := make(map[orchestratorPerformanceKey]*pb.OrchestratorPerformancePoint)
	for rows.Next() {
		var (
			ts                            time.Time
			gwID, gwRegion, resolvedIP    string
			attempts, successes, failures uint64
			latencySum, latencyCount      uint64
			maxLatency                    uint32
		)
		if scanErr := rows.Scan(&ts, &gwID, &gwRegion, &resolvedIP, &attempts, &successes, &failures, &latencySum, &latencyCount, &maxLatency); scanErr != nil {
			s.logger.WithError(scanErr).Warn("Failed to scan orchestrator performance row")
			continue
		}
		var meanLatency float32
		if latencyCount > 0 {
			meanLatency = float32(latencySum) / float32(latencyCount)
		}
		key := orchestratorPerformanceKey{ts: ts, gatewayID: gwID, gatewayRegion: gwRegion, resolvedIP: resolvedIP}
		pointsByKey[key] = &pb.OrchestratorPerformancePoint{
			Timestamp:     timestamppb.New(ts),
			GatewayId:     gwID,
			GatewayRegion: gwRegion,
			ResolvedIp:    resolvedIP,
			Attempts:      attempts,
			Successes:     successes,
			Failures:      failures,
			MeanLatencyMs: meanLatency,
			MaxLatencyMs:  maxLatency,
		}
	}
	if err := rows.Err(); err != nil {
		return nil, wrapClickhouseError(err, "database error")
	}

	gatewayFilter := strings.TrimSpace(req.GetGatewayId())
	resolvedIPFilter := strings.TrimSpace(req.GetResolvedIp())
	if err := s.mergeOrchestratorTranscodeOutcomes(ctx, pointsByKey, tenantID, orchAddr, startTime, endTime, interval, gatewayFilter, resolvedIPFilter); err != nil {
		return nil, err
	}
	if err := s.mergeOrchestratorAIOutcomes(ctx, pointsByKey, tenantID, orchAddr, startTime, endTime, interval, gatewayFilter, resolvedIPFilter); err != nil {
		return nil, err
	}

	keys := make([]orchestratorPerformanceKey, 0, len(pointsByKey))
	for key := range pointsByKey {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		if !keys[i].ts.Equal(keys[j].ts) {
			return keys[i].ts.Before(keys[j].ts)
		}
		if keys[i].gatewayID != keys[j].gatewayID {
			return keys[i].gatewayID < keys[j].gatewayID
		}
		return keys[i].resolvedIP < keys[j].resolvedIP
	})
	points := make([]*pb.OrchestratorPerformancePoint, 0, len(keys))
	for _, key := range keys {
		points = append(points, pointsByKey[key])
	}
	return &pb.GetOrchestratorPerformanceSeriesResponse{Points: points}, nil
}

func (s *PeriscopeServer) performancePoint(points map[orchestratorPerformanceKey]*pb.OrchestratorPerformancePoint, key orchestratorPerformanceKey) *pb.OrchestratorPerformancePoint {
	if point := points[key]; point != nil {
		return point
	}
	point := &pb.OrchestratorPerformancePoint{
		Timestamp:     timestamppb.New(key.ts),
		GatewayId:     key.gatewayID,
		GatewayRegion: key.gatewayRegion,
		ResolvedIp:    key.resolvedIP,
	}
	points[key] = point
	return point
}

func (s *PeriscopeServer) mergeOrchestratorTranscodeOutcomes(ctx context.Context, points map[orchestratorPerformanceKey]*pb.OrchestratorPerformancePoint, tenantID, orchAddr string, startTime, endTime time.Time, interval, gatewayFilter, resolvedIPFilter string) error {
	var query string
	args := []any{tenantID, orchAddr, startTime, endTime}
	switch interval {
	case "1h":
		query = `
				SELECT timestamp_1h AS ts, gateway_id, gateway_region, resolved_ip,
				       sum(attempts), sum(successes), sum(failures),
			       sum(overall_ms_sum), sum(overall_ms_count),
			       max(max_overall_ms), sum(pixels_sum)
			FROM periscope.orchestrator_transcode_hourly
			WHERE tenant_id = ? AND orch_addr = ?
			  AND timestamp_1h >= ? AND timestamp_1h <= ?
		`
	default:
		query = `
				SELECT toStartOfInterval(timestamp, INTERVAL 5 MINUTE) AS ts, gateway_id, gateway_region, resolved_ip,
				       count(), sumIf(1, success = 1), sumIf(1, success = 0),
			       sumIf(toUInt64(overall_ms), success = 1), sumIf(1, success = 1),
			       maxIf(overall_ms, success = 1), sumIf(pixels, success = 1)
			FROM periscope.orchestrator_transcode_outcomes
			WHERE tenant_id = ? AND orch_addr = ?
			  AND timestamp >= ? AND timestamp <= ?
		`
	}
	if gatewayFilter != "" {
		query += " AND gateway_id = ?"
		args = append(args, gatewayFilter)
	}
	if resolvedIPFilter != "" {
		query += " AND resolved_ip = ?"
		args = append(args, resolvedIPFilter)
	}
	query += " GROUP BY ts, gateway_id, gateway_region, resolved_ip"

	rows, err := s.clickhouse.QueryContext(ctx, query, args...)
	if err != nil {
		return wrapClickhouseError(err, "database error")
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var (
			ts                            time.Time
			gatewayID, gatewayRegion      string
			resolvedIP                    string
			attempts, successes, failures uint64
			overallSum, overallCount      uint64
			maxOverall                    uint32
			pixels                        uint64
		)
		if scanErr := rows.Scan(&ts, &gatewayID, &gatewayRegion, &resolvedIP, &attempts, &successes, &failures, &overallSum, &overallCount, &maxOverall, &pixels); scanErr != nil {
			s.logger.WithError(scanErr).Warn("Failed to scan orchestrator transcode performance row")
			continue
		}
		point := s.performancePoint(points, orchestratorPerformanceKey{ts: ts, gatewayID: gatewayID, gatewayRegion: gatewayRegion, resolvedIP: resolvedIP})
		point.TranscodeAttempts = attempts
		point.TranscodeSuccesses = successes
		point.TranscodeFailures = failures
		if overallCount > 0 {
			point.TranscodeMeanOverallMs = float32(overallSum) / float32(overallCount)
		}
		point.TranscodeMaxOverallMs = maxOverall
		point.TranscodePixels = pixels
	}
	if err := rows.Err(); err != nil {
		return wrapClickhouseError(err, "database error")
	}
	return nil
}

func (s *PeriscopeServer) mergeOrchestratorAIOutcomes(ctx context.Context, points map[orchestratorPerformanceKey]*pb.OrchestratorPerformancePoint, tenantID, orchAddr string, startTime, endTime time.Time, interval, gatewayFilter, resolvedIPFilter string) error {
	var query string
	args := []any{tenantID, orchAddr, startTime, endTime}
	switch interval {
	case "1h":
		query = `
				SELECT timestamp_1h AS ts, gateway_id, gateway_region, resolved_ip,
				       sum(attempts), sum(successes), sum(failures),
			       sum(latency_ms_sum), sum(latency_ms_count), max(max_latency_ms)
			FROM periscope.orchestrator_ai_hourly
			WHERE tenant_id = ? AND orch_addr = ?
			  AND timestamp_1h >= ? AND timestamp_1h <= ?
		`
	default:
		query = `
				SELECT toStartOfInterval(timestamp, INTERVAL 5 MINUTE) AS ts, gateway_id, gateway_region, resolved_ip,
				       count(), sumIf(1, success = 1), sumIf(1, success = 0),
			       sumIf(toUInt64(latency_ms), success = 1), sumIf(1, success = 1),
			       maxIf(latency_ms, success = 1)
			FROM periscope.orchestrator_ai_outcomes
			WHERE tenant_id = ? AND orch_addr = ?
			  AND timestamp >= ? AND timestamp <= ?
		`
	}
	if gatewayFilter != "" {
		query += " AND gateway_id = ?"
		args = append(args, gatewayFilter)
	}
	if resolvedIPFilter != "" {
		query += " AND resolved_ip = ?"
		args = append(args, resolvedIPFilter)
	}
	query += " GROUP BY ts, gateway_id, gateway_region, resolved_ip"

	rows, err := s.clickhouse.QueryContext(ctx, query, args...)
	if err != nil {
		return wrapClickhouseError(err, "database error")
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var (
			ts                            time.Time
			gatewayID, gatewayRegion      string
			resolvedIP                    string
			attempts, successes, failures uint64
			latencySum, latencyCount      uint64
			maxLatency                    uint32
		)
		if scanErr := rows.Scan(&ts, &gatewayID, &gatewayRegion, &resolvedIP, &attempts, &successes, &failures, &latencySum, &latencyCount, &maxLatency); scanErr != nil {
			s.logger.WithError(scanErr).Warn("Failed to scan orchestrator AI performance row")
			continue
		}
		point := s.performancePoint(points, orchestratorPerformanceKey{ts: ts, gatewayID: gatewayID, gatewayRegion: gatewayRegion, resolvedIP: resolvedIP})
		point.AiAttempts = attempts
		point.AiSuccesses = successes
		point.AiFailures = failures
		if latencyCount > 0 {
			point.AiMeanLatencyMs = float32(latencySum) / float32(latencyCount)
		}
		point.AiMaxLatencyMs = maxLatency
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
func (s *PeriscopeServer) queryOrchestratorInstances(ctx context.Context, tenantID, orchAddr string) ([]*pb.OrchestratorInstance, error) {
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

	rows, err := s.clickhouse.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, wrapClickhouseError(err, "database error")
	}
	defer func() { _ = rows.Close() }()

	var out []*pb.OrchestratorInstance
	for rows.Next() {
		var inst pb.OrchestratorInstance
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

func capabilityPricesFromArrays(capabilities []string, positions []uint32, pricePerUnits []int64, pixelsPerUnits []int64) []*pb.OrchestratorCapabilityPrice {
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
	out := make([]*pb.OrchestratorCapabilityPrice, 0, count)
	for i := 0; i < count; i++ {
		out = append(out, &pb.OrchestratorCapabilityPrice{
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
func (s *PeriscopeServer) queryOrchestratorVantages(ctx context.Context, tenantID, orchAddr string) ([]*pb.OrchestratorVantage, error) {
	query := `
		SELECT tenant_id, gateway_id, gateway_region, orch_addr, resolved_ip,
		       latitude, longitude, city, country_code, geo_source, geo_resolved_at,
		       latest_latency_ms, score, dialed_recently, last_seen
		FROM periscope.orchestrator_vantage_current FINAL
		WHERE tenant_id = ?
	`
	args := []any{tenantID}
	if orchAddr != "" {
		query += " AND orch_addr = ?"
		args = append(args, orchAddr)
	}
	query += " ORDER BY orch_addr ASC, gateway_id ASC, resolved_ip ASC"

	rows, err := s.clickhouse.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, wrapClickhouseError(err, "database error")
	}
	defer func() { _ = rows.Close() }()

	var out []*pb.OrchestratorVantage
	for rows.Next() {
		var v pb.OrchestratorVantage
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
		v.GeoResolvedAt = timestamppb.New(geoResolvedAt)
		v.LastSeen = timestamppb.New(lastSeen)
		out = append(out, &v)
	}
	return out, nil
}

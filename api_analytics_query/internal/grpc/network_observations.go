package grpc

import (
	"context"
	"encoding/json"
	"fmt"
	"frameworks/api_analytics_query/internal/database/periscopequerydb"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/middleware"
	obs "github.com/Livepeer-FrameWorks/monorepo/pkg/networkobservation"
	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/periscope"
	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"strings"
	"time"
)

func (s *PeriscopeServer) GetNetworkObservations(ctx context.Context, req *pb.NetworkObservationsRequest) (*pb.NetworkObservationsResponse, error) {
	if !middleware.IsServiceCall(ctx) {
		return nil, status.Error(codes.PermissionDenied, "network observation projection requires an internal service")
	}
	if req == nil || len(req.TenantIds) == 0 || len(req.TenantIds) > 32 {
		return nil, status.Error(codes.InvalidArgument, "explicit platform scopes required")
	}
	for _, id := range req.TenantIds {
		if _, err := uuid.Parse(id); err != nil {
			return nil, status.Error(codes.InvalidArgument, "invalid tenant scope")
		}
	}
	if req.Provider != "livepeer" || req.NetworkId == "" || req.Environment == "" {
		return nil, status.Error(codes.InvalidArgument, "network namespace required")
	}
	start, end, err := validateTimeRangeProto(req.TimeRange)
	if err != nil || !end.After(start) || end.Sub(start) > 730*24*time.Hour {
		return nil, status.Error(codes.InvalidArgument, "time range must be within 730 days")
	}
	interval := 300
	switch req.Interval {
	case "1m":
		interval = 60
	case "5m", "":
	case "1h":
		interval = 3600
	default:
		return nil, status.Error(codes.InvalidArgument, "invalid interval")
	}
	limit := int(req.Limit)
	if limit < 1 {
		limit = 1000
	}
	if limit > 10000 {
		limit = 10000
	}
	where := " tenant_id IN (?) AND provider=? AND network_id=? AND environment=? AND timestamp>=? AND timestamp<?"
	args := []any{req.TenantIds, req.Provider, req.NetworkId, req.Environment, start, end}
	for _, filter := range []struct{ column, value string }{{"resource_id", req.ResourceId}, {"resolved_ip", req.ResolvedIp}, {"gateway_region", req.GatewayRegion}} {
		if filter.value != "" {
			if filter.column == "resolved_ip" {
				where += " AND (resolved_ip=? OR (kind='decision' AND resolved_ip=''))"
			} else {
				where += " AND " + filter.column + "=?"
			}
			args = append(args, filter.value)
		}
	}
	history := obs.History{GeneratedAt: time.Now().UTC(), Start: start, End: end, IntervalSeconds: interval, Cohorts: []obs.Cohort{}, Observations: []obs.Metric{}}
	selectMetrics := `kind,resource_id,resolved_ip,gateway_region,workload,profile_key,
	count(),countIf(success=1 AND qualifying=1),countIf(success=0 AND qualifying=1),countIf(qualifying=1),
 toFloat64(quantileTDigestIf(0.5)(latency_ms,qualifying=1)),toFloat64(quantileTDigestIf(0.95)(latency_ms,qualifying=1)),toFloat64(quantileTDigestIf(0.99)(latency_ms,qualifying=1)),
 countIf(source_duration_ms>0 AND qualifying=1),quantileTDigestIf(0.95)(latency_ms/toFloat64(greatest(source_duration_ms,1)),source_duration_ms>0 AND qualifying=1),
 sum(if(attempts>0,attempts-1,0)),sum(fallback_count),countIf(deadline_exceeded=1)`
	aggregateTable, aggregateWhere := "periscope.network_observations", where
	if start.Before(time.Now().Add(-30*24*time.Hour - time.Minute)) {
		history.RolledUp = true
		if interval < 3600 {
			interval = 3600
			history.IntervalSeconds = interval
		}
		aggregateTable = "periscope.network_observation_rollups"
		aggregateWhere = strings.ReplaceAll(where, "timestamp", "bucket") + " AND interval_seconds=3600"
		selectMetrics = `kind,resource_id,resolved_ip,gateway_region,workload,profile_key,
 sum(samples),sum(successes),sum(failures),sum(qualified_samples),
 toFloat64(quantilesTDigestIfMerge(0.5,0.95,0.99)(latency_state)[1]),toFloat64(quantilesTDigestIfMerge(0.5,0.95,0.99)(latency_state)[2]),toFloat64(quantilesTDigestIfMerge(0.5,0.95,0.99)(latency_state)[3]),
 sum(duration_samples),quantileTDigestIfMerge(0.95)(duration_ratio_state),sum(retries),sum(fallbacks),sum(deadline_misses)`
	}
	bucketColumn := "timestamp"
	if history.RolledUp {
		bucketColumn = "bucket"
	}
	query := fmt.Sprintf(`SELECT toStartOfInterval(%s, INTERVAL %d SECOND) AS bucket,%s
 FROM %s FINAL WHERE %s
 GROUP BY bucket,kind,resource_id,resolved_ip,gateway_region,workload,profile_key ORDER BY bucket DESC,resource_id,resolved_ip LIMIT %d`, bucketColumn, interval, selectMetrics, aggregateTable, aggregateWhere, limit+1)
	queryCtx, cancel := withClickhouseTimeout(ctx)
	defer cancel()
	rows, err := periscopequerydb.Query(queryCtx, s.clickhouse, query, args...)
	if err != nil {
		return nil, wrapClickhouseError(err, "network analytics unavailable")
	}
	for rows.Next() {
		var c obs.Cohort
		if err = rows.Scan(&c.Bucket, &c.Kind, &c.ResourceID, &c.IP, &c.Region, &c.Workload, &c.ProfileKey, &c.Samples, &c.Successes, &c.Failures, &c.QualifiedSamples, &c.P50MS, &c.P95MS, &c.P99MS, &c.DurationSamples, &c.P95DurationRatio, &c.Retries, &c.Fallbacks, &c.DeadlineMisses); err != nil {
			rows.Close()
			return nil, wrapClickhouseError(err, "network cohort scan failed")
		}
		c.P95DurationRatio = sanitizeFloat64(c.P95DurationRatio)
		c.P50MS = sanitizeFloat64(c.P50MS)
		c.P95MS = sanitizeFloat64(c.P95MS)
		c.P99MS = sanitizeFloat64(c.P99MS)
		history.Cohorts = append(history.Cohorts, c)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, wrapClickhouseError(err, "network cohort query failed")
	}
	if len(history.Cohorts) > limit {
		history.Truncated = true
		history.Cohorts = history.Cohorts[:limit]
	}
	// Comparison totals cover the full requested window independently of the bounded time series.
	summaryQuery := fmt.Sprintf(`SELECT %s FROM %s FINAL WHERE %s GROUP BY kind,resource_id,resolved_ip,gateway_region,workload,profile_key ORDER BY resource_id,resolved_ip LIMIT %d`, selectMetrics, aggregateTable, aggregateWhere, limit+1)
	rows, err = periscopequerydb.Query(queryCtx, s.clickhouse, summaryQuery, args...)
	if err != nil {
		return nil, wrapClickhouseError(err, "network comparison unavailable")
	}
	history.Summaries = []obs.Cohort{}
	for rows.Next() {
		var c obs.Cohort
		if err = rows.Scan(&c.Kind, &c.ResourceID, &c.IP, &c.Region, &c.Workload, &c.ProfileKey, &c.Samples, &c.Successes, &c.Failures, &c.QualifiedSamples, &c.P50MS, &c.P95MS, &c.P99MS, &c.DurationSamples, &c.P95DurationRatio, &c.Retries, &c.Fallbacks, &c.DeadlineMisses); err != nil {
			rows.Close()
			return nil, wrapClickhouseError(err, "network comparison scan failed")
		}
		c.P50MS = sanitizeFloat64(c.P50MS)
		c.P95MS = sanitizeFloat64(c.P95MS)
		c.P99MS = sanitizeFloat64(c.P99MS)
		c.P95DurationRatio = sanitizeFloat64(c.P95DurationRatio)
		history.Summaries = append(history.Summaries, c)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, wrapClickhouseError(err, "network comparison query failed")
	}
	if len(history.Summaries) > limit {
		history.Truncated = true
		history.Summaries = history.Summaries[:limit]
	}
	columns := "public_data"
	if req.IncludeDetails {
		columns += ",private_data"
	}
	rows, err = periscopequerydb.Query(queryCtx, s.clickhouse, fmt.Sprintf("SELECT %s FROM periscope.network_observations FINAL WHERE %s ORDER BY timestamp DESC,event_id LIMIT %d", columns, where, limit+1), args...)
	if err != nil {
		return nil, wrapClickhouseError(err, "network samples unavailable")
	}
	defer rows.Close()
	for rows.Next() {
		var raw, private string
		if req.IncludeDetails {
			err = rows.Scan(&raw, &private)
		} else {
			err = rows.Scan(&raw)
		}
		if err != nil {
			return nil, wrapClickhouseError(err, "network sample scan failed")
		}
		var m obs.Metric
		if err = json.Unmarshal([]byte(raw), &m); err != nil {
			return nil, status.Error(codes.Internal, "invalid network sample")
		}
		history.Observations = append(history.Observations, m)
		if req.IncludeDetails {
			history.Details = append(history.Details, json.RawMessage(private))
		}
	}
	if err = rows.Err(); err != nil {
		return nil, wrapClickhouseError(err, "network sample query failed")
	}
	if len(history.Observations) > limit {
		history.Truncated = true
		history.Observations = history.Observations[:limit]
		if len(history.Details) > limit {
			history.Details = history.Details[:limit]
		}
	}
	_ = rows.Close()
	if req.IncludeDetails {
		if deliveryErr := s.enrichNetworkDelivery(queryCtx, &history); deliveryErr != nil && s.logger != nil {
			s.logger.WithError(deliveryErr).Warn("Network diagnostics have no Mist output enrichment")
		}
	}
	raw, err := json.Marshal(history)
	if err != nil {
		return nil, status.Error(codes.Internal, "network projection failed")
	}
	return &pb.NetworkObservationsResponse{Json: raw}, nil
}

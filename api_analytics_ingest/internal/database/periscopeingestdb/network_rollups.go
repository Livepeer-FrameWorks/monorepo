package periscopeingestdb

import (
	"context"
	"fmt"
	"time"
)

type NetworkRollupExecutor interface {
	Exec(context.Context, string, ...interface{}) error
}

// RebuildNetworkRollups replaces complete bucket snapshots after event-ID deduplication.
// Replaying a source or a rebuild never adds the same observation twice.
func RebuildNetworkRollups(ctx context.Context, db NetworkRollupExecutor, tenant, network, environment string, since, through time.Time, interval int) error {
	if interval != 300 && interval != 3600 {
		return fmt.Errorf("unsupported network rollup interval")
	}
	query := fmt.Sprintf(`INSERT INTO periscope.network_observation_rollups (
 tenant_id,provider,network_id,environment,bucket,interval_seconds,kind,resource_id,resolved_ip,gateway_region,workload,profile_key,
 samples,successes,failures,qualified_samples,latency_state,duration_samples,duration_ratio_state,retries,fallbacks,deadline_misses,snapshot_version)
 SELECT tenant_id,provider,network_id,environment,toStartOfInterval(timestamp,INTERVAL %d SECOND),toUInt32(%d),kind,resource_id,resolved_ip,gateway_region,workload,profile_key,
 count(),countIf(success=1 AND qualifying=1),countIf(success=0 AND qualifying=1),countIf(qualifying=1),
 quantilesTDigestIfState(0.5,0.95,0.99)(latency_ms,qualifying=1),countIf(source_duration_ms>0 AND qualifying=1),
 quantileTDigestIfState(0.95)(latency_ms/toFloat64(greatest(source_duration_ms,1)),source_duration_ms>0 AND qualifying=1),
 sum(toUInt64(if(attempts>0,attempts-1,0))),sum(toUInt64(fallback_count)),countIf(deadline_exceeded=1),max(inserted_at)
 FROM periscope.network_observations FINAL
 WHERE tenant_id=? AND provider='livepeer' AND network_id=? AND environment=?
 AND toStartOfInterval(timestamp,INTERVAL %d SECOND)>=now()-INTERVAL 30 DAY
 AND toStartOfInterval(timestamp,INTERVAL %d SECOND) IN (
 SELECT DISTINCT toStartOfInterval(timestamp,INTERVAL %d SECOND) FROM periscope.network_observations FINAL
 WHERE tenant_id=? AND provider='livepeer' AND network_id=? AND environment=? AND inserted_at>=? AND inserted_at<?)
 GROUP BY tenant_id,provider,network_id,environment,toStartOfInterval(timestamp,INTERVAL %d SECOND),kind,resource_id,resolved_ip,gateway_region,workload,profile_key`, interval, interval, interval, interval, interval, interval)
	if err := db.Exec(ctx, query, tenant, network, environment, tenant, network, environment, since, through); err != nil {
		return err
	}
	if err := rebuildNetworkGateway(ctx, db, tenant, network, environment, since, through, interval); err != nil {
		return err
	}
	return rebuildNetworkWork(ctx, db, tenant, network, environment, since, through, interval)
}

// rebuildNetworkGateway replaces the gateway-keyed discovery and attempt
// snapshots of the same changed buckets, counted as the performance series
// counts raw rows: every row is a sample, successes and failures are
// qualifying, latency and returned pixels sum over qualifying successes.
func rebuildNetworkGateway(ctx context.Context, db NetworkRollupExecutor, tenant, network, environment string, since, through time.Time, interval int) error {
	query := fmt.Sprintf(`INSERT INTO periscope.network_gateway_rollups (
 tenant_id,provider,network_id,environment,bucket,interval_seconds,kind,resource_id,resolved_ip,gateway_id,gateway_region,
 samples,successes,failures,latency_sum,latency_count,max_latency,returned_pixels,snapshot_version)
 SELECT tenant_id,provider,network_id,environment,toStartOfInterval(timestamp,INTERVAL %d SECOND),toUInt32(%d),kind,resource_id,resolved_ip,gateway_id,gateway_region,
 count(),countIf(success=1 AND qualifying=1),countIf(success=0 AND qualifying=1),
 sumIf(toUInt64(latency_ms),success=1 AND qualifying=1),countIf(success=1 AND qualifying=1),
 maxIf(latency_ms,success=1 AND qualifying=1),sumIf(returned_pixels,success=1 AND returned_pixels_known=1),max(inserted_at)
 FROM periscope.network_observations FINAL
 WHERE tenant_id=? AND provider='livepeer' AND network_id=? AND environment=? AND producer_kind='gateway' AND kind IN ('discovery','attempt')
 AND toStartOfInterval(timestamp,INTERVAL %d SECOND)>=now()-INTERVAL 30 DAY
 AND toStartOfInterval(timestamp,INTERVAL %d SECOND) IN (
 SELECT DISTINCT toStartOfInterval(timestamp,INTERVAL %d SECOND) FROM periscope.network_observations FINAL
 WHERE tenant_id=? AND provider='livepeer' AND network_id=? AND environment=? AND inserted_at>=? AND inserted_at<?)
 GROUP BY tenant_id,provider,network_id,environment,toStartOfInterval(timestamp,INTERVAL %d SECOND),kind,resource_id,resolved_ip,gateway_id,gateway_region`, interval, interval, interval, interval, interval, interval)
	return db.Exec(ctx, query, tenant, network, environment, tenant, network, environment, since, through)
}

package periscopeingestdb

import (
	"context"
	"fmt"
	"time"
)

func rebuildNetworkWork(ctx context.Context, db NetworkRollupExecutor, tenant, network, environment string, since, through time.Time, interval int) error {
	query := fmt.Sprintf(`INSERT INTO periscope.network_work_rollups (
 tenant_id,provider,network_id,environment,bucket,interval_seconds,resource_id,resolved_ip,gateway_region,profile_key,
 service_charge_denominator,ticket_credit_denominator,
 accounted_attempts,unaccounted_samples,dispatched_attempts,dispatched_source_ms,returned_attempts,returned_pixels,
 accepted_segments,accepted_source_ms,accepted_pixels,accepted_rendition_ms,
 service_charge_samples,service_charge_numerator,ticket_credit_samples,ticket_credit_numerator,tickets_sent,
 dispatched_duration_samples,accepted_duration_samples,accepted_output_samples,snapshot_version)
 SELECT tenant_id,provider,network_id,environment,toStartOfInterval(timestamp,INTERVAL %d SECOND),toUInt32(%d),resource_id,resolved_ip,gateway_region,profile_key,
 service_charge_denominator,ticket_credit_denominator,
 countIf(kind='attempt' AND accounting_version=1),countIf(accounting_version!=1),
 countIf(kind='attempt' AND accounting_version=1 AND request_dispatched=1),
 sumIf(toUInt64(source_duration_ms),kind='attempt' AND accounting_version=1 AND request_dispatched=1),
 countIf(kind='attempt' AND accounting_version=1 AND returned_pixels_known=1),
 sumIf(returned_pixels,kind='attempt' AND accounting_version=1 AND returned_pixels_known=1),
 countIf(kind='segment' AND accounting_version=1 AND success=1),
 sumIf(toUInt64(source_duration_ms),kind='segment' AND accounting_version=1 AND success=1),
 sumIf(accepted_pixels,kind='segment' AND accounting_version=1 AND success=1),
 sumIf(toUInt64(source_duration_ms)*toUInt64(accepted_renditions),kind='segment' AND accounting_version=1 AND success=1),
 countIf(service_charge_known=1),sumIf(service_charge_numerator,service_charge_known=1),
 countIf(ticket_credit_known=1),ticket_credit_numerator,sum(tickets_sent),
 countIf(kind='attempt' AND accounting_version=1 AND request_dispatched=1 AND source_duration_ms>0),
 countIf(kind='segment' AND accounting_version=1 AND success=1 AND source_duration_ms>0),
 countIf(kind='segment' AND accounting_version=1 AND success=1 AND accepted_renditions>0),max(inserted_at)
 FROM periscope.network_observations FINAL
 WHERE tenant_id=? AND provider='livepeer' AND network_id=? AND environment=? AND kind IN ('attempt','segment')
 AND toStartOfInterval(timestamp,INTERVAL %d SECOND)>=now()-INTERVAL 30 DAY
 AND toStartOfInterval(timestamp,INTERVAL %d SECOND) IN (
 SELECT DISTINCT toStartOfInterval(timestamp,INTERVAL %d SECOND) FROM periscope.network_observations FINAL
 WHERE tenant_id=? AND provider='livepeer' AND network_id=? AND environment=? AND inserted_at>=? AND inserted_at<?)
 GROUP BY tenant_id,provider,network_id,environment,toStartOfInterval(timestamp,INTERVAL %d SECOND),resource_id,resolved_ip,gateway_region,profile_key,service_charge_denominator,ticket_credit_denominator,ticket_credit_numerator`, interval, interval, interval, interval, interval, interval)
	return db.Exec(ctx, query, tenant, network, environment, tenant, network, environment, since, through)
}

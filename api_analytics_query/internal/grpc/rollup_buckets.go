package grpc

// The 5-minute rollups are written by insert-triggered views, one row per
// insert block and bucket, so a bucket can have several rows whose averages
// describe only their block. These queries combine a bucket's rows from the
// sums, sample counts and session states the views also store, which gives
// the bucket's values however the samples were inserted.

// streamHealthMean is the average of a stream_health_5m measure over a set of
// rows, NULL when none of them has a sample of it.
func streamHealthMean(measure string) string {
	return "(sum(" + measure + "_sum) / nullIf(sum(" + measure + "_samples), 0))"
}

// streamHealthBuckets returns one row per (timestamp_5m, stream, node) of the
// stream_health_5m rows matching where.
func streamHealthBuckets(where string) string {
	return `
		SELECT timestamp_5m, tenant_id, stream_id, node_id,
		       toUInt32(sum(rebuffer_count)) AS bucket_rebuffer_count,
		       toUInt32(sum(issue_count)) AS bucket_issue_count,
		       any(sample_issues) AS bucket_sample_issues,
		       toFloat32(ifNull(` + streamHealthMean("bitrate") + `, 0)) AS bucket_avg_bitrate,
		       toFloat32(ifNull(` + streamHealthMean("fps") + `, 0)) AS bucket_avg_fps,
		       toFloat32(ifNull(` + streamHealthMean("buffer_health") + `, 0)) AS bucket_avg_buffer_health,
		       toNullable(toFloat32(` + streamHealthMean("frame_jitter") + `)) AS bucket_avg_frame_jitter_ms,
		       max(max_frame_jitter_ms) AS bucket_max_frame_jitter_ms,
		       toUInt32(sum(buffer_dry_count)) AS bucket_buffer_dry_count,
		       if(countIf(quality_tier NOT IN ('', 'Unknown')) = 0, 'Unknown',
		          argMaxIf(quality_tier, quality_tier_at, quality_tier NOT IN ('', 'Unknown'))) AS bucket_quality_tier
		FROM stream_health_5m
		WHERE ` + where + `
		GROUP BY timestamp_5m, tenant_id, stream_id, node_id`
}

// clientQoeLossRate is the packet loss of a set of client_qoe_5m rows: lost
// over sent packets, or the average of the stored rates for rows written
// before packet sums were kept.
const clientQoeLossRate = `if(sum(packets_sent_sum) > 0, sum(packets_lost_sum) / sum(packets_sent_sum), avgIf(pkt_loss_rate, rollup_version = 0))`

// clientQoeBuckets returns one row per (timestamp_5m, stream, node) of the
// client_qoe_5m rows matching where.
func clientQoeBuckets(where string) string {
	return `
		SELECT timestamp_5m, tenant_id, stream_id, node_id,
		       toUInt32(uniqMergeIf(sessions_state, rollup_version = 1) + sumIf(active_sessions, rollup_version = 0)) AS bucket_active_sessions,
		       ifNull(sum(bw_in_sum) / nullIf(sum(qoe_samples), 0), 0) AS bucket_avg_bw_in,
		       ifNull(sum(bw_out_sum) / nullIf(sum(qoe_samples), 0), 0) AS bucket_avg_bw_out,
		       toFloat32(ifNull(sum(connection_time_sum) / nullIf(sum(qoe_samples), 0), 0)) AS bucket_avg_connection_time,
		       ` + clientQoeLossRate + ` AS bucket_pkt_loss_rate,
		       sum(bw_in_sum) AS bucket_bw_in_sum,
		       sum(bw_out_sum) AS bucket_bw_out_sum,
		       sum(connection_time_sum) AS bucket_connection_time_sum,
		       sum(qoe_samples) AS bucket_samples,
		       sum(packets_sent_sum) AS bucket_packets_sent,
		       sum(packets_lost_sum) AS bucket_packets_lost
		FROM client_qoe_5m
		WHERE ` + where + `
		GROUP BY timestamp_5m, tenant_id, stream_id, node_id`
}

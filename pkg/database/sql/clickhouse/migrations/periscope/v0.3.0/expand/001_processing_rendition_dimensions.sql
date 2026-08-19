ALTER TABLE periscope.processing_segments_final
    ADD COLUMN IF NOT EXISTS renditions_json String DEFAULT '' AFTER livepeer_session_id;

CREATE OR REPLACE VIEW periscope.processing_segments_final_v AS
SELECT
    tenant_id, node_id, stream_id,
    argMax(process_type, projection_version_ms) AS process_type,
    argMax(output_codec, projection_version_ms) AS output_codec,
    argMax(track_type, projection_version_ms) AS track_type,
    source_event_id,
    min(projection_version_ms) AS billable_at_ms,
    argMax(segment_number, projection_version_ms) AS segment_number,
    argMax(segment_dedupe_key, projection_version_ms) AS segment_dedupe_key,
    argMax(cluster_id, projection_version_ms) AS cluster_id,
    argMax(stream_name, projection_version_ms) AS stream_name,
    argMax(input_codec, projection_version_ms) AS input_codec,
    argMax(media_seconds, projection_version_ms) AS media_seconds,
    argMax(width, projection_version_ms) AS width,
    argMax(height, projection_version_ms) AS height,
    argMax(rendition_count, projection_version_ms) AS rendition_count,
    argMax(input_bytes, projection_version_ms) AS input_bytes,
    argMax(output_bytes_total, projection_version_ms) AS output_bytes_total,
    argMax(turnaround_ms, projection_version_ms) AS turnaround_ms,
    argMax(speed_factor, projection_version_ms) AS speed_factor,
    argMax(livepeer_session_id, projection_version_ms) AS livepeer_session_id,
    argMax(renditions_json, projection_version_ms) AS renditions_json,
    argMax(input_frames, projection_version_ms) AS input_frames,
    argMax(output_frames, projection_version_ms) AS output_frames,
    argMax(input_frames_delta, projection_version_ms) AS input_frames_delta,
    argMax(output_frames_delta, projection_version_ms) AS output_frames_delta,
    argMax(input_bytes_delta, projection_version_ms) AS input_bytes_delta,
    argMax(output_bytes_delta, projection_version_ms) AS output_bytes_delta,
    argMax(rtf_in, projection_version_ms) AS rtf_in,
    argMax(rtf_out, projection_version_ms) AS rtf_out,
    argMax(is_final, projection_version_ms) AS is_final,
    argMax(source_started_at_ms, projection_version_ms) AS source_started_at_ms,
    argMax(source_ended_at_ms, projection_version_ms) AS source_ended_at_ms,
    argMax(edge_received_at_ms, projection_version_ms) AS edge_received_at_ms,
    max(projection_version_ms) AS latest_projection_version_ms
FROM periscope.processing_segments_final
GROUP BY tenant_id, node_id, stream_id, source_event_id;

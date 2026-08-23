ALTER TABLE periscope.api_requests
    ADD COLUMN IF NOT EXISTS source_event_id String DEFAULT hex(sipHash128(concat(
        toString(timestamp), '|', toString(tenant_id), '|', ifNull(source_node, ''), '|',
        auth_type, '|', ifNull(operation_name, ''), '|', operation_type, '|',
        toString(request_count), '|', toString(error_count), '|', toString(total_duration_ms), '|',
        toString(total_complexity), '|', toString(llm_input_tokens), '|', toString(llm_output_tokens), '|',
        llm_model, '|', llm_provider
    ))) AFTER source_node;

ALTER TABLE periscope.api_requests
    ADD COLUMN IF NOT EXISTS ingested_at_ms Int64 DEFAULT toInt64(toUnixTimestamp(timestamp)) * 1000 AFTER source_event_id;

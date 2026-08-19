ALTER TABLE periscope.api_requests
    ADD COLUMN IF NOT EXISTS llm_input_tokens UInt64 DEFAULT 0 AFTER total_complexity;
ALTER TABLE periscope.api_requests
    ADD COLUMN IF NOT EXISTS llm_output_tokens UInt64 DEFAULT 0 AFTER llm_input_tokens;
ALTER TABLE periscope.api_requests
    ADD COLUMN IF NOT EXISTS llm_model LowCardinality(String) DEFAULT '' AFTER llm_output_tokens;
ALTER TABLE periscope.api_requests
    ADD COLUMN IF NOT EXISTS llm_provider LowCardinality(String) DEFAULT '' AFTER llm_model;

ALTER TABLE periscope.api_usage_5m
    ADD COLUMN IF NOT EXISTS service LowCardinality(String) DEFAULT '' AFTER operation_name;
ALTER TABLE periscope.api_usage_5m
    ADD COLUMN IF NOT EXISTS llm_model LowCardinality(String) DEFAULT '' AFTER service;
ALTER TABLE periscope.api_usage_5m
    ADD COLUMN IF NOT EXISTS llm_provider LowCardinality(String) DEFAULT '' AFTER llm_model;
ALTER TABLE periscope.api_usage_5m
    ADD COLUMN IF NOT EXISTS llm_input_tokens UInt64 DEFAULT 0 AFTER complexity;
ALTER TABLE periscope.api_usage_5m
    ADD COLUMN IF NOT EXISTS llm_output_tokens UInt64 DEFAULT 0 AFTER llm_input_tokens;

CREATE OR REPLACE VIEW periscope.api_usage_5m_v AS
SELECT
    window_start, tenant_id, auth_type, operation_type, operation_name,
    service, llm_model, llm_provider,
    min(projection_version_ms) AS billable_at_ms,
    argMax(requests,            projection_version_ms) AS requests,
    argMax(errors,              projection_version_ms) AS errors,
    argMax(duration_ms,         projection_version_ms) AS duration_ms,
    argMax(complexity,          projection_version_ms) AS complexity,
    argMax(llm_input_tokens,    projection_version_ms) AS llm_input_tokens,
    argMax(llm_output_tokens,   projection_version_ms) AS llm_output_tokens,
    argMax(unique_users_state,  projection_version_ms) AS unique_users_state,
    argMax(unique_tokens_state, projection_version_ms) AS unique_tokens_state,
    max(projection_version_ms) AS latest_projection_version_ms
FROM periscope.api_usage_5m
GROUP BY window_start, tenant_id, auth_type, operation_type, operation_name,
         service, llm_model, llm_provider;

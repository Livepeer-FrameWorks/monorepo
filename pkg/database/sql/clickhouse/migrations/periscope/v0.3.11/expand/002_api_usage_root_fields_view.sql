-- One api_usage_5m projection of a window holds a row per root-field signature,
-- so the latest-projection view keys on the signature too; otherwise argMax
-- would keep one signature's counts and drop the others. A source event's
-- signature never changes between projections, so each signature's latest row
-- is current. Readers sum across the view's rows.
CREATE OR REPLACE VIEW periscope.api_usage_5m_v AS
SELECT
    window_start, tenant_id, auth_type, operation_type, operation_name,
    service, llm_model, llm_provider, root_fields,
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
         service, llm_model, llm_provider, root_fields;

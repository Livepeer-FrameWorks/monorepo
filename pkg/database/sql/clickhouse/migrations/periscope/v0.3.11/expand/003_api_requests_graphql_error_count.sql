-- api_requests.error_count counts failed requests; graphql_error_count carries
-- the total number of GraphQL errors those requests returned.
ALTER TABLE periscope.api_requests
    ADD COLUMN IF NOT EXISTS graphql_error_count UInt32 DEFAULT 0 AFTER root_fields;

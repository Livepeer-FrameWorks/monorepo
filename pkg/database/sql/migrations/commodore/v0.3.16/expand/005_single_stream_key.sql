CREATE OR REPLACE FUNCTION commodore.create_user_stream(p_tenant_id UUID, p_user_id UUID, p_title VARCHAR DEFAULT 'My Stream')
RETURNS TABLE(stream_id UUID, stream_key VARCHAR, playback_id VARCHAR, internal_name VARCHAR) AS $$
DECLARE
    new_stream_id UUID;
    new_stream_key VARCHAR(32);
    new_playback_id VARCHAR(16);
    new_internal_name VARCHAR(64);
BEGIN
    -- Generate unique identifiers
    new_stream_id := gen_random_uuid();
    new_stream_key := 'sk_' || commodore.generate_random_string(28);
    new_playback_id := commodore.generate_random_string(16);
    new_internal_name := commodore.generate_random_string(32);  -- Independent random string (not derivable from stream_id)

    -- Create stream record
    INSERT INTO commodore.streams (id, tenant_id, user_id, stream_key, playback_id, internal_name, title)
    VALUES (new_stream_id, p_tenant_id, p_user_id, new_stream_key, new_playback_id, new_internal_name, p_title);

    RETURN QUERY SELECT new_stream_id, new_stream_key, new_playback_id, new_internal_name;
END;
$$ LANGUAGE plpgsql;

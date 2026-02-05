-- ============================================================================
-- SKIPPER SCHEMA - AI VIDEO CONSULTANT
-- ============================================================================
-- Stores knowledge embeddings, chat conversations, and usage metering
-- ============================================================================

CREATE SCHEMA IF NOT EXISTS skipper;

-- ============================================================================
-- EXTENSIONS
-- ============================================================================

CREATE EXTENSION IF NOT EXISTS "uuid-ossp";
CREATE EXTENSION IF NOT EXISTS "pgcrypto";
CREATE EXTENSION IF NOT EXISTS vector;

-- ============================================================================
-- KNOWLEDGE BASE
-- ============================================================================

CREATE TABLE IF NOT EXISTS skipper.skipper_knowledge (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id UUID,
    source_url TEXT NOT NULL,
    source_title TEXT,
    chunk_text TEXT NOT NULL,
    chunk_index INT,
    embedding vector(1536),
    metadata JSONB,
    created_at TIMESTAMP WITH TIME ZONE DEFAULT NOW(),
    updated_at TIMESTAMP WITH TIME ZONE DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS skipper_knowledge_embedding_idx
    ON skipper.skipper_knowledge USING ivfflat (embedding vector_cosine_ops);

CREATE INDEX IF NOT EXISTS skipper_knowledge_tenant_source_idx
    ON skipper.skipper_knowledge (tenant_id, source_url);

-- ============================================================================
-- CONVERSATIONS
-- ============================================================================

CREATE TABLE IF NOT EXISTS skipper.skipper_conversations (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id UUID NOT NULL,
    user_id UUID,
    title TEXT,
    created_at TIMESTAMP WITH TIME ZONE DEFAULT NOW(),
    updated_at TIMESTAMP WITH TIME ZONE DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS skipper.skipper_messages (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    conversation_id UUID REFERENCES skipper.skipper_conversations(id) ON DELETE CASCADE,
    role TEXT NOT NULL,
    content TEXT,
    confidence TEXT,
    sources JSONB,
    tools_used JSONB,
    token_count_input INT,
    token_count_output INT,
    created_at TIMESTAMP WITH TIME ZONE DEFAULT NOW()
);

-- ============================================================================
-- USAGE METERING
-- ============================================================================

CREATE TABLE IF NOT EXISTS skipper.skipper_usage (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id UUID NOT NULL,
    event_type TEXT,
    tokens_input INT,
    tokens_output INT,
    model TEXT,
    created_at TIMESTAMP WITH TIME ZONE DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS skipper_usage_tenant_created_idx
    ON skipper.skipper_usage (tenant_id, created_at);

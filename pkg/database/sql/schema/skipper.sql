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
    tenant_id UUID NOT NULL,
    source_url TEXT NOT NULL,
    source_title TEXT,
    source_root TEXT,
    source_type TEXT,
    chunk_text TEXT NOT NULL,
    chunk_index INT,
    embedding vector(1536),
    metadata JSONB,
    tsv tsvector GENERATED ALWAYS AS (to_tsvector('english', chunk_text)) STORED,
    created_at TIMESTAMP WITH TIME ZONE DEFAULT NOW(),
    updated_at TIMESTAMP WITH TIME ZONE DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS skipper_knowledge_embedding_idx
    ON skipper.skipper_knowledge USING hnsw (embedding vector_cosine_ops)
    WITH (m = 24, ef_construction = 256);

CREATE INDEX IF NOT EXISTS skipper_knowledge_tenant_source_idx
    ON skipper.skipper_knowledge (tenant_id, source_url);

CREATE INDEX IF NOT EXISTS skipper_knowledge_source_root_idx
    ON skipper.skipper_knowledge (tenant_id, source_root);

CREATE INDEX IF NOT EXISTS skipper_knowledge_tsv_idx
    ON skipper.skipper_knowledge USING GIN (tsv);

-- ============================================================================
-- CONVERSATIONS
-- ============================================================================

CREATE TABLE IF NOT EXISTS skipper.skipper_conversations (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id UUID NOT NULL,
    user_id UUID,
    title TEXT,
    summary TEXT,
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
    confidence_blocks JSONB,
    created_at TIMESTAMP WITH TIME ZONE DEFAULT NOW()
);

-- ============================================================================
-- USAGE METERING
-- ============================================================================

CREATE TABLE IF NOT EXISTS skipper.skipper_usage (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id UUID NOT NULL,
    event_type TEXT,
    event_count INT NOT NULL DEFAULT 1,
    tokens_input INT,
    tokens_output INT,
    model TEXT,
    created_at TIMESTAMP WITH TIME ZONE DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS skipper_usage_tenant_created_idx
    ON skipper.skipper_usage (tenant_id, created_at);

CREATE INDEX IF NOT EXISTS skipper_conversations_tenant_user_idx
    ON skipper.skipper_conversations (tenant_id, user_id);

CREATE INDEX IF NOT EXISTS skipper_messages_conv_created_idx
    ON skipper.skipper_messages (conversation_id, created_at DESC);

-- ============================================================================
-- CRAWL JOBS
-- ============================================================================

CREATE TABLE IF NOT EXISTS skipper.skipper_crawl_jobs (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id UUID NOT NULL,
    sitemap_url TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'running',
    error TEXT,
    started_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT NOW(),
    finished_at TIMESTAMP WITH TIME ZONE
);

CREATE INDEX IF NOT EXISTS skipper_crawl_jobs_tenant_idx
    ON skipper.skipper_crawl_jobs (tenant_id, started_at DESC);

-- ============================================================================
-- PAGE CACHE (change detection for smart re-crawling)
-- ============================================================================

CREATE TABLE IF NOT EXISTS skipper.skipper_page_cache (
    id                      UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id               UUID NOT NULL,
    source_root             TEXT NOT NULL,
    page_url                TEXT NOT NULL,
    content_hash            TEXT,
    etag                    TEXT,
    last_modified           TEXT,
    raw_size                BIGINT,
    last_fetched_at         TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT NOW(),
    sitemap_priority        REAL DEFAULT 0.5,
    sitemap_changefreq      TEXT,
    consecutive_unchanged   INT NOT NULL DEFAULT 0,
    consecutive_failures    INT NOT NULL DEFAULT 0,
    source_type             TEXT NOT NULL DEFAULT 'sitemap',
    UNIQUE (tenant_id, page_url)
);

CREATE INDEX IF NOT EXISTS skipper_page_cache_source_idx
    ON skipper.skipper_page_cache (tenant_id, source_root);

CREATE INDEX IF NOT EXISTS skipper_page_cache_staleness_idx
    ON skipper.skipper_page_cache (tenant_id, last_fetched_at ASC);

-- ============================================================================
-- INVESTIGATION REPORTS
-- ============================================================================

CREATE TABLE IF NOT EXISTS skipper.skipper_reports (
    id TEXT PRIMARY KEY,
    tenant_id TEXT NOT NULL,
    trigger TEXT,
    summary TEXT,
    metrics_reviewed JSONB,
    root_cause TEXT,
    recommendations JSONB,
    read_at TIMESTAMP WITH TIME ZONE,
    created_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS skipper_reports_tenant_created_idx
    ON skipper.skipper_reports (tenant_id, created_at DESC);

-- ============================================================================
-- DIAGNOSTIC BASELINES (Welford running averages)
-- ============================================================================

CREATE TABLE IF NOT EXISTS skipper.skipper_baselines (
    tenant_id TEXT NOT NULL,
    stream_id TEXT NOT NULL DEFAULT '',
    metric_name TEXT NOT NULL,
    avg_value DOUBLE PRECISION NOT NULL DEFAULT 0,
    m2 DOUBLE PRECISION NOT NULL DEFAULT 0,
    sample_count BIGINT NOT NULL DEFAULT 0,
    updated_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT NOW(),
    PRIMARY KEY (tenant_id, stream_id, metric_name)
);

-- ============================================================================
-- SOCIAL POST DRAFTS
-- ============================================================================

CREATE TABLE IF NOT EXISTS skipper.skipper_posts (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    content_type TEXT NOT NULL,
    tweet_text TEXT NOT NULL,
    context_summary TEXT,
    trigger_data JSONB,
    status TEXT NOT NULL DEFAULT 'draft',
    sent_at TIMESTAMP WITH TIME ZONE,
    created_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS skipper_posts_status_created_idx
    ON skipper.skipper_posts (status, created_at DESC);

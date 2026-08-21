-- name: SearchKnowledge :many
SELECT id, tenant_id, source_url, source_title, source_type,
       chunk_text, chunk_index, COALESCE(metadata, 'null') AS metadata,
       (1 - (embedding <=> sqlc.arg(embedding)::vector))::double precision AS similarity
FROM skipper.skipper_knowledge
WHERE tenant_id = sqlc.arg(tenant_id)
  AND 1 - (embedding <=> sqlc.arg(embedding)::vector) > sqlc.arg(min_similarity)::double precision
ORDER BY embedding <=> sqlc.arg(embedding)::vector
LIMIT sqlc.arg(row_limit);

-- name: HybridSearchKnowledge :many
SELECT id, tenant_id, source_url, source_title, source_type,
       chunk_text, chunk_index, COALESCE(metadata, 'null') AS metadata,
       (sqlc.arg(vector_weight)::double precision * (1 - (embedding <=> sqlc.arg(embedding)::vector))
           + sqlc.arg(text_weight)::double precision * COALESCE(ts_rank(tsv, plainto_tsquery('english', sqlc.arg(search_query))), 0))::double precision AS similarity
FROM skipper.skipper_knowledge
WHERE tenant_id = sqlc.arg(tenant_id)
  AND 1 - (embedding <=> sqlc.arg(embedding)::vector) > sqlc.arg(min_similarity)::double precision
ORDER BY similarity DESC
LIMIT sqlc.arg(row_limit);

-- name: SearchKnowledgeFiltered :many
SELECT id, tenant_id, source_url, source_title, source_type,
       chunk_text, chunk_index, COALESCE(metadata, 'null') AS metadata,
       (1 - (embedding <=> sqlc.arg(embedding)::vector))::double precision AS similarity
FROM skipper.skipper_knowledge
WHERE tenant_id = sqlc.arg(tenant_id)
  AND source_type = sqlc.arg(source_type)::text
  AND 1 - (embedding <=> sqlc.arg(embedding)::vector) > sqlc.arg(min_similarity)::double precision
ORDER BY embedding <=> sqlc.arg(embedding)::vector
LIMIT sqlc.arg(row_limit);

-- name: HybridSearchKnowledgeFiltered :many
SELECT id, tenant_id, source_url, source_title, source_type,
       chunk_text, chunk_index, COALESCE(metadata, 'null') AS metadata,
       (sqlc.arg(vector_weight)::double precision * (1 - (embedding <=> sqlc.arg(embedding)::vector))
           + sqlc.arg(text_weight)::double precision * COALESCE(ts_rank(tsv, plainto_tsquery('english', sqlc.arg(search_query))), 0))::double precision AS similarity
FROM skipper.skipper_knowledge
WHERE tenant_id = sqlc.arg(tenant_id)
  AND source_type = sqlc.arg(source_type)::text
  AND 1 - (embedding <=> sqlc.arg(embedding)::vector) > sqlc.arg(min_similarity)::double precision
ORDER BY similarity DESC
LIMIT sqlc.arg(row_limit);

-- name: DeleteKnowledgeSourceChunks :exec
DELETE FROM skipper.skipper_knowledge
WHERE tenant_id = sqlc.arg(tenant_id)
  AND source_url = sqlc.arg(source_url);

-- name: InsertKnowledgeChunk :exec
INSERT INTO skipper.skipper_knowledge (
    tenant_id, source_url, source_title, source_root, source_type,
    chunk_text, chunk_index, embedding, metadata
)
VALUES (
    sqlc.arg(tenant_id), sqlc.arg(source_url), sqlc.arg(source_title)::text,
    sqlc.arg(source_root)::text, sqlc.narg(source_type), sqlc.arg(chunk_text),
    sqlc.arg(chunk_index)::integer, sqlc.arg(embedding)::vector, sqlc.arg(metadata)::text::jsonb
);

-- name: DeleteKnowledgeBySource :exec
DELETE FROM skipper.skipper_knowledge
WHERE tenant_id = sqlc.arg(tenant_id)
  AND (source_url = sqlc.arg(source_url) OR source_root = sqlc.arg(source_url));

-- name: ListKnowledgeSources :many
SELECT COALESCE(source_root, source_url) AS source_url,
       COUNT(DISTINCT source_url) AS page_count,
       MAX(NULLIF(metadata->>'ingested_at', '')::timestamptz) AS last_crawl_at
FROM skipper.skipper_knowledge
WHERE tenant_id = sqlc.arg(tenant_id)
GROUP BY COALESCE(source_root, source_url)
ORDER BY source_url;
